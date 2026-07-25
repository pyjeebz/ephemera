# Build log

Raw material: what broke, what surprised me, what things actually cost. Kept as it happens, because the
details that make writing good are exactly the ones you forget within a week.

---

## Phase 0 — boot one microVM by hand

**Goal:** install Firecracker, get a kernel and a rootfs, boot a VM through the raw API, get a shell.

**Numbers**

| Thing | Value |
| --- | --- |
| Guest reaches init | **1.03 s** |
| Full boot → selftest → self-terminate | **1.70 s** wall, exit 0 |
| Firecracker | v1.16.1 (binary is ~2 MB, static) |
| Kernel | vmlinux-6.1.128, 40 MB, uncompressed ELF |
| Guest | Alpine 3.24.1, 1 GB ext4 image, ~70 MB used |

**The environment surprise: nested virt on WSL2 just works.**
Expected this to be the blocker. It wasn't — `/dev/kvm` present, `vmx` flag, `kvm_intel` loaded,
`nested=Y`. A Windows laptop running a real hypervisor inside WSL2 with no ceremony.

**Gotcha 1 — `/dev/kvm` ownership changed underneath me.**
Probed it at the start of the session: `crw-rw-rw- root:polkitd`, mode 0666. Wrote down "no root needed
for Phase 0" as a win. By the time I actually booted, it was `crw-rw---- root:kvm`, mode 0660,
recreated at 20:43 — almost certainly when dockerd started. I'm not in the `kvm` group.

The failure mode is the interesting part: **all four configuration calls returned `204 No Content`, and
only `InstanceStart` failed** with `Kvm error: Permission denied`. Configuration never touches the
hypervisor, so a wall of successes followed by one failure isn't a contradiction — it's the boundary
between describing a machine and building one.

Fix: `sudo usermod -aG kvm $USER` (persistent, needs a WSL restart) plus `sudo chmod 0666 /dev/kvm` for
the session. **Lesson: a capability check is only valid at the moment you make it.**

**Gotcha 2 — `docker info` exits 0 with the daemon down.**
Checked Docker availability with `docker info >/dev/null 2>&1 && echo OK`. It said OK. The daemon had
never been running; `docker info` prints client-only output and exits 0. Found out ~20 minutes later
when a build failed. **Use `docker ps` to ask about the daemon.** Exit 0 means "this command succeeded,"
not "the thing you care about is true."

**Gotcha 3 — Docker bind-mounts `/etc/hostname` and `/etc/resolv.conf` during builds.**
`RUN echo ephemera > /etc/hostname` fails the build outright. Both moved to guest runtime, which is
where they belonged anyway.

**Gotcha 4 — Firecracker only forwards serial *input* from a real TTY.**
Booted fine, shell prompt right there, piped `uname -a` in. Nothing. Added a 3-second delay in case the
guest wasn't ready. Still nothing. Output flows through a pipe perfectly — which is exactly why the boot
logs looked healthy and let me believe the pipe worked. Input silently goes nowhere.

Consequence: the serial console is a debugging tool, not an API. Split the guest into two inits —
`/sbin/eph-init` (interactive shell, for a human at a terminal) and `/sbin/eph-selftest`
(non-interactive, prints diagnostics, halts). This is the thing that made me build the vsock agent two
phases earlier than planned.

**Gotcha 5 — `poweroff` halts the guest but leaves the VMM running.**
First selftest exited with code 124: `timeout` killed it after 60s. Firecracker exits on guest **reset**,
which `reboot=k` routes through the i8042 controller. Switched to `reboot -f` → clean exit 0.

**The nice discovery: you can build a rootfs with no root at all.**
- `mkfs.ext4 -d <dir>` populates an image straight from a directory — no `mount`, which is the part that
  needs privileges.
- `fakeroot` intercepts `chown`/`mknod`/`stat`, so files keep `root:root` ownership in the image while
  you're uid 1000 outside.
- Both must happen in **one** `fakeroot` session or the faked metadata is gone before `mkfs` reads it.
- Verify without mounting: `debugfs -R "stat /sbin/eph-init" rootfs.ext4` → `Mode: 0755 User: 0 Group: 0`.

Also: the kernel config matters more than expected. `CONFIG_DEVTMPFS_MOUNT=y` means the kernel mounts
`/dev` itself before init, which is what hands us `/dev/console` without ever creating device nodes as
root. That single config line is why the no-root path works at all.

---

## Phase 1 — Go control plane

**Goal:** `ephemerad` + `eph`, machines booted/tracked/executed-in/destroyed from Go.

**Numbers**

| Thing | Value |
| --- | --- |
| Boot (VMM start → accepted) | **43–266 ms** (varies with page cache warmth) |
| `eph run <cmd>` end to end | **~1.7–2.5 s** |
| Test suite | 50 test functions; unit ~0.7 s, with real VMs ~8 s |

**Decision: wrote our own Firecracker client instead of using firecracker-go-sdk.**
The SDK's last *tagged* release is v1.0.0 from **September 2022** — its `main` is maintained (commits
into Dec 2025) but adopting it means pinning an untagged commit. Against that: the whole point here is
to learn the stack, the SDK's value is that it hides the API, and allow-listed egress means writing our
own firewall rules regardless — which erodes most of what it would have saved. Full reasoning in
`docs/decisions/0001-own-firecracker-client.md`.

Cost accepted: ~300 lines, and process supervision is genuinely the fiddly part.

**The Go bit worth explaining: HTTP over a Unix socket.**
Firecracker's API is plain HTTP but served on a Unix socket. The standard library handles this by
swapping one function — the transport's dialer — and the URL host becomes a meaningless placeholder:

```go
DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
    var d net.Dialer
    return d.DialContext(ctx, "unix", sock)
},
```

Same trick reused twice more: the daemon's own control socket and the `eph` client.

**Bug 1 — cleanup on the path that never runs.**
Wrote `Shutdown()` to kill the VMM and remove its socket. Tested it. Worked. Then noticed
`61301763.sock` sitting in `run/` after a clean run.

The self-terminating guest never calls `Shutdown` — it resets itself, Firecracker exits, and nobody's
teardown code runs. **The socket's lifetime is the process's lifetime**, so cleanup belongs in the
reaper goroutine that's already waiting on the child, not in the explicit teardown path. Same class of
bug bit the vsock socket later; fixed by giving the reaper a `Cleanup []string`.

**Bug 2 — PID 1 has no `PATH`.**
First `eph run uname -a` came back with `exec: "uname": executable file not found in $PATH`. The vsock
transport worked perfectly on the first try; the agent just had no environment. The kernel hands PID 1
an almost-empty environment and there's no shell profile to fall back on. Set `PATH` in the init script,
plus a fallback in the agent itself.

