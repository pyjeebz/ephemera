# Self-hosting ephemera

ephemera has two server-side parts:

- The **central server** (`ephemera-server`) places sandboxes on hosts and keeps track of them. It never sees your files, command output or secrets.
- A **host agent** (`ephemera-host`) runs sandboxes as Firecracker microVMs. The CLI talks to it directly to send files and stream output.

One machine can run both. Add more hosts later and point them at the same central server.

## What you need

- An x86_64 machine running Ubuntu 24.04, with hardware virtualization (Intel VT-x or AMD-V) turned on. A bare-metal server, a spare PC, or a cloud VM with nested virtualization all work.
- A Tailscale account, or another private network between your laptop and the machine. ephemera serves plain HTTP and has no TLS yet, so the installer refuses to serve on a public address.
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
3. Writes `/etc/ephemera/ephemera.env` with a random host secret and the machine's Tailscale address.
4. Starts both parts as systemd services, `ephemera-server` and `ephemera-host`, and waits until the host has registered.
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

## Add another host

On the new machine, run `bootstrap.sh` as above. Then copy the host secret over from the first machine; it is the `EPHEMERA_HOST_SECRET` line in `/etc/ephemera/ephemera.env`. Put it in a file only root can read, and point the installer at the central server:

```bash
sudo deploy/host/install.sh --role host --server-url http://100.64.0.1:7700 --secret-file /root/ephemera-host-secret
```

Delete the secret file afterwards; the installer has copied it into `/etc/ephemera/ephemera.env`.

## Settings

`/etc/ephemera/ephemera.env` holds the addresses, the host name and the secret. To change them, run `deploy/host/install.sh` again with new options rather than editing the file. Useful options:

- `--listen-ip IP`: serve on another private address instead of the Tailscale one.
- `--name NAME`: the host's name in records and `ephemera ls`.
- `--host-args "..."`: extra `ephemera-host` flags, for example `--capacity 40` (most sandboxes at once; default 20), `--max-lifetime 8h` (oldest a sandbox may get; default 24h) or `--max-snapshot-bytes` (space for setup snapshots; default 20 GB).

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
| `/etc/ephemera/ephemera.env` | addresses and the host secret (root only) |
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

- **Traffic between your laptop and the machine is not encrypted by ephemera.** Tailscale encrypts it. Don't serve ephemera on a public address; the installer refuses to.
- **The host secret lets a machine register as a host.** Anyone who has it can receive sandboxes, and the code in them. Keep it root-only and share it only with your own hosts.
- **A record is only as trustworthy as its host's key.** `ephemera verify` checks a record against the key the central server has for that host.
