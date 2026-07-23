#!/bin/sh
# PID 1 for daemon-managed machines: bring up the pseudo-filesystems, then hand
# over to the agent.
#
# exec matters here — the agent must *become* PID 1 rather than run as its child,
# so that the kernel does not panic when this shell would otherwise exit.
. /sbin/eph-mounts

# The kernel hands PID 1 an almost-empty environment — no PATH at all — and the
# agent's children inherit whatever we set here.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export HOME=/root
export TERM=linux

exec /usr/bin/eph-agent