**Design note — vsock, and why the guest has no network.**
Commands reach the guest over AF_VSOCK: no network interface, no IP, nothing listening on any
host-visible port. The only way in is the VMM's own socket. Firecracker doesn't expose vsock as a socket
family on the host — it listens on one Unix socket and speaks a text handshake (`CONNECT 1024\n` →
`OK <port>\n`), after which the connection is a transparent pipe.

One subtlety that cost a real bug's worth of thought: **the `OK` line and the first guest bytes can
arrive in the same read.** Read the line with a `bufio.Reader` and throw it away, and you silently drop
guest data. The reader has to stay attached to the connection. There's a test pinning exactly this.

Go has no vsock support in `net`, so the guest side is raw `x/sys/unix` — `socket(AF_VSOCK)`, `bind`,
`listen`, `Accept4`. Two flags there matter: `SOCK_CLOEXEC` so exec'd commands can't hold the connection
open, and `SOCK_NONBLOCK` so Go's poller handles it rather than parking an OS thread per connection.

**Orphan reaping — tested by being nasty to it.**
`kill -9` the daemon, leaving two VMMs running with nothing tracking them. On restart it reads its
durable records, finds both pids alive, kills them, clears the sockets:

```
WARN destroying orphaned machine id=0f8bdebe pid=79928
INFO reaped orphaned machines from a previous run count=2
```

Honest limitation: **a VMM cannot be re-adopted.** Supervising a process means owning its wait status,
and you can't `wait()` on a process you didn't spawn. So restarting the daemon destroys running
machines. Defensible for something ephemeral by design, but it's a real constraint, not a feature.

**Mutation-tested the suite** rather than trusting green checkmarks. Reintroduced three fixed bugs; all
three were caught:

| Reintroduced | Caught by |
| --- | --- |
| Drop the bufio wrapper in `vsock.Dial` | `read after handshake: EOF` |
| Stop removing the socket in the reaper | `0c19e8bb.sock leaked after the guest exited on its own` |
| Drop `reboot=k` from boot args | `boot args ... missing "reboot=k"` |

**Still open:** pid reuse in `Reap` (wants a pidfd); zombie reaping in the agent as PID 1 (a generic
`SIGCHLD` reaper would race `cmd.Wait` for exit statuses — needs a subreaper or a real init).

---

## Phase 2 — networking & egress (isolation half)

**Goal:** give a machine a network it can use and cannot abuse. Reach the internet, reach nothing private.

**Numbers**

| Thing | Value |
| --- | --- |
| Networked boot (`create -net` → ready) | **0.88–0.93 s** |
| Standalone `run -net` → command ready | **~1.1–1.2 s** |
| In-guest network config code | **0 lines** (kernel `ip=` does it before init) |
| Firewall | one static `table ip ephemera`, 3 chains, ~6 rules |
| Daemon privilege | `CAP_NET_ADMIN` on the binary — **not** root |

**Verified live:** internet reachable, DNS resolves, and every private destination dropped — the host's
gateway, the host's own LAN IP, the upstream router, and *another machine*. Machine-to-machine block
tested with two real VMs pinging each other's leased address.

**Design: a /30 per machine, no bridge — the topology is the isolation.**
Every machine gets its own point-to-point link: a TAP on the host carrying a `/30`, one address for the
host end, one for the guest. Nothing shared. Two machines therefore have no path to each other that does
not go up through the host's routing table — where the firewall refuses it. A shared bridge would put
every machine on one wire and make machine-to-machine traffic *local delivery the firewall never sees*, so
isolation would hang on a rule being correct instead of on there being no wire. "Who can this VM reach"
collapses to one destination match. Full reasoning in `docs/decisions/0002`.

**The line that is the whole of the guest's networking: `CONFIG_IP_PNP=y`.**
The guest kernel configures its own interface from the `ip=` kernel command line, before init runs. No
DHCP client, no dhcpcd, no shell script racing the interface. The entire guest-side change for Phase 2 is
three lines copying the kernel's chosen resolver out of `/proc/net/pnp` into `/etc/resolv.conf`.

The `ip=` parameter is positional and mostly empty, which makes it look like a typo the first time:

```
ip=10.79.0.2::10.79.0.1:255.255.255.252:ephemera:eth0:off:1.1.1.1
       client   ^gateway  ^netmask        ^host    ^dev ^  ^dns
                (server field left blank — that slot is for NFS root)
```

`off` means "these values are final, don't hunt for a DHCP server." Leave it out and the kernel tries
every autoconfiguration protocol it knows and spends several seconds failing. There's a test pinning the
exact string, because the kernel *silently ignores* a malformed one and hands you a guest with no address
and no clue why.

**Gotcha 1 — the firewall gap I only saw because I mislabelled a test.**
I had a clean forward-chain policy: drop any packet from the pool headed for private space. Machine-to-
machine blocked, host LAN blocked, internet fine. Then a fat-fingered test pinged a machine's *own* address
and it succeeded, which sent me looking — and the thing I found was worse than the typo.

A packet from a guest to the **host itself** — its gateway `10.79.0.1`, or the host's `eth0` address — is
delivered *locally*, through the `input` hook. It is never forwarded. So the forward chain, where all my
filtering lived, never sees it. A guest could reach any service running on the host and my "default deny"
had a door in it.

The fix is a second chain on the `input` hook that drops everything from the pool:

```
chain input {
    type filter hook input priority filter; policy accept;
    ip saddr $POOL drop   # the host is off limits too
}
```

Safe because the guest needs *zero* IP traffic to the host: the control channel is vsock, which is not IP
and does not pass through nftables at all. **Lesson: "default deny" on the forward chain is not default
deny. Forward and input are different hooks, and local delivery quietly skips the one you were watching.**

**Gotcha 2 — a TAP's lifetime is a file descriptor unless you say otherwise.**
`TUNSETIFF` creates the device; when the fd that created it closes, the device vanishes. Firecracker opens
the TAP *by name* in a different process, so if the daemon just created-and-closed, the interface would be
gone before the VMM looked for it. `TUNSETPERSIST` hands ownership to the kernel. Teardown is the mirror:
clear persist, close, gone. There is no "delete" ioctl — persistence *is* the delete switch.

**Gotcha 3 — file capabilities die on every rebuild.**
`CAP_NET_ADMIN` lives on the binary's inode. `go build` writes a new inode. So every rebuild silently
drops the capability and the next `-net` run fails with "operation not permitted" until you re-run
`host-setup.sh`. Annoying, documented loudly, and genuinely the price of not running a root daemon.

