#!/bin/sh
# Install the ephemera CLI from a GitHub release.
#
#   curl -fsSL https://ephemera.run/install | sh
#
# Environment:
#   EPHEMERA_VERSION      a release tag such as v0.1.0 (default: the latest)
#   EPHEMERA_INSTALL_DIR  where to put the binary (default: /usr/local/bin if
#                         writable, else ~/.local/bin)
#   EPHEMERA_DOWNLOAD_URL a mirror holding the release files (default: GitHub)
#
# It downloads the archive for this OS and CPU, checks it against the
# release's checksums.txt, and installs one binary. Nothing else is changed.
set -eu

REPO="pyjeebz/ephemera"
RELEASES="https://github.com/$REPO/releases"

say() { echo "ephemera: $*"; }
die() { echo "ephemera: error: $*" >&2; exit 1; }

fetch() { # fetch URL FILE
	# Downloads from GitHub must be HTTPS. A mirror set with
	# EPHEMERA_DOWNLOAD_URL is the user's own choice.
	if command -v curl >/dev/null 2>&1; then
		if [ -n "${EPHEMERA_DOWNLOAD_URL:-}" ]; then
			curl -fsSL -o "$2" "$1"
		else
			curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
		fi
	elif command -v wget >/dev/null 2>&1; then
		if [ -n "${EPHEMERA_DOWNLOAD_URL:-}" ]; then
			wget -q -O "$2" "$1"
		else
			wget -q --https-only -O "$2" "$1"
		fi
	else
		die "needs curl or wget"
	fi
}

latest() {
	# The latest release's page redirects to .../releases/tag/vX.Y.Z.
	if command -v curl >/dev/null 2>&1; then
		url="$(curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "$RELEASES/latest")"
	else
		url="$(wget -q --https-only -S --spider "$RELEASES/latest" 2>&1 | sed -n 's/^ *Location: //p' | tail -n1)"
	fi
	tag="${url##*/}"
	case "$tag" in
	v[0-9]*) echo "$tag" ;;
	*) die "could not find the latest release at $RELEASES; set EPHEMERA_VERSION=vX.Y.Z" ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "needs sha256sum or shasum to check the download"
	fi
}

main() {
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN*) die "on Windows, download ephemera_<version>_windows_amd64.zip from $RELEASES" ;;
	*) die "no release for $(uname -s); releases cover Linux, macOS and Windows: $RELEASES" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "no release for $(uname -m) CPUs; releases cover x86_64 and arm64: $RELEASES" ;;
	esac

	version="${EPHEMERA_VERSION:-}"
	[ -n "$version" ] || version="$(latest)"
	case "$version" in
	v[0-9]*) ;;
	*) die "EPHEMERA_VERSION must look like v0.1.0, not $version" ;;
	esac

	dir="${EPHEMERA_INSTALL_DIR:-}"
	if [ -z "$dir" ]; then
		if [ -w /usr/local/bin ]; then
			dir=/usr/local/bin
		else
			dir="$HOME/.local/bin"
		fi
	fi

	name="ephemera_${version}_${os}_${arch}"
	base="${EPHEMERA_DOWNLOAD_URL:-$RELEASES/download/$version}"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	say "downloading $name"
	fetch "$base/$name.tar.gz" "$tmp/$name.tar.gz" || die "could not download $base/$name.tar.gz"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download $base/checksums.txt"

	want="$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")"
	[ -n "$want" ] || die "checksums.txt has no entry for $name.tar.gz"
	got="$(sha256 "$tmp/$name.tar.gz")"
	[ "$got" = "$want" ] || die "$name.tar.gz does not match its checksum; not installing it"

	tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
	{ mkdir -p "$dir" && install -m 0755 "$tmp/$name/ephemera" "$dir/ephemera"; } 2>/dev/null ||
		die "could not write to $dir; set EPHEMERA_INSTALL_DIR to a folder you can write, or run with sudo"

	installed="$("$dir/ephemera" version 2>/dev/null | cut -d' ' -f2)"
	say "installed $installed ($os-$arch) to $dir/ephemera"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "$dir is not on your PATH; add it, e.g. echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
	esac
	say "next: ephemera login --server <your server's URL>"
}

# Everything runs from main, so a download cut off halfway runs nothing.
main "$@"
