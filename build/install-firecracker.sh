#!/usr/bin/env bash
# Install the Firecracker + jailer binaries into ~/.local/bin (no root needed).
#
# Firecracker ships a single static binary per release. We don't need a package
# manager or sudo — just download the release tarball, verify it, and drop the
# two binaries we care about onto PATH.
set -euo pipefail

VERSION="${FIRECRACKER_VERSION:-v1.16.1}"
ARCH="$(uname -m)"                    # x86_64
DEST="$HOME/.local/bin"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

url="https://github.com/firecracker-microvm/firecracker/releases/download/${VERSION}/firecracker-${VERSION}-${ARCH}.tgz"

echo ">> downloading firecracker ${VERSION} (${ARCH})"
curl -fsSL "$url" -o "$TMP/fc.tgz"

echo ">> extracting"
tar -xzf "$TMP/fc.tgz" -C "$TMP"
# tarball layout: release-<version>-<arch>/{firecracker,jailer,...}-<version>-<arch>
rel="$TMP/release-${VERSION}-${ARCH}"

mkdir -p "$DEST"
install -m 0755 "$rel/firecracker-${VERSION}-${ARCH}" "$DEST/firecracker"
install -m 0755 "$rel/jailer-${VERSION}-${ARCH}"      "$DEST/jailer"

echo ">> installed:"
"$DEST/firecracker" --version | head -1
"$DEST/jailer" --version | head -1
echo ">> location: $DEST/{firecracker,jailer}"