**Gotcha 4 — `eph exec <id> -- cmd` ate the `--`.**
Go's `flag` package stops parsing at the first non-flag argument. For `eph exec abc -- ls`, that first
non-flag is the machine id `abc`, so parsing stops *before* the `--`, and the `--` arrives as a literal
command token — the guest dutifully tried to exec a program called `--`. `eph run` never hit this because
its flags come first and `flag` consumes the `--` as the end-of-flags marker. Fixed by dropping a leading
`--` from the command explicitly.

**Decision: CAP_NET_ADMIN on the binary, and a firewall the daemon never touches.**
The whole privileged story is one script, run once. It grants the capability, turns on forwarding, and
installs a static nftables table keyed on the *pool subnet* rather than on interface names — so one
ruleset governs every machine that will ever boot, and the daemon adds nothing per machine. The security
posture is one file you can `nft list ruleset` and read, not an emergent property of code that runs on
every create. Chose `nft` over `iptables-nft` (the primitive, not the shim) and the `ip` family over
`inet` for maximum compatibility. This also closed ADR 0001's "re-evaluate the SDK" trigger: networking
didn't want it. Full reasoning in `docs/decisions/0002`.

**Still open (networking):** per-machine egress policy (as opposed to one policy for the whole pool) is
deferred; the static design is what it would grow out of.

---

## Phase 2 — resource caps (cgroup v2)

**Goal:** a machine cannot spend the whole host. Cap CPU and memory on the VMM process.

**Numbers**

| Thing | Value |
| --- | --- |
| `memory.max` | guest RAM + 64 MiB headroom (256 → 320 MiB) |
| `cpu.max` | vCPUs in whole cores (`"100000 100000"` = 1 core) |
| Placement | `CLONE_INTO_CGROUP` — capped before the first instruction |
| Daemon privilege | still just `CAP_NET_ADMIN` + an owned cgroup subtree |

**Verified live:** the VMM lands in its leaf's `cgroup.procs`; limits hold the right values; the leaf is
removed on destroy. Enforcement checked directly — a 20%-of-a-core cap held a busy loop to ~21% measured
(419 ms CPU over 2 s wall), and a 64 MiB cap got a 200 MiB allocator OOM-killed (exit 137, `oom_kill 1`).

**Design: one leaf per machine, spawned straight into it.**
`memory.max` and `cpu.max` are written to a fresh cgroup, and the VMM is placed into it by the clone that
starts it — Go's `SysProcAttr.UseCgroupFD`, which is `clone3(CLONE_INTO_CGROUP)`. The alternative (fork,
then write the pid to `cgroup.procs`) leaves the process briefly in the parent's cgroup, which for a memory
limit is exactly the window a runaway would use. Spawning into the cgroup removes the window.

**The headroom that isn't optional.**
`memory.max` set to exactly the guest's RAM OOMs the VMM the moment the guest fills its last page: the
limit also covers Firecracker's own footprint and the page tables mapping guest memory. 64 MiB of slack
makes the cap a backstop against a leak, not a limit that fires in normal running.

**Gotcha 1 — the delegation boundary, which cost the most time by far.**
Owning the subtree let me `mkdir` leaves and write limits. Then every single spawn died with
`fork/exec ... permission denied`, which looks like a Firecracker or a filesystem problem and is neither.

Placing a process into a cgroup is a *migration*, and cgroup v2 requires write access to the
`cgroup.procs` of the **common ancestor** of the process's current cgroup and the destination. The daemon
starts in `/init.scope`, outside the subtree, so the common ancestor is the hierarchy **root** — and root's
`cgroup.procs` is root-owned. I proved it with one line: `echo $$ > ephemera/leaf/cgroup.procs` is refused
with the same error. Owning a subtree lets you build cgroups in it; it does **not** let you move a process
across the boundary into it.

The clean fix is a systemd unit with `Delegate=yes` — the service starts *inside* its scope and there is no
boundary to cross. This box has no systemd user session, so host-setup grants the one missing thing: write
to the root `cgroup.procs`. On a single-user box that is not a real boundary (you already have root); on a
shared host it would be wrong, and the systemd path is mandatory instead. **Lesson: cgroup delegation is
about a subtree you own, but process *placement* is gated by an ancestor you may not — the two are
different permissions and the second is the one nobody mentions.**

`Available()` now checks that ancestor is writable, so a misconfigured host disables caps with an
explanation instead of failing on every boot.

**Gotcha 2 — `rmdir` a cgroup races the kill.**
A leaf can only be removed once empty, and it empties when its last process is *reaped*, not when it is
signalled. So the orphan-reaper's `SIGKILL` returns, the `rmdir` fires, and the cgroup is still briefly
non-empty → `EBUSY`. `cgroup.Remove` retries past that window.

**Gotcha 3 — `$BASHPID` is empty when your "bash" is zsh.**
Not product code, but it ate a debugging cycle: my first enforcement test placed `$BASHPID` into a leaf to
run a spinner, saw zero CPU usage, and briefly thought placement was broken. The processes were fine; the
variable was empty and the workload ran uncapped in the wrong cgroup. Using `$$` from a plain `sh -c` and
*verifying membership before trusting the result* is the fix. General lesson, cheaply bought: when a test
says the thing didn't happen, first prove the test did what you think it did.

**Decision recorded in `docs/decisions/0003`.** Also: caps are a *restriction*, so unlike the network
(a *grant*, opt-in with `-net`) they apply automatically whenever the subtree is available.

**Still open:** the jailer — chroot, namespaces, seccomp — the last Phase 2 isolation piece.

---

## Phase 2 — the jail (unprivileged confinement)

**Goal:** the VMM process can see only the files it needs and no process outside its own tree. Without root.

**Numbers**

| Thing | Value |
| --- | --- |
| Jailed boot (`create` → ready) | ~1.4 s |
| Mounts the VMM can see | **8** (images, 3 device nodes, /proc) vs dozens for a normal process |
| Privilege added | **none** — a user namespace, not root |
| Namespaces | user + mount + pid isolated; **net shared** (so the firewall still applies) |

**Verified live:** a jailed VMM runs in its own user, mount, and pid namespaces; its `/proc/<pid>/mountinfo`
shows the jail directory as `/` with only the bound-in images and device nodes; boot, exec over the
relocated vsock, and teardown all work; and jail composes with cgroups in one clone (capped *and* jailed,
single spawn, no errors).

