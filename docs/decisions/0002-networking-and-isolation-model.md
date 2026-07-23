# 0002 — Point-to-point links, a static nftables firewall, and one capability

- **Status:** Accepted (privilege model amended by [0005](0005-privileged-network-helper.md))
- **Date:** 2026-07-23
- **Phase:** 2 (Isolation & networking)

> **Amendment (0005):** where this record says "the daemon holds `CAP_NET_ADMIN`", the capability has since
> moved to a small `eph-netadmin` helper; the daemon holds none. The topology and firewall below are
> unchanged.

## Context

A machine needs to reach the internet without being able to reach the host, the host's LAN, or another
machine. That is the isolation-by-default principle the project committed to, and Phase 2 is where it
stops being a README line and becomes rules.

Three things had to be decided together, because each one constrains the others:

1. **Topology.** How are machines addressed and connected to the host?
2. **Firewall.** What enforces the egress policy, and who owns it?
3. **Privilege.** Networking is the first thing that needs `CAP_NET_ADMIN`. How much of it does the daemon
   get, and for how long?

The host runs the `nf_tables` kernel backend, with both `nft` and `iptables-nft` present. cgroup v2 is
unified; there is no systemd session delegation.

## Decision

**A point-to-point `/30` per machine, a single static nftables table installed once by `host-setup.sh`,
and a daemon that holds `CAP_NET_ADMIN` for TAP creation and touches no firewall at runtime.**

- Every machine gets its own `/30` out of a private pool (default `10.79.0.0/16`): one address for the
  host end of a TAP device, one for the guest. No bridge, no shared segment.
- The guest configures itself from the kernel's `ip=` command-line parameter (`CONFIG_IP_PNP=y`), before
  init runs. There is no DHCP and no in-guest network code.
- The firewall is one `table inet ephemera`, keyed on the **pool's address range** rather than on
  interface names. It masquerades pool traffic out the default-route interface and drops any pool packet
  destined for private space (`10/8`, `172.16/12`, `192.168/16`, `169.254/16`, `127/8`), allowing the rest.
- `host-setup.sh` installs all of it — capability, forwarding, firewall — as one-time host state.

## Reasons

1. **The topology *is* the isolation.** With a `/30` per machine there is no path from one machine to
   another that does not pass through the host's routing table, where the firewall gets to refuse it. A
   shared bridge would put every machine on one segment, making machine-to-machine traffic local delivery
   the firewall never sees — isolation would then depend on a rule being right instead of on there being
   no wire. "Who can this VM reach" collapses to a single destination match. The host itself is off limits
   too, via a separate input-chain drop: a guest sends the host no IP traffic (the control channel is
   vsock, not IP), so nothing legitimate is lost by refusing all of it.
2. **A static firewall is a firewall you can read.** Because rules match the pool subnet, one ruleset
   covers every machine that will ever boot — the daemon adds nothing per machine. The entire security
   posture is one file you can `nft list ruleset` and audit, rather than an emergent property of code that
   runs on every create and destroy. Nothing to leak, nothing to get out of sync, nothing to clean up
   after a crash.
3. **nftables over iptables-nft.** Both drive the same kernel backend, so this is not about capability.
   It is that `nft` is the primitive and `iptables-nft` is a translation shim in front of it; a project
   whose stated goal is to learn the stack should write the thing, not the compatibility layer for the
   older thing. One atomic `nft -f` also replaces the whole table transactionally — no half-applied
   ruleset — which the iptables model does not give you.
4. **One capability, not a root daemon.** `CAP_NET_ADMIN` on the binary lets ephemerad create TAP devices
   and nothing else: it cannot read other users' files, load modules, or become root. Keeping the firewall
   out of the daemon is what makes this possible — if the daemon programmed nftables at runtime it would
   need the capability anyway, but now the blast radius of a daemon bug stops at "can manage interfaces."

## Costs we are accepting

- **A `/30` spends four addresses to carry two.** Irrelevant against a `/16` (16,384 links), and RFC 3021
  `/31`s could halve it later if it ever mattered.
- **Uniform policy for the whole pool.** Every machine gets the same egress rules; there is no per-machine
  allow-list yet. That is a real limitation for "let *this* machine reach only *this* API," and the static
  design is exactly what a per-machine policy would have to grow out of. Deferred, not dismissed.
- **`host-setup.sh` must be re-run after every `go build`.** File capabilities live on the inode and a
  rebuild writes a new one. Annoying; documented loudly in the script.
- **The resolver must be public.** Private space is denied, so a private DNS server would be blocked. The
  default (`1.1.1.1`) is fine; a `-dns` pointing inside a LAN would not be.

## On the ADR-0001 revisit trigger

ADR 0001 said to re-evaluate `firecracker-go-sdk` once networking and the jailer landed. Networking has
landed, and it did **not** want the SDK: the whole networking layer is `internal/vmnet` (TAP ioctls plus a
`/30` allocator) and a shell script, none of which the SDK's CNI-plugin model would have simplified — the
CNI assumptions cut *against* the point-to-point-plus-static-firewall design rather than toward it. The
jailer is still outstanding and remains the SDK's strongest case; that re-evaluation moves to when the
jailer is implemented. The Phase 2 networking work confirms ADR 0001 rather than overturning it.

## Still outstanding in Phase 2

cgroup v2 CPU/memory caps and the jailer. Both are isolation, neither is networking, and folding them into
this decision would have made it about everything. They get their own work and, where warranted, their own
record.
