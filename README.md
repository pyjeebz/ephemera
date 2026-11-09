# ephemera

The start of something great...

Ephemera runs your dev servers, tests and coding agents in short-lived,
locked-down sandboxes. You keep editing locally; commands run in a sandbox
that has no outbound network unless you allow it, and that is destroyed when
the command ends.

> **Status: early development.** Sandboxes run as Firecracker microVMs
> on a Linux host with KVM (`--backend firecracker`), or on Docker, which is
> for building ephemera only: containers share the host kernel.

## How it works

| Part | Binary | Job |
| --- | --- | --- |
| CLI | `ephemera` | Syncs your worktree and runs commands |
| Central server | `ephemera-server` | Tracks hosts and places sandboxes. Never sees files, output or secrets |
| Host agent | `ephemera-host` | Runs the sandboxes on a Linux machine |

`ephemera run` asks the central server for a sandbox, sends your files
straight to the host, streams the command's output back, and destroys the
sandbox, even if you press Ctrl-C.

Only files git would track are sent: tracked files and untracked files that
are not ignored. Ignored files such as `.env.local` stay on your machine
unless you pass `--include`.

## Try it locally

Needs Go 1.24+ and Docker on Linux. The host agent runs as root, so it can
reach ports inside sandboxes.

```sh
go build -o bin/ ./cmd/...
export EPHEMERA_HOST_SECRET=dev-secret   # shared by the server and its hosts

bin/ephemera-server token create my-laptop > token.txt
bin/ephemera-server &
bin/ephemera-host &

bin/ephemera login < token.txt && rm token.txt
bin/ephemera run -- sh -c 'ls && uname -a'
```

`ephemera login` checks the token and saves it, readable only by you, in
your user config directory. `ephemera logout` removes it. The variables
`EPHEMERA_SERVER` and `EPHEMERA_TOKEN` override the saved login, which is
handy in CI.

The central server keeps its API tokens (stored only as hashes) and its list
of running sandboxes in a state directory: `/var/lib/ephemera` as root,
otherwise your user config directory, or `--state DIR`. Manage tokens with
`ephemera-server token create|ls|revoke`; the running server picks up
changes. Each host reports its sandboxes every 15 seconds, and any sandbox
the central server has no record of is destroyed, so nothing keeps running
unseen.

For a dev server, use `ephemera dev`. It keeps the sandbox running, forwards
ports to your machine, and sends only the files you change, so hot reload
works as usual:

```sh
bin/ephemera dev -p 3000 --net -- sh -c 'npm install && npm run dev'
```

Then open http://localhost:3000. Ctrl-C stops the dev server and destroys
the sandbox. Ports work even when the server listens only on the sandbox's
own `127.0.0.1`.

Flags for `run` and `dev`:

| Flag | Meaning |
| --- | --- |
| `--allow HOST` | Allow outbound access to a host: `example.com`, `*.example.com`, `host:port` (repeatable) |
| `--net` | Allow outbound access to any public host |
| `--include FILE` | Send an ignored file anyway |
| `--env KEY=VALUE` | Set an environment variable |
| `--image`, `--cpus`, `--memory` | Sandbox image and limits |
| `--json` | Newline-delimited JSON events, for agents and scripts |
| `-p PORT`, `-p LOCAL:SANDBOX` | `dev` only: forward a port to localhost |

### Network

Sandboxes have no network unless you allow it:

```sh
bin/ephemera run --allow registry.npmjs.org -- npm install
```

A microVM has no network device at all. Programs in it reach the outside
only through an HTTP proxy at `127.0.0.1:3128` (`HTTPS_PROXY` and
`HTTP_PROXY` are set for them), which carries each connection over vsock to
an egress proxy on the host for that sandbox alone. It lets a connection
through only if the host is allowed and resolves to a public address:
private networks, localhost, cloud metadata servers and Tailscale addresses
stay blocked even with `--net`, as do connections to raw IP addresses.
Programs that ignore the proxy settings cannot connect at all.

Every attempt is logged. `ephemera run` ends with a summary that names each
blocked host, `ephemera dev` reports blocked connections as they happen, and
`--json` emits a `connection` event for each. On Docker, `--net` gives the
container ordinary networking and `--allow` is not available.

`ephemera run` exits with the command's exit code, or 125 if ephemera itself
failed. `ephemera ls` and `ephemera rm` list and remove sandboxes.

## Setting up a host

A host is any x86_64 Linux machine with KVM (Ubuntu 24.04). On a fresh
machine:

```sh
sudo deploy/host/bootstrap.sh --hostname my-host
```

It checks for KVM, installs Firecracker and Go, creates an `ephemera` user,
and joins Tailscale with SSH. Add `--lock-public-ssh` on a server with a
public IP once Tailscale works, and `--idle-shutdown 30` on a cloud test
server to power it off after 30 idle minutes
(`deploy/host/idle-shutdown.sh status` shows why it is awake).

The microVM image is built on the host:

```sh
sudo deploy/guest/build-image.sh /var/lib/ephemera/image
```

It writes a kernel and an Ubuntu 24.04 root filesystem whose init is the
ephemera guest agent (`cmd/ephemera-guest`), with Python and pip, git,
Node.js 24 LTS with npm, and build tools (gcc, make) for native packages.
Downloads are checked against pinned SHA-256 sums; the build needs network
for Ubuntu's packages. Then run the host agent on microVMs:

```sh
sudo -E bin/ephemera-host --backend firecracker
```

Every sandbox boots that image read-only, with a disk of its own that
holds everything it writes, so `apt install` works. The disk is deleted
with the sandbox.

As root, the host agent runs every microVM under Firecracker's jailer: in a
chroot holding only that VM's files, as a user ID of its own, with cgroup
limits on memory, CPU and processes. Jails go in `--jail-dir`
(`/var/lib/ephemera/jail`), which must be on the same filesystem as the
image. `--jail=false` turns this off for development. The host reaches the guest agent over vsock, so the VM
needs no network to run commands or serve forwarded ports.

The microVM tests use the same image:

```sh
EPHEMERA_GUEST_IMAGE=/var/lib/ephemera/image go test ./internal/sandbox/firecracker/ ./internal/e2e/
```

Run them as root, with `EPHEMERA_JAILED=1` for the end-to-end tests, to
test with the jailer.

## Development

```sh
go vet ./...
go test -race ./...
```

The end-to-end tests in `internal/e2e` run against Docker when it is
available and skip that part otherwise.

## Layout

```
cmd/ephemera          CLI
cmd/ephemera-server   central server
cmd/ephemera-host     host agent
cmd/ephemera-guest    agent inside each microVM (its init)
internal/api          shared types
internal/client       CLI side: central server and host calls, run, dev
internal/config       saved CLI login
internal/control      central server
internal/egress       per-sandbox egress proxy: allow lists, address checks, log
internal/guest        guest agent API (inside the microVM)
internal/host         host agent API
internal/sandbox      backend interface; Docker, Firecracker and test backends
internal/stream       output framing (stdout, stderr, exit code)
internal/worktree     which files to send, tar packing, upload checks
deploy/guest          microVM image build
deploy/host           host setup and idle shutdown scripts
```