**Design: a user namespace is the whole trick.** Firecracker's own jailer is a *privileged* program that
drops to safe — it needs root. We never had root to drop, and on this box `sudo` prompts for a password, so
a root jailer is a non-starter. But inside a fresh user namespace an ordinary uid becomes root over *that
namespace*, which is exactly enough to bind-mount a minimal root, `pivot_root` into it, and mount a private
`/proc`. Step outside and the process is still just our uid with no real capability. `eph-jail` is a tiny
helper the daemon spawns into the namespaces (Go gives no hook between clone and exec, so the setup has to
run in a spawned process); it pivots, then exec's Firecracker. Full reasoning in `docs/decisions/0004`.

**What we deliberately did NOT build.** Firecracker already runs as our unprivileged user, so there is no
privilege-drop to write. Its seccomp filters are on by default, so there is no syscall filter to write. All
that was actually missing was the chroot and the pid/mount namespaces — so that is all the jail is. This is
the mirror of ADR 0001: hand-rolling a *client* was fine because a bug is a bad error message; a security
boundary is different, so the jail assembles kernel-enforced primitives and leans on Firecracker's *own*
audited seccomp rather than reinventing one.

**Gotcha 1 — the userns can't touch the host's TAP, and the fix is an ioctl I'd never used.**
The plan was to keep the host network namespace (so the firewall keeps working) and just isolate the
filesystem and pids. But a process in a *child* user namespace has no `CAP_NET_ADMIN` over the *host*
network namespace — so a jailed Firecracker cannot open a host TAP. Isolating the netns instead would have
meant a per-machine netns, veth pairs, and rerouting the whole firewall — throwing away the clean
point-to-point design.

The escape is the TAP *owner* exception. `tun_not_capable()` in the kernel lets a process open a TAP without
`CAP_NET_ADMIN` if the device's owner uid matches the caller's — the mechanism that exists precisely so
unprivileged programs can use pre-created taps. So `vmnet` now sets `TUNSETOWNER` (and `TUNSETGROUP`) to our
uid when it creates the tap, and a jailed VMM — our uid, mapped through the userns — opens its own interface
with no capability at all. Networking survives the jail, firewall untouched. **Lesson: "needs CAP_NET_ADMIN"
often means "unless you own it"; the owner exceptions on taps (and elsewhere) are how unprivileged network
code gets written.**

**Gotcha 2 — you can't observe a chroot with `readlink /proc/<pid>/root`.**
Checking the confinement from the host, `/proc/<pid>/root` pointed at `/`, not the jail — which for a
worried minute looked like the chroot hadn't taken. It had. That symlink is rendered from the *reader's*
mount namespace, and the jail's root is a mount private to the VMM's namespace, so from outside it cannot be
named and collapses to `/`. The real evidence is `/proc/<pid>/mountinfo`, which lists the process's own
mount table: root = the jail dir, plus the eight binds, and nothing else. **Lesson: to inspect another
process's filesystem view, read its mountinfo, not its root symlink — the symlink lies across namespaces.**

**Gotcha 3 — a read-only bind mount takes two steps.** `mount(MS_BIND | MS_RDONLY)` silently ignores the
read-only flag; the bind lands read-write. You have to bind first, then `mount(MS_BIND | MS_REMOUNT |
MS_RDONLY)` the same target. The kernel has always worked this way and the man page mentions it in passing,
but the first version bound the kernel and firecracker binary writable without complaint.

**Decision recorded in `docs/decisions/0004`.** Jailing is opt-in (`-jail`) for now — new, security-sensitive,
wants soak time — but needs no privileged setup, so making it default-on later is one line.

**Phase 2 done:** filtered network, capped resources, jailed VMM.

---

## Phase 2 — the collision, and a zero-privilege daemon

**What happened:** networking and the jail were each finished and verified. Then a machine asked for *both*
and would not boot. `-jail` worked. `-jail -net` failed with a bare `permission denied` on spawning the
jail helper.

**The rule nobody mentions:** a process holding a **file capability cannot create a user namespace** with
the unprivileged self-mapping. The kernel blocks it on purpose — allowing it would let a process launder
its capabilities into a namespace where it is root. The daemon held `CAP_NET_ADMIN` (to make TAPs), so the
daemon could not build the jail. Proven directly: the *uncapped* `eph` jails fine; the *capped* one, same
binary, fails.

Both obvious escapes were dead ends:

- **Create the userns later, inside the helper, with `unshare`.** Fails with `EINVAL` — `unshare(NEWUSER)`
  needs a single-threaded process, and every Go program is multithreaded from the first instruction. The
  userns has to come from the spawn-time `clone`, i.e. from a process that is not holding the capability.
- **Wrap the spawn in an uncapped intermediate.** Works, but leaves the VMM a grandchild of the daemon, so
  every machine carries a spare process and tracking/killing/inspecting it needs extra bookkeeping.

**The fix: take the capability off the daemon entirely.** A tiny `eph-netadmin` binary is now the only thing
that carries `CAP_NET_ADMIN` — it creates and destroys TAPs and does nothing else. The daemon and CLI hold
*nothing* and shell out to it. With no capability on the daemon, the jail's clean spawn-time clone is
allowed again, and jail + network + cap compose in one Firecracker process, no wrapper. `docs/decisions/0005`.

**The upgrade in the story:** through most of Phase 2 the pitch was "unprivileged-ish — one capability." It
is now simply **zero-privilege**: `getcap` reports nothing on `ephemerad` or `eph`; the only privileged
thing in the whole system is a few dozen lines of TAP code, and every other privileged act is a one-time
host-setup step or an unprivileged user namespace.

**Lesson worth keeping:** capabilities and user namespaces are two ways to be "privileged without root," and
they actively refuse to coexist in the same process. If you want both powers, they have to live in
different processes — which, once forced on you, turns out to be the better design anyway: the privileged
part shrinks to something you can read in one sitting.

**Verified:** daemon and CLI uncapped; a machine that is jailed, networked, and capped at once boots in
~1.2 s and reaches the internet through its owner-stamped TAP; teardown removes the tap and the jail.

---

## Phase 3 — snapshot & restore (the foundation for fork)

**Goal:** stop paying the boot cost every time. Freeze a booted machine to disk, bring it back in
milliseconds.

**Numbers**

| Thing | Value |
| --- | --- |
| Cold boot to agent-ready | ~1 s |
| **Restore to agent-ready** | **~63 ms** |
| Snapshot artifacts | two files: device/vCPU state + guest RAM |

