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
sandbox, even if you press Ctrl-C. The CLI makes each sandbox's access
token itself and gives the central server only its hash, so the server
can place a sandbox but can't open it.

Only files git would track are sent: tracked files and untracked files that
are not ignored. Ignored files such as `.env.local` stay on your machine
unless you pass `--include`.

## Install the CLI

```sh
curl -fsSL https://ephemera.run/install | sh
```

It downloads the latest release for your OS and CPU (Linux or macOS, x86_64
or arm64), checks it against the release's `checksums.txt`, and installs
`ephemera` into `/usr/local/bin`, or `~/.local/bin` if that isn't writable.
`EPHEMERA_VERSION=v0.1.0` installs a specific release, and
`EPHEMERA_INSTALL_DIR` picks the folder. On Windows, download the zip from
the [releases page](https://github.com/pyjeebz/ephemera/releases).

Then connect it to a server: `ephemera login --server <URL>`. To run your own
server and hosts, see [docs/self-hosting.md](docs/self-hosting.md).

## Try it locally

Needs Go 1.25+ and Docker on Linux. The host agent runs as root, so it can
reach ports inside sandboxes.

```sh
go build -o bin/ ./cmd/...

bin/ephemera-server token create my-laptop > token.txt
bin/ephemera-server &

# The host joins the server once, with a one-time code.
bin/ephemera-server join-code create | sudo bin/ephemera-host join --server http://127.0.0.1:7700
sudo bin/ephemera-host &

bin/ephemera login < token.txt && rm token.txt
bin/ephemera run -- sh -c 'ls && uname -a'
```

`ephemera-host join` keeps the host's key in `/var/lib/ephemera/host.key`
and what it joined as in `/var/lib/ephemera/host.json`. From then on the
host signs in to the server with that key; there is no shared secret.

`ephemera login` checks the token and saves it, readable only by you, in
your user config directory. On a server with GitHub sign-in, running it in
a terminal signs you in through the browser instead: it shows a code, opens
the approval page, and saves a token for this machine once you approve.
`--with-token` reads a token from stdin either way. `ephemera logout` removes it. The variables
`EPHEMERA_SERVER` and `EPHEMERA_TOKEN` override the saved login, which is
handy in CI. If a command can't reach the server or a host, `ephemera doctor`
checks the login, the network, the token and the hosts, and says what to fix.

The central server keeps its API tokens (stored only as hashes) and its list
of running sandboxes in a state directory: `/var/lib/ephemera` as root,
otherwise your user config directory, or `--state DIR`. Manage tokens with
`ephemera-server token create|ls|revoke`; the running server picks up
changes. Each host keeps a connection open to the central server, which
sends it work over that connection, so the server never needs to reach the
host. Every 15 seconds the host lists its sandboxes, and any the central
server has no record of is destroyed, so nothing keeps running unseen.

For a dev server, use `ephemera dev`. It keeps the sandbox running, forwards
ports to your machine, and sends only the files you change, so hot reload
works as usual:

```sh
bin/ephemera dev -p 3000 --net -- sh -c 'npm install && npm run dev'
```

Then open http://localhost:3000. Ctrl-C stops the dev server and destroys
the sandbox. Ports work even when the server listens only on the sandbox's
own `127.0.0.1`.

Several branches can run at once on the same port. `-p 3000` shares the
port through a small local router that the first session starts: each
session is at `http://<branch>.localhost:3000` (or `--name`), and plain
`localhost:3000` shows the newest one. `ephemera routes` lists them and
`ephemera focus <name>` switches plain localhost. The router exits 30
seconds after the last session ends. `-p 8080:3000` binds local port 8080
for one session only, without the router.

Flags for `run` and `dev`:

| Flag | Meaning |
| --- | --- |
| `--allow HOST` | Allow outbound access to a host: `example.com`, `*.example.com`, `host:port` (repeatable) |
| `--net` | Allow outbound access to any public host |
| `--include FILE` | Send an ignored file anyway |
| `--env KEY=VALUE` | Set an environment variable |
| `--image`, `--cpus`, `--memory` | Sandbox image and limits |
| `--json` | Newline-delimited JSON events, for agents and scripts |
| `-p PORT`, `-p LOCAL:SANDBOX` | `dev` only: forward a port, shared by session name or bound locally |

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

### Setup snapshots

```sh
bin/ephemera run --setup "npm ci" --allow registry.npmjs.org -- npm test
```

`--setup` runs a command before the real one, then saves the prepared
sandbox as a project snapshot on the host. Later runs start from it and
skip setup. The snapshot is tied to the project, the setup command, the
sandbox size and the project's lockfiles (`package-lock.json`,
`requirements.txt`, `go.sum` and others; `--setup-input FILE` adds more), so
changing any of them makes a new one. It works with `dev` too.

