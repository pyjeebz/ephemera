# ephemera

The start of something great...

Ephemera runs your dev servers, tests and coding agents in short-lived,
locked-down sandboxes. You keep editing locally; commands run in a sandbox
that has no outbound network unless you allow it, and that is destroyed when
the command ends.

> **Status: early development.** Sandboxes currently run on a Docker
> backend, which is for building ephemera only: containers share the host
> kernel. Firecracker microVMs are next.

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
| `--net` | Allow outbound network access (off by default) |
| `--include FILE` | Send an ignored file anyway |
| `--env KEY=VALUE` | Set an environment variable |
| `--image`, `--cpus`, `--memory` | Sandbox image and limits |
| `--json` | Newline-delimited JSON events, for agents and scripts |
| `-p PORT`, `-p LOCAL:SANDBOX` | `dev` only: forward a port to localhost |

`ephemera run` exits with the command's exit code, or 125 if ephemera itself
failed. `ephemera ls` and `ephemera rm` list and remove sandboxes.

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
internal/api          shared types
internal/client       CLI side: central server and host calls, run, dev
internal/config       saved CLI login
internal/control      central server
internal/host         host agent API
internal/sandbox      backend interface, Docker backend, test backend
internal/stream       output framing (stdout, stderr, exit code)
internal/worktree     which files to send, tar packing, upload checks
```
