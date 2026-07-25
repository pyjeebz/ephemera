# Architecture

ephemera is a control-plane daemon (`ephemerad`) that boots, isolates, and tears down Firecracker
microVMs — **boxes** — on a single Linux host, plus a CLI (`ephemera`, aliased `eph`) and — later — a web
UI. Everything runs locally; there are no cloud dependencies. ephemera is **agent-agnostic**: it gives you
the computer, and you run your own agent (Claude Code, aider, your own) *inside* a box.

## Components

| Component      | What it is                                                              |
| -------------- | ----------------------------------------------------------------------- |
| `ephemerad`    | Long-running daemon. HTTP control API, owns box lifecycle. Unprivileged. |
| `ephemera`     | CLI client for the daemon (also drives a box directly); `eph` is its alias. Unprivileged. |
| `eph-jail`     | Helper spawned per VMM: enters the namespaces, pivots, execs Firecracker. |
| `eph-netadmin` | The one privileged binary — carries `CAP_NET_ADMIN`, creates/destroys TAPs. |
| Guest agent    | Tiny in-box process (over vsock) for exec / file / tty.                 |
| Web UI         | SvelteKit SPA (shadcn/Geist), embedded in the daemon, served on `-http`. |

## Control API

`ephemerad` listens on a Unix socket (`run/ephemerad.sock`, mode 0600) by default — a local-first daemon
has no reason to be on the network, and file permissions beat an open port.

| Method   | Path                          | Purpose                                         |
| -------- | ----------------------------- | ----------------------------------------------- |
| `GET`    | `/healthz`                    | liveness                                        |
| `POST`   | `/v1/machines`                | boot a machine, returns once its agent is ready |
| `GET`    | `/v1/machines`                | list                                            |
| `GET`    | `/v1/machines/{id}`           | inspect                                         |
| `DELETE` | `/v1/machines/{id}`           | destroy                                         |
| `POST`   | `/v1/machines/{id}/exec`      | run a command, streams NDJSON frames            |
| `POST`   | `/v1/machines/{id}/snapshot`  | freeze a jailed machine to disk (keeps running) |
| `GET`    | `/v1/snapshots`               | list snapshots                                  |
| `DELETE` | `/v1/snapshots/{id}`          | delete a snapshot and its files                 |
| `POST`   | `/v1/snapshots/{id}/fork`     | start a new machine from a snapshot             |

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
- [~] **Phase 4 — ephemera as a computer.** The reframe: ephemera is a fast, forkable, disposable-or-
      persistent **computer you use like a laptop**, and it is **agent-agnostic** — you bring your own agent
      (Claude Code, aider, your own) and run it *inside* the machine. Ephemera's job is to be a great
      computer; the agent is just software on it.
      - [x] **Interactive shell.** `eph shell <id>` opens a real terminal in a machine — a pty over vsock,
            with job control and full-screen programs, not just one-shot `exec`. This is what makes it usable
            as a computer rather than a place to fire commands.
      - [x] **Persistence — named computers.** The overlay's writable upper is either tmpfs (ephemeral, for
            a throwaway or a fork) or a per-computer writable disk (persistent). A **computer** is a named,
            persistent machine: `eph computer create/ls/start/stop/rm`, and `eph shell <name>`. `stop` syncs
            then parks it (disk kept); `start` boots a fresh machine on the same disk with state intact. Same
            shared read-only base, so fork/snapshot are unaffected. See
            [decision 0008](decisions/0008-persistent-computers.md).
      - [x] **A computer's toolchain.** The guest image ships bash, coreutils, git, curl, vim, less, ssh, and
            python3 — the basics a real box has; users install the rest, including their agent. Shell defaults
            to bash.
      - [x] **Live terminal resize.** The interactive session is a framed protocol (data vs resize), so
            SIGWINCH on the host reaches the guest pty and full-screen programs reflow.
      - [x] **Box verbs.** The CLI is `ephemera` (with `eph` a symlink alias): `new`, `list`, `ssh`, `scp`,
            `exec`, `stop`, `start`, `rm`, `snapshot`, `fork` — single-word verbs over a "box". `new [name]`
            makes a kept computer or a throwaway; `list`/`rm` unify both; `scp` copies files in and out.
      _Checkpoint met: `eph new dev`, `eph ssh dev`, install and run your own agent, `eph stop`/`start` with
      state intact._
