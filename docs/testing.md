# Real-device testing

This is the manual acceptance path for testing Ephemera with a real video on a phone, tablet, or another computer on the same network.

## 1. Build the binaries

```bash
go build -o bin/ephemera ./cmd/ephemera
go build -o bin/relay ./cmd/relay
```

## 2. Start the relay

Senders need a token. Mint one into a tokens file (the file stores only its hash; the token itself is printed once):

```bash
./bin/relay token -file tokens.txt me
```

Find the computer's LAN IP, so links open from a phone:

```bash
hostname -I              # Linux
ipconfig getifaddr en0   # macOS
```

Then start the relay with that file, telling it the address receivers will use:

```bash
./bin/relay serve -addr 0.0.0.0:7100 -tokens tokens.txt -public-url http://192.168.1.42:7100
```

Without `-public-url`, links use the address the sender dialed, which is right for a public relay (`wss://ephemera.run` gives `https://ephemera.run/<id>`) but means `127.0.0.1` on a LAN.

A relay started with neither `-tokens` nor `-open` refuses to start. `-open` lets anyone share without a token; use it only on a machine nobody else can reach.

Senders are held to the relay's limits: `-max-ttl` (seconds, default 86400) and `-max-size-mb` (default 5120). A share over either is refused, and `ephemera send` exits with the reason.

To keep background uploads, give the relay a data directory and a disk budget:

```bash
./bin/relay serve -addr 0.0.0.0:7100 -tokens tokens.txt -public-url http://192.168.1.42:7100 -data-dir ./relay-data -upload-gb 10
```

Without `-upload-gb`, senders are told the relay isn't keeping uploads and their links stream live only. `-cache-mb` and `-away-mb` size the optional in-memory replay and away-prefix stores.

## 3. Share a real video

From a second terminal, log in with the token from step 2 (paste it, or pipe it in):

```bash
./bin/ephemera login
```

`EPHEMERA_TOKEN` overrides the stored login, which is handy in scripts. Then share:

```bash
./bin/ephemera send \
  -relay ws://127.0.0.1:7100/daemon \
  -ttl 600 \
  /path/to/video.mp4
```

Notes:

- The file uploads to the relay in the background; the sender prints progress and exits once the relay has all of it. `-upload=false` streams live only.
- `-cache` enables replay of byte ranges that were already watched.
- `-away 1` uploads the first 1 MiB instead, if the relay won't keep the whole file.
- Use a standard MP4/H.264 file for the safest browser test.

Once the relay accepts the share, the sender prints its link:

```text
link: http://192.168.1.42:7100/pw_kcOAr43ghggBx2MnKxQ
```

The sender connects to the relay on `127.0.0.1`: plaintext is only allowed to the same machine.

## 4. Open the link on another device

Open the printed link on the phone or other device, on the same Wi-Fi.

## 5. Verify the live path

On the receiving device:

1. The receiver page opens.
2. The status is `live`.
3. The video plays.
4. Scrubbing works.
5. The sender terminal shows range-read activity.

## 6. Verify offline behavior

1. Stop the sender partway through the upload (Ctrl-C).
2. Refresh the receiver page. The status should become `offline`, and the uploaded start of the file should still play.
3. Requests past what's uploaded (or cached, with `-cache`) should fail honestly with `503`.
4. Run `ephemera resume` with the same `-relay`. The upload continues where it stopped, not from the start.
5. When the sender prints `uploaded: the relay has the whole file`, it exits. The page should read "ready to watch, even with the sender offline", and the whole video should play and seek.

## 7. Network notes

- Both devices should be on the same Wi-Fi unless the relay is on a public server.
- Allow TCP port `7100` through the local firewall.
- For remote testing, run the relay on a public server or use a tunnel such as Tailscale, Cloudflare Tunnel, or ngrok.

## 8. TLS

A sender only uses plaintext (`ws://`, `http://`, `tcp://`) to a relay on its own machine. Anywhere else it needs `wss://`, because the sender token and owner key would otherwise cross the network in the clear. `-insecure` overrides this on purpose. A bare host such as `-relay relay.example.com` means `wss://relay.example.com/daemon`.

Either let the relay terminate TLS itself:

```bash
./bin/relay serve -addr 0.0.0.0:443 -tokens tokens.txt -tls-cert fullchain.pem -tls-key privkey.pem
```

or keep it on plain HTTP behind a TLS proxy (Caddy, nginx, a Cloudflare or ngrok tunnel) and point senders at the proxy's `wss://` address. A relay serving plain HTTP off its own machine prints a warning, since it can't tell whether a proxy is in front.

Behind a proxy, every request reaches the relay from the proxy's address, so all receivers would share one rate-limit bucket. Tell the relay which addresses are your proxies, and it limits each request under the client address the proxy forwarded in `X-Forwarded-For`:

```bash
./bin/relay serve -addr 127.0.0.1:7100 -tokens tokens.txt -public-url https://relay.example.com -trusted-proxies 127.0.0.1,::1
```

`-trusted-proxies` takes IPs or CIDRs, comma-separated. The header is ignored on requests from any other address, since a client can send whatever it likes. The proxy must append the client's address: Caddy does by default; nginx needs `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`.

A proxy that puts the client's address in a header of its own, like Fly.io's `Fly-Client-IP`, is read with `-client-ip-header Fly-Client-IP`. That header wins over `X-Forwarded-For`. With `-trusted-proxies` set it's read only on requests from those proxies; without, on every request, so leave `-trusted-proxies` out only where the proxy is the sole way in, as on Fly.

An IPv6 client is limited by its /64, not its full address: an ISP hands each subscriber at least a /64, so one client could otherwise rotate through endless fresh buckets.

For a self-signed or private-CA certificate, a Linux sender can trust it with `SSL_CERT_FILE=/path/to/ca.pem`.

## 9. Moderation

Start the relay with an admin token and a data dir, so link reports and account suspensions survive restarts:

```bash
./bin/relay serve -addr 0.0.0.0:7100 -tokens tokens.txt -data-dir ./relay-data -admin-token "$EPHEMERA_ADMIN_TOKEN"
```

Receivers report a link with the "Report this link" form under the player (5 reports per IP, then one a minute). The data dir holds `reports.jsonl` and `suspended.txt`; it never holds file bytes or anything about who reported.

With `EPHEMERA_ADMIN_TOKEN` set:

```bash
./bin/ephemera reports                    # newest first: link, reason, account, file, note
./bin/ephemera kill -suspend <share-id>   # take the link down, suspend its sender, kill their other links
./bin/ephemera suspended                  # list suspended accounts
./bin/ephemera unsuspend <account>
```

`kill -suspend` still works after the link has expired, as long as it was reported: the report remembers the account. A suspended sender's `ephemera send` stops with "this sender account is suspended".
