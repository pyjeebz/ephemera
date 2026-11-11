#!/usr/bin/env bash
# Power off this machine after it has been idle for a while, so a cloud test
# server stops billing for compute when nobody is using it. On Google Cloud,
# powering off from inside the VM stops the instance; only its disk is billed.
#
# Busy means any of: someone has an open login session, a Firecracker
# microVM or Docker container is running, or the 1-minute load average is
# above a threshold. Create /etc/ephemera/keep-awake to never power off.
#
# Usage:
#   sudo ./idle-shutdown.sh install [--minutes 30]   install the systemd timer
#   sudo ./idle-shutdown.sh status                    show what counts as busy now
#   sudo ./idle-shutdown.sh uninstall
#   ./idle-shutdown.sh check [--dry-run]              one check (the timer runs this)
set -euo pipefail

NAME=ephemera-idle-shutdown
BIN=/usr/local/sbin/$NAME
CONF=/etc/ephemera/idle-shutdown.conf
KEEP_AWAKE=/etc/ephemera/keep-awake
STATE=/run/$NAME/last-active # /run is cleared at boot: a fresh boot counts as active

IDLE_MINUTES=30
LOAD_THRESHOLD=0.5
# shellcheck source=/dev/null
[ -r "$CONF" ] && . "$CONF"

log() { echo "$NAME: $*"; }
die() { echo "$NAME: error: $*" >&2; exit 1; }

# logged_in_users lists users with an open login session. A session that
# has ended but left a process behind (such as a dbus-daemon) is "closing"
# and does not count; real work it started shows up as load, a microVM or
# a container instead.
logged_in_users() {
	if command -v loginctl >/dev/null 2>&1; then
		local id
		for id in $(loginctl list-sessions --no-legend 2>/dev/null | awk '{print $1}'); do
			loginctl show-session "$id" -p State -p Class -p Name 2>/dev/null |
				awk -F= '{v[$1] = $2} END {if (v["Class"] == "user" && (v["State"] == "active" || v["State"] == "online")) print v["Name"]}'
		done | sort -u | paste -sd, -
		return
	fi
	# Without logind, fall back to processes of real users.
	ps -eo uid=,user=,comm= | awk '$1 >= 1000 && $1 != 65534 && $3 != "systemd" && $3 != "(sd-pam)" && $3 != "dbus-daemon" {print $2}' | sort -u | paste -sd, -
}

# busy_reasons prints one line per reason the machine is busy.
busy_reasons() {
	[ -e "$KEEP_AWAKE" ] && echo "keep-awake file $KEEP_AWAKE exists"

	local users
	users=$(logged_in_users)
	[ -n "$users" ] && echo "logged-in users: $users"

	local vms
	vms=$(pgrep -cx firecracker || true)
	[ "${vms:-0}" -gt 0 ] && echo "$vms microVM(s) running"

	if command -v docker >/dev/null 2>&1; then
		local containers
		containers=$(docker ps -q 2>/dev/null | wc -l)
		[ "$containers" -gt 0 ] && echo "$containers container(s) running"
	fi

	local load
	load=$(cut -d' ' -f1 /proc/loadavg)
	awk -v l="$load" -v t="$LOAD_THRESHOLD" 'BEGIN { exit !(l > t) }' && echo "load average $load > $LOAD_THRESHOLD"
	return 0
}

idle_seconds() {
	if [ ! -e "$STATE" ]; then
		echo 0
		return
	fi
	echo $(($(date +%s) - $(stat -c %Y "$STATE")))
}

mark_active() {
	mkdir -p "$(dirname "$STATE")"
	touch "$STATE"
}

cmd_check() {
	local dry=0
	[ "${1:-}" = --dry-run ] && dry=1
	[ -e "$STATE" ] || mark_active
	local reasons
	reasons=$(busy_reasons)
	if [ -n "$reasons" ]; then
		mark_active
		return 0
	fi
	local idle limit
	idle=$(idle_seconds)
	limit=$((IDLE_MINUTES * 60))
	if [ "$idle" -lt "$limit" ]; then
		return 0
	fi
	if [ "$dry" -eq 1 ]; then
		log "idle for $((idle / 60)) min: would power off now (dry run)"
		return 0
	fi
	log "idle for $((idle / 60)) min (limit $IDLE_MINUTES); powering off"
	logger -t "$NAME" "idle for $((idle / 60)) min; powering off"
	systemctl poweroff
}

cmd_status() {
	local reasons idle
	reasons=$(busy_reasons)
	idle=$(idle_seconds)
	echo "idle limit:    $IDLE_MINUTES min (load threshold $LOAD_THRESHOLD)"
	if [ -n "$reasons" ]; then
		echo "state:         busy"
		# shellcheck disable=SC2001
		echo "$reasons" | sed 's/^/  - /'
	else
		echo "state:         idle for $((idle / 60)) min $((idle % 60)) s"
		echo "powers off in: about $(((IDLE_MINUTES * 60 - idle + 59) / 60)) min unless something becomes busy"
	fi
	systemctl is-active --quiet "$NAME.timer" 2>/dev/null &&
		echo "timer:         active" || echo "timer:         not installed"
}

cmd_install() {
	[ "$(id -u)" -eq 0 ] || die "run as root"
	local minutes=$IDLE_MINUTES
	while [ $# -gt 0 ]; do
		case "$1" in
		--minutes) minutes="${2:-}"; shift 2 ;;
		*) die "unknown option: $1" ;;
		esac
	done
	{ [[ "$minutes" =~ ^[0-9]+$ ]] && [ "$minutes" -ge 5 ]; } || die "--minutes must be a number, at least 5"

	# Running the installed copy again only changes the settings.
	[ "$(realpath "$0")" = "$BIN" ] || install -m 0755 "$0" "$BIN"
	mkdir -p /etc/ephemera
	cat >"$CONF" <<EOF
# Read by $BIN.
IDLE_MINUTES=$minutes
LOAD_THRESHOLD=$LOAD_THRESHOLD
EOF
	cat >/etc/systemd/system/$NAME.service <<EOF
[Unit]
Description=Power off when idle for $minutes minutes

[Service]
Type=oneshot
ExecStart=$BIN check
EOF
	cat >/etc/systemd/system/$NAME.timer <<EOF
[Unit]
Description=Check every minute whether the machine is idle

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=10s

[Install]
WantedBy=timers.target
EOF
	systemctl daemon-reload
	mark_active
	systemctl enable --now --quiet "$NAME.timer"
	log "installed: powers off after $minutes idle minutes. Keep it awake with: sudo touch $KEEP_AWAKE"
}

cmd_uninstall() {
	[ "$(id -u)" -eq 0 ] || die "run as root"
	systemctl disable --now --quiet "$NAME.timer" 2>/dev/null || true
	rm -f /etc/systemd/system/$NAME.service /etc/systemd/system/$NAME.timer "$BIN" "$CONF"
	systemctl daemon-reload
	log "uninstalled"
}

case "${1:-}" in
install) shift; cmd_install "$@" ;;
uninstall) cmd_uninstall ;;
status) cmd_status ;;
check) shift; cmd_check "$@" ;;
*) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 2 ;;
esac
