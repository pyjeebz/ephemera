#!/usr/bin/env bash
# Prepare a fresh Ubuntu 24.04 x86_64 machine to be an ephemera host:
# KVM, Firecracker, Go, a dedicated "ephemera" user, and Tailscale SSH.
#
# Usage:
#   sudo ./bootstrap.sh --hostname ephemera-lab --laptop
#   sudo ./bootstrap.sh --hostname ephemera-host-1 --lock-public-ssh
#   sudo ./bootstrap.sh --hostname ephemera-test --idle-shutdown 30
#
# Safe to run again: every step checks what is already done.
set -euo pipefail

# Pinned toolchain. Firecracker defaults to the latest release, verified
# against the checksum file published with it; set FIRECRACKER_VERSION=v1.x.y
# to pin one.
GO_VERSION="1.27.3"
GO_SHA256="65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40"
FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-latest}"

HOST_USER="ephemera"
TS_TAG="tag:lab"
HOSTNAME_ARG=""
LAPTOP=0
LOCK_PUBLIC_SSH=0
IDLE_SHUTDOWN=""

log() { echo "bootstrap: $*"; }
die() { echo "bootstrap: error: $*" >&2; exit 1; }

usage() {
	cat <<'EOF'
Usage: sudo ./bootstrap.sh --hostname NAME [options]

  --hostname NAME     machine and Tailscale name, e.g. ephemera-lab
  --tag TAG           Tailscale tag to advertise (default tag:lab)
  --laptop            keep running with the lid closed; never sleep
  --lock-public-ssh   firewall: allow inbound only over Tailscale
                      (for a server with a public IP; run once Tailscale works)
  --idle-shutdown MIN power off after MIN idle minutes, for cloud test servers
                      (needs idle-shutdown.sh next to this script)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--hostname) HOSTNAME_ARG="${2:-}"; shift 2 ;;
	--tag) TS_TAG="${2:-}"; shift 2 ;;
	--laptop) LAPTOP=1; shift ;;
	--lock-public-ssh) LOCK_PUBLIC_SSH=1; shift ;;
	--idle-shutdown) IDLE_SHUTDOWN="${2:-}"; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

