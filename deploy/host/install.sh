#!/usr/bin/env bash
# Install ephemera on a machine prepared by bootstrap.sh: build the
# binaries, build the guest image, and run the central server and host
# agent as systemd services. Run it again to upgrade.
#
# Usage:
#   sudo deploy/host/install.sh                       # server and host on this machine
#   sudo deploy/host/install.sh --role host --server-url http://100.64.0.1:7700 \
#        --join-code-file /root/join-code               # another host for that server
#   sudo deploy/host/install.sh --uninstall [--purge]
set -euo pipefail

ROLE="all"
LISTEN_IP=""
SERVER_URL=""
JOIN_CODE_FILE=""
HOST_NAME="$(hostname -s)"
HOST_ARGS=""
SKIP_IMAGE=0
UNINSTALL=0
PURGE=0

ENV_FILE=/etc/ephemera/ephemera.env
DATA=/var/lib/ephemera
BIN=/usr/local/bin
UNITS=/etc/systemd/system
SERVER_USER=ephemera-server
IDENTITY=/var/lib/ephemera/host.json
REPO="$(cd "$(dirname "$0")/../.." && pwd)"

log() { echo "install: $*"; }
die() { echo "install: error: $*" >&2; exit 1; }

usage() {
	cat <<'EOF'
Usage: sudo deploy/host/install.sh [options]

  --role all|host       all: central server and host agent (default)
                        host: only a host agent, joining --server-url
  --listen-ip IP        address to serve on (default: this machine's Tailscale IPv4)
  --server-url URL      central server for --role host, e.g. http://100.64.0.1:7700
  --join-code-file PATH file holding a one-time join code, for --role host
                        (make one on the server: sudo ephemera-server join-code create)
  --name NAME           host name (default: this machine's hostname)
  --host-args "ARGS"    extra ephemera-host flags, e.g. "--capacity 40 --max-lifetime 8h"
  --skip-image          don't build the guest image (it must already be in /var/lib/ephemera/image)
  --uninstall           stop and remove the services and binaries; keeps data
  --purge               with --uninstall, also delete /var/lib/ephemera and /etc/ephemera
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--role) ROLE="${2:-}"; shift 2 ;;
	--listen-ip) LISTEN_IP="${2:-}"; shift 2 ;;
	--server-url) SERVER_URL="${2:-}"; shift 2 ;;
	--join-code-file) JOIN_CODE_FILE="${2:-}"; shift 2 ;;
	--name) HOST_NAME="${2:-}"; shift 2 ;;
	--host-args) HOST_ARGS="${2:-}"; shift 2 ;;
	--skip-image) SKIP_IMAGE=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	--purge) PURGE=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0 $*"
command -v systemctl >/dev/null || die "needs systemd"

# --- Uninstall --------------------------------------------------------------

if [ "$UNINSTALL" -eq 1 ]; then
	log "stopping services (this destroys every sandbox on this host)"
	systemctl disable --now ephemera-host.service ephemera-server.service 2>/dev/null || true
	rm -f "$UNITS/ephemera-host.service" "$UNITS/ephemera-server.service"
	systemctl daemon-reload
	rm -f "$BIN/ephemera" "$BIN/ephemera-server" "$BIN/ephemera-host"
	if [ "$PURGE" -eq 1 ]; then
		rm -rf "$DATA" /etc/ephemera
		id "$SERVER_USER" >/dev/null 2>&1 && userdel "$SERVER_USER"
		log "removed ephemera, its data, tokens, guest image and host key"
	else
		log "removed ephemera; kept $DATA and /etc/ephemera (--purge deletes them)"
	fi
	exit 0
fi

# --- Checks -----------------------------------------------------------------

case "$ROLE" in
all) ;;
host)
	[ -n "$SERVER_URL" ] || die "--role host needs --server-url"
	[ -n "$JOIN_CODE_FILE" ] || [ -f "$IDENTITY" ] ||
		die "--role host needs a join code: make one on the server (sudo ephemera-server join-code create), save it to a file, and pass --join-code-file PATH"
	;;
