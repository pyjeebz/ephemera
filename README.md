# ephemera

On-demand Linux microVMs, controllable by AI agents — self-hosted, local-first, no cloud dependencies.

Firecracker microVMs under a small control-plane daemon, with an AI agent loop that can drive a live
machine. Built to run on my own hardware.

## Principles

1. **Local-first / zero-cloud** — no S3, no managed services. Rootfs images are built locally from Docker
   images; storage is on local disk. It runs on one Linux box (or a WSL2 machine with KVM).
2. **Stronger isolation by default** — egress is allow-listed, not open; resources are cgroup-capped;
   VMs run under the jailer.
3. **MCP-native agent loop** — the sandbox is exposed as an MCP server so Claude Code (and other clients)
   can drive it directly.

## Status

Early. See [`docs/architecture.md`](docs/architecture.md) for the phased roadmap and where we are.

## Requirements

- Linux with KVM (`/dev/kvm`), or WSL2 with nested virtualization enabled
- Go 1.26+
- Docker (for building rootfs images locally)
- Firecracker + jailer (installed into `~/.local/bin` by the setup steps)

## License

MIT — see [LICENSE](LICENSE).
