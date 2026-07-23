#!/bin/sh
# Shared early-boot setup, sourced by whichever init the kernel was pointed at.
#
# The kernel is built with CONFIG_DEVTMPFS_MOUNT=y, so /dev already exists by the
# time we run — that is what gives us /dev/console for stdio without anyone ever
# creating device nodes as root on the host. We mount the rest ourselves.
mount -t proc  proc  /proc
mount -t sysfs sysfs /sys
mkdir -p /dev/pts /dev/shm
mount -t devpts devpts /dev/pts
mount -t tmpfs  tmpfs  /dev/shm

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
