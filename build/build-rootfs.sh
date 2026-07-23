#!/usr/bin/env bash
# Turn the guest Dockerfile into an ext4 rootfs image Firecracker can boot.
#
#   docker build  ->  docker export  ->  fakeroot extract  ->  mkfs.ext4 -d
#
# Two tricks let this run without root:
#   * mkfs.ext4 -d populates a filesystem image directly from a directory, so we
#     never mount anything (mount(2) needs real privileges).
#   * fakeroot intercepts chown/mknod/stat, so extracted files keep root:root
#     ownership — and the /dev nodes in the export survive — even as uid 1000.
# The extract and the mkfs must share ONE fakeroot session, or the faked metadata
# is gone before mke2fs reads it.
set -euo pipefail

IMAGE_TAG="${IMAGE_TAG:-ephemera-guest:latest}"
ROOTFS_SIZE="${ROOTFS_SIZE:-1G}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$root/build/rootfs"
img="$dest/rootfs.ext4"
mkdir -p "$dest"

if ! docker ps >/dev/null 2>&1; then
  echo "!! docker daemon not reachable — start it first:" >&2
  echo "     sudo service docker start" >&2
  exit 1
fi

echo ">> building guest image ($IMAGE_TAG)"
docker build -q -t "$IMAGE_TAG" "$root/build/guest" >/dev/null

echo ">> exporting container filesystem"
cid="$(docker create "$IMAGE_TAG" /bin/true)"
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT
tarball="$(mktemp)"
docker export "$cid" -o "$tarball"

staging="$(mktemp -d)"
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$staging" "$tarball"' EXIT

echo ">> assembling ${ROOTFS_SIZE} ext4 image"
rm -f "$img"
truncate -s "$ROOTFS_SIZE" "$img"

fakeroot -- bash -euo pipefail -s "$tarball" "$staging" "$img" <<'BUILD'
tarball="$1"; staging="$2"; img="$3"
tar -xf "$tarball" -C "$staging"
# Firecracker exposes the rootfs as /dev/vda; give the guest a matching fstab.
printf '/dev/vda / ext4 rw,relatime 0 1\n' > "$staging/etc/fstab"
mkfs.ext4 -F -q -L ephemera-root -d "$staging" "$img"
BUILD

echo ">> rootfs ready: $img"
ls -lh "$img" | awk '{print "   size: " $5}'
