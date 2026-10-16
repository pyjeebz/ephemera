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

Then start the relay with that file:

```bash
./bin/relay serve -addr 0.0.0.0:7100 -tokens tokens.txt
```

A relay started with neither `-tokens` nor `-open` refuses to start. `-open` lets anyone share without a token; use it only on a machine nobody else can reach.

Senders are held to the relay's limits: `-max-ttl` (seconds, default 86400) and `-max-size-mb` (default 5120). A share over either is refused, and `ephemera send` exits with the reason.

To enable the optional in-memory replay and away-prefix stores:

```bash
./bin/relay serve -addr 0.0.0.0:7100 -tokens tokens.txt -cache-mb 256 -away-mb 64
```

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
  -cache \
  -away 1 \
  /path/to/video.mp4
```

Notes:

- `-cache` enables replay of byte ranges that were already watched.
- `-away 1` uploads the first 1 MiB of the file to the relay.
- Use a standard MP4/H.264 file for the safest browser test.

The sender prints a link like:

```text
link: https://ephemera.run/pw_kcOAr43ghggBx2MnKxQ
```

For local testing, replace the placeholder domain with the relay's real address:

```text
http://127.0.0.1:7100/pw_kcOAr43ghggBx2MnKxQ
```

## 4. Open the link on another device

Find the computer's LAN IP:

```bash
hostname -I
```

On macOS:

```bash
ipconfig getifaddr en0
```

Then open this URL on the phone or other device:

```text
http://<computer-ip>:7100/<share-id>
```

Example:

```text
http://192.168.1.42:7100/pw_kcOAr43ghggBx2MnKxQ
```

## 5. Verify the live path

On the receiving device:

1. The receiver page opens.
2. The status is `live`.
3. The video plays.
4. Scrubbing works.
5. The sender terminal shows range-read activity.

## 6. Verify offline behavior

1. Let the away-prefix upload finish.
2. Stop the sender daemon.
3. Refresh the receiver page.
4. The status should become `offline`.
5. With `-cache`, already-watched ranges should replay from RAM.
6. With `-away`, the uploaded prefix should remain playable while the sender is offline.
7. Requests outside the cached or away-prefix coverage should fail honestly with `503`.

## 7. Network notes

- Both devices should be on the same Wi-Fi unless the relay is on a public server.
- Allow TCP port `7100` through the local firewall.
- For remote testing, run the relay on a public server or use a tunnel such as Tailscale, Cloudflare Tunnel, or ngrok.
