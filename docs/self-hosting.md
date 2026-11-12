# Self-hosting ephemera

ephemera has two server-side parts:

- The **central server** (`ephemera-server`) places sandboxes on hosts and keeps track of them. It never sees your files, command output or secrets.
- A **host agent** (`ephemera-host`) runs sandboxes as Firecracker microVMs. The CLI talks to it directly to send files and stream output.

One machine can run both. Add more hosts later and point them at the same central server.

## What you need

- An x86_64 machine running Ubuntu 24.04, with hardware virtualization (Intel VT-x or AMD-V) turned on. A bare-metal server, a spare PC, or a cloud VM with nested virtualization all work.
- A Tailscale account, or another private network between your laptop and the machine. Your laptop talks to hosts over TLS, checked against each host's own key, but the central server still serves plain HTTP, so the installer refuses to serve on a public address.
- About 2 GB of disk for the guest image, plus room for sandboxes. Each sandbox's disk is sparse, so it takes only what the sandbox writes.

Memory sets how many sandboxes fit. An idle sandbox costs the host about 50–60 MB, so a 16 GB machine holds well over a hundred idle ones. Busy sandboxes use what their workload uses, up to their limit (1 GB by default).

## Install

On the machine, from a checkout of this repository:

```bash
git clone https://github.com/pyjeebz/ephemera.git && cd ephemera
```

```bash
sudo deploy/host/bootstrap.sh --hostname ephemera-1
```

```bash
sudo deploy/host/install.sh
```

`bootstrap.sh` prepares the machine. It checks KVM, then installs Firecracker and Go, creates an `ephemera` user, and joins Tailscale with SSH. On a server with a public IP, run it again with `--lock-public-ssh` once Tailscale works, so the machine accepts connections only over Tailscale.

`deploy/host/install.sh` installs ephemera itself:

1. Builds `ephemera`, `ephemera-server` and `ephemera-host` into `/usr/local/bin`.
2. Builds the guest image into `/var/lib/ephemera/image`. This takes a few minutes and needs network.
3. Writes `/etc/ephemera/ephemera.env` with the machine's addresses.
4. Starts both parts as systemd services, `ephemera-server` and `ephemera-host`, and waits until the host has connected.
5. Creates the first API token, named `admin`, and prints it once.

## Connect your laptop

Install the CLI:

```bash
curl -fsSL https://ephemera.run/install | sh
```

On Windows, download the zip from the releases page.

Log in with the server address and token that `deploy/host/install.sh` printed:

```bash
ephemera login --server http://100.64.0.1:7700
```

Try it:

```bash
ephemera run -- echo hello from a microVM
```

If that fails, `ephemera doctor` says which step is broken (the login, the network, the token or a host) and how to fix it.

## Tokens and limits

Make one token per person, laptop, agent or CI system, so each can be limited and revoked on its own:

```bash
sudo ephemera-server token create ci --max-sandboxes 4 --max-minutes 600
```

```bash
sudo ephemera-server token ls
```

```bash
sudo ephemera-server token revoke ci
```

`--max-sandboxes` caps how many run at once, and `--max-minutes` caps sandbox-minutes per UTC day. `ephemera-server token limits <name>` changes them later. The server picks up changes without a restart.

Every token belongs to an account, and a token only sees and destroys its own account's sandboxes. Tokens made without `--account` share the account `local`, which suits one person or a small team who trust each other. To keep people apart, give each their own account:

```bash
sudo ephemera-server token create sam-laptop --account sam
```

Each host belongs to the account whose join code it used, and runs only that account's sandboxes.

## Add another host

A new host joins the central server once, with a one-time code. On the server machine, make one; it works once, for 15 minutes:

```bash
sudo ephemera-server join-code create
```

Add `--account NAME` to join the host to that account instead of `local`. On the new machine, run `bootstrap.sh` as above, save the code to a file, and point the installer at the central server:

```bash
sudo deploy/host/install.sh --role host --server-url http://100.64.0.1:7700 --join-code-file /root/join-code
```

The host makes its own key, joins with the code, and from then on signs in with that key. Delete the code file afterwards; it can't be used again anyway. `ephemera-host join` prints the key's fingerprint, so you can check it against `ephemera-server` logs or the dashboard.

To take a host out of an account, `DELETE /v1/hosts/<id>` with one of the account's tokens. Its sandboxes are destroyed, and it can't sign in again until it joins with a new code.

## Settings

`/etc/ephemera/ephemera.env` holds the addresses and settings. To change them, run `deploy/host/install.sh` again with new options rather than editing the file. Useful options:

- `--listen-ip IP`: serve on another private address instead of the Tailscale one.
- `--name NAME`: the host's name in its account, in records and in `ephemera ls`. It is set when the host joins.
- `--host-args "..."`: extra `ephemera-host` flags, for example `--capacity 40` (most sandboxes at once; default 20), `--max-lifetime 8h` (oldest a sandbox may get; default 24h) or `--max-snapshot-bytes` (space for setup snapshots; default 20 GB).

## Postgres instead of files

