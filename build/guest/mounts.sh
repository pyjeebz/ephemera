#!/bin/sh
# Shared early-boot setup, sourced by whichever init the kernel was pointed at.
#
# The kernel mounts devtmpfs at /dev at boot (CONFIG_DEVTMPFS_MOUNT=y), but the
# overlay init pivots into a new root and the kernel's /dev is left behind on the
# old one — so the new /dev is just the base image's static stub. Remount a fresh
# devtmpfs here to get the live device nodes back: /dev/ptmx for pseudo-terminals
# (interactive shells), /dev/kvm, the disks, everything. devtmpfs is a singleton,
# so this is the same instance the kernel mounted, not a second copy.
mount -t devtmpfs devtmpfs /dev
mount -t proc  proc  /proc
mount -t sysfs sysfs /sys
mkdir -p /dev/pts /dev/shm
# ptmxmode makes /dev/ptmx usable; the guest opens it to allocate a pty pair.
mount -t devpts -o gid=5,mode=620,ptmxmode=666 devpts /dev/pts
mount -t tmpfs  tmpfs  /dev/shm

# Bring up loopback. The kernel creates lo but leaves it DOWN, so 127.0.0.1 is
# unreachable until something raises it — even a box with no external network
# should have working loopback, the way any real computer does. Databases, dev
# servers, and the desktop's VNC bridge all talk to 127.0.0.1 and expect it up.
ip link set lo up 2>/dev/null || ifconfig lo up 2>/dev/null

# A networked guest already has its hostname, address and default route: the
# kernel is built with CONFIG_IP_PNP=y and did all of it from the ip= boot
# argument, before init existed. This line is for the machines that have no
# network at all, where none of that ran.
hostname ephemera

# The kernel writes the nameservers it was given to /proc/net/pnp, in exactly
# the format resolv.conf wants. Copying it means the resolver is chosen by the
# host at boot rather than baked into the image — which matters once egress is
# filtered, because the firewall and the guest have to agree on which resolver
# is the allowed one.
if grep -q '^nameserver' /proc/net/pnp 2>/dev/null; then
	grep '^nameserver' /proc/net/pnp > /etc/resolv.conf
else
	echo "nameserver 1.1.1.1" > /etc/resolv.conf
fi
