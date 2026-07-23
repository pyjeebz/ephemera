#!/bin/sh
# Non-interactive init: prove the guest works, then halt.
#
# Firecracker only forwards serial *input* from a real TTY, so a piped shell
# session can't be scripted. Booting straight into this instead gives a
# deterministic, automatable check that the VM came up correctly.
. /sbin/eph-mounts

echo
echo "=== ephemera guest selftest ==="
echo "alpine:   $(cat /etc/alpine-release)"
echo "kernel:   $(uname -r) $(uname -m)"
echo "hostname: $(hostname)"
echo "identity: $(id)"
echo "cpus:     $(nproc)"
echo "memory:   $(free -m | awk '/^Mem:/{print $2" MiB"}')"
echo "rootfs:   $(df -h / | awk 'NR==2{print $1"  "$2" total, "$4" free"}')"
echo "rw test:  $(echo ok > /tmp/.probe && cat /tmp/.probe && rm -f /tmp/.probe)"
echo "block:    $(ls /dev/vd* 2>/dev/null | tr '\n' ' ')"
echo "vsock:    $(test -e /dev/vsock && echo present || echo 'absent (not configured until phase 3)')"
echo "net:      $(ip -o link 2>/dev/null | awk -F': ' '{print $2}' | tr '\n' ' ')"
echo "=== selftest OK ==="
echo

# Firecracker exits when the guest triggers a *reset*, which `reboot=k` on the
# kernel cmdline routes through the i8042 controller. A poweroff would halt the
# guest but leave the VMM running.
reboot -f
