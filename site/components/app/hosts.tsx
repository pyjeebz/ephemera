"use client"

import { Plus, X } from "lucide-react"
import { useState, useSyncExternalStore } from "react"

import { Button } from "@/components/ui/button"
import { api, type Host } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { ago, date } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { CopyLine } from "./copy-line"
import {
  Capacity,
  EmptyState,
  HostDot,
  PageHeader,
  pageAction,
  Panel,
  PanelBar,
  Problem,
  Quiet,
  Segments,
  td,
  th,
} from "./dash"
import { installCommand, joinCommand, useJoinCode } from "./join-code"

const FILTERS = ["All", "Online", "Offline"] as const

function onHash(change: () => void) {
  window.addEventListener("hashchange", change)
  return () => window.removeEventListener("hashchange", change)
}
type Filter = (typeof FILTERS)[number]

export function Hosts() {
  const now = useNow()
  const hosts = useApi<Host[]>("/v1/hosts", 5000)
  // The overview's "Add a host" lands here with #add, which opens the form.
  const asked = useSyncExternalStore(onHash, () => window.location.hash === "#add", () => false)
  const [open, setOpen] = useState(false)
  const adding = open || asked
  const setAdding = (v: boolean) => {
    setOpen(v)
    if (!v && asked) {
      history.replaceState(null, "", window.location.pathname)
      window.dispatchEvent(new HashChangeEvent("hashchange"))
    }
  }
  const [filter, setFilter] = useState<Filter>("All")

  const list = hosts.data ?? []
  const online = list.filter((h) => h.online)
  const shown = list.filter((h) => filter === "All" || h.online === (filter === "Online"))
  const room = online.reduce((n, h) => n + (h.capacity || 0), 0)
  const running = list.reduce((n, h) => n + h.running, 0)

  return (
    <>
      <Problem error={hosts.error} />
      <PageHeader
        title="Hosts"
        sub={
          hosts.data &&
          (list.length
            ? `${online.length} of ${list.length} online · ${running} running · room for ${room || "none"} online`
            : "The machines your sandboxes run on.")
        }
        action={
          <Button size="lg" className={pageAction} onClick={() => setAdding(true)}>
            <Plus /> Add a host
          </Button>
        }
      />
      <Panel className="mt-8">
        <PanelBar>
          <div className="-ml-3 flex items-center">
            <Segments options={FILTERS} value={filter} onChange={setFilter} />
          </div>
        </PanelBar>
        {adding && <AddHost onClose={() => setAdding(false)} />}
        {!hosts.data ? (
          <div className="h-[300px]" />
        ) : list.length === 0 ? (
          !adding && (
            <EmptyState
              title="No hosts yet"
              actions={
                <Button size="xl" onClick={() => setAdding(true)}>
                  <Plus /> Add a host
                </Button>
              }
            >
              A host is any x86_64 Linux machine with KVM: a spare PC, a VPS, or a cloud VM with nested virtualization.
            </EmptyState>
          )
        ) : shown.length === 0 ? (
          <Quiet>No {filter.toLowerCase()} hosts.</Quiet>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[860px]">
              <thead>
                <tr>
                  <th className={th}>Name</th>
                  <th className={th}>Sandboxes</th>
                  <th className={th}>Key fingerprint</th>
                  <th className={th}>Version</th>
                  <th className={th}>Joined</th>
                  <th className={cn(th, "w-[120px]")}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {shown.map((h) => (
                  <tr key={h.id}>
                    <td className={td}>
                      <span className="flex items-center gap-2.5 font-medium" title={h.id}>
                        <HostDot host={h} />
                        {h.name}
                      </span>
                    </td>
                    <td className={cn(td, "text-muted-foreground")}>
                      {h.online ? (
                        <Capacity host={h} />
                      ) : h.last_seen && Date.parse(h.last_seen) > 0 ? (
                        <>Offline, seen {ago(h.last_seen, now)}</>
                      ) : (
                        <>Joined, hasn’t connected</>
                      )}
                    </td>
                    <td className={cn(td, "max-w-[260px]")}>
                      <span className="block truncate font-mono text-xs text-muted-foreground" title={h.fingerprint}>
                        {h.fingerprint}
                      </span>
                    </td>
                    <td className={cn(td, "text-muted-foreground")}>{h.version ?? "–"}</td>
                    <td className={cn(td, "tabular-nums text-muted-foreground")}>{date(h.joined)}</td>
                    <td className={cn(td, "text-right")}>
                      <Remove host={h} onRemoved={hosts.reload} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
      <p className="mt-4 max-w-[720px] text-[13px] leading-5 text-faint">
        The fingerprint is the host’s key; the install printed the same one. Removing a host destroys its sandboxes, and
        it can’t sign in again until it joins with a new code.
      </p>
    </>
  )
}

// AddHost makes a join code and shows the command that uses it.
function AddHost({ onClose }: { onClose: () => void }) {
  const join = useJoinCode(true)
  return (
    <div className="mt-6 mb-2 rounded-xl border border-border/70 bg-card/60 px-5 py-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="font-medium">Add a host</h2>
          <p className="mt-1 max-w-[620px] leading-[21px] text-muted-foreground">
            Run this on any x86_64 Linux machine with KVM. It installs Firecracker and the host agent, and joins this
            account. The host connects out to ephemera, so it needs no open ports for us.
          </p>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
          <X />
        </Button>
      </div>
      {join.code && !join.expired ? (
        <>
          <CopyLine command={installCommand(join.code)} className="mt-4 rounded-lg bg-background" />
          <p className="mt-3 text-[13px] text-muted-foreground">
            <span className="tabular-nums">Works once · {join.clock} left</span> ·{" "}
            <button
              type="button"
              disabled={join.busy}
              onClick={join.make}
              className="text-foreground underline underline-offset-2"
            >
              Make a new one
            </button>
          </p>
          <details className="mt-3 text-[13px] text-muted-foreground">
            <summary className="cursor-pointer list-none text-foreground underline-offset-2 hover:underline">
              Already have ephemera-host?
            </summary>
            <CopyLine command={joinCommand(join.code)} className="mt-3 rounded-lg bg-background" />
          </details>
        </>
      ) : (
        <Button className="mt-4" disabled={join.busy} onClick={join.make}>
          {join.busy ? "Making a code…" : "Make a new code"}
        </Button>
      )}
      {join.error && <p className="mt-3 text-[13px] text-destructive">{join.error}</p>}
    </div>
  )
}

// Remove asks once more: a removed host has to join again.
function Remove({ host, onRemoved }: { host: Host; onRemoved: () => void }) {
  const [state, setState] = useState<"idle" | "sure" | "removing">("idle")
  const [error, setError] = useState<string>()
  return (
    <>
      <Button
        variant={state === "sure" ? "destructive" : "ghost"}
        size="sm"
        disabled={state === "removing"}
        onBlur={() => state === "sure" && setState("idle")}
        onClick={async () => {
          if (state === "idle") return setState("sure")
          setState("removing")
          try {
            await api(`/v1/hosts/${host.id}`, { method: "DELETE" })
            onRemoved()
          } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
            setState("idle")
          }
        }}
        className={state === "idle" ? "text-muted-foreground hover:text-destructive" : undefined}
      >
        {state === "sure" ? `Remove ${host.name}?` : state === "removing" ? "Removing…" : "Remove"}
      </Button>
      {error && <p className="mt-1 text-xs text-destructive">{error}</p>}
    </>
  )
}
