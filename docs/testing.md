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

`-guests` lets people share without a token too, but as guests: links up to an hour and files up to 1 GB (`-guest-ttl`, `-guest-size-mb`), and ten new shares an hour from one address (`-guest-shares-per-hour`). It works with or without `-tokens`; senders with a token get the full limits. A guest's account is named for its address (`guest-203.0.113.7`, or the /64 for IPv6), so `ephemera suspend guest-203.0.113.7` stops that address and nobody else.

People can also get a token without the terminal, by signing in with Google or GitHub on the relay's home page. Each provider needs an OAuth app whose callback is `<public-url>/auth/<provider>/callback`:

- **Google**: in the Google Cloud console, APIs & Services → Credentials → Create OAuth client ID → Web application. Authorised redirect URI `https://ephemera.run/auth/google/callback`. The consent screen only needs the `openid` and `email` scopes.
- **GitHub**: Settings → Developer settings → OAuth Apps → New OAuth App. Authorization callback URL `https://ephemera.run/auth/github/callback`. It asks for no scopes.

Pass the client ids as flags and the secrets in the environment:

```bash
EPHEMERA_GOOGLE_SECRET=... EPHEMERA_GITHUB_SECRET=... ./bin/relay serve -public-url https://ephemera.run -data-dir ./relay-data \
  -google-client-id 1234.apps.googleusercontent.com -github-client-id Iv1.abcd -guests
```

Sign-in needs `-public-url` (the providers send people back to it) and `-data-dir` (accounts go in `accounts.json` there, holding token hashes, never tokens). Each sign-in mints a new token; an account keeps its five newest. An account is named for the provider and the login or email it first signed in with (`github:octocat`, `google:alice@example.com`), which is the name `ephemera suspend` takes. Ten sign-ins an hour per address.

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

Once the relay accepts the share, the sender prints its link on stdout (everything else goes to stderr):

```text
http://192.168.1.42:7100/pw_kcOAr43ghggBx2MnKxQ
```

The raw bytes are at the link plus `/file`, for `curl`, `ffprobe` or DuckDB. Scripts and agents can ask for JSON events instead, one per line:

```bash
./bin/ephemera send -json -relay ws://127.0.0.1:7100/daemon /path/to/video.mp4
```

```text
{"event":"link","share_id":"pw_kc…","name":"video.mp4","size":52428800,"mime":"video/mp4","link":"http://…/pw_kc…","file_url":"http://…/pw_kc…/file","expires":"2026-10-27T18:04:00Z"}
{"event":"upload","share_id":"pw_kc…","have":0,"size":52428800}
{"event":"uploaded","share_id":"pw_kc…","size":52428800,"expires":"2026-10-27T18:04:00Z"}
```

To take a link back, run `ephemera revoke <link>` on the machine that shared it (`-json` prints a `revoked` event). The relay checks the link's owner key, which only the sender has, stops serving the link and deletes its copy. The web page has a delete button on each link for the same thing.

The other events are `upload_refused` (the relay won't keep the file, so the link streams live), `expired` and `error` (with `code` and `message`). Exit codes: `0` done (uploaded or expired), `1` error, `2` bad flags or arguments, `3` the relay refused the share (token, limits, a killed link).

The sender connects to the relay on `127.0.0.1`: plaintext is only allowed to the same machine.

### Or send from a browser

Open `http://192.168.1.42:7100/` (any relay serves the sender on its home page, and at `/send`) and drop a file. The tab does what `ephemera send` does: the link appears at once and streams from the tab while the file uploads, and the page says when the tab can close. On a relay with `-guests`, anyone can send straight away under the guest limits; with sign-in on, the page offers Google or GitHub (in a popup, so files already streaming keep going) and keeps the token in that browser. The account menu has the token for `ephemera login`. A token from `relay token` can be pasted instead. Browsers only allow the page's crypto on HTTPS or `localhost`, so use a TLS relay (or a tunnel) when testing from another device.

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

## 7. Read the numbers

Every receiver page reports how its playback went when it's closed or hidden: how long the first frame took, stalls, seek waits. The relay adds the file's bitrate, how fast the sender delivered live reads, and how much it served from its own copy. Each report is one line in the relay's log, and an admin can see the summary:

```bash
./bin/ephemera stats -relay http://127.0.0.1:7100 -token "$EPHEMERA_ADMIN_TOKEN"
```

```text
uploads: 1 finished, 907 MB on the relay's disk, 0 turned away for lack of room
first byte, any client: from the relay's copy 1.5ms median, 4.0ms p90 (40 requests); live from the sender 180ms median, 1.2s p90 (12 requests)
playbacks: 3 (live 2, available 1)
first frame: 420ms median, 1.5s p90
play to playing: 30ms median
stalls: 66% of playbacks never stalled; 2.5% of watch time spent stalled
seeks: 300ms median wait
speed: files play at 4.0 Mbps median, senders deliver live at 12.0 Mbps median; sender slower than the file in 33% of playbacks
```

The "first byte" line covers every file request from any client (browsers, `curl`, DuckDB, agents), timed on the relay from the request arriving to its first byte, split by whether the bytes came from the relay's own copy or live from the sender.

"Sender slower than the file" is the one to watch: those links stutter while they stream live, until the upload catches up. `-json` prints the raw numbers. Reports keep no IP addresses and live in memory only (the latest 1000).

## 8. Network notes

- Both devices should be on the same Wi-Fi unless the relay is on a public server.
- Allow TCP port `7100` through the local firewall.
- For remote testing, run the relay on a public server or use a tunnel such as Tailscale, Cloudflare Tunnel, or ngrok.

## 9. TLS

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

## 10. Moderation

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
