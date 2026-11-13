<p align="center">
  <img src="docs/logo/dark.svg#gh-dark-mode-only" alt="ephemera" height="56">
  <img src="docs/logo/light.svg#gh-light-mode-only" alt="ephemera" height="56">
</p>

<p align="center">
  A throwaway microVM for every command, on your own machines.<br>
  <a href="https://ephemera.run/docs">Docs</a> ·
  <a href="https://ephemera.run/docs/quickstart">Quickstart</a> ·
  <a href="https://ephemera.run/docs/security">Security</a> ·
  <a href="CHANGELOG.md">Changelog</a>
</p>

---

ephemera runs your tests, dev servers and coding agents' commands in a fresh Firecracker microVM, on a Linux machine you own, and deletes it when the command ends. You keep editing on your laptop. The sandbox gets the files git tracks, has no network unless you allow it, and leaves a signed record of what it did.

```bash
ephemera run -- npm test
ephemera run --allow registry.npmjs.org -- npm install
ephemera dev -p 3000 --net -- npm run dev
```

- **Nothing runs on your laptop.** An agent's `rm -rf`, a postinstall script, a test that phones home: each happens in a microVM that's gone a second later.
- **No network unless you say so.** A sandbox has no network device. `--allow` lets named hosts through, and every attempt is logged.
- **Fast.** A sandbox starts from a snapshot in about 65 ms; an idle one costs its host about 60 MB.
- **Your machines.** Sandboxes run on hosts you bring. Your code goes from your laptop straight to them, over TLS checked against each host's own key.
- **A receipt.** Each sandbox's host signs a record of every command, file sync and connection. `ephemera verify` checks it.

> **Status: early.** v0.1 is out. The API and flags may still change before 1.0.

## Get started

```bash
curl -fsSL https://ephemera.run/install | sh
ephemera login
```

Sign in with GitHub, approve the code, then [add a host](https://ephemera.run/docs/guides/hosts): any x86_64 Linux machine with KVM. Then, in any git repository:

```bash
ephemera run -- echo hello from a microVM
```

The [quickstart](https://ephemera.run/docs/quickstart) walks through it. To run the central server yourself too, see [Self-hosting](https://ephemera.run/docs/self-hosting).

## How it works

| Part | Binary | Job |
| --- | --- | --- |
| CLI | `ephemera` | Sends your worktree, runs commands, streams output, saves records |
| Central server | `ephemera-server` | Keeps accounts, tokens and hosts, and places sandboxes. Never sees files, output or secrets |
| Host agent | `ephemera-host` | Runs sandboxes as Firecracker microVMs on a Linux machine with KVM |
| Guest agent | `ephemera-guest` | The first process in each microVM; runs your commands |

Hosts keep one connection open to the central server, so they need no open port for it. The CLI makes each sandbox's access token itself and gives the central server only its hash, so the server can place a sandbox but can't open it. [How it works](https://ephemera.run/docs/how-it-works) and [Security and data flow](https://ephemera.run/docs/security) have the details.

## Development

Needs Go 1.25 or later, and Docker on Linux for the end-to-end tests.

```bash
go build -o bin/ ./cmd/...
go vet ./...
go test -race ./...
```

The end-to-end tests in `internal/e2e` run against Docker when it's available and skip that part otherwise. The Postgres store's tests run when `EPHEMERA_TEST_POSTGRES` names a server where they may create databases, for example a throwaway one in Docker:

```bash
docker run -d --rm -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
EPHEMERA_TEST_POSTGRES='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

CI runs the same checks, with Postgres, plus shellcheck and a release build, on every push to `main`.

### Try it locally

A central server and a host on one Linux machine, with the Docker backend. Docker is for building ephemera only: containers share the host's kernel, and allow lists need Firecracker. The host agent runs as root so it can reach ports inside sandboxes.

```bash
go build -o bin/ ./cmd/...

bin/ephemera-server token create my-laptop > token.txt
bin/ephemera-server &

# The host joins the server once, with a one-time code.
bin/ephemera-server join-code create | sudo bin/ephemera-host join --server http://127.0.0.1:7700
sudo bin/ephemera-host &

bin/ephemera login --with-token < token.txt && rm token.txt
bin/ephemera run -- sh -c 'ls && uname -a'
```

### Firecracker hosts

The microVM image is built on the host, with network for Ubuntu's packages:

```bash
sudo deploy/guest/build-image.sh /var/lib/ephemera/image
sudo bin/ephemera-host --backend firecracker
```

The microVM tests use the same image. Run them as root, with `EPHEMERA_JAILED=1` for the end-to-end tests to test with the jailer:

```bash
EPHEMERA_GUEST_IMAGE=/var/lib/ephemera/image go test ./internal/sandbox/firecracker/ ./internal/e2e/
```

`sudo bin/ephemera-host bench --compare bench-old.json` measures a host: cold boot, snapshot restore, `ephemera run` end to end, output latency, setup snapshots and 20 sandboxes at once. Stop the host agent first.

### Docs

The docs at [ephemera.run/docs](https://ephemera.run/docs) are the Mintlify project in `docs/`. Preview them with:

```bash
cd docs && npx mint dev
```

### Releasing

Add a `## vX.Y.Z` section to `CHANGELOG.md` with the release notes, then either push a tag:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

or run the release workflow by hand from the Actions tab with the version, which tags the commit it runs on. The workflow tests that commit, builds the archives with `deploy/release/build.sh` (the CLI for Linux, macOS and Windows, and the server, host agent and guest agent for Linux x86_64), and publishes them with `checksums.txt` and `install.sh` on a GitHub release, using the version's changelog section as its notes. A version with a suffix, such as `v0.2.0-rc.1`, is published as a pre-release.

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
deploy/fly            the hosted central server on Fly.io
deploy/guest          microVM image build
deploy/host           host setup, install and idle shutdown scripts
deploy/release        release build
deploy/systemd        systemd services for the central server and host agent
docs                  the docs site (Mintlify)
site                  ephemera.run: landing page, dashboard and sign-in (Next.js)
install.sh            CLI install script
CHANGELOG.md          release notes, one section per version
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