A snapshot holds the project's code and dependencies, so the host keeps it
only encrypted (AES-256-GCM), with a key that stays in your config
directory and goes straight to the host when needed, never through the
central server. The host decrypts it only into the folder of the sandbox
using it, which is deleted with the sandbox. Snapshots made on another
guest image are dropped; the least recently used go once they pass
`--max-snapshot-bytes` (20 GB) on the host.

Secrets stay out of snapshots. Files sent with `--include`, such as
`.env.local`, are added only after setup has finished and the snapshot is
saved, and `--env` values go only to the main command. Setup can't read
them, so a setup that needs a credential, such as a private registry token,
isn't supported yet.

### Records

When a sandbox is destroyed, its host signs a record of what happened in
it: every command with its exit code and duration, every file sync, every
network connection allowed or blocked, and that its disk was deleted. It
holds no output, file contents or environment values. The CLI saves it in
your config directory (`EPHEMERA_RECORDS` or `--record FILE` to choose):

```sh
bin/ephemera verify ~/.config/ephemera/records/sbx_6abe91d8a77dfffa.json
```

`verify` checks the signature and that the signing key is the one the
central server has for that host, then prints the record. Any change to a
record makes it fail. Each host keeps its key in `--key-file`
(`/var/lib/ephemera/host.key`), created on first start.

### Time limits

`ephemera run --timeout 10m` stops the command after ten minutes with exit
code 124. `ephemera dev --timeout 2h` destroys the sandbox after two hours.
Every host also has a maximum lifetime for any sandbox, `--max-lifetime`
(24 hours by default), so a forgotten sandbox cannot run forever.

### Usage limits

Each API token can have limits, set when it is created or later:

```sh
bin/ephemera-server token create ci-agent --max-sandboxes 5 --max-minutes 600
bin/ephemera-server token limits ci-agent --max-sandboxes 2 --max-minutes 120
```

`--max-sandboxes` caps how many run at once; `--max-minutes` caps
sandbox-minutes per UTC day. Over a limit, new sandboxes are refused; a new
sandbox's lifetime is capped at the minutes left today, and running ones
are stopped when the day's minutes run out. `ephemera usage` shows a
token's use and limits.

### Coding agents (MCP)

`ephemera mcp` serves ephemera to a coding agent over the Model Context
Protocol, so the agent runs commands in sandboxes instead of on your
machine. For Claude Code:

```sh
claude mcp add ephemera -- ephemera mcp
```

Other agents take the same command in their MCP settings:

```json
{ "mcpServers": { "ephemera": { "command": "ephemera", "args": ["mcp"] } } }
```