[ -n "$HOSTNAME_ARG" ] || { usage >&2; die "--hostname is required"; }
[[ "$HOSTNAME_ARG" =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]] || die "hostname must be lowercase letters, digits and dashes"
[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0 $*"

# --- Preflight: the machine must be able to run microVMs -------------------

# shellcheck source=/dev/null
. /etc/os-release
{ [ "${ID:-}" = ubuntu ] && [ "${VERSION_ID:-}" = "24.04" ]; } ||
	die "needs Ubuntu 24.04, found ${PRETTY_NAME:-unknown}"
[ "$(uname -m)" = x86_64 ] || die "needs an x86_64 CPU, found $(uname -m)"

if grep -qw vmx /proc/cpuinfo; then
	modprobe kvm_intel || true
elif grep -qw svm /proc/cpuinfo; then
	modprobe kvm_amd || true
else
	die "CPU virtualization is off: enable Intel VT-x or AMD-V (SVM) in the BIOS/UEFI, then run again"
fi
[ -c /dev/kvm ] || die "/dev/kvm is missing even though the CPU supports virtualization; check the BIOS setting and dmesg | grep -i kvm"
log "KVM is available"

# --- Packages ---------------------------------------------------------------

export DEBIAN_FRONTEND=noninteractive
log "updating packages"
apt-get update -q
apt-get upgrade -yq
apt-get install -yq --no-install-recommends \
	ca-certificates curl git jq make tar iproute2 nftables acl ufw unattended-upgrades
systemctl enable --now unattended-upgrades >/dev/null

hostnamectl set-hostname "$HOSTNAME_ARG"

# --- The ephemera user ------------------------------------------------------
# Logged into only through Tailscale SSH, so it has no password. It needs
# root for microVM networking, hence passwordless sudo on this dedicated box.

if ! id "$HOST_USER" >/dev/null 2>&1; then
	useradd --create-home --shell /bin/bash "$HOST_USER"
	log "created user $HOST_USER"
fi
usermod -aG kvm,sudo "$HOST_USER"
passwd -l "$HOST_USER" >/dev/null
sudoers="/etc/sudoers.d/90-$HOST_USER"
echo "$HOST_USER ALL=(ALL) NOPASSWD:ALL" >"$sudoers.tmp"
chmod 0440 "$sudoers.tmp"
visudo -cf "$sudoers.tmp" >/dev/null || die "generated sudoers file is invalid"
mv "$sudoers.tmp" "$sudoers"

# MicroVMs reach the network through the host.
cat >/etc/sysctl.d/90-ephemera.conf <<'EOF'
net.ipv4.ip_forward = 1
EOF
sysctl --quiet --system

# --- Laptop: keep running with the lid closed -------------------------------

if [ "$LAPTOP" -eq 1 ]; then
	mkdir -p /etc/systemd/logind.conf.d
	cat >/etc/systemd/logind.conf.d/90-ephemera-lid.conf <<'EOF'
[Login]
HandleLidSwitch=ignore
HandleLidSwitchExternalPower=ignore
HandleLidSwitchDocked=ignore
EOF
	systemctl mask --quiet sleep.target suspend.target hibernate.target hybrid-sleep.target
	systemctl restart systemd-logind
	log "laptop will stay awake with the lid closed"
fi

# --- Firecracker ------------------------------------------------------------

fc_version="$FIRECRACKER_VERSION"
if [ "$fc_version" = latest ]; then
	fc_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
		https://github.com/firecracker-microvm/firecracker/releases/latest)"
	fc_version="${fc_url##*/}"
fi
[[ "$fc_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "could not work out the Firecracker version (got '$fc_version')"

if [ "$(firecracker --version 2>/dev/null | head -n1 | awk '{print $2}')" = "$fc_version" ]; then
	log "Firecracker $fc_version already installed"
else
	log "installing Firecracker $fc_version"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	base="https://github.com/firecracker-microvm/firecracker/releases/download/$fc_version"
	tgz="firecracker-$fc_version-x86_64.tgz"
	curl -fsSL -o "$tmp/$tgz" "$base/$tgz"
	curl -fsSL -o "$tmp/$tgz.sha256.txt" "$base/$tgz.sha256.txt"
	want="$(awk '{print $1}' "$tmp/$tgz.sha256.txt")"
	echo "$want  $tmp/$tgz" | sha256sum -c --quiet - || die "Firecracker download failed its checksum"
	tar -xzf "$tmp/$tgz" -C "$tmp"
	rel="$tmp/release-$fc_version-x86_64"
	install -m 0755 "$rel/firecracker-$fc_version-x86_64" /usr/local/bin/firecracker
	install -m 0755 "$rel/jailer-$fc_version-x86_64" /usr/local/bin/jailer
	rm -rf "$tmp"
	trap - EXIT
fi

# --- Go ---------------------------------------------------------------------

if [ "$(/usr/local/go/bin/go env GOVERSION 2>/dev/null || true)" = "go$GO_VERSION" ]; then
	log "Go $GO_VERSION already installed"
else
	log "installing Go $GO_VERSION"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	curl -fsSL -o "$tmp/go.tgz" "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz"
	echo "$GO_SHA256  $tmp/go.tgz" | sha256sum -c --quiet - || die "Go download failed its checksum"
	rm -rf /usr/local/go
	tar -xzf "$tmp/go.tgz" -C /usr/local
	rm -rf "$tmp"
	trap - EXIT
fi
# shellcheck disable=SC2016 # expanded at login, not now
echo 'export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"' >/etc/profile.d/90-go.sh

# --- Tailscale --------------------------------------------------------------

if ! command -v tailscale >/dev/null; then
	log "installing Tailscale"
	curl -fsSL https://tailscale.com/install.sh | sh
fi
systemctl enable --now tailscaled >/dev/null

ts_state="$(tailscale status --json 2>/dev/null | jq -r '.BackendState // empty' || true)"
if [ "$ts_state" = Running ]; then
	tailscale set --ssh --hostname="$HOSTNAME_ARG"
	log "Tailscale already connected"
else
	log "connecting to Tailscale: open the link below in a browser and sign in"
	tailscale up --ssh --hostname="$HOSTNAME_ARG" --advertise-tags="$TS_TAG"
fi
ts_ip="$(tailscale ip -4 | head -n1)"

# --- Firewall (servers with a public IP) ------------------------------------

if [ "$LOCK_PUBLIC_SSH" -eq 1 ]; then
	[ -n "$ts_ip" ] || die "Tailscale is not connected; not locking the firewall"
	ufw --force reset >/dev/null
	ufw default deny incoming >/dev/null
	ufw default allow outgoing >/dev/null
	ufw allow in on tailscale0 >/dev/null
	ufw allow 41641/udp >/dev/null # Tailscale direct connections
	ufw --force enable >/dev/null
	log "firewall on: inbound only over Tailscale (public SSH is closed)"
fi

# --- Idle shutdown (cloud test servers) ------------------------------------

if [ -n "$IDLE_SHUTDOWN" ]; then
	idle_script="$(dirname "$0")/idle-shutdown.sh"
	[ -f "$idle_script" ] || die "--idle-shutdown needs idle-shutdown.sh next to this script"
	bash "$idle_script" install --minutes "$IDLE_SHUTDOWN"
fi

# --- Summary ----------------------------------------------------------------

cat <<EOF

bootstrap: done
  host          $HOSTNAME_ARG
  kvm           $(stat -c '%G %a' /dev/kvm)
  firecracker   $(firecracker --version | head -n1)
  go            $(/usr/local/go/bin/go env GOVERSION)
  tailscale     $ts_ip ($TS_TAG)

Connect with:  tailscale ssh $HOST_USER@$HOSTNAME_ARG
EOF
