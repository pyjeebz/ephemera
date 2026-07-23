#!/bin/sh
# PID 1 inside the guest — interactive variant.
#
# Run build/boot-vm.sh from a real terminal and this drops you into a root shell
# on the serial console. Phase 1 replaces this with the control-plane-driven
# guest agent.
. /sbin/eph-mounts

echo
echo "  ephemera guest"
echo "  alpine $(cat /etc/alpine-release 2>/dev/null)  |  kernel $(uname -r)"
echo

exec /bin/sh
