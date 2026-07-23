#!/usr/bin/env bash
# Boot one microVM by hand, straight against the Firecracker API.
#
# This is deliberately raw — no SDK, no daemon. Firecracker starts as an idle
# process serving a REST API over a unix socket; you PUT the machine's shape
# into it (kernel, disks, cpu/mem), then PUT an InstanceStart action to boot.
# Phase 1 replaces this script with ephemerad doing the same calls in Go.
#
# The VM's serial console is wired to *this process's* stdio, so running the
# script from a terminal drops you into a guest shell; piping into it feeds
# commands to the guest.
set -euo pipefail

VCPUS="${VCPUS:-1}"
MEM_MIB="${MEM_MIB:-256}"
# /sbin/eph-init = interactive shell (needs a real TTY for serial input)
# /sbin/eph-selftest = non-interactive check that halts on its own
GUEST_INIT="${GUEST_INIT:-/sbin/eph-init}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kernel="$(readlink -f "$root/build/kernel/vmlinux")"
rootfs="$root/build/rootfs/rootfs.ext4"

[[ -f "$kernel" ]] || { echo "!! no kernel — run build/fetch-kernel.sh" >&2; exit 1; }
[[ -f "$rootfs" ]] || { echo "!! no rootfs — run build/build-rootfs.sh" >&2; exit 1; }

rundir="$root/run"; mkdir -p "$rundir"
sock="$rundir/fc-$$.sock"
rm -f "$sock"

fcpid=""
cleanup() { [[ -n "$fcpid" ]] && kill "$fcpid" 2>/dev/null || true; rm -f "$sock"; }
trap cleanup EXIT

firecracker --api-sock "$sock" &
fcpid=$!

# The API socket appears a moment after the process starts.
for _ in $(seq 1 200); do [[ -S "$sock" ]] && break; sleep 0.02; done
[[ -S "$sock" ]] || { echo "!! firecracker never created $sock" >&2; exit 1; }

api() {
  curl -sS --fail-with-body --unix-socket "$sock" \
       -X PUT "http://localhost$1" \
       -H 'Content-Type: application/json' \
       -d "$2"
}

# console=ttyS0  -> kernel logs + our shell land on the serial port
# reboot=k panic=1 -> guest reboot/panic exits the VM instead of hanging
# pci=off        -> Firecracker has no PCI bus; skip probing for one
api /boot-source "$(printf '{"kernel_image_path":"%s","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=%s"}' "$kernel" "$GUEST_INIT")"
api /drives/rootfs "$(printf '{"drive_id":"rootfs","path_on_host":"%s","is_root_device":true,"is_read_only":false}' "$rootfs")"
api /machine-config "$(printf '{"vcpu_count":%d,"mem_size_mib":%d}' "$VCPUS" "$MEM_MIB")"
api /actions '{"action_type":"InstanceStart"}'

wait "$fcpid"
