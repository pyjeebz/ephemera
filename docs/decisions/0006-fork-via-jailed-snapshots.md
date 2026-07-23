# 0006 — Fork by restoring a jailed snapshot, with a sparse per-fork disk

- **Status:** Accepted (disk half superseded by [0007](0007-read-only-base-with-ram-overlay.md))
- **Date:** 2026-07-23
- **Phase:** 3 (Snapshots, fork & warm pool)

> **Update (0007):** the per-fork *sparse disk copy* below was the interim answer and has been replaced by a
> shared read-only base with a RAM overlay — forks now copy nothing (15–31 ms, down from 78–85 ms). The
> vsock-per-fork mechanism and "fork restores a jailed snapshot" decision here are unchanged.

## Context

Fork is the Phase 3 headline: turn one snapshot into several running machines at once, in under 100 ms
each. Restoring a single snapshot already worked (ADR-less, in the buildlog) at ~63 ms. Two things stood
between that and fork, and both are about giving each copy something of its own.

1. **The vsock socket collides.** A restored guest binds the exact vsock path stored in its snapshot. Two
   restores of one snapshot would try to bind the same host socket, and the second fails.
2. **The disk collides.** All copies would reference the same rootfs image, read-write. Two guests writing
   one ext4 image corrupts it.

## Decision

**Fork restores a *jailed* snapshot, and gives each copy a private, sparse copy of the disk.**

- **Per-fork vsock, for free, from the jail.** A jailed guest's vsock lives at the in-jail path
  `/run/vsock.sock`, which resolves to a *different host socket* in every restore's own chroot. So the
  collision simply does not happen — each fork's socket is distinct because each fork's root is distinct.
  This is why fork requires the snapshot to have been taken from a jailed machine: only then is the stored
  path the chroot-relative one that varies per copy.
- **Per-fork disk, by sparse copy.** Each fork gets its own writable rootfs, copied from the snapshot's
  frozen disk into its jail directory (so it lands at `/rootfs.ext4` after the pivot and is cleaned up with
  the jail). The copy skips holes — a 1 GiB image is ~50 MiB of actual data — which is what keeps it fast.
- The snapshot's memory and state files are bound **read-only** into every fork, so one memory image backs
  all of them at once without copying.

## Reasons

1. **The jail was already the right tool.** The per-fork vsock problem has an ugly general solution
   (rewrite the snapshot, or per-machine network namespaces) and a free one: the chroot each machine
   already has. Reusing it means fork inherits the isolation work rather than bolting on more.
2. **Sparse copy hit the target without new machinery.** A byte-for-byte copy of the disk made a fork take
   ~12 s — the copy, not the ~63 ms memory restore, was the whole cost. Copying only the data extents
   (`SEEK_DATA`/`SEEK_HOLE`) dropped it to **78–85 ms per fork**, under the checkpoint, with no change to
   the guest or the image. The simplest thing that could work, worked.
3. **Read-only shared memory is what makes it a fork and not N boots.** Every copy is reconstituted from
   the same memory image, mapped read-only, so the expensive artifact — a booted, agent-running guest's
   RAM — is produced once and reused, not regenerated.

## Costs we are accepting

- **The disk is copied, not shared.** Sparse makes it cheap (~50 MiB), but it is still O(disk data) per
  fork, not O(1). The zero-copy answer is a **read-only base image plus a guest-side overlay** (the base
  attached read-only and shared by every fork, per-fork writes going to a tmpfs upper that lives in each
  fork's own restored memory). That removes the copy entirely but needs guest-init changes and a rebuilt
  image; deferred as the next optimisation now that the mechanism is proven.
- **Fork requires a jailed snapshot.** A non-jailed snapshot can still be restored, but only one at a time,
  because its vsock path is absolute. Fine: fork is a jailed-world feature by construction.
- **Snapshots are self-contained copies.** Snapshot writes three files (state, memory, disk) out of the
  jail so they outlive the source machine. The memory copy is O(RAM); it happens once, off the hot path,
  never during a restore.

## A latent issue this surfaced

Regular multi-machine operation shares one rootfs image read-write across machines — the same collision
fork solves, but present today for two plain `create`s of jailed machines that happen to run at once. It
has not bitten because machines have been booted one at a time, but it is a real bug. The same read-only
base + guest-overlay that gives fork its zero-copy disk fixes it for everyone, so the two wants point at the
same next piece of work.

## Verified

Live: one snapshot restored into two machines at once, each in its own jail with its own vsock socket and
its own disk; a distinct value written into each fork's disk is read back unclobbered from both, proving
they are independent rather than one machine addressed twice. Fork times: 78 ms and 83 ms.
