"use client"

import { BookOpen, Check, Copy, Plus } from "lucide-react"
import Link from "next/link"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { api, type Host, type Run } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { useCopy } from "@/lib/use-copy"
import { ago, duration, sandboxName, since, started } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { DASHBOARD_PROMPT, DOCS } from "@/lib/site"
import { cn } from "@/lib/utils"
import {
  EmptyState,
  ENDED,
  LiveBox,
  Network,
  Panel,
  PanelBar,
  Problem,
  Quiet,
  Result,
  SearchField,
  Segments,
  PageHeader,
  td,
  th,
} from "./dash"

// One page of runs from the API.
const PAGE = 100

const FILTERS = ["All", "Running", "Ended", "Didn’t finish"] as const
type Filter = (typeof FILTERS)[number]

const keepers: Record<Filter, (r: Run) => boolean> = {
  All: () => true,
  Running: (r) => !r.ended_at,
  Ended: (r) => !!r.ended_at,
  "Didn’t finish": (r) => !!r.ended_at && (r.end_reason ?? "destroyed") !== "destroyed",
}

// Sandboxes is every sandbox, running and gone, to search and filter.
export function Sandboxes() {
  const now = useNow()
  const hosts = useApi<Host[]>("/v1/hosts", 5000)
  const first = useApi<Run[]>("/v1/runs", 3000)
  const [older, setOlder] = useState<Run[]>([])
  const [more, setMore] = useState<"idle" | "loading" | "done">("idle")
  const [filter, setFilter] = useState<Filter>("All")
  const [query, setQuery] = useState("")
  const [open, setOpen] = useState<string>()

  // The newest page refreshes; older pages, once loaded, stay as they are.
  const seen = new Set(first.data?.map((r) => r.id))
  const runs = [...(first.data ?? []), ...older.filter((r) => !seen.has(r.id))]
  const all = more === "done" || (first.data !== undefined && first.data.length < PAGE)

  const q = query.trim().toLowerCase()
  const shown = runs
    .filter(keepers[filter])
    .filter((r) => !q || [r.name, r.id, r.host, r.owner].some((v) => v?.toLowerCase().includes(q)))


  const loadOlder = async () => {
    const last = runs.at(-1)
    if (!last) return
    setMore("loading")
    try {
      const page = await api<Run[]>(`/v1/runs?before=${encodeURIComponent(last.created_at)}`)
      setOlder((o) => [...o, ...page])
      setMore(page.length < PAGE ? "done" : "idle")
    } catch {
      setMore("idle")
    }
  }

  return (
    <>
      <Problem error={hosts.error ?? first.error} />
      <PageHeader title="Sandboxes" sub="Every sandbox, running and gone, newest first." />
      <Panel className="mt-8">
        <PanelBar end={<Segments options={FILTERS} value={filter} onChange={setFilter} />}>
          <SearchField value={query} onChange={setQuery} placeholder="Search by name, host or token" />
        </PanelBar>
        {!first.data || !hosts.data ? (
          <div className="h-[440px]" />
        ) : runs.length === 0 ? (
          <FirstSandbox hasHost={hosts.data.length > 0} />
        ) : shown.length === 0 ? (
          <Quiet>{q ? `Nothing called “${query.trim()}”.` : `No ${filter.toLowerCase()} sandboxes.`}</Quiet>
        ) : (
          <RunTable runs={shown} now={now} open={open} onOpen={setOpen} onStop={first.reload} />
        )}
        {runs.length > 0 && (
          <div className="flex items-center justify-between gap-4 border-t border-border/60 py-3 text-[13px] text-faint">
            <span className="tabular-nums">
              {shown.length} of {runs.length}
              {all ? "" : "+"} {runs.length === 1 ? "sandbox" : "sandboxes"}
            </span>
            {!all && (
              <Button variant="ghost" size="sm" disabled={more === "loading"} onClick={loadOlder}>
                {more === "loading" ? "Loading…" : "Load older"}
              </Button>
            )}
          </div>
        )}
      </Panel>
      <p className="mt-4 text-[13px] text-faint">
        ephemera keeps when each sandbox ran, where, how it ended and what it could reach. Never the command, files or
        output.
      </p>
    </>
  )
}