**How it works.** Pause the guest (`PATCH /vm` → Paused), so its memory is a still image rather than a
moving one, then `PUT /snapshot/create` writes two files: the device and vCPU state, and the guest RAM. To
bring it back, a fresh Firecracker gets `PUT /snapshot/load` with those two files and `resume_vm: true`,
and the guest is running again the instant the call returns.

**The thing that makes it worth doing.** The memory image contains a guest that had already booted and was
already running its agent. So a restore does not just skip the kernel and userland coming up — it comes
back with the agent *already listening*. No `WaitAgent`, no boot: the machine is ready the moment restore
returns. That is the entire pitch of the phase, and it is why 63 ms beats a 1 s boot by more than the
number alone suggests — the restored machine is immediately useful, not just immediately present.

**Proving it is a resume, not a fast reboot.** A restore that quietly rebooted the guest would still pass a
naive "does it work" check. Two things pin it down: a marker written into the running guest survives the
round trip, and — the surer one — PID 1's start time (field 22 of `/proc/1/stat`, in ticks since the
guest's boot) is identical before and after. A reboot resets that clock; a resume cannot. It is unchanged,
so the guest was genuinely resumed.

**Design notes / what is deferred.**
- **Full snapshots only** for now. Diff snapshots (just the pages changed since a base) need dirty-page
  tracking turned on at boot and a base to diff against — that is fork's problem, not this slice's.
- **Non-jailed only** for now. A jailed VMM writes inside its chroot, so the snapshot paths need the same
  translation the boot images get; deferred until the mechanism itself was proven.
- **The vsock path is the fork constraint.** A restored guest binds the exact socket path stored in the
  snapshot, so two restores of one snapshot would collide on it. Neat resolution waiting in the wings: the
  jail already gives every machine its own `/run/vsock.sock` inside its own chroot, so once snapshot
  composes with the jail, forks get distinct sockets for free.

---

## Phase 3 — fork (the <100 ms checkpoint)

**Goal:** one snapshot → several live machines at once, each independent, under 100 ms each.

**Numbers**

| Thing | Value |
| --- | --- |
| Fork (jailed restore, per copy) | **78–85 ms** ✅ |
| Cold boot, for comparison | ~1 s |
| Disk copied per fork | ~50 MiB (sparse; image is 1 GiB in name) |

**How the two collisions were solved.** A fork needs its own vsock socket and its own disk, or the copies
are not independent.

- **vsock — the jail gives it for free.** A jailed guest listens on the in-jail path `/run/vsock.sock`,
  which is a *different host socket* in every chroot. So restoring the same snapshot into several jails
  produces distinct sockets with no effort — which is exactly why fork restores a *jailed* snapshot: only
  then is the stored path the chroot-relative one that varies per copy. This is the payoff of the deferred
  "snapshot composes with jail" work.
- **disk — a private sparse copy.** Each fork gets its own writable rootfs, copied from the snapshot's
  frozen disk into its jail directory. Memory and state are bound **read-only**, so one memory image backs
  every fork at once.

**Gotcha — the copy, not the restore, was the whole cost.** First cut: a jailed fork took **11.8 seconds**.
The memory restore was its usual ~63 ms; the other 11.7 s was `io.Copy` faithfully copying a 1 GiB rootfs
image byte for byte. But the image is 1 GiB in *name* and ~50 MiB in *fact* — the rest is a hole. Copying
only the data extents with `SEEK_DATA`/`SEEK_HOLE` dropped it to **~80 ms**, under the checkpoint, with no
change to the guest or the image. **Lesson: before optimising the clever part (memory restore), check the
boring part (a file copy) isn't quietly doing 20× the work by writing zeros.**

**What's left for a *true* zero-copy fork.** Even sparse, the disk is O(data) per fork, not O(1). The
zero-copy answer is a **read-only base image + a guest-side overlay** — done next, below.

---

## Phase 3 — zero-copy fork, via a read-only base + RAM overlay

**Goal:** stop copying the disk per fork entirely, and fix the corruption bug in the same move.

**Numbers**

| Thing | Value |
| --- | --- |
| Fork (was: sparse copy) | 78–85 ms |
| **Fork (now: shared read-only base)** | **15–31 ms** |
| Disk copied per fork | **none** |

**The change.** Every machine now boots a **read-only** base image and overlays a **tmpfs** upper over it
(`eph-overlay-init` runs as PID 1: mount overlay, `pivot_root`, exec the real init named on the cmdline as
`eph.init=`). Every guest write lands in RAM; the disk is never touched. So a fork shares the one base image
with no copy — it is purely a memory restore — and its writes are isolated in the overlay its restored
memory already carries. `docs/decisions/0007`.

**Two wins from one change.** Fork went zero-copy *and* a real bug closed: every machine used to mount the
shared rootfs read-write, so two at once would both write the ext4 journal and corrupt the image (unnoticed
only because machines had been booted one at a time). A read-only image can't be written by anyone.
Verified: two concurrent machines wrote and read their own files, and the base image's checksum was
unchanged afterward.

**Gotcha — a read-only mount can't replay a journal.** First boot on the read-only drive panicked:
`EXT4-fs (vda): INFO: recovery required on readonly filesystem` → `cannot proceed` → `Unable to mount root
fs`. A freshly built ext4 carries a journal, and if it is flagged as needing recovery the kernel must
*write* to replay it — impossible read-only, so it refuses to mount root at all. Fix: build the image
**without a journal** (`mkfs.ext4 -O ^has_journal`). The base is only ever mounted read-only and overlaid,
so a journal was pure liability. **Lesson: "read-only root" is not just a mount flag — the image has to be
built to never need a write, journal included.**

**Phase 3 checkpoint met and then some:** fork a running VM in **15–31 ms**, well under the 100 ms target,
copying nothing.

---

## Phase 3 — the warm pool

**Goal:** take even the fork off the request path. Keep machines pre-forked and ready; hand one out
instantly.

**Numbers**

| Thing | Value |
| --- | --- |
| Boot | ~1 s |
| Fork | ~15–31 ms |
| **Get from a warm pool** | **~12 µs** |

**How it works.** `internal/pool` forks N machines from one snapshot ahead of time and holds them in a
channel, resumed with their agents up. `Get` receives one and starts a background refork to replace it, so
the caller waits for a channel receive — microseconds — not for a VM. Every machine in the pool is an
identical fork of the same snapshot, which is what makes them interchangeable.