By default the central server keeps its tokens and sandboxes in files under `/var/lib/ephemera/server`. That's all one machine needs. To keep them in Postgres instead, which also keeps a history of finished sandboxes, set the database's URL in `/etc/ephemera/ephemera.env` and restart the server:

```bash
EPHEMERA_DATABASE_URL=postgres://ephemera:PASSWORD@db.internal:5432/ephemera
```

The server creates its tables on first start and updates them on upgrades. `ephemera-server token` commands use the database when `EPHEMERA_DATABASE_URL` is set, so run them with it: `sudo --preserve-env=EPHEMERA_DATABASE_URL ephemera-server token ls`, or pass `--database URL`. Tokens in the files are not copied over; make new ones.

In Postgres, token names are unique within an account rather than across them. When two accounts have a token with the same name, revoke or change it by its id.

### Sign in with GitHub

With Postgres, people can also sign in with GitHub and make their own tokens, each in an account of their own. Make a GitHub OAuth app (GitHub, Settings, Developer settings, OAuth Apps) with the callback URL `https://<your server>/auth/github/callback`, then add to `/etc/ephemera/ephemera.env`:

```bash
EPHEMERA_GITHUB_CLIENT_ID=...
EPHEMERA_GITHUB_CLIENT_SECRET=...
EPHEMERA_PUBLIC_URL=https://ephemera.example.com
EPHEMERA_DASHBOARD_URL=https://ephemera.example.com/dashboard
```

With sign-in on, `ephemera login` in a terminal signs a machine in through the browser: it shows a code like `K7QM-2XRT` and opens the dashboard's `/device` page, where the person checks the code and approves. The CLI then gets a device token named after the machine, listed under the account's tokens. The code works for 10 minutes, and its token can be collected once; the server makes the token only then, so it never stores it in the clear. `ephemera login --with-token`, or piping a token in, still works, for CI.

Sign-in asks GitHub only for `read:user`: the person's id, login, name and avatar, never their repositories. GitHub's access token is used once to read those and isn't kept. A GitHub user's account is `gh-` and their GitHub user id, so renaming their login changes nothing. Sessions last 30 days and live in an `HttpOnly` cookie; only a hash of each is stored. Requests that change something with a session must come from the dashboard's own site.

The server has no HTTPS of its own yet, so put it behind a proxy that has it, and use `https://` in both URLs: browsers only send the session cookie over HTTPS.

## Upgrade

Pull and run the installer again:

```bash
git pull && sudo deploy/host/install.sh
```

It rebuilds the binaries, keeps the secret, tokens and image, and restarts the services. Restarting the host agent destroys the sandboxes running on it, so upgrade when none are in use.

## Operate

```bash
systemctl status ephemera-server ephemera-host
```

```bash
journalctl -u ephemera-host -f
```

```bash
sudo ephemera-host bench
```

`ephemera-host bench` measures the machine: start times, density, setup snapshots and output latency. Stop the host agent first (`sudo systemctl stop ephemera-host`) so the two don't compete.

What lives where:

| Path | What |
| --- | --- |
| `/etc/ephemera/ephemera.env` | addresses and settings (root only) |
| `/var/lib/ephemera/host.json` | which server and account the host joined, and its host id |
| `/var/lib/ephemera/server/hosts.json` | the hosts that joined, and unused join codes (as hashes) |
| `/var/lib/ephemera/server/` | API tokens, stored as hashes, and the central server's state |
| `/var/lib/ephemera/host.key` | the key the host signs records with; keep it to keep verifying old records |
| `/var/lib/ephemera/image/` | the guest kernel and root filesystem |
| `/var/lib/ephemera/snapshots/` | encrypted setup snapshots, least recently used dropped first |

The central server runs as its own `ephemera-server` user, with no privileges and a read-only view of the system. The host agent runs as root because the jailer gives every microVM its own user, chroot and cgroup limits.

## Uninstall

```bash
sudo deploy/host/install.sh --uninstall
```

This stops the services and removes the binaries, but keeps `/var/lib/ephemera` and `/etc/ephemera`. Add `--purge` to delete those too: tokens, the guest image, setup snapshots and the host key.

## Security notes

- **Files, commands and output go from your laptop to a host over TLS 1.3**, with a certificate made from the host's own key. The CLI checks that key against the one the host joined with, and pins it in `known_hosts.json` next to your login the first time, like ssh. If a host's key ever changes, the CLI refuses to connect and says so; `ephemera hosts trust <host>` accepts the new key on purpose. Set `EPHEMERA_STRICT_HOSTS=1` to refuse hosts not pinned yet, for CI. `ephemera hosts` lists each host's key and whether it's pinned.
- **The central server still serves plain HTTP**, including API tokens. Keep it on a private network, or put a TLS proxy in front of it. Don't serve it on a public address; the installer refuses to.
- **A join code lets a machine become a host in your account.** Anyone who uses it first can receive your sandboxes, and the code in them. It works once, for 15 minutes; hand it over the way you would a password.
- **A host's key is its identity.** `/var/lib/ephemera/host.key` signs the host in and signs its records. Keep it root-only. If it leaks, remove the host and join again with a new key.
- **A record is only as trustworthy as its host's key.** `ephemera verify` checks a record against the key the central server has for that host.
