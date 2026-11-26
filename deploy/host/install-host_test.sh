#!/bin/sh
# Tests for install-host.sh's choice of server, name and address, without
# installing anything: sh deploy/host/install-host_test.sh
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
export EPHEMERA_INSTALL_TEST=1
# shellcheck disable=SC1091 # checked on its own
. "$here/install-host.sh"

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
IDENTITY="$dir/host.json"
ENV_FILE="$dir/ephemera.env"
failed=0

# check NAME WANT_SERVER WANT_NAME WANT_ADDRESS: runs previous with the given
# server, name and address (set by the caller) and compares.
check() {
	previous
	if [ "$server|$name|$address" != "$2|$3|$4" ]; then
		echo "FAIL $1: got $server|$name|$address, want $2|$3|$4"
		failed=1
	else
		echo "ok   $1"
	fi
}

v01_env() {
	cat >"$ENV_FILE" <<'EOF'
# Written by deploy/host/install.sh; run it again to change these.
EPHEMERA_HOST_SECRET=secret
EPHEMERA_SERVER_LISTEN=100.111.12.48:7700
EPHEMERA_SERVER_URL=http://100.111.12.48:7700
EPHEMERA_HOST_LISTEN=100.111.12.48:7701
EPHEMERA_HOST_URL=http://100.111.12.48:7701
EPHEMERA_HOST_NAME=ephemera-host-1
EPHEMERA_HOST_ARGS=
EOF
}

# A new machine joins the hosted server.
rm -f "$IDENTITY" "$ENV_FILE"
server="" name="" address=""
check "fresh machine" "https://api.ephemera.run" "" ""

# A v0.1 host joins the hosted server with its code, not the self-hosted
# server in its old env file; it keeps the address the laptop reaches.
v01_env
server="" name="" address=""
check "v0.1 host, no --server" "https://api.ephemera.run" "" "100.111.12.48"

# --server still wins for a v0.1 host that moves to a new self-hosted server.
server="https://ephemera.example.com" name="" address=""
check "v0.1 host, --server" "https://ephemera.example.com" "" "100.111.12.48"

# A joined host keeps its server and name on upgrade.
cat >"$IDENTITY" <<'EOF'
{
  "server": "https://api.ephemera.run",
  "name": "box-1",
  "id": "host_18de21ee"
}
EOF
cat >"$ENV_FILE" <<'EOF'
EPHEMERA_SERVER_URL=https://api.ephemera.run
EPHEMERA_HOST_LISTEN=10.0.0.12:7701
EPHEMERA_HOST_URL=https://10.0.0.12:7701
EPHEMERA_HOST_ARGS=
EOF
server="" name="" address=""
check "joined host, upgrade" "https://api.ephemera.run" "box-1" "10.0.0.12"

# Flags still override a joined host's address.
server="" name="" address="10.0.0.99"
check "joined host, --address" "https://api.ephemera.run" "box-1" "10.0.0.99"

# reach sorts addresses by who can reach them.
for want in 127.0.0.1=loopback 10.0.0.12=private 192.168.1.5=private 172.20.0.3=private \
	100.111.12.48=private 172.32.0.1=public 100.128.0.1=public 203.0.113.7=public; do
	got="$(reach "${want%%=*}")"
	if [ "$got" != "${want#*=}" ]; then
		echo "FAIL reach ${want%%=*}: got $got, want ${want#*=}"
		failed=1
	fi
done

exit "$failed"
