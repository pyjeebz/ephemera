# Deploying a relay

There are two ways to run a public relay:

- **Fly.io**, which hosts `ephemera.run`. Fly's proxy does TLS and certificates, so there's no server to look after. See [Fly.io](#flyio) below.
- **Your own Linux server**, with Caddy in front. Choose this when bandwidth is the bill: Fly charges for every byte receivers watch, while a VPS plan usually includes terabytes.

## Your own server

A public relay is two processes on one Linux server: Caddy on ports 80 and 443 for TLS, and the relay on `127.0.0.1:7100` behind it. The files are in `deploy/`:

- `deploy/Caddyfile`: Caddy config. It gets and renews the certificate, and serves HTTPS, WSS, HTTP/2 and HTTP/3.
- `deploy/ephemera-relay.service`: systemd unit for the relay, locked down, with no fixed user account.

The steps below use `ephemera.run`. To host your own, replace it with your domain in both files.

### 1. The server

- Linux with systemd 247 or newer (Ubuntu 22.04+, Debian 12+).
- A public IPv4 address.
- Bandwidth matters most. Every byte a receiver watches passes through the relay, so pick a plan by its traffic allowance, not its CPU.
- Open TCP 80 and 443, and UDP 443 (HTTP/3). Keep 7100 closed: the relay listens on localhost only.

With `ufw`:

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 443/udp
```

### 2. DNS

Point the domain at the server. In GoDaddy: **My Products → the domain → DNS → Manage DNS**.

| Type  | Name  | Value                  | TTL    |
|-------|-------|------------------------|--------|
| A     | `@`   | the server's IPv4      | 600 s  |
| CNAME | `www` | `@`                    | 1 hour |
| AAAA  | `@`   | the server's IPv6, *only if IPv6 works on it* | 600 s |

- Delete GoDaddy's default `A @ Parked` record. Two `A` records for `@` would send some visitors to the parking page.
- Turn off **Forwarding** for the domain if it's on.
- An `AAAA` record for a server whose IPv6 doesn't actually work makes certificate issuance fail. Leave it out if unsure.

Check from your own machine before starting Caddy, since Caddy can only get a certificate once DNS points at it:

```bash
dig +short ephemera.run      # the server's IP
dig +short www.ephemera.run
```

### 3. The relay

Build it on your machine for the server's CPU (`uname -m` on the server: `x86_64` is amd64, `aarch64` is arm64) and copy it over:

```bash
GOOS=linux GOARCH=amd64 go build -o relay ./cmd/relay
scp relay you@server:
ssh you@server 'sudo install -m 0755 relay /usr/local/bin/relay'
```

On the server, create the sender tokens file and the admin token. Both stay root-only:

```bash
sudo mkdir -p /etc/ephemera
sudo relay token -file /etc/ephemera/tokens.txt yourname    # prints your sender token once
echo "EPHEMERA_ADMIN_TOKEN=$(openssl rand -hex 32)" | sudo tee /etc/ephemera/relay.env >/dev/null
sudo chmod 600 /etc/ephemera/tokens.txt /etc/ephemera/relay.env
sudo cat /etc/ephemera/relay.env    # keep this admin token somewhere safe
```

Install and start the service:

```bash
sudo cp deploy/ephemera-relay.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ephemera-relay
journalctl -u ephemera-relay -f    # "relay listening on 127.0.0.1:7100"
```

### 4. Caddy

Install Caddy from its official repository (Debian and Ubuntu; other systems: https://caddyserver.com/docs/install):

```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

Put the config in place. Uncomment the `email` line and set your address, so Let's Encrypt can warn you if renewal ever fails:

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo nano /etc/caddy/Caddyfile
sudo systemctl reload caddy
journalctl -u caddy -f    # "certificate obtained successfully"
```

### 5. Check it

```bash
curl -I https://ephemera.run    # 200, with strict-transport-security
```

From your laptop:

```bash
ephemera login                                  # paste the sender token
ephemera send -relay ephemera.run clip.mp4      # a bare host means wss://ephemera.run/daemon
# link: https://ephemera.run/<id>
```

Open the link on a phone using mobile data, not your Wi-Fi, so the test really crosses the internet.

### Running it

**Add a sender.** Mint a token, then restart: the relay reads the tokens file at startup.

```bash
sudo relay token -file /etc/ephemera/tokens.txt alice
sudo systemctl restart ephemera-relay
```

A restart drops live shares for a few seconds. Senders redial on their own, but the in-memory replay cache and away prefixes start empty.

**Moderate.** From anywhere, with the admin token:

```bash
export EPHEMERA_ADMIN_TOKEN=...
ephemera reports -relay https://ephemera.run
ephemera kill -suspend -relay https://ephemera.run <id>
```

Reports and suspensions live in `/var/lib/ephemera` and survive restarts. That directory never holds file bytes.

**Update the relay.** Build and copy the new binary as in step 3, then `sudo systemctl restart ephemera-relay`.

**Logs.** `journalctl -u ephemera-relay` and `journalctl -u caddy`.

### How the pieces fit

- Caddy talks to the relay over loopback, so the relay trusts `X-Forwarded-For` only from `127.0.0.1` and `::1` (`-trusted-proxies`). Rate limits then apply per real client, not to Caddy as a whole. Caddy replaces any `X-Forwarded-For` a client sends with the address it actually saw.
- `-public-url https://ephemera.run` makes senders print links under the public address, whatever address they dialed.
- `flush_interval -1` passes video bytes to the receiver as they arrive. Buffering would delay the first frame.
- `stream_close_delay 5m` keeps sender WebSockets open through a Caddy config reload.

## Fly.io

`fly.toml` and the `Dockerfile` at the repo root run the relay as one always-on Fly Machine. Fly's proxy terminates TLS and forwards plain HTTP to the relay on port 8080.

### Settings that matter

- **One machine.** The share registry lives in memory, so a receiver sent to a second machine would find no share there. The data volume keeps it at one, since `fly deploy` starts one machine per volume. Deploy with `--ha=false` all the same.
- **Never stopped.** `auto_stop_machines = "off"`: a stopped machine drops every live share.
- **Client IP.** Fly's `X-Forwarded-For` ends with the app's own IP, so the relay reads `Fly-Client-IP` instead (`-client-ip-header`). Fly's proxy overwrites that header, so clients can't pick their own rate-limit bucket. Without `-trusted-proxies` the header is read on every request, which is only safe because Fly's proxy is the sole way in.
- **Idle sockets.** A sender with no viewers sends nothing. `idle_timeout = 600` keeps its WebSocket open through quiet stretches.
- **Memory.** The VM has 1 GB. The replay cache and away store are capped at 192 and 384 MiB, with `GOMEMLIMIT` under the VM size.
- **Uploads.** Background uploads live on the `ephemera_data` volume under `/data/uploads`, capped by `-upload-gb`. Keep the cap a gigabyte or so under the volume's size: reports and suspensions share the volume, and the budget counts each upload's full size from the start, so a full volume shows up as uploads being turned away, not as failed writes. Grow both together: `fly volumes extend <volume id> --size 10 --app ephemera-relay` (find the id with `fly volumes list`), then raise `-upload-gb`. Volumes cost $0.15/GB a month.

### First deploy

```bash
fly auth login
fly apps create ephemera-relay
fly volumes create ephemera_data --app ephemera-relay --region yyz --size 10
```

Mint the sender tokens file locally and stage it and an admin token as secrets. Fly needs secret files base64-encoded:

```bash
go build -o bin/relay ./cmd/relay
./bin/relay token -file tokens.txt yourname       # prints your sender token once
fly secrets set --app ephemera-relay --stage \
  EPHEMERA_TOKENS="$(base64 -w0 < tokens.txt)" \
  EPHEMERA_ADMIN_TOKEN="$(openssl rand -hex 32)"
```

Keep `tokens.txt`: adding a sender later means appending to it and setting the secret again. It holds hashes, not the tokens themselves. Keep the admin token somewhere safe too, since `fly secrets` won't show it again.

```bash
fly deploy --ha=false
curl -I https://ephemera-relay.fly.dev      # 200
```

### The domain

```bash
fly certs add ephemera.run --app ephemera-relay
fly certs add www.ephemera.run --app ephemera-relay
fly ips list --app ephemera-relay
```

In GoDaddy (**Domain Portfolio → ephemera.run → DNS**), replace the parked `A @` record and set:

| Type  | Name  | Value                          | TTL   |
|-------|-------|--------------------------------|-------|
| A     | `@`   | the app's IPv4 from `fly ips list` | 600 s |
| AAAA  | `@`   | the app's IPv6 from `fly ips list` | 600 s |
| CNAME | `www` | `ephemera-relay.fly.dev`       | 1 hour |

Turn off GoDaddy **Forwarding** if it's on. `fly certs show ephemera.run --app ephemera-relay` reports when the certificate is issued.

### Running it

- **Add a sender.** `./bin/relay token -file tokens.txt alice`, then set `EPHEMERA_TOKENS` again as above, without `--stage`. That restarts the machine.
- **Update the relay.** `fly deploy --ha=false`.
- **Moderate.** `ephemera reports -relay https://ephemera.run` and `ephemera kill -suspend ...` work as on your own server, with `EPHEMERA_ADMIN_TOKEN` set.
- **Logs.** `fly logs --app ephemera-relay`.

A deploy or secrets change restarts the machine and drops live shares for a few seconds, as a restart does on your own server. Senders redial on their own, and uploaded files come back with the relay, but the replay cache and away prefixes start empty.

Egress costs $0.02/GB in North America and Europe, with no free allowance. If that becomes the bill, the same binary runs on a VPS with the steps above.
