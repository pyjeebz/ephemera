#!/usr/bin/env bash
# Build the release archives and their checksums.
#
# Usage: deploy/release/build.sh vX.Y.Z [OUTDIR]
#
# OUTDIR (default dist/) gets:
#   ephemera_vX.Y.Z_<os>_<arch>.tar.gz      the CLI, for Linux and macOS
#   ephemera_vX.Y.Z_windows_<arch>.zip      the CLI, for Windows
#   ephemera-host_vX.Y.Z_linux_amd64.tar.gz ephemera-server, ephemera-host and ephemera-guest
#   install.sh                              the CLI install script, so it can be served from a release
#   checksums.txt                           SHA-256 of every file above
set -euo pipefail

VERSION="${1:-}"
OUT="${2:-dist}"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || {
	echo "usage: $0 vX.Y.Z [OUTDIR]" >&2
	exit 2
}
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
GO="${GO:-go}"
LDFLAGS="-s -w -X main.version=$VERSION"

rm -rf "$OUT"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

docs=(README.md)
[ -f "$REPO/LICENSE" ] && docs+=(LICENSE)

build() { # build GOOS GOARCH CMD DEST
	(cd "$REPO" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$4" "./cmd/$3")
}

# The CLI, everywhere it runs.
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os="${target%/*}" arch="${target#*/}"
	name="ephemera_${VERSION}_${os}_${arch}"
	dir="$work/$name"
	mkdir -p "$dir"
	exe=ephemera
	[ "$os" = windows ] && exe=ephemera.exe
	build "$os" "$arch" ephemera "$dir/$exe"
	for d in "${docs[@]}"; do cp "$REPO/$d" "$dir/"; done
	if [ "$os" = windows ]; then
		(cd "$work" && zip -qr "$OUT/$name.zip" "$name")
	else
		tar -C "$work" -czf "$OUT/$name.tar.gz" --owner=0 --group=0 "$name"
	fi
	echo "built $name"
done

# The server side: Linux x86_64 with KVM only.
name="ephemera-host_${VERSION}_linux_amd64"
dir="$work/$name"
mkdir -p "$dir"
for cmd in ephemera-server ephemera-host ephemera-guest; do
	build linux amd64 "$cmd" "$dir/$cmd"
done
for d in "${docs[@]}"; do cp "$REPO/$d" "$dir/"; done
tar -C "$work" -czf "$OUT/$name.tar.gz" --owner=0 --group=0 "$name"
echo "built $name"

cp "$REPO/install.sh" "$OUT/install.sh"
(cd "$OUT" && sha256sum -- *.tar.gz *.zip install.sh >checksums.txt)
echo "wrote $OUT/checksums.txt"
