#!/bin/sh
# PID 1, before anything else. Give the machine a writable root that lives in
# RAM, leaving the disk image untouched and shared.
#
# Every write the guest makes — to /etc, /run, /tmp, anywhere — lands in a tmpfs
# upper layer instead of the disk. The disk is only ever the read-only lower
# layer of an overlay. Three things fall out of that, and they are the whole
# reason this exists:
#
#   * the base image is never modified, so any number of machines can share one
#     image read-only without corrupting it (a plain rw mount writes the ext4
#     journal even if userspace writes nothing);
#   * a fork needs no copy of the disk — it shares the same read-only base and
#     keeps its own writes in its own memory, which the snapshot already carries;
#   * the machine is truly ephemeral: its filesystem changes evaporate with its
#     RAM, which is what a throwaway sandbox should do.
#
# The real init to hand control to is named on the kernel command line as
# eph.init=, since this one has taken init='s usual place.

# Mount /proc just long enough to read the command line, then let it go — the
# real init mounts its own.
mount -t proc proc /proc
real_init="$(sed -n 's/.*eph\.init=\([^ ]*\).*/\1/p' /proc/cmdline)"
umount /proc
[ -n "$real_init" ] || real_init=/sbin/eph-init

# tmpfs holds the writable upper and work directories; the overlay stacks them
# over the read-only disk root.
mount -t tmpfs tmpfs /mnt
mkdir -p /mnt/upper /mnt/work /mnt/root
mount -t overlay overlay -o lowerdir=/,upperdir=/mnt/upper,workdir=/mnt/work /mnt/root

# Move into the overlay and detach the disk from the new view. The overlay keeps
# its own reference to the disk as the lower layer, so a lazy unmount hides it
# without pulling it out from under us.
mkdir -p /mnt/root/oldroot
cd /mnt/root
pivot_root . oldroot
cd /
umount -l /oldroot

exec "$real_init"
