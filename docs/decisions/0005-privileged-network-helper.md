# 0005 — A privileged network helper, so the daemon holds no capability

- **Status:** Accepted
- **Date:** 2026-07-23
- **Phase:** 2 (Isolation & networking)

## Context

ADRs 0002 and 0004 were each right on their own and quietly incompatible together.

- **0002 (networking)** gave the daemon `CAP_NET_ADMIN` as a file capability, so it could create TAP
  devices. Minimal, unprivileged-ish, and it worked.
- **0004 (the jail)** confines each VMM in a user namespace, created unprivileged, so a machine is isolated
  without the daemon holding root.

Then the two met, and jail + networking would not boot. The cause is a kernel rule that neither decision
saw coming: **a process holding a file capability is refused creation of a user namespace with the
unprivileged self-mapping.** The kernel blocks it deliberately — allowing it would let a process launder
its capabilities into a namespace where it is root. So the very capability the daemon needed for networking
made it unable to build the jail.

Two escape routes were closed:

- **Create the userns later, from inside the helper, with `unshare`.** Fails: `unshare(CLONE_NEWUSER)`
  requires a single-threaded process, and every Go program is multi-threaded from the first instruction.
  The user namespace has to come from the spawn-time `clone`, which means from a process that is *not*
  holding the capability.
- **Keep the capability and wrap the spawn in an uncapped intermediate.** Works, but leaves the VMM a
  grandchild of the daemon, so tracking, killing, and inspecting it all need extra bookkeeping, and every
  machine carries a spare process.

## Decision

**Take the capability off the daemon entirely.** A small binary, `eph-netadmin`, is the only component that
carries `CAP_NET_ADMIN`. It does exactly three things — create a TAP, destroy a TAP, check its own
capability — and the daemon and CLI, now holding *no* capability at all, reach the network only by
executing it.

With the capability gone from the daemon, the jail's clean spawn-time `clone(CLONE_NEWUSER)` is permitted
again, and jail + networking + caps compose with no wrapper and no grandchild: one Firecracker process,
jailed, networked, and capped.

`build/host-setup.sh` grants the file capability to `eph-netadmin` instead of to the daemon and CLI.

## Reasons

1. **It resolves the conflict at the root.** The daemon cannot both hold a capability and build a user
   namespace, so the capability leaves the daemon. Nothing else had to bend: the jail is unchanged, the
   firewall is unchanged, the TAP-owner trick from 0004 still lets the jailed VMM open its own interface.
2. **It is a strictly smaller trusted surface, not just a relocated one.** The privileged code is now a few
   dozen lines that create and destroy a TAP — auditable in one sitting. A bug in the daemon, which is where
   the complexity and the untrusted inputs live, has no capability to abuse; the worst it can do to the
   network is ask the helper to make or remove an interface.
3. **The daemon becomes genuinely zero-privilege.** Through Phase 2 the story was "unprivileged-ish — one
   capability." Now it is simply unprivileged: the daemon and CLI hold nothing, and every privileged action
   in the system is a one-time host-setup step or a call into one small helper. That is a cleaner claim and
   a truer one.

## Costs we are accepting

- **A subprocess per TAP operation.** Creating or destroying a machine's interface now forks `eph-netadmin`
  rather than making an in-process ioctl. Negligible against a VM boot, and TAP churn is once per machine.
- **A fourth binary to ship and to cap.** `eph-netadmin` sits beside the daemon like `eph-jail`, and
  host-setup must be re-run after rebuilding it, since a file capability does not survive a new inode.
- **The helper boundary is a text interface.** The daemon passes an interface name and addresses as
  arguments; a bug there is a bad TAP, not a crash. The surface is small and fixed.

## Amends

This supersedes the privilege model described in **0002** ("the daemon holds `CAP_NET_ADMIN`") and the
matching line in **0004**. The topology, firewall, cgroup, and jail decisions in those records stand
unchanged; only *where the network capability lives* has moved — from the daemon to `eph-netadmin`.

## Verified

The daemon and CLI run with no capability (`getcap` reports nothing on them). A jailed machine boots with
the uncapped daemon — the exact case that failed while the daemon was capped. With `eph-netadmin` capped by
host-setup, a machine that is jailed, networked, and capped at once reaches the internet through its
owner-stamped TAP.
