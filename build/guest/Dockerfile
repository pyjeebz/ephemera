# The guest image. Everything that runs *inside* an ephemera microVM is defined
# here, so the rootfs is reproducible and reviewable like any other build input.
#
# Phase 0 keeps it deliberately minimal: a base userland plus an init that brings
# up the pseudo-filesystems and hands over a shell on the serial console. Later
# phases add the vsock guest agent, dev toolchains, and the desktop.
FROM alpine:3.24

RUN apk add --no-cache \
      util-linux \
      e2fsprogs-extra \
      bash \
      coreutils \
      ca-certificates \
      curl \
      git \
      vim \
      less \
      openssh-client \
      python3

# A box should feel like a real dev machine, not a stripped sandbox: a proper
# shell (bash), the tools you reach for without thinking (git, curl, vim, less,
# ssh), certificates so https works, and python3 to build with. Everything else
# a user installs — including their own agent — since ephemera is agent-agnostic.

# Note: /etc/hostname and /etc/resolv.conf are bind-mounted by Docker during a
# build and cannot be written here — the guest sets both at boot instead.
#
# The kernel boots into /sbin/eph-overlay-init, which makes the root a
# RAM-backed overlay over the read-only disk and then hands off to the real init
# named on the cmdline as eph.init=. The real inits:
#   /sbin/eph-init       interactive shell on the serial console
#   /sbin/eph-selftest   non-interactive check that halts (used by tests/CI)
#   /sbin/eph-agent-init the vsock agent, used by the daemon
COPY overlay-init.sh /sbin/eph-overlay-init
COPY mounts.sh       /sbin/eph-mounts
COPY init.sh         /sbin/eph-init
COPY selftest.sh     /sbin/eph-selftest
COPY agent-init.sh   /sbin/eph-agent-init
COPY desktop-init.sh /sbin/eph-desktop-init
RUN chmod +x /sbin/eph-overlay-init /sbin/eph-mounts /sbin/eph-init /sbin/eph-selftest /sbin/eph-agent-init /sbin/eph-desktop-init

# Built on the host by build-rootfs.sh: a static CGO-free binary, so it needs no
# loader or libc inside the guest.
COPY eph-agent /usr/bin/eph-agent
RUN chmod +x /usr/bin/eph-agent

# A desktop box adds a headless graphical stack: a RAM framebuffer (Xvfb, since a
# Firecracker guest has no display device), a minimal window manager and terminal,
# and a VNC server to stream the pixels out. Only built when DESKTOP=1, so the
# default box stays lean and boots fast — you opt into pixels. See ADR 0009.
ARG DESKTOP=0
RUN if [ "$DESKTOP" = "1" ]; then \
      apk add --no-cache xvfb x11vnc openbox xterm font-misc-misc ttf-dejavu ffmpeg; \
    fi
