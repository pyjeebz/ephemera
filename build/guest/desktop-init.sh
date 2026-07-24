#!/bin/sh
# PID 1 for a desktop box: bring up the pseudo-filesystems, start a minimal
# graphical stack behind a VNC server, then hand over to the agent.
#
# There is no display device in a Firecracker guest, so the X server is headless:
# Xvfb paints into a RAM framebuffer and x11vnc serves those pixels. x11vnc binds
# localhost only; the sole path to it is the agent's vsock desktop bridge, which
# keeps the desktop as unreachable from any network as everything else in the box.
#
# exec at the end matters: the agent must *become* PID 1 rather than run as a
# child, so the kernel does not panic when this shell would otherwise exit. The X
# stack runs as background children of the agent.
. /sbin/eph-mounts

export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export HOME=/root
export DISPLAY=:0

# X wants a world-writable /tmp and a socket directory; the read-only base ships
# neither on the writable overlay, so make them here.
mkdir -p /tmp/.X11-unix
chmod 1777 /tmp
mkdir -p /var/log

# Headless framebuffer. -ac drops host access control (the box is already isolated
# and single-user) and -nolisten tcp keeps X off the network entirely.
Xvfb :0 -screen 0 1280x800x24 -ac -nolisten tcp >/var/log/xvfb.log 2>&1 &

# Wait for X to create its socket before starting clients against it.
i=0
while [ ! -S /tmp/.X11-unix/X0 ] && [ "$i" -lt 50 ]; do
	sleep 0.1
	i=$((i + 1))
done

openbox >/var/log/openbox.log 2>&1 &
xterm -geometry 100x30+40+40 >/var/log/xterm.log 2>&1 &

# The VNC server: read the framebuffer and serve it on localhost for the vsock
# bridge. -forever keeps it up across viewer disconnects; -shared allows more than
# one viewer; -nopw because the boundary is the vsock socket, not a password.
x11vnc -display :0 -localhost -rfbport 5900 -forever -shared -nopw -quiet \
	>/var/log/x11vnc.log 2>&1 &

exec /usr/bin/eph-agent
