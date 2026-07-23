# The guest image. Everything that runs *inside* an ephemera microVM is defined
# here, so the rootfs is reproducible and reviewable like any other build input.
#
# Phase 0 keeps it deliberately minimal: a base userland plus an init that brings
# up the pseudo-filesystems and hands over a shell on the serial console. Later
# phases add the vsock guest agent, dev toolchains, and the desktop.
FROM alpine:3.24

RUN apk add --no-cache \
      util-linux \
      e2fsprogs-extra

# Note: /etc/hostname and /etc/resolv.conf are bind-mounted by Docker during a
# build and cannot be written here — the guest sets both at boot instead.
#
# Three inits ship in the image; the kernel cmdline picks one:
#   /sbin/eph-init       interactive shell on the serial console
#   /sbin/eph-selftest   non-interactive check that halts (used by tests/CI)
#   /sbin/eph-agent-init the vsock agent, used by the daemon
COPY mounts.sh     /sbin/eph-mounts
COPY init.sh       /sbin/eph-init
COPY selftest.sh   /sbin/eph-selftest
COPY agent-init.sh /sbin/eph-agent-init
RUN chmod +x /sbin/eph-mounts /sbin/eph-init /sbin/eph-selftest /sbin/eph-agent-init

# Built on the host by build-rootfs.sh: a static CGO-free binary, so it needs no
# loader or libc inside the guest.
COPY eph-agent /usr/bin/eph-agent
RUN chmod +x /usr/bin/eph-agent
