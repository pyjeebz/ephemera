#!/usr/bin/env bash
# Build the microVM guest image: a kernel and a root filesystem with the
# ephemera guest agent as its init. Run it on a host, as root.
#
# Usage:
#   sudo ./build-image.sh OUTDIR [GUEST_BINARY]
#
# GUEST_BINARY defaults to building ./cmd/ephemera-guest with the Go found
# on PATH (or /usr/local/go/bin). OUTDIR gets vmlinux and rootfs.ext4.
#
# The kernel and the Ubuntu 24.04 base come from Firecracker's own CI
# artifacts and are checked against pinned SHA-256 sums.
set -euo pipefail

FC_CI="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64"
KERNEL_FILE="vmlinux-6.1.155"
KERNEL_SHA256="e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2"
BASE_FILE="ubuntu-24.04.squashfs"
BASE_SHA256="68321e0482baeb3844dafe8a6b08a6902401a7afc41fbfd8c3d9ea08aadd244f"
ROOTFS_SIZE="2G"

log() { echo "build-image: $*"; }
die() { echo "build-image: error: $*" >&2; exit 1; }

[ $# -ge 1 ] || die "usage: $0 OUTDIR [GUEST_BINARY]"
[ "$(id -u)" -eq 0 ] || die "run as root (it unpacks a root filesystem)"
out="$1"
guest_bin="${2:-}"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$out"
cache="$out/.cache"
mkdir -p "$cache"

fetch() { # fetch FILE SHA256
	local f="$cache/$1"
	if [ ! -f "$f" ] || ! echo "$2  $f" | sha256sum -c --quiet - 2>/dev/null; then
		log "downloading $1"
		curl -fsSL -o "$f.part" "$FC_CI/$1"
		echo "$2  $f.part" | sha256sum -c --quiet - || die "$1 failed its checksum"
		mv "$f.part" "$f"
	fi
}
fetch "$KERNEL_FILE" "$KERNEL_SHA256"
fetch "$BASE_FILE" "$BASE_SHA256"

command -v unsquashfs >/dev/null || {
	log "installing squashfs-tools"
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q squashfs-tools >/dev/null
}

if [ -z "$guest_bin" ]; then
	gobin="$(command -v go || echo /usr/local/go/bin/go)"
	[ -x "$gobin" ] || die "no Go toolchain found; pass a prebuilt GUEST_BINARY"
	guest_bin="$cache/ephemera-guest"
	log "building the guest agent"
	(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$gobin" build -trimpath -o "$guest_bin" ./cmd/ephemera-guest)
fi
[ -x "$guest_bin" ] || die "guest binary $guest_bin is not executable"

work="$(mktemp -d "$out/.rootfs.XXXXXX")"
trap 'rm -rf "$work"' EXIT
log "unpacking the Ubuntu base"
unsquashfs -q -f -d "$work/root" "$cache/$BASE_FILE" >/dev/null

install -m 0755 "$guest_bin" "$work/root/sbin/ephemera-guest"
mkdir -p "$work/root/workspace"
# Nothing should log in to a sandbox; drop any SSH host keys and keys.
rm -f "$work/root"/etc/ssh/ssh_host_* "$work/root"/root/.ssh/authorized_keys
echo "ephemera" >"$work/root/etc/hostname"

log "writing rootfs.ext4"
rm -f "$out/rootfs.ext4.part"
truncate -s "$ROOTFS_SIZE" "$out/rootfs.ext4.part"
mkfs.ext4 -q -F -L ephemera-root -d "$work/root" "$out/rootfs.ext4.part"
mv "$out/rootfs.ext4.part" "$out/rootfs.ext4"
cp "$cache/$KERNEL_FILE" "$out/vmlinux"

log "done: $out/vmlinux, $out/rootfs.ext4"
log "boot with: init=/sbin/ephemera-guest"