// FirstSandbox is the empty panel: a host first, then a first run.
export function FirstSandbox({ hasHost }: { hasHost: boolean }) {
  const { copied, copy } = useCopy(DASHBOARD_PROMPT)
  if (!hasHost) {
    return (
      <EmptyState
        title="No sandboxes yet"
        actions={
          <>
            <Button asChild size="xl">
              <Link href="/onboarding/install">
                <Plus /> Set up a host
              </Link>
            </Button>
            <Button asChild size="xl" variant="outline">
              <a href={DOCS}>
                <BookOpen /> Quickstart guide
              </a>
            </Button>
          </>
        }
      >
        Sandboxes run on your own machines. Add one, and they show up here while they live.
      </EmptyState>
    )
  }
  return (
    <EmptyState
      title="No sandboxes yet"
      actions={
        <>
          <Button size="xl" onClick={copy} aria-live="polite">
            {copied ? <Check /> : <Copy />} {copied ? "Copied" : "Copy setup prompt"}
          </Button>
          <Button asChild size="xl" variant="outline">
            <a href={DOCS}>
              <BookOpen /> Quickstart guide
            </a>
          </Button>
        </>
      }
    >
      Paste the prompt into your coding agent, or run{" "}
      <code className="font-mono text-[13px] text-foreground">ephemera run --name hello -- echo hi</code> in a repo.
    </EmptyState>
  )
}

export function RunTable({
  runs,
  now,
  open,
  onOpen,
  onStop,
  compact,
}: {
  runs: Run[]
  now: number
  open?: string
  onOpen?: (id: string | undefined) => void
  onStop?: () => void
  // compact is the overview's short list: name, how it ended, how long, when.
  compact?: boolean
}) {
  if (compact) {
    if (!runs.length) return <Quiet>Nothing has ended yet.</Quiet>
    return (
      <div className="overflow-x-auto">
        <table className="w-full min-w-[520px]">
          <thead>
            <tr>
              <th className={th}>Name</th>
              <th className={th}>Ended</th>
              <th className={cn(th, "text-right")}>Lived</th>
              <th className={cn(th, "text-right")}>When</th>
            </tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id}>
                <td className={cn(td, "max-w-[220px]")}>
                  <span className="block truncate font-medium" title={r.id}>
                    {sandboxName(r)}
                  </span>
                </td>
                <td className={td}>
                  <Result run={r} />
                </td>
                <td className={cn(td, "text-right tabular-nums")}>
                  {duration(since(r.created_at, r.ended_at ? Date.parse(r.ended_at) : now))}
                </td>
                <td className={cn(td, "text-right tabular-nums text-muted-foreground")}>
                  {ago(r.ended_at ?? r.created_at, now)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[860px]">
        <thead>
          <tr>
            <th className={cn(th, "w-[84px]")}>
              <span className="sr-only">State</span>
            </th>
            <th className={th}>Name</th>
            <th className={th}>Status</th>
            <th className={th}>Host</th>
            <th className={th}>Token</th>
            <th className={th}>Network</th>
            <th className={th}>Started</th>
            <th className={cn(th, "text-right")}>Lived</th>
            <th className={cn(th, "w-[92px]")}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => {
            const isOpen = open === r.id
            const lived = since(r.created_at, r.ended_at ? Date.parse(r.ended_at) : now)
            return [
              <tr
                key={r.id}
                onClick={onOpen && (() => onOpen(isOpen ? undefined : r.id))}
                aria-expanded={onOpen ? isOpen : undefined}
                className={cn(
                  onOpen && "cursor-pointer transition-colors duration-150 hover:bg-raised/40",
                  isOpen && "bg-raised/40",
                )}
              >
                <td className={cn(td, "py-3")}>{!r.ended_at && <LiveBox />}</td>
                <td className={cn(td, "max-w-[280px]")}>
                  <span className="block truncate font-medium text-foreground" title={r.id}>
                    {sandboxName(r)}
                  </span>
                </td>
                <td className={td}>
                  <Result run={r} />
                </td>
                <td className={td}>{r.host}</td>
                <td className={cn(td, "text-muted-foreground")}>{r.owner}</td>
                <td className={td}>
                  <Network run={r} />
                </td>
                <td className={cn(td, "tabular-nums text-muted-foreground")}>{started(r.created_at, now)}</td>
                <td className={cn(td, "text-right tabular-nums")}>{duration(lived)}</td>
                <td className={cn(td, "text-right")} onClick={(e) => e.stopPropagation()}>
                  {!r.ended_at && onStop && <Stop run={r} onStopped={onStop} />}
                </td>
              </tr>,
              isOpen && (
                <tr key={r.id + "-detail"} className="bg-raised/40">
                  <td />
                  <td colSpan={8} className="px-3 pb-6 pt-2">
                    <Detail run={r} />
                  </td>
                </tr>
              ),
            ]
          })}
        </tbody>
      </table>
    </div>
  )
}

