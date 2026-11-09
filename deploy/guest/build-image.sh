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
# artifacts, and Node.js from nodejs.org, all checked against pinned SHA-256
# sums. Python's pip and venv, git and build tools come from Ubuntu's
# archive, installed with apt inside the image, so the build needs network.
set -euo pipefail

FC_CI="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64"
KERNEL_FILE="vmlinux-6.1.155"
KERNEL_SHA256="e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2"
BASE_FILE="ubuntu-24.04.squashfs"
BASE_SHA256="68321e0482baeb3844dafe8a6b08a6902401a7afc41fbfd8c3d9ea08aadd244f"
NODE_VERSION="v24.22.2" # LTS
NODE_FILE="node-$NODE_VERSION-linux-x64.tar.xz"
NODE_SHA256="48cc0e21e048790e54c0cf5749c2725005313d652651c40b7a21feb9697aaf75"
APT_PACKAGES="python3-pip python3-venv git build-essential ca-certificates unzip xz-utils"
ROOTFS_SIZE="4G" # sparse: only what is written takes space

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

fetch() { # fetch URL FILE SHA256
	local f="$cache/$2"
	if [ ! -f "$f" ] || ! echo "$3  $f" | sha256sum -c --quiet - 2>/dev/null; then
		log "downloading $2"
		curl -fsSL -o "$f.part" "$1"
		echo "$3  $f.part" | sha256sum -c --quiet - || die "$2 failed its checksum"
		mv "$f.part" "$f"
	fi
}
fetch "$FC_CI/$KERNEL_FILE" "$KERNEL_FILE" "$KERNEL_SHA256"
fetch "$FC_CI/$BASE_FILE" "$BASE_FILE" "$BASE_SHA256"
fetch "https://nodejs.org/dist/$NODE_VERSION/$NODE_FILE" "$NODE_FILE" "$NODE_SHA256"

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
root="$work/root"

# The chroot borrows the host's /dev and /proc while apt runs. Cleanup
# unmounts them first and deletes with --one-file-system, and refuses to
# delete anything if a mount is still in place, so it can never reach into
# the host.
unmount_chroot() {
	local m
	for m in "$root/tmp" "$root/proc" "$root/dev/pts" "$root/dev"; do
		mountpoint -q "$m" 2>/dev/null && umount "$m"
	done
	return 0
}
cleanup() {
	unmount_chroot || true
	if grep -qF " $work/" /proc/mounts; then
		echo "build-image: warning: $work still has mounts; not deleting it" >&2
		return
	fi
	rm -rf --one-file-system "$work"
}
trap cleanup EXIT

log "unpacking the Ubuntu base"
unsquashfs -q -f -d "$root" "$cache/$BASE_FILE" >/dev/null

log "installing $APT_PACKAGES"
mount --bind /dev "$root/dev"
mount -t devpts devpts "$root/dev/pts" 2>/dev/null || true
mount -t proc proc "$root/proc"
mkdir -p "$root/tmp"
mount -t tmpfs -o mode=1777 tmpfs "$root/tmp"
# Replace resolv.conf rather than write through it: in the base image it
# may be a symlink to a path that would resolve on the host.
rm -f "$root/etc/resolv.conf"
cp -L /etc/resolv.conf "$root/etc/resolv.conf"
# The base is slimmed down: recreate the directories apt expects.
mkdir -p "$root/var/cache/apt/archives/partial" "$root/var/lib/apt/lists/partial" "$root/var/log/apt"
printf '#!/bin/sh\nexit 101\n' >"$root/usr/sbin/policy-rc.d" # start no services
chmod 0755 "$root/usr/sbin/policy-rc.d"
chroot "$root" env DEBIAN_FRONTEND=noninteractive apt-get update -q >/dev/null
# shellcheck disable=SC2086 # the package list is meant to split
chroot "$root" env DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends $APT_PACKAGES >/dev/null

log "installing Node.js $NODE_VERSION"
tar -xJf "$cache/$NODE_FILE" -C "$root/usr/local" --strip-components=1 \
	--exclude='*/CHANGELOG.md' --exclude='*/README.md' --exclude='*/LICENSE'

# Check every tool runs, while the chroot still has /dev and /proc.
# shellcheck disable=SC2016 # expanded inside the image, not here
tools="$(chroot "$root" sh -ec 'python3 --version; python3 -m pip --version | cut -d" " -f1-2; git --version; echo "node $(node --version)"; echo "npm $(npm --version)"; gcc --version | head -1')" ||
	die "a tool in the image does not run"
log "tools in the image: $(echo "$tools" | paste -sd, - | sed 's/,/, /g')"

chroot "$root" apt-get clean
rm -rf "$root"/var/lib/apt/lists/* "$root/usr/sbin/policy-rc.d" "$root/etc/resolv.conf"
unmount_chroot

install -m 0755 "$guest_bin" "$work/root/sbin/ephemera-guest"
mkdir -p "$work/root/workspace" "$work/root/mnt"
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
