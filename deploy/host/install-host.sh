#!/bin/sh
# Install the ephemera host agent from a GitHub release, and join this
# machine to an account with a one-time code from the dashboard:
#
#   curl -fsSL https://ephemera.run/host | sudo sh -s -- ej_...
#
# Options follow the code; see usage() below, or pass --help.
#
# Environment:
#   EPHEMERA_VERSION      a release tag such as v0.2.0 (default: the latest)
#   EPHEMERA_DOWNLOAD_URL a mirror holding the release files (default: GitHub)
#
# It installs Firecracker, the host agent and its guest image, and runs the
# agent as the ephemera-host systemd service. It sets up no network: your
# laptop must reach this machine at --address, however you connect the two.
# Run it again, without a code, to upgrade.
set -eu

REPO="pyjeebz/ephemera"
RELEASES="https://github.com/$REPO/releases"
# Firecracker's release that matches the guest kernel's CI artifacts.
FC_VERSION="v1.15.1"
FC_SHA256="d4a32ab2322d887ca1bc4a4e7afa9cc35393e6362dfc2b3becb389d362e4275a"

DATA=/var/lib/ephemera
BIN=/usr/local/bin
ENV_FILE=/etc/ephemera/ephemera.env
UNIT=/etc/systemd/system/ephemera-host.service
IDENTITY=$DATA/host.json

say() { echo "ephemera: $*"; }
die() {
	echo "ephemera: error: $*" >&2
	exit 1
}

fetch() { # fetch URL FILE
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

checksum() { # checksum FILE NAME SUMS: dies unless FILE matches NAME's line in SUMS
	want="$(awk -v f="$2" '$2 == f || $2 == "*" f { print $1 }' "$3")"
	[ -n "$want" ] || die "checksums.txt has no entry for $2"
	[ "$(sha256sum "$1" | cut -d' ' -f1)" = "$want" ] || die "$2 does not match its checksum; not installing it"
}

# private reports whether an IPv4 address is on a private network:
# RFC 1918, or carrier-grade NAT (100.64.0.0/10), which VPNs such as
# Tailscale use.
private() {
	case "$1" in
	10.* | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) return 0 ;;
	100.6[4-9].* | 100.[7-9][0-9].* | 100.1[01][0-9].* | 100.12[0-7].*) return 0 ;;
	esac
	return 1
}

# addresses lists this machine's IPv4 addresses, skipping loopback and the
# bridges and taps that containers and VMs make.
addresses() {
	ip -4 -o addr show scope global 2>/dev/null | awk '{
		dev = $2; sub(/@.*/, "", dev); split($4, a, "/")
		if (dev == "lo" || dev ~ /^(docker|br-|veth|virbr|cni|flannel|tap|lxc|lxdbr|vmnet|fc)/) next
		print a[1]
	}'
}