**The shape that makes it safe.** The pool is a buffered channel plus a bounded set of forker goroutines,
with one rule for shutdown: cancel first, then wait, then drain. Cancelling makes any in-flight fork
destroy its machine instead of handing it out; waiting lets those finish; draining destroys whatever
already made it into the channel. A `closed` flag under a mutex keeps `Get`'s refill from starting a new
forker after `Close` has begun — the one race worth guarding, since a goroutine started after the wait
would leak a VM.

**Why microseconds and not milliseconds:** the expensive things — boot, then fork — already happened,
before the request arrived. The pool trades a little standing memory (N idle machines) for taking their
cost entirely off the hot path. On a 7.6 GB box the pool stays small; the knob is memory.

**Phase 3 mechanisms complete:** snapshot/restore, zero-copy fork, and a warm pool that serves in
microseconds. What remains is exposing them through the daemon and CLI.

---

## Phase 3 — the daemon and CLI surface

**Goal:** make snapshot and fork drivable by hand.

`ephemerad` gained `POST /v1/machines/{id}/snapshot` (jailed machines only, since only a jailed snapshot
forks; the machine keeps running), `GET`/`DELETE /v1/snapshots`, and `POST /v1/snapshots/{id}/fork`.
Snapshots persist in their own store — unlike a machine record, which dies with its process, a snapshot is
files on disk that outlive the daemon. The CLI mirrors it: `eph snapshot <id>`, `eph snapshots`, `eph fork
<snap>`.

End to end, by hand: create a base, drop a marker in it, `eph snapshot` it (base keeps running), `eph
fork` it twice — both copies come up in tens of milliseconds carrying the marker, and a write in one is
absent in the other. The whole point in one demo: a booted, customised machine, cloned on demand, each
clone isolated.

**A jail-cleanup gap this closed.** Machine records now carry the jail directory, and the orphan reaper
removes it — a jailed machine's sockets live inside that directory, so without it a crashed daemon would
leave them behind. Latent for every jailed machine, not just forks; fixed for all.

**A shell gotcha, not a code one, worth one line so I stop repeating it:** `pkill -f 'bin/ephemerad'` in a
test script matches the *script's own command line* and kills the shell running it. Kill by recorded PID,
or match the executable name exactly (`pgrep -x ephemerad`), never a substring the command itself contains.

**Phase 3 complete:** boot once, snapshot, and fork ready copies in ~20 ms or serve pre-forked ones in
microseconds — all drivable from `eph`.

---

## Phase 4 — the reframe: ephemera is a computer

**The pivot.** Phase 4 was going to be a host-side agent loop — ephemera calls Claude, Claude drives the
VM. Building it clarified that it is the wrong shape. The goal is to *use ephemera like a laptop*: a fast,
forkable, disposable-or-persistent **computer**, and one that is **agent-agnostic** — you run your own
agent (Claude Code, aider, your own) *inside* the machine, the way an app runs on a laptop. So the
host-side loop got deleted, and the work turned to making ephemera a real computer you can log into and
use. The first thing that needs: an interactive shell.

**Interactive shell — `eph shell <id>`.** A real terminal in a machine, not one-shot `exec`. The guest
agent allocates a pseudo-terminal (`/dev/ptmx` → `/dev/pts/N`), runs a shell on it, and relays raw bytes
over the same vsock the exec path uses; the host puts the local terminal into raw mode (hand-rolled
termios) so keystrokes pass through untouched and the guest's pty does the echoing and line editing. Job
control, Ctrl-C, `vi` — all work. A pty request turns the vsock connection into a raw byte stream instead
of the JSON frame stream exec uses.

**Gotcha — the overlay ate `/dev`.** The first pty attempt failed: `python3 -c "import pty"` →
`out of pty devices`, and `/dev/ptmx` was missing. Cause: the read-only-base overlay from Phase 3
`pivot_root`s into a new root, and the kernel's `devtmpfs` — mounted at `/dev` before init — stays behind
on the *old* root. So the new `/dev` was just the base image's static stub (a couple of nodes), not the
live device tree. Nothing had needed the difference until pty allocation did. Fix: remount `devtmpfs` on
the new `/dev` in the guest's mount setup, which also brings back `/dev/kvm`, the disks, everything.
**Lesson: `pivot_root` does not carry sub-mounts across; the new root's `/dev` (and `/proc`, `/sys`) is
whatever the image baked in until you remount the real thing.**

**Connecting for a shell.** A pty needs a full-duplex byte stream, which the HTTP control API does not
carry well. So the machine response now exposes the machine's `vsock_path`, and `eph shell` — running on
the same host as the daemon — connects **directly** to that socket for the raw relay. Local-first makes
this clean: the socket is right there, owned by the same user.

**Also:** added `python3` to the guest image, so a machine is something you can actually build on.

**Next:** persistence — a computer you keep should keep its state. The overlay's writable upper becomes a
mode: tmpfs (ephemeral) or a per-computer data disk (persistent), over the same shared read-only base.

---

## Phase 4 — persistence, and named computers

**Goal:** a computer you keep keeps its state. Install something, stop it, start it, and it's still there.

**The mechanism, in one sentence:** the overlay's writable upper layer becomes swappable — a **tmpfs** for
an ephemeral machine, a **per-computer writable disk** for a persistent one — and nothing below it changes.
The guest's overlay init already stacks a writable upper over the read-only base; persistence is just
pointing that upper at a disk (attached as `/dev/vdb`, named by `eph.persist=` on the cmdline) instead of
RAM. The base stays shared and read-only, so fork and snapshot are untouched. `docs/decisions/0008`.

**Named computers — the laptop surface.** A *computer* is a name plus one of these disks: `eph computer
create <name>` makes the disk and boots it, `stop` parks it (disk kept), `start` boots a fresh machine on
the same disk with the state back, `rm` deletes it, and `eph shell <name>` drops you in. A stopped computer
is just a disk sitting on disk; the daemon reaps its records like anything else, so a daemon restart parks
every computer, and its state waits on the disk for the next `start`.

**Gotcha — a kill is not a clean stop.** The first lifecycle test wrote a config file, stopped the
computer, started it, and the file was *gone* — even though the direct persistence test passed. The base
mechanism was fine; the difference was `sync`. A machine has no graceful shutdown: `stop` kills the VMM,
and the guest's page cache — where a just-written file still lives — dies with it, never reaching the disk.
So `stop` now runs `sync` in the guest first, flushing the overlay's dirty pages onto the persist disk. An
*unclean* stop (daemon crash) still loses the last unsynced writes, exactly like pulling power from a
laptop: the journal keeps the disk consistent, recent unsaved work is the cost. **Lesson: "persistent
disk" is only half of it; without a flush on the way out, a kill throws away whatever the guest had not
yet written down.**

