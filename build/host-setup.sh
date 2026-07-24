#!/usr/bin/env bash
# One-time host setup for machine networking. Needs root; run it with sudo.
#
# Phases 0 and 1 needed no privilege at all. Networking is where that ends, and
# this script draws the line deliberately: it hands ONE small binary a single
# capability, installs a firewall that governs the whole machine pool, and does
# nothing else. ephemerad and eph run with NO capability — they reach the network
# only by executing eph-netadmin, which is the one component that may touch a TAP.
#
# Two consequences worth knowing:
#   * The daemon itself is unprivileged. Only eph-netadmin carries CAP_NET_ADMIN,
#     and all it can do is create and destroy TAP devices — it cannot read your
#     files, load modules, or escalate. A bug in the daemon has nothing to launder.
#   * The firewall matches on the pool's address range, not on interface names,
#     so it covers every machine that will ever boot into the pool without the
#     daemon adding a single rule per machine.
#
# Re-running is safe and idempotent. You *will* need to re-run it after every
# `go build`: file capabilities live on the inode, and building writes a new one.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Only the network helper is privileged. The daemon and CLI hold nothing.
BINARIES=("$REPO/bin/eph-netadmin")

# Must match ephemerad's -pool / eph's -pool. The firewall is written in terms
# of this range, so the two have to agree or machines get an address the rules
# do not cover.
POOL="${EPHEMERA_POOL:-10.79.0.0/16}"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "host-setup: needs root — run: sudo $0" >&2
  exit 1
fi

# The interface packets leave by. Derived from the default route rather than
# hard-coded, because it is eth0 on this box and something else on the next one.
EGRESS_IF="$(ip route show default | awk '/default/ {print $5; exit}')"
if [[ -z "$EGRESS_IF" ]]; then
  echo "host-setup: no default route — cannot tell which interface reaches the internet" >&2
  exit 1
fi

echo ">> egress interface: $EGRESS_IF"
echo ">> machine pool:     $POOL"

echo
echo ">> ip forwarding"
# Without this the host receives the guest's packets and drops them on the
# floor, which looks exactly like a firewall problem for about fifteen minutes.
sysctl -w net.ipv4.ip_forward=1 >/dev/null
install -d -m 0755 /etc/sysctl.d
cat > /etc/sysctl.d/99-ephemera.conf <<'EOF'
# ephemera: machines reach the outside world by being routed through the host.
net.ipv4.ip_forward = 1
EOF
echo "   net.ipv4.ip_forward = 1"

echo
echo ">> /dev/net/tun"
if [[ ! -c /dev/net/tun ]]; then
  modprobe tun || {
    echo "host-setup: /dev/net/tun is missing and the tun module would not load" >&2
    exit 1
  }
fi
ls -l /dev/net/tun

echo
echo ">> firewall (nftables)"
# One table the daemon does not own and does not touch. Replacing it wholesale
# each run — delete-then-add — means the ruleset is always exactly what this
# script says, with no residue from a previous version.
#
# The policy is isolation by default: a machine may reach the public internet
# and nothing private. That single rule blocks three things at once —
#   * other machines (the pool is inside a private range),
#   * the host and everything else on its LAN,
#   * link-local and loopback games,
# which is the whole point of giving each machine its own /30 rather than a
# shared bridge: "who can this VM talk to" collapses to one destination match.
# An ip (v4) table, not inet: every address here is IPv4, the guest has no IPv6,
# and ip-family NAT is supported on every kernel that has nftables at all —
# which sidesteps the versions where inet-family NAT was not yet a thing.
nft -f - <<EOF
table ip ephemera
delete table ip ephemera
table ip ephemera {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        # Rewrite the machine's private source address to the host's as traffic
        # leaves the box — the internet has never heard of 10.79.x.x and could
        # not route a reply back to it.
        ip saddr $POOL oifname "$EGRESS_IF" masquerade
    }

    chain input {
        type filter hook input priority filter; policy accept;
        # Traffic a machine sends to the host *itself* — its gateway address, or
        # the host's LAN address — is delivered locally, not forwarded, so the
        # forward chain never sees it. Without this a machine could reach any
        # service the host runs. It needs none: the control channel is vsock,
        # which is not IP and does not pass through here at all. So the host is
        # simply off limits.
        ip saddr $POOL drop
    }

    chain forward {
        type filter hook forward priority filter; policy accept;
        # Return traffic for a connection the machine opened is always fine;
        # judging it again would just be a way to break every reply.
        ct state established,related accept
        # Everything a machine *initiates* is judged by the egress chain. Traffic
        # the host forwards for other reasons is none of our business — the
        # accept policy leaves it untouched.
        ip saddr $POOL jump egress
    }

    chain egress {
        # The one rule that makes this a sandbox. Private space is off limits:
        # no other machine, no host service, no LAN neighbour.
        ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 127.0.0.0/8 } drop
        # Anything left is the public internet, which the machine may reach.
        accept
    }
}
EOF
echo "   installed table ip ephemera (public internet only; private space denied)"

