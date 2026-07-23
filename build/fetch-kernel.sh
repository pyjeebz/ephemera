#!/usr/bin/env bash
# Fetch a Firecracker-compatible guest kernel into build/kernel/.
#
# Firecracker boots an *uncompressed ELF* vmlinux via the Linux boot protocol —
# it cannot boot a compressed bzImage, which is what a distro kernel package
# ships. Upstream publishes known-good CI kernels, which is what we use here.
# We also grab the matching .config so the enabled options are inspectable.
set -euo pipefail

KERNEL_VERSION="${KERNEL_VERSION:-6.1.128}"
CI_CHANNEL="${CI_CHANNEL:-v1.12}"
ARCH="$(uname -m)"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$root/build/kernel"
mkdir -p "$dest"

base="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/${CI_CHANNEL}/${ARCH}"
img="vmlinux-${KERNEL_VERSION}"

if [[ -f "$dest/$img" ]]; then
  echo ">> kernel already present: $dest/$img"
else
  echo ">> downloading $img"
  curl -fsSL --progress-bar "$base/$img" -o "$dest/$img"
  curl -fsSL "$base/$img.config" -o "$dest/$img.config" || true
fi

# Sanity: must be an uncompressed ELF, not a bzImage.
if ! head -c 4 "$dest/$img" | grep -q $'\x7fELF'; then
  echo "!! $img is not an ELF image — Firecracker will refuse to boot it" >&2
  exit 1
fi

ln -sf "$img" "$dest/vmlinux"
echo ">> kernel ready: $dest/$img"
ls -lh "$dest/$img" | awk '{print "   size: " $5}'
