# Architecture

ephemera is a control-plane daemon (`ephemerad`) that boots, isolates, and tears down Firecracker
microVMs on a single Linux host, plus a CLI (`eph`) and — later — an MCP server and web UI. Everything
runs locally; there are no cloud dependencies.

## Components

| Component     | What it is                                                              |
| ------------- | ----------------------------------------------------------------------- |
| `ephemerad`   | Long-running daemon. HTTP control API, owns VM lifecycle.               |
| `eph`         | CLI client for the daemon.                                              |
| Guest agent   | Tiny in-VM process (over vsock) for exec / file / tty.                  |
| MCP server    | Exposes the sandbox as MCP tools so Claude Code can drive it.           |
| Web UI        | SvelteKit desktop-in-browser (VNC) — last phase.                       |

## Host layout

- **Kernel**: a Firecracker-compatible `vmlinux`, shared read-only across VMs.
- **Rootfs**: ext4 images built locally from Docker images (no cloud registry needed at runtime).
- **Runtime dir**: per-VM sockets, jailer chroots, logs under `./run/`.

## Roadmap

Each phase is a vertical slice that boots to a working checkpoint.

- [x] **Phase 0 — Boot one microVM by hand.** Install Firecracker + jailer, fetch a kernel, build a
      minimal rootfs from a Docker image, boot it via the API socket, get a serial console.
      _Checkpoint met: guest boots in ~1.0s, selftest passes, VM self-terminates with exit 0
      (~1.7s wall for the whole cycle). Firecracker v1.16.1, kernel 6.1.128, Alpine 3.24.1._
- [ ] **Phase 1 — Go control plane MVP.** `ephemerad` wraps firecracker-go-sdk. `POST /machines` boots,
      exec a command, `DELETE` tears down and cleans up.
      _Checkpoint: `eph run "uname -a"` boots, runs, cleans up._
- [ ] **Phase 2 — Isolation & networking.** TAP + bridge + NAT egress, egress allow-list by default,
      cgroup v2 CPU/mem caps, the jailer.
      _Checkpoint: VM has filtered network, capped resources, runs under jailer._
- [ ] **Phase 3 — Guest agent, snapshots & fork.** vsock guest agent (exec/files/tty), snapshot/restore,
      fork-in-ms, warm pool.
      _Checkpoint: fork a running VM in <100 ms; warm pool serves instant machines._
- [ ] **Phase 4 — AI agent loop.** Anthropic-driven loop with exec/file tools, budgets, rate limits,
      safety; exposed MCP-native.
      _Checkpoint: "build a snake game" runs end-to-end in a sandbox._
- [ ] **Phase 5 — Web UI + live desktop.** VNC/RFB desktop, SvelteKit UI, live preview URLs.
      _Checkpoint: a browser desktop you watch the agent use._

## Host requirements (verified on this box)

- KVM present (`/dev/kvm`, world-writable), nested virt enabled — Firecracker runs.
- cgroup **v2**, iptables on the **nf_tables** backend.
- Phases 0–1 need **no root**. Phase 2+ runs a privileged daemon (TAP / iptables / jailer).
