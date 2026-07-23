# 0004 — A hand-rolled, unprivileged jail instead of Firecracker's jailer

- **Status:** Accepted
- **Date:** 2026-07-23
- **Phase:** 2 (Isolation & networking)

## Context

The last piece of Phase 2 is confining the VMM itself: a chroot so Firecracker can see only the handful of
files it needs, and a process namespace so it cannot see or signal anything else on the host. Firecracker
ships a tool for exactly this — the `jailer` — and the roadmap named it.

But the jailer is built on an assumption this project spent Phases 1 and 2 rejecting. It is a **privileged**
program: it runs as root, does `chroot`, `mknod`, and `setns`, sets up cgroups, and only then drops to an
unprivileged uid and exec's Firecracker. Its whole reason to exist is to be the root front-end that drops
down to safe. Our daemon has never had root to drop — it holds `CAP_NET_ADMIN` for TAP devices and owns a
cgroup subtree, and nothing more.

Two facts reframed the decision:

1. **We already have most of what the jailer provides.** Firecracker already runs as our unprivileged user,
   so there is no privilege to drop. Its seccomp filters are on by default — the jailer does not add them.
   Network isolation and resource caps are done (ADRs 0002, 0003). What is genuinely missing is only the
   filesystem chroot and the process/mount namespaces.
2. **On this host the jailer is barely runnable.** `sudo` prompts for a password, so a daemon that shells
   out to a root jailer per machine cannot run unattended. Adopting it would mean a root daemon — the exact
   thing the design avoids.

## Decision

**Hand-roll the confinement with a user namespace, unprivileged.** A small helper, `eph-jail`, is spawned
by the daemon into new user, mount, and pid namespaces (Go's `SysProcAttr.Cloneflags` with uid/gid
mappings). Inside a fresh user namespace an ordinary uid becomes root — root over *that namespace only* —
which is exactly enough to bind-mount a minimal root, `pivot_root` into it, mount a private `/proc`, and
exec Firecracker. Step outside the namespace and the process is still just the uid that started it, with no
capability over anything real.

Deliberately **not** a new network namespace: the machine keeps the host's network, so the point-to-point
TAP and the static firewall from ADR 0002 are unchanged. Seccomp stays Firecracker's own, on by default —
the jail does not try to replace it.

## Reasons

1. **It fits the privilege model instead of breaking it.** The jail needs no root, no setuid helper, and no
   new file capability — only that the kernel permit unprivileged user namespaces, which it does here. The
   daemon that was unprivileged in Phase 1 stays unprivileged now.
2. **We only build what is missing.** Filesystem and process isolation are the gap; the jailer's
   privilege-drop and seccomp machinery would have been dead weight, because we never had the privilege and
   already have the filters.
3. **It composes with what is already there.** Because the confinement is expressed as clone flags on the
   spawn, `CLONE_INTO_CGROUP` folds into the same clone: the VMM lands in its cgroup *and* its namespaces in
   one syscall. Verified — a machine is capped and jailed at once, in a single spawn.
4. **The one real conflict has a clean resolution.** A process in a child user namespace has no
   `CAP_NET_ADMIN` over the host network namespace, so a jailed Firecracker could not normally open a host
   TAP. The kernel's TAP-*owner* exception fixes it: a device whose owner uid matches the caller may be
   opened without the capability. `vmnet` now marks each TAP owned by our uid (`TUNSETOWNER`), so a jailed
   VMM opens its own interface with no privilege and networking survives the jail intact.

## Where NOT hand-rolling would have been the wrong instinct

This is the mirror of ADR 0001. There, hand-rolling the Firecracker *client* was right: the risk of a bug
was a bad error message, and the learning was the point. Here the thing being built is a security boundary,
and a subtly-wrong one is a sandbox escape, not a bad error. So the calculus is different — which is why the
jail leans on primitives the kernel enforces (namespaces, `pivot_root`, TAP ownership) and on Firecracker's
*own* audited seccomp, rather than reimplementing a syscall filter. Hand-rolled here means "assemble trusted
kernel primitives ourselves," not "write our own security-critical filtering."

## Costs we are accepting

- **Two processes, not one.** Go offers no hook between clone and exec, so the namespace setup has to run in
  a spawned helper (`eph-jail`) that pivots and then exec's Firecracker. One more binary to ship beside the
  daemon.
- **Path duality.** Inside the jail the VMM sees `/vmlinux`, `/rootfs.ext4`, `/run/firecracker.sock`; the
  daemon reaches the same sockets at their host location under the jail directory. The machine layer holds
  both views, and getting them crossed would leave a machine unreachable. Covered by tests.
- **Opt-in, for now.** Unlike caps, jailing is off unless asked for (`-jail`). It is new and
  security-sensitive and wants soak time before it becomes the default it is designed to be. It needs no
  privileged setup, so making it default-on later is a one-line change, not a new host requirement.
- **Depends on unprivileged user namespaces being enabled.** Some hardened kernels disable them; `Available`
  checks the knobs and the feature simply stays off there.

## Verified

Live: a jailed VMM runs in its own user, mount, and pid namespaces, shares the net namespace, and its mount
table holds only the eight things `eph-jail` bound in — the images, three device nodes, and a private
`/proc`. Boot, exec over the relocated vsock, and teardown all work, and jail + cgroup compose in one clone.

## Phase 2 complete

Networking (0002), resource caps (0003), and now confinement close out Phase 2: a machine has a filtered
network, capped resources, and runs jailed — with the daemon still holding nothing but `CAP_NET_ADMIN` and a
delegated cgroup subtree.
