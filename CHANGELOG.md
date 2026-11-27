# Changelog

## Unreleased

### Added

- A host you use only over SSH: `--address 127.0.0.1` on the host installer makes the host listen on the machine itself, with nothing open to anyone else, and you run `ephemera` there. The installer no longer warns that such a host is open to the internet, and its last message says to run ephemera on the machine. With no private address, as on many rented servers, it now names both ways out: `--address` with the public address, or `--address 127.0.0.1`. The hosts guide has a [Use it over SSH](https://ephemera.run/docs/guides/hosts#use-it-over-ssh) section.
- Docs: [Run it on one machine](https://ephemera.run/docs/guides/one-machine), for a Linux laptop, or Windows through WSL2, that is its own host.

### Changed

- A sandbox with no network now refuses each connection with a reason and logs it, like one with an allow list, instead of having no proxy at all. Nothing more gets out, but a first `npm install` without `--allow` used to fail as a bare `ENOTFOUND registry.npmjs.org`; now the run ends with "blocked registry.npmjs.org:443 (the sandbox has no network; add --allow registry.npmjs.org)", and the record lists the attempts. This needs the host and the CLI on this release.
- `ephemera doctor`'s fixes no longer assume Tailscale. A dropped connection points at the host's firewall, a cloud firewall or security group, and VPN access rules; the MTU fix names whatever interface reaches the host. The docs list every way to reach a host: a LAN, a VPN, a public address behind a firewall, or SSH.

### Fixed

- The site sends `X-Frame-Options: DENY` and `frame-ancestors 'none'`, so no other site can frame the dashboard and trick a click on Approve or Revoke. The session cookie's `SameSite=Lax` already kept a framed dashboard signed out; this no longer relies on it.
- A sandbox that reached its maximum lifetime counted as running for another hour: in the dashboard, against its host's capacity, and against its token's sandboxes-at-once and daily minutes. Its host keeps its signed record for an hour so the CLI can still collect it, and kept listing it all that time. Now the run ends as soon as the host says the sandbox expired, with the new `end_reason` `expired` ("Timed out" in the dashboard), and `DELETE /v1/sandboxes/{id}` still returns its record for that hour. Hosts older than v0.3.0 don't say, so their sandboxes end as before.

## v0.3.0 (2026-11-26)

Upgrade in any order: v0.2 CLIs, central servers and hosts work with v0.3 ones. Run results (exit codes and connection counts) appear for a host's runs once that host is on v0.3.0; `api.ephemera.run` already runs this release's server. Upgrading a host rebuilds its guest image.

### Added

- `GET /v1/runs` gives each finished run `exit_code`, the last command's exit code, and `connections_allowed` and `connections_blocked`, how many connections its egress proxy let through and refused. Hosts send these three numbers when a sandbox ends, and nothing more: no commands, host names or output. Each is missing when unknown: while a sandbox runs, when no command ran (`exit_code` only), and for hosts older than this release. An older server ignores them, so hosts and servers can be upgraded in any order. Postgres keeps them in three new nullable columns; a server on files has no run history, as before.
- `GET /v1/tokens` gives each token `last_used`, when it last made a request. The server writes it at most once a minute per token, so it can be up to a minute behind, and leaves it out for a token never used. The token file and Postgres both keep it.
- The dashboard shows each ended run's exit code ("Ended · exit 0", in red when it isn't 0) on the Sandboxes page and the overview, the connection counts in a run's details, and a "Last used" column on the Tokens page.
- `ephemera hosts` has a LAST SEEN column: `now` for an online host, and for an offline one when it last checked in, such as `4 days ago`.
- When every host in the account is offline, `ephemera run` and `ephemera dev` say which and since when, instead of the central server's generic error: "no host can take a sandbox: your only host, lab-1, is offline: it was last seen 4 days ago; on that machine: systemctl status ephemera-host". `ephemera login` prints the same note after signing in, so a dead host shows up before the first run, not at it.
- Docs: an [Inside the sandbox](https://ephemera.run/docs/guides/sandbox) page: root with no `sudo`, no systemd or Docker, a writable `/etc/hosts`, no DNS server, and exactly what the default image holds.
- Docs: the self-hosting guide gives the tailnet access rule a laptop needs to reach a host on tcp:7701, and the MTU fix for WSL2.

### Changed

- `ephemera doctor` tries each host in three steps, a TCP connection, a small TLS request and an 8 KB one, and says which failed. A dropped connection points at firewalls and tailnet access rules (allow tcp:7701 to the host); a stalled handshake or large request points at the MTU (`sudo ip link set tailscale0 mtu 1200`). Both used to read `no answer from <address> within 5s`.

### Fixed

- The guest image's `/etc/hosts` names the sandbox's hostname, `ephemera`, and no longer carries a line for the container the Ubuntu base was built in. Upgrade a host to pick it up.

## v0.2.1 (2026-11-20)

### Fixed

- The host installer joined a v0.1 host to its old self-hosted server instead of `https://api.ephemera.run`: with a join code and no `--server`, it took the server from the v0.1 env file, so the join failed with "connection refused" and the code had to be used again with `--server https://api.ephemera.run`. A host that hasn't joined yet now joins the hosted server unless given `--server`; a joined host still keeps its server and name on upgrade, and every host keeps its address.

## v0.2.0 (2026-11-18)

**Upgrade the CLI, the central server and every host together.** v0.1 CLIs, servers and hosts don't work with v0.2. Hosts now connect out to the central server and join an account once with a one-time code; the CLI sends the server only a hash of each sandbox's token. Re-join each host with the host installer, `curl -fsSL https://ephemera.run/host | sudo sh -s -- <join code>`, after making a code in the dashboard or with `ephemera-server join-code create`.

### Added

- Sandbox names. `ephemera run --name api-tests` (and `ephemera dev --name`, and `name` on the MCP `run_command` tool) names a sandbox for what it's for; without one the server makes one up, like `amber-dune-42`. Names are lowercase letters, digits and hyphens, up to 40 characters, and unique among an account's running sandboxes: a clash gets `409`. `ephemera ls` lists sandboxes by name, and `ephemera rm`, `DELETE /v1/sandboxes/{id}` and `destroy_sandbox` take a name or an id. Sandboxes and runs in the API carry `name`, and `--json` events carry it too. The signed record format doesn't change.
- `ephemera verify api-tests` checks the newest record of the sandbox named `api-tests`; `ephemera verify` alone checks the newest record of all, and an id or a path works as before. Records are saved as `<name>-<id>.json`, so the folder reads by name and repeat runs keep their own. The MCP `verify_record` tool takes `sandbox` (a name, an id or a path) and needs nothing for the newest record.
- A host installer: `curl -fsSL https://ephemera.run/host | sudo sh -s -- <join code>` sets up a host from a release, with no checkout, Go or network of its own. It checks KVM, installs Firecracker 1.15.1 (checked against a pinned SHA-256), the host agent and its guest image, joins the account and starts the `ephemera-host` service. It serves on the machine's one private IPv4 address, or `--address`. Run it again to upgrade; `--uninstall` removes it. The host release archive now holds `build-image.sh` and the systemd unit, and `install-host.sh` is a release file of its own.
- `ephemera doctor` says why the CLI can't run a sandbox. It checks the login, the central server, the token and each host in turn, and prints a fix for the first that fails. Exit 1 if a check fails. `--json` prints the same checks.
- `GET /v1/hosts` returns each host's `url`, `online` (true while it checks in often enough to get sandboxes), `capacity`, `version`, `running` (how many of the account's sandboxes are on it), `joined` and the key's `fingerprint`.
- The dashboard, signed in with a session, can use `GET /v1/hosts`, `GET /v1/sandboxes` and `GET /v1/usage`, stop sandboxes and remove hosts, all for the whole account. A session can't make sandboxes. `GET /v1/usage` with a session adds up the whole account's day.
- `GET /v1/runs` lists the account's sandboxes, running and finished, newest first, 100 at a time; `?before=<time>` pages back. Each has its host, token name, spec, start and end, and why it ended. Postgres only, as files don't keep finished sandboxes.
- Postgres. With `EPHEMERA_DATABASE_URL` (or `--database`), the central server keeps tokens, sandboxes and usage in Postgres instead of files, and keeps finished sandboxes as a history. It creates and updates its tables itself. Files stay the default.
- Sign in with GitHub. With Postgres and a GitHub OAuth app (`EPHEMERA_GITHUB_CLIENT_ID`, `EPHEMERA_GITHUB_CLIENT_SECRET`, `EPHEMERA_PUBLIC_URL`, `EPHEMERA_DASHBOARD_URL`), people sign in at `/auth/github` and get an account of their own, `gh-<GitHub user id>`. With the session, `GET`, `POST` and `DELETE /v1/tokens` list, make and revoke that account's tokens, and `GET /v1/me` says who is signed in. Sign-in asks only for `read:user` and doesn't keep GitHub's access token. `/auth/github?next=/some/path` lands on that path of the dashboard's site after signing in; anything that isn't a plain path lands on the dashboard.
- Accounts. `ephemera-server token create --account NAME` puts a token in an account, and a token sees and destroys only its own account's sandboxes. Tokens made without one, including every existing token, share the account `local`, so nothing changes for them. `GET /v1/whoami` and sandboxes include the account.

### Changed

- `ephemera dev --name` now names the sandbox as well as the session, so it must be a valid sandbox name (lowercase letters, digits and hyphens). Without it, the session is still the git branch. `ephemera ls` has a `NAME` column first.
- The CLI's default central server is the hosted one, `https://api.ephemera.run`, so `ephemera login` needs no `--server` there. A self-hosted server still takes `--server`, and a saved login keeps its server. `ephemera doctor`'s fixes point to the dashboard and the host installer.
- `ephemera login` signs a machine in through the browser when the server has GitHub sign-in: it shows a code such as `K7QM-2XRT`, opens the approval page, and saves a device token named after the machine once the person approves. Codes last 10 minutes, a token can be collected once, and the approval page allows 10 wrong codes an account an hour. `--with-token`, or a piped token, reads an API token as before, and is used whenever the server has no browser sign-in. New endpoints: `POST /v1/device/codes`, `POST /v1/device/token`, and with a session `GET /v1/device` and `POST /v1/device/approve`. Tokens now have a `kind`, `device` or `api`.
- Hosts serve TLS 1.3 to the CLI, with a certificate made from the key they joined with, and `--url` is now `https://`. The CLI checks that key against the server's record of it and pins it, like ssh's known_hosts, in `known_hosts.json` next to the saved login; a pinned host whose key changes is refused with a message, and `ephemera trust <host>` accepts the new key. `ephemera hosts` lists the account's hosts, their keys and whether each is pinned. `EPHEMERA_STRICT_HOSTS=1` refuses hosts not pinned yet. Sandboxes now include `host_key`, and `doctor` checks each host over the same pinned TLS.
- The CLI makes each sandbox's access token and sends the central server only its SHA-256 (`token_hash` in `POST /v1/sandboxes`). The server passes the hash to the host and never sees the token, so it can't open a sandbox to read the code in it. A create without `token_hash`, from an older CLI, is refused: upgrade the CLI too.
- Hosts join an account once, with a one-time code from `ephemera-server join-code create` (or `POST /v1/join-codes` from the dashboard): `ephemera-host join --server URL CODE`. A code works once, for 15 minutes. The host then signs in with its own Ed25519 key, by signing a fresh challenge from the server; `EPHEMERA_HOST_SECRET` is gone. A host runs only its account's sandboxes, `GET /v1/hosts` lists only the account's hosts, and `DELETE /v1/hosts/{id}` removes one. A host the server has no record of destroys its sandboxes. `install.sh` joins the host itself, and takes `--join-code-file` in place of `--secret-file`; upgrading a one-machine install joins it automatically.
- Hosts connect out to the central server and keep that connection open; the server sends creates and destroys down it instead of calling the host. The server no longer needs to reach hosts at all, so a host can sit behind NAT, as long as the CLI can reach its `--url` for files and commands. A host that loses the connection keeps retrying, waiting 1 s, doubling to 60 s. `POST /v1/hosts` and the host's create and destroy endpoints are gone, so hosts and servers from v0.1 don't work with this version or each other: upgrade both.
- Building from source needs Go 1.25 or later (was 1.24), for the Postgres driver. Go itself supports only 1.26 and 1.27 now.

## v0.1.1 (2026-11-11)

### Changed

- Install the CLI with `curl -fsSL https://ephemera.run/install | sh`, which serves `install.sh` from the latest release. The docs no longer suggest `go install`.

### Fixed

- `go install github.com/pyjeebz/ephemera/cmd/ephemera@<version>` reported `ephemera dev`. It now reports the version installed.

## v0.1.0 (2026-11-11)

The first release of ephemera: run dev servers, tests and coding agents in short-lived, locked-down Firecracker microVMs on your own hardware, while you keep editing on your laptop.

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/pyjeebz/ephemera/main/install.sh | sh
```

The script picks the archive for your OS and CPU and checks it against `checksums.txt`. On Windows, download the zip below. With Go: `go install github.com/pyjeebz/ephemera/cmd/ephemera@v0.1.0`.

To run your own server and hosts, follow [docs/self-hosting.md](https://github.com/pyjeebz/ephemera/blob/v0.1.0/docs/self-hosting.md).

### What's in it

- **`ephemera run -- <cmd>`** sends your Git-tracked files to a fresh microVM, streams the output, returns the exit code, and destroys the VM, even on Ctrl-C.
- **`ephemera dev`** keeps a sandbox running, syncs files as you save them, and forwards ports to `localhost`. Each branch also gets its own address, like `feature-login.localhost:3000`.
- **Outbound traffic is blocked by default.** `--allow registry.npmjs.org` opens one host. Private addresses and cloud metadata stay blocked even with `--net`. Every connection attempt is logged.
- **Setup snapshots.** `--setup "npm ci"` saves the prepared sandbox, so later runs skip the install. The snapshot is encrypted with a key that stays on your laptop, and files from `--include` are never saved in it.
- **Signed records.** When a sandbox is destroyed, its host signs a record of the commands, connections and file syncs, and that the disk was deleted. `ephemera verify` checks it against the host's key.
- **Limits.** `--timeout` for a command, a maximum lifetime per sandbox and per host, and per-token caps on sandboxes at once and sandbox-minutes per day.
- **Coding agents.** `ephemera mcp` is an MCP server with `run_command`, `list_sandboxes`, `destroy_sandbox` and `verify_record`.
- **Self-hosting.** `deploy/host/install.sh` runs the central server and host agent as systemd services, with the server unprivileged. `ephemera-host bench` measures a host.

### Files

| File | What |
| --- | --- |
| `ephemera_v0.1.0_<os>_<arch>.tar.gz` / `.zip` | The CLI for Linux and macOS (`amd64`, `arm64`) and Windows |
| `ephemera-host_v0.1.0_linux_amd64.tar.gz` | `ephemera-server`, `ephemera-host` and `ephemera-guest` for Linux x86_64 |
| `install.sh` | The CLI install script |
| `checksums.txt` | SHA-256 of every file |

### Known limits

- **No TLS yet.** Run the server and hosts on a private network such as Tailscale. The installer refuses public addresses.
- **Hosts need x86_64 Linux with KVM.** Tested on Ubuntu 24.04, including in a cloud VM with nested virtualization.
- **No accounts or hosted version yet.** Access is by API tokens made with `ephemera-server token create`.
- **A setup step can't use credentials** such as a private registry token, because secrets are kept out of snapshots.
- **Lightly tested:** the Windows CLI, and MCP clients other than through the protocol tests.
