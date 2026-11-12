# Changelog

## Unreleased

### Added

- `ephemera doctor` says why the CLI can't run a sandbox. It checks the login, the central server, the token and each host in turn, and prints a fix for the first that fails. Exit 1 if a check fails. `--json` prints the same checks.
- `GET /v1/hosts` returns each host's `url`, and `online`: true while it checks in often enough to get sandboxes.
- Postgres. With `EPHEMERA_DATABASE_URL` (or `--database`), the central server keeps tokens, sandboxes and usage in Postgres instead of files, and keeps finished sandboxes as a history. It creates and updates its tables itself. Files stay the default.
- Sign in with GitHub. With Postgres and a GitHub OAuth app (`EPHEMERA_GITHUB_CLIENT_ID`, `EPHEMERA_GITHUB_CLIENT_SECRET`, `EPHEMERA_PUBLIC_URL`, `EPHEMERA_DASHBOARD_URL`), people sign in at `/auth/github` and get an account of their own, `gh-<GitHub user id>`. With the session, `GET`, `POST` and `DELETE /v1/tokens` list, make and revoke that account's tokens, and `GET /v1/me` says who is signed in. Sign-in asks only for `read:user` and doesn't keep GitHub's access token.
- Accounts. `ephemera-server token create --account NAME` puts a token in an account, and a token sees and destroys only its own account's sandboxes. Tokens made without one, including every existing token, share the account `local`, so nothing changes for them. `GET /v1/whoami` and sandboxes include the account.

### Changed

- `ephemera login` signs a machine in through the browser when the server has GitHub sign-in: it shows a code such as `K7QM-2XRT`, opens the approval page, and saves a device token named after the machine once the person approves. Codes last 10 minutes, a token can be collected once, and the approval page allows 10 wrong codes an account an hour. `--with-token`, or a piped token, reads an API token as before, and is used whenever the server has no browser sign-in. New endpoints: `POST /v1/device/codes`, `POST /v1/device/token`, and with a session `GET /v1/device` and `POST /v1/device/approve`. Tokens now have a `kind`, `device` or `api`.
- Hosts serve TLS 1.3 to the CLI, with a certificate made from the key they joined with, and `--url` is now `https://`. The CLI checks that key against the server's record of it and pins it, like ssh's known_hosts, in `known_hosts.json` next to the saved login; a pinned host whose key changes is refused with a message, and `ephemera hosts trust <host>` accepts the new key. `ephemera hosts` lists the account's hosts, their keys and whether each is pinned. `EPHEMERA_STRICT_HOSTS=1` refuses hosts not pinned yet. Sandboxes now include `host_key`, and `doctor` checks each host over the same pinned TLS.
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
