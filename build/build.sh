#!/usr/bin/env bash
# Build the host binaries into bin/.
#
# The CLI ships under two names: `ephemera` (the real command) and `eph` (a short
# alias — a symlink to it). The guest agent (eph-agent) is not built here; it is
# compiled into the rootfs image by build-rootfs.sh.
#
# After building anything that carries a file capability (eph-netadmin), re-run
# build/host-setup.sh, since a new inode loses the capability.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

mkdir -p bin
go build -o bin/ephemerad    ./cmd/ephemerad
go build -o bin/ephemera     ./cmd/ephemera
go build -o bin/eph-jail      ./cmd/eph-jail
go build -o bin/eph-netadmin  ./cmd/eph-netadmin

# eph is the short alias for ephemera.
ln -sf ephemera bin/eph

echo "built: $(ls bin/ | tr '\n' ' ')"
