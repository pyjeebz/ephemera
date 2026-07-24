# ephemera

Spin up a **box** — a fast, isolated Linux computer — and use it like a laptop. Self-hosted, local-first,
no cloud.

A box is a Firecracker microVM: it boots in about a second, forks from a snapshot in tens of milliseconds,
and is thrown away when you're done — or kept, with its state intact, if you name it. You get a real
terminal, a real network, real isolation. ephemera is **agent-agnostic**: it gives you the computer, and
you run whatever you like on it — your own agent (Claude Code, Codex, aider), your tools, your code.

```console
$ eph new dev              # spin up a box named "dev" (kept)
$ eph ssh dev              # a real terminal inside it
$ eph scp ./app.py dev:/root/app.py
$ eph stop dev             # park it — its disk survives
$ eph start dev            # back, exactly as you left it

$ eph new                  # a throwaway box (no name, gone when you remove it)
$ eph fork dev-snapshot    # a clone, up in milliseconds
$ eph list                 # your boxes
```

The command is `ephemera`; `eph` is a shorter alias for the same thing.

## Principles

1. **Local-first / zero-cloud.** No S3, no managed services. Root images are built locally from Docker
   images; everything lives on one Linux box (a WSL2 machine with KVM works too).
2. **Isolation by default, without root.** Each box runs jailed (its own namespaces), on a filtered network
   (public internet only — nothing private), with capped CPU and memory. The daemon itself holds **no
   privilege**: the one capability needed for networking lives in a tiny helper, nothing else.
3. **Agent-agnostic.** ephemera is the computer, not the agent. Bring your own — install it in a box and
   run it there, like any other program.

## How it's built

Under the hood: a read-only base image shared by every box with a copy-on-write overlay (so a fork copies
nothing), snapshot/restore of a running box, a warm pool that serves pre-forked boxes in microseconds, a
guest agent over vsock for exec and interactive shells, and a control-plane daemon (`ephemerad`) the CLI
talks to. All hand-rolled on Firecracker's API — see [`docs/architecture.md`](docs/architecture.md) for the
phased build and the [decision records](docs/decisions/) for the why.

## Status

A working personal tool, built in the open, phase by phase. Boxes boot, fork, snapshot, persist, and give
you a terminal today; a graphical desktop is planned. See the roadmap for where things are.

## Requirements

- Linux with KVM (`/dev/kvm`), or WSL2 with nested virtualization
- Go 1.26+
- Docker (to build the root image locally)
- Firecracker (installed into `~/.local/bin` by `build/install-firecracker.sh`)

## Build

```console
$ build/install-firecracker.sh   # firecracker into ~/.local/bin
$ build/fetch-kernel.sh          # a guest kernel
$ build/build-rootfs.sh          # the box's root image (needs Docker)
$ build/build.sh                 # ephemerad, ephemera (+ eph alias), helpers
$ sudo build/host-setup.sh       # one-time: networking + cgroup delegation
```

## License

MIT — see [LICENSE](LICENSE).
