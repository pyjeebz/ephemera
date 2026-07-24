# 0008 — Persistence as a swappable overlay upper: tmpfs or a data disk

- **Status:** Accepted
- **Date:** 2026-07-24
- **Phase:** 4 (ephemera as a computer)

## Context

The Phase 4 reframe is that ephemera is a computer you use like a laptop. A laptop keeps its state: the
things you install, your files, your configuration are there next time. But everything through Phase 3 is
deliberately ephemeral — every machine boots a read-only base with a **tmpfs** overlay, so its writes
evaporate when it stops. That is exactly right for a throwaway sandbox or a fork, and exactly wrong for a
computer you keep.

So persistence has to be added without giving up what the ephemeral model bought: a shared, immutable base
that lets fork copy nothing and lets any number of machines run at once.

## Decision

**Make the overlay's writable upper layer a swappable thing: tmpfs for an ephemeral machine, a per-computer
writable disk for a persistent one.** Everything below it — the shared read-only base — is unchanged.

- A **persist disk** is a writable, journaled ext4 image, attached as the machine's second drive
  (`/dev/vdb`). The guest's overlay init points the overlay's `upperdir` at it (via `eph.persist=` on the
  kernel command line) instead of at a tmpfs. Every write the machine makes lands there and survives a stop.
- The base image stays read-only and shared, so **fork and snapshot are untouched** — they operate on the
  base, which no persistent computer ever modifies.
- A **computer** is the user-facing shape: a name plus one of these disks. `create` makes the disk and
  boots a machine on it; `stop` throws the machine away but keeps the disk; `start` boots a fresh machine on
  the same disk with the state back; `rm` deletes it. A stopped computer is just a disk on disk.

## Reasons

1. **It reuses the machinery instead of forking it.** The overlay init already stacks a writable upper over
   a read-only base; persistence is just choosing what that upper is. No second boot path, no
   writable-root image type, no change to the base. The one guest-side change is three lines: mount the disk
   if `eph.persist=` names one, else the tmpfs.
2. **The base stays shared and immutable, so fork/snapshot keep working.** A writable root would have
   forced a copy per computer and reopened the corruption problem two machines sharing an image caused
   (ADR 0007). Keeping writes in a separate upper disk means the base is still one read-only file everyone
   shares.
3. **The disk is small and honest.** It holds only the computer's *diff* from the base — what you installed
   and changed — not a whole root filesystem. It is sparse, journaled (so an unclean stop replays cleanly),
   and lives outside any jail directory so a jailed machine's teardown cannot delete it.
4. **Default follows intent.** A named computer you keep is persistent; a throwaway machine or a fork is
   ephemeral (tmpfs). The user does not choose a storage mode, they choose whether they are making a
   computer or a sandbox, and the storage follows.

## The gotcha that shaped the stop path

A machine has no graceful shutdown — stopping it means killing the VMM. The guest's filesystem cache is not
flushed on the way out, so a naive stop lost every write that had not yet reached the disk: the first
lifecycle test wrote a file, stopped, started, and the file was gone. The base mechanism was fine (a direct
test that called `sync` passed); the *stop* was dropping the page cache.

So **`stop` runs `sync` in the guest before killing it**, pushing the overlay's dirty pages onto the persist
disk. That is what makes a clean stop actually keep recent work. An *unclean* stop — a daemon crash, a
kill -9 — still loses the last unsynced writes, exactly like yanking power from a laptop; the journal keeps
the disk consistent, and recent unsaved work is the cost. That is the honest contract, and it is the same
one every real computer offers.

## Costs we are accepting

- **A clean stop depends on the agent.** `sync` goes through the guest agent over vsock; a wedged guest
  cannot be flushed, and its unsynced writes are lost on stop. Acceptable, and the same as a frozen laptop.
- **No live migration or snapshotting of a running persistent computer yet.** Snapshot/fork still target the
  ephemeral base; snapshotting a persistent computer's *disk* (to branch a set-up environment) is a natural
  next step but not built.
- **One disk per computer, uniform size.** Good enough; resizing and quotas are later refinements.

## Verified

A computer created, written to (a config file and a project), stopped, and started came back with both
intact — on a new machine and a fresh kernel, reading the same disk. An ephemeral machine, by contrast, was
confirmed to run on a tmpfs overlay whose writes do not persist. `create`/`ls`/`start`/`stop`/`rm` and
`eph shell <name>` all drive it from the CLI.