**Verified:** a computer with a config file and a project survived stop/start intact, on a new machine and
a fresh kernel reading the same disk; an ephemeral machine was confirmed to run on a tmpfs overlay that
does not persist.

---

## Phase 4 — the finishing touches: a toolchain, a resizable terminal, and box verbs

Three smaller pieces to make ephemera feel like a computer rather than a demo.

**A toolchain.** A box you can't build on isn't a computer, it's a screensaver. The guest image now ships
the basics a real box has — bash (and it's the default shell now), coreutils, git, curl, vim, less, ssh,
ca-certificates, python3. Everything else, including your agent, you install yourself. That's the whole
point of agent-agnostic: ephemera hands you a machine and a package manager, not opinions.

**A terminal that resizes.** `eph ssh` gave you a real pty, but drag the window wider and vim stayed
convinced it was 80×24 — the guest never heard that the terminal changed size. The fix was to stop treating
the connection as a plain byte pipe and give it *frames*: a one-byte kind, then a payload. Kind `0` is
terminal data, kind `1` is a resize (rows, cols). The host catches `SIGWINCH`, sends a resize frame, and
the guest calls `TIOCSWINSZ` on the pty. Full-screen programs reflow.

One properly stupid bug on the way there. The session opens by sending the request as JSON, then the two
sides start framing. I wrote the request with a `json.Encoder` — which helpfully appends a `\n`. The guest
read that trailing newline as the *next frame's kind byte*: `0x0A`, an unknown frame kind, and the session
wedged. The framing was fine; the encoder's good manners weren't. `json.Marshal` and write the bytes
myself, no newline. **Lesson: when you hand-roll a wire protocol, a convenience that appends a byte you
didn't ask for is not a convenience.**

**Box verbs.** The CLI had grown a `computer` sub-noun (`eph computer create`, `eph computer start`…) and
it read like filling out a form. A computer should have a short, physical vocabulary. So the CLI is now
`ephemera`, with `eph` as a symlink alias, and the verbs are single words on a **box**:

```console
$ eph new dev          # a box named dev — kept
$ eph new              # a throwaway box
$ eph ssh dev          # a terminal in it
$ eph scp ./x dev:/root/x
$ eph fork snap        # a clone from a snapshot
$ eph stop dev / start dev / rm dev
$ eph list             # kept boxes and throwaways, together
```

`new` with a name makes a persistent computer; without one, a throwaway. `list` and `rm` stopped caring
whether a thing is a named computer or an anonymous machine — you have *boxes*, some kept, some not.

A word on the name. I wanted `box` as the command — it's the noun the whole UX leans on. But there's an
existing `box` CLI (box.ascii.dev) and, more to the point, Box, Inc. owns the word as a trademark in
software. So `box` stays the noun in the *language* — help text, docs, how you think about it — and
`ephemera`/`eph` is what you type. `eph` is three characters; the ergonomics survive. **Lesson: a great
command name you can't legally own is a liability, not a brand — keep it as vocabulary, not as the binary.**

**Next:** Phase 5 — the desktop. A box you watch, in a browser, at 60fps.

---

## Phase 5 — a desktop, streamed out of a box that has no screen

Here is the fun part. A Firecracker microVM has **no display device.** None. Its whole hardware world is
virtio-block, virtio-net, virtio-vsock, and a serial console — no GPU, no VGA, no framebuffer, no PCI
graphics. This is deliberate on Firecracker's part, and it means the usual "point a VNC client at the VM's
screen" is off the table before we start: there is no screen to point at.

So the pixels have to be *manufactured inside the box.* A headless X server (**Xvfb**) paints into a
framebuffer that lives in RAM, a window manager and a terminal draw onto it, and **x11vnc** reads that
framebuffer and speaks RFB — the VNC protocol. The only question left is how the bytes get out.

**They get out over vsock.** The same private channel the shell already uses. x11vnc binds `127.0.0.1` only;
the agent inside the box listens on a second vsock port and splices it to that local VNC server. The result
is that a desktop box needs **no network and exposes no port** — the desktop is exactly as isolated as
everything else. This is also why I skipped the batteries-included stacks (KasmVNC, Guacamole): they bundle
their own web server and want to talk straight to the browser, which throws the vsock property away. x11vnc
is a dumb RFB server we tunnel ourselves, and *we own the transport.* `docs/decisions/0009`.

### Increment 1 — pixels out (prove the pipe)

A separate, heavier desktop image (the lean box keeps its ~1 s boot), the agent's vsock→VNC bridge, and
`eph desktop --raw <box>` to re-expose the stream as a local port for any native VNC viewer. It booted, and
x11vnc came up listening on `127.0.0.1:5900`, and I connected, and… nothing. An empty greeting, then EOF.

The box's **loopback interface was down.** The kernel creates `lo` but leaves it `DOWN`, and nothing had
raised it — this box has no network, so none of the usual network init ran. x11vnc could *bind* `127.0.0.1`
(the address is assigned to `lo` regardless of link state) but the agent could not *connect* to it:
"Network unreachable." One line in the shared guest mounts — `ip link set lo up` — and every box now has
working loopback, the way any real computer does. **Lesson: "it's listening on 127.0.0.1" and "you can
reach 127.0.0.1" are two different claims, and a box with no network quietly fails the second.**

With `lo` up, the whole chain lit: a live RFB handshake out over vsock, ServerInit reporting a 1280×800
desktop named `ephemera:0`. And the number that mattered: a full **3.91 MiB** uncompressed frame came back
in ~0.53 s — but first-byte was ~510 ms and the bytes themselves streamed in ~20 ms. So that half-second is
x11vnc *rendering* a cold full-screen frame, not the wire. vsock moved four megabytes in twenty
milliseconds. **The transport is not the bottleneck; encoding is** — exactly what a real client's
compression is built to fix. The load-bearing risk of the whole phase, retired in an afternoon.

### Increment 2 — in the browser (no noVNC)

"Open a tab" instead of "install a VNC viewer." The daemon lives on a Unix socket, which a browser cannot
reach, so the CLI grows a tiny local web server: it serves one page and, at `/ws`, a **WebSocket** that
proxies the RFB bytes to the box's desktop vsock port. `eph desktop <box>` now opens a browser by default;
`--raw` keeps the native-viewer port.

The tempting move here is to vendor **noVNC** — a capable, large JavaScript library of many ES modules. But
this whole project has a bias: we wrote our own Firecracker client, our own jail, our own vsock handshake.
So the WebSocket server and the RFB client are both **hand-rolled and dependency-free.** The server is ~150
lines: the upgrade handshake (SHA-1 the client key with the magic GUID, base64 it back), and binary frames
with the client-side masking unwound. The client is a single embedded HTML file: a canvas, and just enough
RFB to be a desktop — the no-auth handshake, a *forced* 32-bpp pixel format so blitting to the canvas is a
plain byte reorder rather than a format-negotiation puzzle, Raw and CopyRect updates, and pointer/keyboard
input mapped to X keysyms. No CDN, no build step, no `node_modules`. One binary, one page.

I can't see a browser from here, so I proved it the layer down: a hand-rolled WebSocket *client* that drove
the exact browser path. The upgrade completes with a valid accept token; a full RFB handshake runs through
the proxy in both directions (the client's masked frames get unmasked correctly, or the handshake would
stall); and a full 3.91 MiB framebuffer pulls through in 32 KiB WebSocket frames — which is the moment the
16-bit frame-length encoding actually gets exercised, the branch the tiny handshake never touches. **Lesson:
a hand-rolled wire protocol has branches your happy path never reaches; find the one that only triggers on
big payloads and make something big go through it.** The last mile — actual pixels on a canvas, live mouse
and keys — is a real browser in front of a human.

**Next:** 5c — the SvelteKit web UI that lists your boxes and embeds this desktop, so the whole thing is a
tab. (And "60 fps" stays honest: RFB gives a responsive desktop, not smooth full-motion video; the
video-codec path is a stretch we take only if a static desktop ever feels heavy.)

---

## Phase 5, Increment 3 — the whole thing, in a tab

The desktop worked in a browser, but you still drove everything from the CLI. Increment 3 is the web UI: a
dashboard that lists your boxes, spins them up, stops and forks them, and opens their desktops — a real
control panel. Two problems stood between "the daemon" and "a web app," and they are worth naming because
they shaped the design ([decision 0010](decisions/0010-web-ui-and-http-surface.md)).

**The daemon lives on a Unix socket, and a browser can't dial one.** ephemerad listens on
`run/ephemerad.sock`, mode 0600 — file permissions instead of an open port, on purpose. So the web surface
is a deliberate, *opt-in* addition: `ephemerad -http 127.0.0.1:8080` starts a second listener on TCP that
serves the **same handler** — the whole API, the desktop WebSocket, and the UI — from one origin. Off by
default, meant for loopback, and it warns if you bind it anywhere routable, because it is the full control
plane guarded by nothing but the address it sits on. The safe default stays safe; you turn on the browser
door when you want it.

**A web app is files that have to come from somewhere.** The rest of ephemera is one self-contained binary —
no CDN, everything embedded. The UI plays by the same rule: `web/` is a SvelteKit SPA, its build lands in
`internal/webui/dist`, and `go:embed` bakes it into the daemon. One binary ships the UI. The built output is
committed, so `go build` and the Go tests never need Node; `build.sh` rebuilds it when Node is around. This
is the one place ephemera stopped hand-rolling from scratch — a Node toolchain enters the repo — and that
cost is real and bounded to `web/`.

The RFB client I'd written for the standalone page became a **framework-agnostic module** (`lib/rfb.js`):
hand it a canvas and a WebSocket URL and it renders a desktop and forwards input. The dashboard's "Desktop"
button opens a `/box/{id}` route that points that module at the daemon's `…/desktop/ws`. Same client, two
front doors.

**On the look:** the design language is [shadcn/ui](https://ui.shadcn.com)'s token system with
[Geist](https://vercel.com/font), Vercel's typeface — the palette, radius, and componentry that make
developer tools feel right, applied by hand rather than pulling in Tailwind. Geist is self-hosted via
Fontsource so it ships embedded, no CDN, consistent with everything else. **Lesson: a "design system" is
mostly a set of tokens and a font; you can wear the look without wearing the toolchain.**

Verified headlessly, which for a UI means: the SPA is served (index, hashed assets, and a client route like
`/box/abc` falling back to the app so the router takes it), the JSON API answers same-origin so the
dashboard's calls work, a Geist woff2 serves as `font/woff2`, and the desktop WebSocket handshakes through
the daemon (`RFB 003.008`). What a headless check can't do is *look* at it — the rendered dashboard and the
live canvas are a browser in front of a human.

**Next:** the last piece of the UI — a terminal in the browser (an in-page terminal over a shell WebSocket),
so a box offers both its desktop and its shell from the same tab.

---

## Phase 5c, finished — a terminal in the tab

The box already showed its desktop in the browser; the last piece is its *shell*. And this one was mostly
plumbing already in place, which is the nice kind of feature to build.

`eph ssh` runs an interactive pty in the guest over vsock, using a tiny framing that lets one connection
carry both the terminal's bytes and window-resize events (`agent.Shell`). The browser terminal needs exactly
that session — it just reaches it through a WebSocket instead of a raw socket. So the daemon's shell route
(`…/shell/ws`) is thin: it upgrades the WebSocket, then hands `agent.Shell` an `io.Pipe` for keystrokes, an
adapter that writes the shell's output back as binary WebSocket messages, and a channel for resizes. The web
terminal and the CLI terminal are now the *same session code* behind two different front doors.

The one wrinkle is telling keystrokes from resizes on the browser's socket. WebSocket messages are already
framed, so no length prefixes are needed — just a one-byte tag on what the browser sends: `0x00` + bytes for
keystrokes, `0x01` + rows + cols for a resize. Output back to the browser is all terminal bytes, so it needs
no tag at all. xterm.js on the page turns keystrokes into data messages and window resizes into resize
messages; the daemon demuxes them into the pipe and the resize channel. **Lesson: when a transport already
frames messages for you, don't re-implement framing inside it — a single tag byte is enough.**

xterm.js is the one real dependency the terminal adds, and like everything else the UI ships it is bundled by
Vite and served from the box — no CDN. Desktop and terminal became tabs on the box page; the RFB desktop
turned into its own component along the way, so the page is just a tab bar over two views.

Verified the way a terminal can be without a screen: drive the shell WebSocket headlessly, send a resize,
type `echo $((6*7))`, and watch both the echoed command *and* `42` come back — proof the keystrokes reached
the guest pty and the shell actually ran. The rendered xterm and live typing are, as ever, a browser in front
of a human.

**That completes 5c** — the whole of ephemera is now a tab: list your boxes, spin one up, and open its
desktop or its shell, all from the browser. What's left in Phase 5 is only the optional **5d** smoothness
path, and it stays optional: RFB gives a responsive desktop, and a video codec is a cost we pay only if a
static desktop ever feels heavy.