usage() {
	cat <<'EOF'
Usage: curl -fsSL https://ephemera.run/host | sudo sh -s -- [CODE] [options]

  CODE             a one-time join code from the dashboard, under Hosts (ej_...);
                   not needed to upgrade a host that already joined
  --server URL     central server to join (default https://api.ephemera.run)
  --address IP     the address your laptop reaches this machine on
                   (default: its one private IPv4 address)
  --name NAME      this host's name in the account (default: the hostname)
  --host-args "…"  extra ephemera-host flags, e.g. "--capacity 40 --max-lifetime 8h"
  --uninstall      stop and remove the host agent; with --purge, also its data and key

  EPHEMERA_VERSION=vX.Y.Z installs that release instead of the latest.
EOF
}

uninstall() {
	say "stopping the host agent (this destroys every sandbox on this host)"
	systemctl disable --now ephemera-host.service 2>/dev/null || true
	rm -f "$UNIT"
	systemctl daemon-reload
	rm -f "$BIN/ephemera-host"
	if [ "$1" = purge ]; then
		rm -rf "$DATA" /etc/ephemera
		say "removed the host agent, its guest image, snapshots and key"
		say "remove the host from your account too, in the dashboard under Hosts"
	else
		say "removed the host agent; kept $DATA and /etc/ephemera (--purge deletes them)"
	fi
}

main() {
	code="" server="" address="" name="" host_args="" mode=install purge=keep
	while [ $# -gt 0 ]; do
		case "$1" in
		--server) server="${2:-}" && shift 2 ;;
		--address) address="${2:-}" && shift 2 ;;
		--name) name="${2:-}" && shift 2 ;;
		--host-args) host_args="${2:-}" && shift 2 ;;
		--uninstall) mode=uninstall && shift ;;
		--purge) purge=purge && shift ;;
		-h | --help) usage && exit 0 ;;
		ej_*) code="$1" && shift ;;
		*) die "unknown option: $1 (a join code starts with ej_)" ;;
		esac
	done

	[ "$(id -u)" -eq 0 ] || die "run as root: curl -fsSL https://ephemera.run/host | sudo sh -s -- <code>"
	command -v systemctl >/dev/null 2>&1 || die "needs systemd"
	if [ "$mode" = uninstall ]; then
		uninstall "$purge"
		exit 0
	fi

	# --- This machine ---------------------------------------------------
	[ "$(uname -s)" = Linux ] || die "hosts run on Linux, not $(uname -s)"
	[ "$(uname -m)" = x86_64 ] || die "hosts need an x86_64 CPU, not $(uname -m)"
	if [ ! -c /dev/kvm ]; then
		if grep -qw vmx /proc/cpuinfo; then
			modprobe kvm_intel 2>/dev/null || true
		elif grep -qw svm /proc/cpuinfo; then
			modprobe kvm_amd 2>/dev/null || true
		else
			die "CPU virtualization is off: turn on Intel VT-x or AMD-V in the BIOS or UEFI. On a cloud VM, turn on nested virtualization"
		fi
	fi
	[ -c /dev/kvm ] || die "/dev/kvm is missing; check the BIOS setting and: dmesg | grep -i kvm"
	[ -f /etc/systemd/system/ephemera-server.service ] &&
		die "this machine also runs a central server; upgrade it with deploy/host/install.sh from a checkout"
	if [ -f /etc/os-release ]; then
		# shellcheck disable=SC1091 # the machine's own file
		. /etc/os-release
		if [ "${ID:-}" != ubuntu ] || [ "${VERSION_ID:-}" != 24.04 ]; then
			say "warning: ephemera is tested on Ubuntu 24.04; this is ${PRETTY_NAME:-unknown}"
		fi
	fi

	if [ -f "$IDENTITY" ]; then
		[ -z "$code" ] || say "already joined; keeping $IDENTITY and ignoring the code"
	elif [ -z "$code" ]; then
		die "no join code: make one in the dashboard, under Hosts, and run: curl -fsSL https://ephemera.run/host | sudo sh -s -- <code>"
	fi

	# A host that joined keeps its server and name; the address the laptop
	# reaches is kept from the last install unless given. So an upgrade
	# changes none of them.
	if [ -f "$IDENTITY" ]; then
		[ -n "$server" ] || server="$(sed -n 's/.*"server": *"\([^"]*\)".*/\1/p' "$IDENTITY" | head -n1)"
		[ -n "$name" ] || name="$(sed -n 's/.*"name": *"\([^"]*\)".*/\1/p' "$IDENTITY" | head -n1)"
	fi
	if [ -f "$ENV_FILE" ]; then
		[ -n "$server" ] || server="$(sed -n 's/^EPHEMERA_SERVER_URL=//p' "$ENV_FILE")"
		[ -n "$address" ] || address="$(sed -n 's/^EPHEMERA_HOST_LISTEN=\(.*\):7701$/\1/p' "$ENV_FILE")"
	fi
	[ -n "$server" ] || server="https://api.ephemera.run"
	case "$server" in
	https://* | http://*) ;;
	*) die "--server must be a URL such as https://api.ephemera.run" ;;
	esac
	if [ -z "$address" ]; then
		found=""
		for a in $(addresses); do
			private "$a" && found="$found $a"
		done
		# shellcheck disable=SC2086 # split the list into words on purpose
		set -- $found
		case $# in
		1) address="$1" ;;
		0) die "found no private address. Pass --address with the IPv4 address your laptop reaches this machine on" ;;
		*) die "found several private addresses:$found. Pass --address with the one your laptop reaches this machine on" ;;
		esac
	fi
	case "$address" in
	*[!0-9.]* | "" | 0.0.0.0) die "--address must be one IPv4 address, not '$address'" ;;
	esac
	ip -4 -o addr show 2>/dev/null | grep -q " $address/" || die "$address is not an address of this machine"
	private "$address" ||
		say "warning: $address is public, so port 7701 will be open to the internet. The host serves TLS with its own key and checks each sandbox's token, but a private network is safer"
	[ -n "$name" ] || name="$(hostname -s)"
	case "$name" in
	*[!A-Za-z0-9._-]* | "" | [!A-Za-z0-9]*) die "--name must be letters, digits, dots, dashes and underscores" ;;
	esac
	case "$host_args" in
	*"
