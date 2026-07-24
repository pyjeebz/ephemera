#!/bin/sh
# PID 1, before anything else. Give the machine a writable root by overlaying a
# writable upper layer over the read-only disk image, leaving the image itself
# untouched and shared.
#
# Every write the guest makes — to /etc, /run, /tmp, anywhere — lands in the
# upper layer instead of the disk. Where the upper *lives* decides whether the
# machine is ephemeral or persistent:
#
#   * no persist disk  -> the upper is a tmpfs, so changes evaporate with the
#     machine's RAM. This is a throwaway sandbox, and it is what lets any number
#     of machines (and forks) share one read-only base with no copy.
#   * a persist disk    -> the upper lives on a writable disk that survives across
#     stops and starts, so the machine is a computer you keep: its installs,
#     files, and state are still there next time.
#
# Either way the base image is never written, so it stays shareable read-only.
# The real init to hand control to is named on the kernel command line as
# eph.init=, and eph.persist= names the block device to use for the upper.

# Mount /proc just long enough to read the command line, then let it go — the
# real init mounts its own.
mount -t proc proc /proc
real_init="$(sed -n 's/.*eph\.init=\([^ ]*\).*/\1/p' /proc/cmdline)"
persist_dev="$(sed -n 's/.*eph\.persist=\([^ ]*\).*/\1/p' /proc/cmdline)"
umount /proc
[ -n "$real_init" ] || real_init=/sbin/eph-init

# Mount the layer that holds the writable upper and work directories: a persist
# disk if one was given and is really there, otherwise RAM. A persist disk keeps
# its own journal, so an unclean stop replays cleanly on the next mount.
if [ -n "$persist_dev" ] && [ -b "$persist_dev" ]; then
	mount "$persist_dev" /mnt
else
	mount -t tmpfs tmpfs /mnt
fi
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
