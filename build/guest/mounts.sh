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

hostname ephemera
echo "nameserver 1.1.1.1" > /etc/resolv.conf