// Stop asks once more before it destroys a sandbox.
export function Stop({ run, onStopped }: { run: { id: string; name?: string }; onStopped: () => void }) {
  const [state, setState] = useState<"idle" | "sure" | "stopping" | string>("idle")
  if (state !== "idle" && state !== "sure" && state !== "stopping") {
    return <span className="text-[13px] text-destructive">{state}</span>
  }
  return (
    <Button
      variant={state === "sure" ? "destructive" : "ghost"}
      size="sm"
      disabled={state === "stopping"}
      onBlur={() => state === "sure" && setState("idle")}
      aria-label={state === "idle" ? `Stop ${sandboxName(run)}` : undefined}
      onClick={async () => {
        if (state === "idle") return setState("sure")
        setState("stopping")
        try {
          await api(`/v1/sandboxes/${run.id}`, { method: "DELETE" })
          onStopped()
        } catch (e) {
          setState(e instanceof Error ? e.message : "couldn’t stop it")
        }
      }}
      className={state === "idle" ? "text-muted-foreground" : undefined}
    >
      {state === "sure" ? "Stop it?" : state === "stopping" ? "Stopping…" : "Stop"}
    </Button>
  )
}

function Detail({ run }: { run: Run }) {
  const s = run.spec
  const why = run.ended_at ? ENDED[run.end_reason ?? "destroyed"][1] : "It’s still running."
  return (
    <div className="grid grid-cols-1 gap-8 whitespace-normal md:grid-cols-3">
      <div>
        <p className="text-[13px] text-faint">How it ended</p>
        <p className="mt-1.5 leading-[21px]">{why}</p>
      </div>
      <div>
        <p className="text-[13px] text-faint">What it had</p>
        <p className="mt-1.5 text-muted-foreground">
          {[s.image, s.cpus && `${s.cpus} CPU`, s.memory_mb && `${s.memory_mb} MB`].filter(Boolean).join(" · ") ||
            "host defaults"}
        </p>
        {s.network === "allow" && (
          <ul className="mt-2 space-y-1 font-mono text-[13px]">
            {s.allow?.map((h) => (
              <li key={h}>{h}</li>
            ))}
          </ul>
        )}
        <p className="mt-2 text-[13px] text-muted-foreground">
          {s.network === "allow"
            ? "Everything else was blocked."
            : s.network === "open"
              ? "It could reach anything."
              : "It had no network."}
        </p>
      </div>
      <div>
        <p className="text-[13px] text-faint">Id</p>
        <p className="mt-1.5 font-mono text-[13px] text-muted-foreground">{run.id}</p>
        <p className="mt-4 text-[13px] text-faint">The record</p>
        <p className="mt-1.5 leading-[21px] text-muted-foreground">
          On the machine that ran it, not here. Check it with{" "}
          <code className="font-mono text-[13px] text-foreground">ephemera verify {run.id}</code>
        </p>
      </div>
    </div>
  )
}