- [~] **Phase 5 — Web UI + live desktop.** A graphical desktop you watch and drive, in a browser. A
      Firecracker guest has **no display device**, so the pixels are made inside the box (a headless X server
      paints a RAM framebuffer) and streamed out over **vsock** — the same private channel as the shell, so a
      desktop box needs no network and exposes no port. See [decision 0009](decisions/0009-desktop-over-vsock.md).
      - [x] **5a — Pixels out of a box.** A separate, heavier desktop image (Xvfb + openbox + xterm + x11vnc);
            the agent bridges a second vsock port to the VNC server; `eph new --desktop` boots one and
            `eph desktop --raw <box>` exposes it as a local VNC port. _Verified: a live RFB session out over
            vsock, and vsock shown not to be the bottleneck (~4 MiB frame streamed in ~20 ms)._
      - [x] **5b — In the browser.** `eph desktop <box>` serves a self-hosted page and a WebSocket that proxies
            the RFB stream; open a tab, no native viewer. Both the WebSocket server and the RFB client are
            hand-rolled and dependency-free — no noVNC, no CDN, no build step. _Verified headlessly through the
            WebSocket: upgrade, full RFB handshake both directions, and a full framebuffer through 32 KiB frames._
      - [x] **5c — The web UI.** A SvelteKit SPA the daemon embeds (`go:embed`) and serves on an opt-in
            loopback TCP surface (`ephemerad -http`), same origin as the API and the WebSockets. See
            [decision 0010](decisions/0010-web-ui-and-http-surface.md). Styled with the shadcn/ui token system
            and Geist (the Vercel dev-tool look), self-hosted, no CDN.
            - [x] **Dashboard** — list boxes, new (throwaway/kept/desktop), stop/start/rm, snapshot, fork.
            - [x] **Desktop in the UI** — a `/box/{id}` route rendering the RFB client (shared as a module) to
                  a canvas over the daemon's desktop WebSocket.
            - [x] **Terminal in the UI** — an xterm.js terminal over a shell WebSocket, resizing with the
                  window; the same guest pty session as `eph ssh`. Desktop and terminal are tabs on the box.
      - [x] **5d — Smoothness (the video path).** A **Crisp / Smooth** toggle on the desktop: Crisp is the
            RFB framebuffer (low latency); Smooth is **H.264** — the box's agent runs a per-connection `ffmpeg`
            capture of the X display, streamed as fragmented MP4 over a third vsock port and played in the
            browser via **Media Source** (`avc1.42C01F`). Far lighter on the wire for motion (~9 KiB/s idle vs
            RFB's multi-MiB raw frames). Not WebRTC — localhost needs no ICE, so it is MSE-over-WebSocket behind
            the same vsock bridge. Input in video mode still goes through the VNC server (an input-only RFB
            client), so the desktop stays clickable. Software encode costs CPU; the toggle lets you judge the
            trade for yourself.
      _Checkpoint met: a browser desktop you watch (or drive) an agent use — crisp or smooth, your call._

## Host requirements (verified on this box)

- KVM present (`/dev/kvm`, world-writable), nested virt enabled — Firecracker runs.
- cgroup **v2**; `nft` and `iptables-nft` both present on the **nf_tables** backend.
- Guest kernel built with `CONFIG_IP_PNP=y` and `CONFIG_VIRTIO_NET=y` — the guest configures its own
  network from the kernel command line, so there is no in-guest network code.
- Phases 0–1 need **no root**. The daemon and CLI hold **no capability**; networking's `CAP_NET_ADMIN`
  lives on the `eph-netadmin` helper alone, granted by a one-time `build/host-setup.sh` (which also sets up
  forwarding, the firewall, and the cgroup delegation). Jailing needs only that the kernel permit
  unprivileged user namespaces.