echo
echo ">> cgroup v2 delegation (resource caps)"
# The same one-time-privilege trick as the capability below: root carves out a
# subtree and hands it to the user, and from then on the daemon makes one cgroup
# per machine with no privilege at all.
#
# Two things have to be true for a machine's cpu.max/memory.max to work:
#   1. the subtree's *parent* offers the cpu and memory controllers — enabled by
#      writing them into the parent's subtree_control;
#   2. the subtree itself passes them down to its children, the per-machine
#      leaves — the same write, one level down.
# Then the whole subtree is chowned to the user so the daemon owns it.
CG_ROOT="/sys/fs/cgroup"
CG_SUB="$CG_ROOT/ephemera"
CG_USER="${SUDO_USER:-$(id -un)}"

if [[ ! -d "$CG_ROOT" ]] || [[ "$(stat -fc %T "$CG_ROOT" 2>/dev/null)" != "cgroup2fs" ]]; then
  echo "   $CG_ROOT is not cgroup v2 — skipping resource caps"
else
  # The parent must offer the controllers before a child can. On this host the
  # root already delegates everything; on another it might not, so ensure it.
  for c in cpu memory; do
    if ! grep -qw "$c" "$CG_ROOT/cgroup.subtree_control"; then
      echo "+$c" > "$CG_ROOT/cgroup.subtree_control" 2>/dev/null || true
    fi
  done

  mkdir -p "$CG_SUB"
  # Pass cpu and memory down to the per-machine leaves.
  echo "+cpu +memory" > "$CG_SUB/cgroup.subtree_control"
  # Delegate: the user now owns the subtree and can make and remove leaves in it.
  chown -R "$CG_USER" "$CG_SUB"

  # Owning the subtree lets the daemon create leaves and write limits, but not
  # yet *place a process* into one. Putting a process into a cgroup is a
  # migration, and cgroup v2 requires write access to the cgroup.procs of the
  # common ancestor of the process's current cgroup and the destination. The
  # daemon starts outside the subtree, so that ancestor is the hierarchy root —
  # whose cgroup.procs is root-owned. Grant the user that one file.
  #
  # On a host with a systemd user session you would instead run ephemerad from a
  # unit with Delegate=yes: it would start already inside its own scope, the
  # boundary crossing would not exist, and this line would be unnecessary. On a
  # single-user box (WSL2 here) without that session, this is the honest
  # equivalent — and on a single-user box, write to the root cgroup.procs is not
  # a meaningful boundary: you already have root.
  chown "$CG_USER" "$CG_ROOT/cgroup.procs"
  echo "   delegated $CG_SUB to $CG_USER (controllers: $(cat "$CG_SUB/cgroup.subtree_control"))"
  echo "   granted $CG_USER write to $CG_ROOT/cgroup.procs (process placement)"
fi

echo
echo ">> granting CAP_NET_ADMIN to the network helper"
missing=0
for bin in "${BINARIES[@]}"; do
  if [[ ! -f "$bin" ]]; then
    echo "   $bin not built yet — skipping"
    missing=1
    continue
  fi
  setcap cap_net_admin+ep "$bin"
  echo "   $(getcap "$bin")"
done

if [[ "$missing" -eq 1 ]]; then
  echo
  echo "   build the helper and re-run:"
  echo "     go build -o bin/eph-netadmin ./cmd/eph-netadmin"
  echo "     sudo $0"
fi

echo
echo ">> done. the daemon and CLI hold no capability; only eph-netadmin does."
echo "   networking (opt-in per machine):"
echo "     ./bin/ephemera run -net -- wget -qO- https://example.com     # reaches the internet"
echo "     ./bin/ephemera run -net -- wget -qO- http://172.19.0.1       # blocked, by design"
echo "     ./bin/ephemerad -network"
echo "   isolation composes now — jailed AND networked AND capped:"
echo "     ./bin/ephemera run -jail -net -- wget -qO- https://example.com"
echo "   resource caps (automatic once delegated — every machine, networked or not):"
echo "     ./bin/ephemera run -- sh -c 'cat /sys/fs/cgroup/memory.max'  # capped, not 'max'"
echo
echo "   re-run this after every go build of eph-netadmin (file caps do not survive a rebuild)."
