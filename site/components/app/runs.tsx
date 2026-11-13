"use client"

import { useState } from "react"

import { Button } from "@/components/ui/button"
import { api, type Run } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { ENDED, Empty, Problem } from "./dash"
import { RunTable } from "./overview"

type Filter = { label: string; keep: (r: Run) => boolean }

const FIXED: Filter[] = [
  { label: "All", keep: () => true },
  { label: "Running", keep: (r) => !r.ended_at },
  { label: "Didn’t finish", keep: (r) => !!r.ended_at && (r.end_reason ?? "destroyed") !== "destroyed" },
]

export function Runs() {
  const now = useNow()
  const first = useApi<Run[]>("/v1/runs", 5000)
  const [older, setOlder] = useState<Run[]>([])
  const [more, setMore] = useState<"idle" | "loading" | "done">("idle")
  const [filter, setFilter] = useState("All")
  const [open, setOpen] = useState<string>()

  // The newest page refreshes; older pages, once loaded, stay as they are.
  const seen = new Set(first.data?.map((r) => r.id))
  const runs = [...(first.data ?? []), ...older.filter((r) => !seen.has(r.id))]
  // A first page shorter than a full one has everything.
  const all = more === "done" || (first.data !== undefined && first.data.length < 100)

  const tokens = [...new Set(runs.map((r) => r.owner))].sort()
  const filters = [...FIXED, ...tokens.map((t) => ({ label: t, keep: (r: Run) => r.owner === t }))]
  const keep = (filters.find((f) => f.label === filter) ?? FIXED[0]).keep
  const shown = runs.filter(keep)

  const loadOlder = async () => {
    const last = runs.at(-1)
    if (!last) return
    setMore("loading")
    try {
      const page = await api<Run[]>(`/v1/runs?before=${encodeURIComponent(last.created_at)}`)
      setOlder((o) => [...o, ...page])
      setMore(page.length < 100 ? "done" : "idle")
    } catch {
      setMore("idle")
    }
  }

  return (
    <>
      <Problem error={first.error} />
      <div className="mt-8 flex flex-wrap gap-2 text-[13px]">
        {filters.map((f) => (
          <button
            key={f.label}
            type="button"
            aria-pressed={f.label === filter}
            onClick={() => setFilter(f.label)}
            className={cn(
              "border px-3 py-1.5 transition-colors duration-150",
              f.label === filter
                ? "border-sand/60 text-foreground"
                : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {f.label}
          </button>
        ))}
      </div>

      <div className="mt-6">
        {first.data && shown.length === 0 ? (
          <Empty>{runs.length ? "No runs match." : "No runs yet."}</Empty>
        ) : (
          <RunTable runs={shown} now={now} open={open} onOpen={setOpen} detail={(r) => <Detail run={r} />} />
        )}
      </div>
      {!all && runs.length > 0 && (
        <Button variant="outline" className="mt-4" disabled={more === "loading"} onClick={loadOlder}>
          {more === "loading" ? "Loading…" : "Older runs"}
        </Button>
      )}
      <p className="mt-4 text-xs text-faint">
        ephemera keeps when each sandbox ran, where, how it ended and what it was allowed to reach. Never the command,
        files or output.
      </p>
    </>
  )
}

function Detail({ run }: { run: Run }) {
  const s = run.spec
  const why = run.ended_at ? ENDED[run.end_reason ?? "destroyed"][1] : "It’s still running."
  return (
    <div className="grid grid-cols-1 gap-8 md:grid-cols-3">
      <div>
        <p className="text-xs text-faint">How it ended</p>
        <p className="mt-1.5 text-[13px]">{why}</p>
      </div>
      <div>
        <p className="text-xs text-faint">What it had</p>
        <p className="mt-1.5 font-mono text-[13px] text-muted-foreground">
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
        <p className="mt-2 text-xs text-muted-foreground">
          {s.network === "allow"
            ? "Everything else was blocked."
            : s.network === "open"
              ? "It could reach anything."
              : "It had no network."}
        </p>
      </div>
      <div>
        <p className="text-xs text-faint">The record</p>
        <p className="mt-1.5 text-[13px] leading-5 text-muted-foreground">
          On the machine that ran it, not here. Check it with{" "}
          <code className="font-mono text-foreground">ephemera verify {run.id}.json</code>
        </p>
      </div>
    </div>
  )
}