It offers `run_command` (a shell command in a fresh sandbox, with
optional `allow`, `network_open`, `timeout_seconds`, `include` and `env`;
it returns the exit code, output, a network summary naming blocked hosts,
and the record's path), `list_sandboxes`, `destroy_sandbox` and
`verify_record`.

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

Then install ephemera as services:

```sh
sudo deploy/host/install.sh
```

It builds the binaries and the guest image, runs the central server and
host agent under systemd on the machine's Tailscale address, and prints a
first API token. [docs/self-hosting.md](docs/self-hosting.md) covers
tokens, more hosts, upgrades and what lives where. The rest of this
section is for running the parts by hand while developing.

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
sudo bin/ephemera-host --backend firecracker
```

Every sandbox boots that image read-only, with a disk of its own that
holds everything it writes, so `apt install` works. The disk is deleted
with the sandbox.

When it starts, the host agent boots one VM, waits for its guest agent and
saves a snapshot of it (about 8 seconds). Sandboxes of the default size then
start by restoring that snapshot instead of booting: about 65 ms instead of
about 900 ms. Each restored sandbox gets its own copy of the snapshot's
disk, has its clock set and its random number generator reseeded from the
host, so no two sandboxes share random numbers, and shares the snapshot's
memory until it writes to it: an idle sandbox costs the host about 60 MB.
Other sizes boot as before. `--snapshots=false` turns this off.

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

### Benchmarks

```sh
sudo bin/ephemera-host bench --compare bench-old.json
```

Measures the host with the real backend, host agent, central server and
client: cold boot, snapshot restore, `ephemera run` end to end, output
latency, a setup snapshot's save and load, and `--density` (20) sandboxes
started at once with their memory. It takes a few minutes, prints a table
and writes the results, with the commit, machine and settings, to
`bench-<commit>-<time>.json`. `--compare` shows each result's change from
an earlier report. Stop the host agent first, so the two don't compete
for the machine.

## Development

```sh
go vet ./...
go test -race ./...
```

The end-to-end tests in `internal/e2e` run against Docker when it is
available and skip that part otherwise. The Postgres store's tests run when
`EPHEMERA_TEST_POSTGRES` names a server where they may create databases,
for example a throwaway one in Docker:

```sh
docker run -d --rm -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
EPHEMERA_TEST_POSTGRES='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

CI runs the same checks, with Postgres, plus shellcheck and a release
build, on every push to `main`.

### Releasing

Add a `## vX.Y.Z` section to `CHANGELOG.md` with the release notes, then
either push a tag:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

or run the release workflow by hand from the Actions tab with the version,
which tags the commit it runs on. The workflow tests that commit, builds the
archives with `deploy/release/build.sh` (the CLI for Linux, macOS and
Windows, and the server, host agent and guest agent for Linux x86_64), and
publishes them with `checksums.txt` and `install.sh` on a GitHub release,
using the version's changelog section as its notes. A version with a
suffix, such as `v0.2.0-rc.1`, is published as a pre-release.

## Layout

```
cmd/ephemera          CLI
cmd/ephemera-server   central server
cmd/ephemera-host     host agent
cmd/ephemera-guest    agent inside each microVM (its init)
internal/api          shared types
internal/bench        host benchmarks (ephemera-host bench)
internal/client       CLI side: central server and host calls, run, dev
internal/config       saved CLI login
internal/control      central server
internal/egress       per-sandbox egress proxy: allow lists, address checks, log
internal/guest        guest agent API (inside the microVM)
internal/host         host agent API
internal/mcp          MCP server for coding agents
internal/pgstore      central server storage in Postgres (schema in migrations/)
internal/record       signing and checking sandbox records
internal/router       local router: several dev sessions on one port, by name
internal/sandbox      backend interface; Docker, Firecracker and test backends
internal/snapcrypt    encryption of project snapshots at rest
internal/stream       output framing (stdout, stderr, exit code)
internal/worktree     which files to send, tar packing, upload checks
deploy/guest          microVM image build
deploy/host           host setup, install and idle shutdown scripts
deploy/release        release build
deploy/systemd        systemd services for the central server and host agent
docs                  self-hosting guide
install.sh            CLI install script
CHANGELOG.md          release notes, one section per version
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
