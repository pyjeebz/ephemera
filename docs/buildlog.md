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
