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
# Two inits ship in the image; the kernel cmdline picks one:
#   /sbin/eph-init     interactive shell on the serial console
#   /sbin/eph-selftest non-interactive check that halts (used by tests/CI)
COPY mounts.sh   /sbin/eph-mounts
COPY init.sh     /sbin/eph-init
COPY selftest.sh /sbin/eph-selftest
RUN chmod +x /sbin/eph-mounts /sbin/eph-init /sbin/eph-selftest
