# 0007 — A read-only base image with a per-machine RAM overlay

- **Status:** Accepted (completes the disk model 0006 deferred)
- **Date:** 2026-07-23
- **Phase:** 3 (Snapshots, fork & warm pool)

## Context

ADR 0006 got fork under the 100 ms checkpoint with a *sparse copy* of the disk per fork, and named the real
answer it was standing in for: a read-only base plus a guest-side overlay, which copies nothing. Two things
pushed that from "later" to "now":

1. **The copy is still O(disk), not O(1).** Sparse made it ~50 MiB instead of 1 GiB, but a fork still paid
   for its whole disk. A warm pool of many forks would feel it.
2. **A real bug was waiting behind the same design.** Every machine mounted the shared rootfs image
   read-write. Two of them at once — two plain `create`s, not even forks — would both write the ext4
   journal and corrupt the image. It had not bitten only because machines had been booted one at a time.

Both wants point at the same fix, so it was time to do it.

## Decision

**Every machine boots a read-only base image and overlays a RAM-backed writable layer over it.** The kernel
mounts the disk read-only; a first-stage init (`eph-overlay-init`, PID 1) stacks a `tmpfs` upper over it
with `overlayfs`, `pivot_root`s into the result, and hands off to the real init named on the cmdline as
`eph.init=`. Every write the guest makes lands in the tmpfs upper, in its own memory; the disk is never
touched.

- The drive is attached `is_read_only`, and the image is built **without a journal** (`mkfs.ext4 -O
  ^has_journal`) so a read-only mount is always clean — a journal flagged "needs recovery" cannot be
  replayed read-only, and the kernel refuses to mount root at all.
- A fork shares the one read-only base image with no copy. Its writes live in the RAM overlay that its
  restored memory already carries, so copies are isolated by construction.

## Reasons

1. **Fork becomes genuinely zero-copy.** With the base shared read-only, a fork copies nothing — it is just
   a memory restore. Measured **15–31 ms per fork**, down from 78–85 ms with the sparse copy, and well under
   the checkpoint. The expensive artifact, a booted guest's RAM, is produced once and reused.
2. **It fixes the corruption bug for everyone, not just forks.** A read-only image cannot be written by
   anyone, so any number of machines share it safely. Verified: two concurrent machines each wrote and read
   their own file, and the base image's checksum was unchanged afterward.
3. **It is what a throwaway sandbox should be.** A machine's filesystem changes evaporate with its RAM,
   which is the honest behaviour for something ephemeral by design — and it means nothing a guest does can
   ever persist into the next machine off the same image.
4. **The overlay is the guest's job, and the guest is the right place for it.** No host-side device-mapper,
   no privileged block plumbing, no per-fork scratch file. One small init script and a kernel that already
   has `CONFIG_OVERLAY_FS=y`.

## Costs we are accepting

- **Writable space is bounded by RAM.** The upper layer is tmpfs, so a machine that writes a lot competes
  with its own memory. Fine for sandboxes, and the size is the machine's memory, which is already a knob. A
  machine that needs a big scratch disk would want a second, writable drive — a later addition, not a
  change to this.
- **An extra pivot at boot.** `eph-overlay-init` mounts an overlay and `pivot_root`s before the real init
  runs. It costs a few milliseconds and adds one script to the image.
- **Snapshots assume a stable base.** A fork's restored memory is consistent with the base image it was
  taken over; rebuilding the base image out from under existing snapshots would be incoherent. The base is
  immutable in practice (nothing writes it), but a self-contained snapshot that copies the base once, at
  snapshot time, is the robust version if base images ever churn. Deferred.

## Supersedes

The per-fork sparse disk copy from **0006**. The vsock-per-fork mechanism and the "fork restores a jailed
snapshot" decision in 0006 stand unchanged; only the disk half is replaced — copy becomes share.

## Verified

Root is an `overlay` mount with the disk read-only beneath it; guest writes succeed and are ephemeral. Two
concurrent machines run isolated with the base image checksum unchanged. Fork of a jailed snapshot: 15 ms
and 31 ms, no copy. Full suite green with every machine now on the overlay.