"*) die "--host-args must be one line" ;;
	esac

	# --- Packages the image build needs -----------------------------------
	missing=""
	for tool in unsquashfs:squashfs-tools mkfs.ext4:e2fsprogs chroot:coreutils ip:iproute2 sha256sum:coreutils; do
		command -v "${tool%%:*}" >/dev/null 2>&1 || missing="$missing ${tool#*:}"
	done
	if [ -n "$missing" ]; then
		command -v apt-get >/dev/null 2>&1 || die "install these first:$missing"
		say "installing$missing"
		# shellcheck disable=SC2086 # one package per word
		DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends $missing >/dev/null
	fi

	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	# --- Firecracker ------------------------------------------------------
	if [ "$(firecracker --version 2>/dev/null | head -n1 | awk '{print $2}')" = "$FC_VERSION" ] && command -v jailer >/dev/null 2>&1; then
		say "Firecracker $FC_VERSION already installed"
	else
		say "installing Firecracker $FC_VERSION"
		tgz="firecracker-$FC_VERSION-x86_64.tgz"
		fetch "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/$tgz" "$tmp/$tgz" ||
			die "could not download Firecracker"
		[ "$(sha256sum "$tmp/$tgz" | cut -d' ' -f1)" = "$FC_SHA256" ] || die "Firecracker does not match its pinned checksum"
		tar -xzf "$tmp/$tgz" -C "$tmp"
		install -m 0755 "$tmp/release-$FC_VERSION-x86_64/firecracker-$FC_VERSION-x86_64" "$BIN/firecracker"
		install -m 0755 "$tmp/release-$FC_VERSION-x86_64/jailer-$FC_VERSION-x86_64" "$BIN/jailer"
	fi

	# --- ephemera ---------------------------------------------------------
	version="${EPHEMERA_VERSION:-}"
	[ -n "$version" ] || version="$(latest)"
	case "$version" in
	v[0-9]*) ;;
	*) die "EPHEMERA_VERSION must look like v0.2.0, not $version" ;;
	esac
	pkg="ephemera-host_${version}_linux_amd64"
	base="${EPHEMERA_DOWNLOAD_URL:-$RELEASES/download/$version}"
	say "downloading ephemera $version"
	fetch "$base/$pkg.tar.gz" "$tmp/$pkg.tar.gz" || die "could not download $base/$pkg.tar.gz"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download $base/checksums.txt"
	checksum "$tmp/$pkg.tar.gz" "$pkg.tar.gz" "$tmp/checksums.txt"
	tar -xzf "$tmp/$pkg.tar.gz" -C "$tmp"
	src="$tmp/$pkg"
	for f in ephemera-host ephemera-guest build-image.sh ephemera-host.service; do
		[ -f "$src/$f" ] || die "$pkg.tar.gz has no $f; use a release from v0.2.0 on"
	done

	# --- Guest image --------------------------------------------------------
	# Built again whenever the guest agent changes, so host and guest match.
	changed=""
	mkdir -p "$DATA"
	chmod 0755 "$DATA"
	guest_sum="$(sha256sum "$src/ephemera-guest" | cut -d' ' -f1)"
	if [ -f "$DATA/image/vmlinux" ] && [ -f "$DATA/image/rootfs.ext4" ] &&
		[ "$(cat "$DATA/image/guest.sha256" 2>/dev/null)" = "$guest_sum" ]; then
		say "guest image is up to date"
	else
		say "building the guest image (a few minutes; it downloads Ubuntu's packages)"
		bash "$src/build-image.sh" "$DATA/image" "$src/ephemera-guest"
		echo "$guest_sum" >"$DATA/image/guest.sha256"
		changed=1
	fi

	# --- Service --------------------------------------------------------------
	mkdir -p /etc/ephemera
	chmod 0700 /etc/ephemera
	umask 077
	cat >"$ENV_FILE.new" <<EOF
