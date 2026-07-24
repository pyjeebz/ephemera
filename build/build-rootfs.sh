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

# --desktop builds the heavier desktop image (headless X + VNC, ADR 0009) into a
# separate output, so the default box stays lean. Same build path, one build arg.
DESKTOP=0
for arg in "$@"; do
  case "$arg" in
    --desktop) DESKTOP=1 ;;
    *) echo "!! unknown argument: $arg (only --desktop)" >&2; exit 2 ;;
  esac
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$root/build/rootfs"
mkdir -p "$dest"

if [[ "$DESKTOP" == "1" ]]; then
  IMAGE_TAG="${IMAGE_TAG:-ephemera-desktop:latest}"
  # A desktop needs room for the X stack; the extra is sparse until written.
  ROOTFS_SIZE="${ROOTFS_SIZE:-3G}"
  img="$dest/rootfs-desktop.ext4"
  build_args=(--build-arg DESKTOP=1)
else
  IMAGE_TAG="${IMAGE_TAG:-ephemera-guest:latest}"
  ROOTFS_SIZE="${ROOTFS_SIZE:-1G}"
  img="$dest/rootfs.ext4"
  build_args=()
fi

if ! docker ps >/dev/null 2>&1; then
  echo "!! docker daemon not reachable — start it first:" >&2
  echo "     sudo service docker start" >&2
  exit 1
fi

# The agent runs inside the guest, so it is built statically (CGO off) — the
# minimal rootfs has no toolchain and we do not want to depend on its libc.
echo ">> building guest agent"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o "$root/build/guest/eph-agent" "$root/cmd/eph-agent"

echo ">> building guest image ($IMAGE_TAG)"
docker build -q "${build_args[@]}" -t "$IMAGE_TAG" "$root/build/guest" >/dev/null

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
# Firecracker exposes the rootfs as /dev/vda. The guest boots it read-only and
# overlays a RAM upper for writes, so the fstab and the image both say read-only.
printf '/dev/vda / ext4 ro,relatime 0 1\n' > "$staging/etc/fstab"
# No journal: the image is only ever mounted read-only, so a journal buys
# nothing — and worse, a journal flagged "needs recovery" cannot be replayed on
# a read-only mount, which makes the kernel refuse to mount root at all. Build
# it out and the read-only mount is always clean.
mkfs.ext4 -F -q -L ephemera-root -O '^has_journal' -d "$staging" "$img"
BUILD

echo ">> rootfs ready: $img"
ls -lh "$img" | awk '{print "   size: " $5}'
