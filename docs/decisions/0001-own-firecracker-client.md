# 0001 — Talk to Firecracker through our own client, not firecracker-go-sdk

- **Status:** Accepted
- **Date:** 2026-07-22
- **Phase:** 1 (Go control plane MVP)

## Context

Firecracker runs as a separate process and is controlled entirely through a small REST API served over a
Unix domain socket. Booting a VM is four requests — `PUT /boot-source`, `PUT /drives/{id}`,
`PUT /machine-config`, `PUT /actions {InstanceStart}` — which `build/boot-vm.sh` already proves with
nothing but `curl`.

Something has to wrap those calls for `ephemerad`: issue the HTTP requests, launch and supervise the
`firecracker` process, and later wire up the jailer and networking. Two candidates:

1. **`firecracker-go-sdk`** — the official Go library. Handles the API calls, process supervision,
   jailer integration, and networking via CNI plugins.
2. **Our own thin client** — a small package speaking the same REST API directly.

## Decision

**Write our own thin client now, and explicitly re-evaluate the SDK at Phase 2**, when the jailer and
networking land and the SDK's help would be most concrete.

## Reasons

1. **The SDK's release cadence has stalled.** Its most recent *tagged* release is `v1.0.0` from
   2022-09-07, which long predates the Firecracker v1.16.1 we run. `main` is still maintained (commits
   as recent as 2025-12), but adopting it means pinning an untagged commit — a worse dependency story
   than having no dependency.
2. **The stated goal of this project is to learn the whole stack.** The SDK's value is that it hides the
   API; here that is a cost, not a benefit. Owning the client means every call the daemon makes is
   visible and deliberate.
3. **Our isolation goals cut against the SDK's opinions.** ephemera commits to egress that is
   allow-listed rather than open (see README principles). The SDK's networking is built around CNI
   plugins with different assumptions, so we would be writing our own firewall rules regardless — which
   erodes much of what the SDK would save.
4. **Zero version drift.** A client we own targets exactly the Firecracker version we ship and cannot
   silently diverge from it.

## Costs we are accepting

- Roughly 300 lines we would otherwise not write: the typed API calls plus process supervision.
- **Process supervision is the genuinely fiddly part** — reaping the child, cleaning up sockets, and not
  leaking VMs when the daemon itself dies. The SDK has already solved this; we have not.
- If Phase 2 shows the SDK's jailer/CNI wiring saves substantial work, we will have written code twice.

## Revisit trigger

At **Phase 2**, when TAP devices, NAT, egress filtering, and the jailer are implemented. If the SDK's
handling of those turns out to be a material saving *and* compatible with allow-listed egress, adopting
it then is a legitimate outcome — this decision is deliberately not permanent.