*) die "--role must be all or host" ;;
esac
[[ "$HOST_ARGS" != *$'\n'* ]] || die "--host-args must be one line"
[[ "$HOST_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$ ]] || die "--name must be letters, digits, dots, dashes and underscores"

[ -c /dev/kvm ] || die "/dev/kvm is missing; run deploy/host/bootstrap.sh first"
for tool in firecracker jailer; do
	command -v "$tool" >/dev/null || die "$tool is not installed; run deploy/host/bootstrap.sh first"
done
GO="$(command -v go || echo /usr/local/go/bin/go)"
[ -x "$GO" ] || die "Go is not installed; run deploy/host/bootstrap.sh first"
{ [ -f "$REPO/go.mod" ] && [ -d "$REPO/cmd/ephemera-host" ]; } || die "run this from an ephemera checkout"

# ephemera speaks plain HTTP: tokens and code cross the network unencrypted
# unless the network itself is private and encrypted, as Tailscale is.
if [ -z "$LISTEN_IP" ]; then
	LISTEN_IP="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
	[ -n "$LISTEN_IP" ] || die "no Tailscale address found; connect Tailscale, or pass --listen-ip with an address on a private network"
fi
[[ "$LISTEN_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "--listen-ip must be an IPv4 address"
case "$LISTEN_IP" in
0.0.0.0) die "--listen-ip 0.0.0.0 would serve on every interface, including public ones; pick one address" ;;
100.* | 10.* | 192.168.* | 127.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) ;;
*) die "$LISTEN_IP looks public; ephemera has no TLS yet, so serve it on a private network such as Tailscale" ;;
esac
[ "$ROLE" = all ] && SERVER_URL="http://$LISTEN_IP:7700"

# --- Binaries ---------------------------------------------------------------

log "building ephemera from $REPO"
build="$(mktemp -d)"
trap 'rm -rf "$build"' EXIT
(cd "$REPO" && for cmd in ephemera ephemera-server ephemera-host; do
	"$GO" build -trimpath -o "$build/$cmd" "./cmd/$cmd"
done)
for cmd in ephemera ephemera-server ephemera-host; do
	install -m 0755 "$build/$cmd" "$BIN/$cmd"
done

# --- Guest image ------------------------------------------------------------

mkdir -p "$DATA"
chmod 0755 "$DATA"
if [ -f "$DATA/image/vmlinux" ] && [ -f "$DATA/image/rootfs.ext4" ]; then
	log "guest image already built"
elif [ "$SKIP_IMAGE" -eq 1 ]; then
	die "no guest image in $DATA/image and --skip-image was given"
else
	log "building the guest image (a few minutes; needs network)"
	bash "$REPO/deploy/guest/build-image.sh" "$DATA/image"
fi

# --- Configuration ----------------------------------------------------------

mkdir -p /etc/ephemera
chmod 0700 /etc/ephemera
umask 077
cat >"$ENV_FILE.tmp" <<EOF
# Written by deploy/host/install.sh; run it again to change these.
EPHEMERA_SERVER_LISTEN=$LISTEN_IP:7700
EPHEMERA_SERVER_URL=$SERVER_URL
EPHEMERA_HOST_LISTEN=$LISTEN_IP:7701
EPHEMERA_HOST_URL=http://$LISTEN_IP:7701
EPHEMERA_HOST_ARGS=$HOST_ARGS
EOF
mv "$ENV_FILE.tmp" "$ENV_FILE"
umask 022

# --- Services ---------------------------------------------------------------

services=(ephemera-host)
if [ "$ROLE" = all ]; then
	services=(ephemera-server ephemera-host)
	id "$SERVER_USER" >/dev/null 2>&1 ||
		useradd --system --no-create-home --home-dir "$DATA/server" --shell /usr/sbin/nologin "$SERVER_USER"
fi
for s in "${services[@]}"; do
	install -m 0644 "$REPO/deploy/systemd/$s.service" "$UNITS/$s.service"
done
systemctl daemon-reload
for s in "${services[@]}"; do
	systemctl enable --quiet "$s.service"
done
# The host joins before it starts, and it joins a running server.
systemctl stop ephemera-host.service 2>/dev/null || true

if [ "$ROLE" = all ]; then
	systemctl restart ephemera-server.service
	log "waiting for the central server"
	for _ in $(seq 1 30); do
		curl -fsS -o /dev/null "$SERVER_URL/healthz" 2>/dev/null && break
		sleep 1
	done
	curl -fsS -o /dev/null "$SERVER_URL/healthz" || die "the central server did not start: journalctl -u ephemera-server"
fi

# --- Join -------------------------------------------------------------------

if [ -f "$IDENTITY" ]; then
	log "host already joined; keeping $IDENTITY"
else
	if [ "$ROLE" = all ]; then
		code="$("$BIN/ephemera-server" join-code create --state "$DATA/server" 2>/dev/null)"
	else
		code="$(tr -d '[:space:]' <"$JOIN_CODE_FILE")"
	fi
	[ -n "$code" ] || die "no join code"
	printf '%s\n' "$code" | "$BIN/ephemera-host" join --server "$SERVER_URL" --name "$HOST_NAME" ||
		die "the host could not join $SERVER_URL"
	unset code
fi
systemctl start ephemera-host.service

log "waiting for the host agent to connect (it builds a snapshot first, about 10 s)"
since="$(date '+%Y-%m-%d %H:%M:%S' -d '-5 sec' 2>/dev/null || date '+%Y-%m-%d %H:%M:%S')"
connected=0
for _ in $(seq 1 90); do
	if journalctl -u ephemera-host --since "$since" -q --no-pager 2>/dev/null | grep -q 'connected to central server'; then
		connected=1
		break
	fi
	systemctl is-active --quiet ephemera-host.service || break
	sleep 1
done
[ "$connected" -eq 1 ] || die "the host agent did not connect: journalctl -u ephemera-host"

# --- First token ------------------------------------------------------------

token=""
if [ "$ROLE" = all ] && ! "$BIN/ephemera-server" token ls --state "$DATA/server" 2>/dev/null | sed 1d | grep -q .; then
	token="$("$BIN/ephemera-server" token create --state "$DATA/server" admin 2>/dev/null)"
fi

cat <<EOF

install: done
  role          $ROLE
  host          $HOST_NAME at http://$LISTEN_IP:7701
  server        $SERVER_URL
  services      ${services[*]} (systemctl status ${services[*]})
EOF
if [ -n "$token" ]; then
	cat <<EOF

An API token named "admin" was created. It is shown only this once:

  $token

On your laptop:

  ephemera login --server $SERVER_URL

and paste the token. Make more tokens with:

  sudo ephemera-server token create <name> [--max-sandboxes N] [--max-minutes N]
EOF
elif [ "$ROLE" = all ]; then
	echo
	echo "Tokens already exist. On your laptop: ephemera login --server $SERVER_URL"
fi
