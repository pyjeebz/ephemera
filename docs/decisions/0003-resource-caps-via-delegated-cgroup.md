# 0003 — Resource caps via a delegated cgroup subtree, placed with CLONE_INTO_CGROUP

- **Status:** Accepted
- **Date:** 2026-07-23
- **Phase:** 2 (Isolation & networking)

## Context

A machine should not be able to spend the whole host. Firecracker's `machine-config` sets the guest's
*virtual* CPU count and RAM, but those are promises to the guest, not limits on the host — a runaway VMM,
a memory leak, or a guest that pins every vCPU can still take the box down. The host-side cap is cgroup v2:
`memory.max` and `cpu.max` on the VMM process.

Two things had to be decided:

1. **How does an unprivileged daemon get to write cgroup limits at all?** Creating cgroups under
   `/sys/fs/cgroup` needs privilege the daemon deliberately does not have — it holds `CAP_NET_ADMIN` for
   TAP devices and nothing more (ADR 0002).
2. **How does the VMM end up *inside* its cgroup**, given that placing a process into a cgroup is a
   privileged-ish operation with its own rules?

## Decision

**A delegated subtree the daemon owns, per-machine leaves it creates unprivileged, and placement at spawn
time via `CLONE_INTO_CGROUP`.** Caps apply automatically whenever the subtree is available, rather than on
request.

- `build/host-setup.sh` (root, once) creates `/sys/fs/cgroup/ephemera`, enables the `cpu` and `memory`
  controllers for its children, and `chown`s the subtree to the user. After that the daemon makes one leaf
  per machine and writes `memory.max`/`cpu.max` with no privilege.
- The VMM is placed into its leaf by the clone that creates it — Go's `SysProcAttr.UseCgroupFD`, which is
  `clone3(CLONE_INTO_CGROUP)` — so it is inside its limits before it runs a single instruction. No window
  where a machine exists uncapped, and no race between spawn and a follow-up write to `cgroup.procs`.
- `memory.max` = guest RAM + 64 MiB headroom; `cpu.max` = the vCPU count in whole cores.

## Reasons

1. **Same privilege model as the network, for the same reason.** One-time root setup, unprivileged
   runtime. The daemon owning a subtree can do everything it needs *inside* that subtree and nothing
   outside it — the blast radius of a daemon bug is the ephemera cgroup, not the machine.
2. **`CLONE_INTO_CGROUP` closes the uncapped window.** The older pattern — fork, then write the child's pid
   to `cgroup.procs` — leaves the process running in the parent's cgroup for the moment between the two,
   which for a memory limit is exactly the moment a runaway would escape. Spawning directly into the
   cgroup removes the window and the race in one move. It also needs Go 1.22+, which we have.
3. **Caps are a restriction, so they default to on.** The network is a capability you grant with `-net`;
   a resource cap is a restriction you apply, and the sensible default for a restriction is "on when
   possible." So a plain machine is capped once the subtree exists, and only an explicit `-cgroup-root ""`
   turns it off. A daemon that cannot find the subtree logs once and runs uncapped — capping is defence in
   depth, not a precondition for booting.
4. **Headroom, not a hair-trigger.** `memory.max` set to exactly the guest's RAM would OOM the VMM the
   instant the guest touched its last page, because the limit also has to cover Firecracker's own
   footprint and the page tables mapping guest memory. The 64 MiB of slack means the cap is a backstop
   against a leak or a runaway, not a limit that bites in normal running.

## The delegation-boundary problem, and the cost we are accepting

Owning the subtree is enough to *create leaves and write limits*. It is **not** enough to *place a process
into one*. Putting a process into a cgroup is a migration, and cgroup v2 requires write access to the
`cgroup.procs` of the **common ancestor** of where the process currently is and where it is going. The
daemon starts life outside the subtree — in whatever cgroup launched it, `/init.scope` on this box — so
that common ancestor is the hierarchy root, whose `cgroup.procs` is root-owned. Every spawn therefore
failed with a bare `EACCES` that read like a Firecracker problem, until the cause was clear.

**On a system with a systemd user session this simply does not arise:** you run `ephemerad` from a unit
with `Delegate=yes`, systemd starts it already inside its own delegated scope, the common ancestor of the
daemon and its leaves is *within* the owned subtree, and there is no boundary to cross. That is the right
answer and the intended deployment.

This box (WSL2) has no systemd user session, so `host-setup.sh` grants the one thing missing: write access
to the root `cgroup.procs`, the ancestor the migration checks. **On a single-user host this is not a
meaningful boundary — the user already has root** — but it is a real widening of access that would be
wrong on a shared or production host, where the systemd-unit path is mandatory instead. `Available()`
checks for this grant up front, so the daemon disables caps with an explanation rather than failing on
every create.

Accepted, with eyes open:

- **A grant that is fine here and wrong elsewhere.** Documented in the script and above; the production
  path is the systemd unit, not this grant.
- **Uniform caps.** Every machine gets `vcpus` cores and its RAM + headroom; there is no per-machine
  override beyond the vCPU/RAM it was created with. Fine for now.
- **The cap is a backstop, not a scheduler.** `cpu.max` limits, it does not weight or prioritise; two busy
  machines still contend. Shares/weights are a later refinement if they are ever wanted.

## Verified

Live, against real cgroupfs: the VMM lands in its leaf's `cgroup.procs` (so `CLONE_INTO_CGROUP` did its
job), the limit files hold the right values, and the leaf is removed on destroy. Enforcement itself
checked directly — a 20%-of-a-core cap held a busy loop to ~21% measured, and a 64 MiB cap got a 200 MiB
allocator OOM-killed (`memory.events` `oom_kill 1`, exit 137).

## Still outstanding in Phase 2

The jailer — chroot, namespaces, seccomp — is the last isolation piece and gets its own record.
