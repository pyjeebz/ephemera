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
export EPHEMERA_TOKEN=dev-token EPHEMERA_HOST_SECRET=dev-secret

bin/ephemera-server &
bin/ephemera-host &

bin/ephemera run -- sh -c 'ls && uname -a'
```

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
internal/client       CLI side: central server and host calls, run
internal/control      central server
internal/host         host agent API
internal/sandbox      backend interface, Docker backend, test backend
internal/stream       output framing (stdout, stderr, exit code)
internal/worktree     which files to send, tar packing, upload checks
```
