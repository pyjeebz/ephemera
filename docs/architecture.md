# Architecture

ephemera is a control-plane daemon (`ephemerad`) that boots, isolates, and tears down Firecracker
microVMs on a single Linux host, plus a CLI (`eph`) and — later — an MCP server and web UI. Everything
runs locally; there are no cloud dependencies.

## Components

| Component      | What it is                                                              |
| -------------- | ----------------------------------------------------------------------- |
| `ephemerad`    | Long-running daemon. HTTP control API, owns VM lifecycle. Unprivileged. |
| `eph`          | CLI client for the daemon (also drives a machine directly). Unprivileged. |
| `eph-jail`     | Helper spawned per VMM: enters the namespaces, pivots, execs Firecracker. |
| `eph-netadmin` | The one privileged binary — carries `CAP_NET_ADMIN`, creates/destroys TAPs. |
| Guest agent    | Tiny in-VM process (over vsock) for exec / file / tty.                  |
| MCP server     | Exposes the sandbox as MCP tools so Claude Code can drive it.           |
| Web UI         | SvelteKit desktop-in-browser (VNC) — last phase.                       |

## Control API

`ephemerad` listens on a Unix socket (`run/ephemerad.sock`, mode 0600) by default — a local-first daemon
has no reason to be on the network, and file permissions beat an open port.

| Method   | Path                        | Purpose                                        |
| -------- | --------------------------- | ---------------------------------------------- |
| `GET`    | `/healthz`                  | liveness                                       |
| `POST`   | `/v1/machines`              | boot a machine, returns once its agent is ready |
| `GET`    | `/v1/machines`              | list                                           |
| `GET`    | `/v1/machines/{id}`         | inspect                                        |
| `DELETE` | `/v1/machines/{id}`         | destroy                                        |
| `POST`   | `/v1/machines/{id}/exec`    | run a command, streams NDJSON frames           |

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
- [x] **Phase 1 — Go control plane MVP.** `ephemerad` drives Firecracker through our own client
      (see [decision 0001](decisions/0001-own-firecracker-client.md)). Machines are booted, tracked,
      executed in, and destroyed; commands reach the guest over a vsock agent, so a machine needs no
      network interface to be useful.
      _Checkpoint met: `eph run uname -a` boots, executes, and cleans up in ~2.5s. Managed machines via
      `eph create/ls/exec/rm`; orphans from a crashed daemon are reaped on restart._
- [x] **Phase 2 — Isolation & networking.** _Checkpoint met: a machine has a filtered network, capped
      resources, and runs jailed — with the daemon holding **no capability at all**. The one privileged
      component is `eph-netadmin`, a small setcap'd helper that does nothing but create and destroy TAPs
      (see [decision 0005](decisions/0005-privileged-network-helper.md)); everything else is a one-time
      host-setup step or an unprivileged user namespace._
      - [x] **Networking & egress.** A point-to-point `/30` per machine over a TAP device — no bridge, so
            machine-to-machine traffic is a routing decision the host firewall refuses rather than local
            delivery it never sees. Guest self-configures from the kernel `ip=` cmdline (no DHCP, no
            in-guest code). A static nftables table (`build/host-setup.sh`) masquerades the pool out and
            drops all egress to private space — public internet only, isolation by default. The daemon
            holds `CAP_NET_ADMIN` for TAP creation and touches no firewall at runtime.
            See [decision 0002](decisions/0002-networking-and-isolation-model.md).
      - [x] **Resource caps.** cgroup v2 CPU/memory per machine. One leaf per VMM under a delegated
            subtree; the VMM is spawned straight into it with `CLONE_INTO_CGROUP`, so it is capped before
            its first instruction. `memory.max` = guest RAM + 64 MiB headroom, `cpu.max` = vCPUs in cores.
            Caps apply automatically when the subtree is available (a restriction, not a grant).
            See [decision 0003](decisions/0003-resource-caps-via-delegated-cgroup.md).
      - [x] **The jail.** Each VMM confined to a `pivot_root` chroot and its own user, mount, and pid
            namespaces — entered unprivileged via a user namespace (`eph-jail` helper), so no root and no
            new capability. Net namespace stays shared so the firewall still applies; the TAP is opened by
            the jailed VMM via the TAP-owner exception. Firecracker's own seccomp is the syscall boundary.
            Opt-in (`-jail`) for now. See [decision 0004](decisions/0004-hand-rolled-unprivileged-jail.md).
            _A capped daemon could not create the user namespace (the kernel forbids it), which is why the
            network capability had to leave the daemon — see [decision 0005](decisions/0005-privileged-network-helper.md)._
- [~] **Phase 3 — Snapshots, fork & warm pool.** (The vsock guest agent's exec path landed in Phase 1.)
      - [x] **Snapshot & restore.** Pause a running guest, write its device state and RAM to two files
            (`PUT /snapshot/create`), and bring it back in a fresh VMM (`PUT /snapshot/load`, resume). The
            restored guest is the *same running instance* — its agent is already up, so the machine is ready
            with no boot to wait through. Measured **~63 ms to restore vs ~1 s to cold-boot**; verified it is
            a resume not a reboot (PID 1's start time is unchanged).
      - [x] **Fork (zero-copy).** Restore one snapshot into several machines at once, each independent. The
            jail gives every copy its own vsock socket (`/run/vsock.sock` is a different host path in each
            chroot); the read-only base image is **shared with no copy**, and each fork's writes live in the
            RAM overlay its restored memory carries. **Measured 15–31 ms per fork.** Every machine now boots
            a read-only base with a tmpfs overlay, which also fixes a latent bug (concurrent machines used to
            share one writable image). See [0006](decisions/0006-fork-via-jailed-snapshots.md) (vsock) and
            [0007](decisions/0007-read-only-base-with-ram-overlay.md) (disk).
      - [x] **Warm pool.** `internal/pool` keeps N machines pre-forked and resumed, agents up. `Get` takes
            one and reforks a replacement in the background, so a request waits for a channel receive, not a
            VM. **Measured ~12 µs to serve** from a full pool (vs ~20 ms to fork, ~1 s to boot).
      _Checkpoint: fork a running VM in <100 ms ✅ (15–31 ms); warm pool serves instant machines ✅ (~12 µs)._
- [ ] **Phase 4 — AI agent loop.** Anthropic-driven loop with exec/file tools, budgets, rate limits,
      safety; exposed MCP-native.
      _Checkpoint: "build a snake game" runs end-to-end in a sandbox._
- [ ] **Phase 5 — Web UI + live desktop.** VNC/RFB desktop, SvelteKit UI, live preview URLs.
      _Checkpoint: a browser desktop you watch the agent use._

## Host requirements (verified on this box)

- KVM present (`/dev/kvm`, world-writable), nested virt enabled — Firecracker runs.
- cgroup **v2**; `nft` and `iptables-nft` both present on the **nf_tables** backend.
- Guest kernel built with `CONFIG_IP_PNP=y` and `CONFIG_VIRTIO_NET=y` — the guest configures its own
  network from the kernel command line, so there is no in-guest network code.
- Phases 0–1 need **no root**. The daemon and CLI hold **no capability**; networking's `CAP_NET_ADMIN`
  lives on the `eph-netadmin` helper alone, granted by a one-time `build/host-setup.sh` (which also sets up
  forwarding, the firewall, and the cgroup delegation). Jailing needs only that the kernel permit
  unprivileged user namespaces.