# Written by the ephemera host installer; run it again to change these.
EPHEMERA_SERVER_URL=$server
EPHEMERA_HOST_LISTEN=$address:7701
EPHEMERA_HOST_URL=https://$address:7701
EPHEMERA_HOST_ARGS=$host_args
EOF
	umask 022
	cmp -s "$src/ephemera-host" "$BIN/ephemera-host" || changed=1
	cmp -s "$ENV_FILE.new" "$ENV_FILE" || changed=1
	cmp -s "$src/ephemera-host.service" "$UNIT" || changed=1
	running=""
	systemctl is-active --quiet ephemera-host.service && running=1
	if [ -n "$running" ] && [ -z "$changed" ]; then
		rm -f "$ENV_FILE.new"
		say "$name is up to date ($version) and running; left it alone"
		exit 0
	fi
	if [ -n "$running" ]; then
		say "stopping the running host agent to upgrade it (its sandboxes are destroyed)"
		systemctl stop ephemera-host.service
	fi
	install -m 0755 "$src/ephemera-host" "$BIN/ephemera-host"
	mv "$ENV_FILE.new" "$ENV_FILE"
	install -m 0644 "$src/ephemera-host.service" "$UNIT"
	systemctl daemon-reload
	systemctl enable --quiet ephemera-host.service

	# --- Join ---------------------------------------------------------------
	if [ ! -f "$IDENTITY" ]; then
		printf '%s\n' "$code" | "$BIN/ephemera-host" join --server "$server" --name "$name" ||
			die "could not join $server; the code works once, for 15 minutes, so make a new one if it was used"
	fi
	code=""

	since="$(date '+%Y-%m-%d %H:%M:%S')"
	systemctl start ephemera-host.service
	say "waiting for the host agent to connect (it builds a snapshot first, about 10 s)"
	i=0
	while [ "$i" -lt 90 ]; do
		journalctl -u ephemera-host --since "$since" -q --no-pager 2>/dev/null | grep -q 'connected to central server' && break
		systemctl is-active --quiet ephemera-host.service || i=90
		i=$((i + 1))
		sleep 1
	done
	journalctl -u ephemera-host --since "$since" -q --no-pager 2>/dev/null | grep -q 'connected to central server' ||
		die "the host agent did not connect; see: journalctl -u ephemera-host -n 50"

	cat <<EOF

ephemera: done. $name is online at https://$address:7701 ($version)

  Your laptop must reach that address to send files and read output.
  See the host in the dashboard, under Hosts, or with: ephemera hosts
  Logs: journalctl -u ephemera-host -f
  Upgrade: run this again without a code. Remove: add --uninstall
EOF
}

# Everything runs from main, so a download cut off halfway runs nothing.
main "$@"
