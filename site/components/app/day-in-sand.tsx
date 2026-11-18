"use client"

import type { Run } from "@/lib/api"
import { sandboxName } from "@/lib/format"
import { cn } from "@/lib/utils"

// The sand palette, light to dark, and the one for runs that didn't finish.
const TONES = ["#F3DCAE", "#E6C287", "#D9B070", "#C99B5A", "#B98A4D", "#A67840"]
const BAD = "#C4633F"

const WIDE = 4 // grains across one hour's pile
const TALL = 10 // grains up before a grain stands for more than one run

// A grain drops onto its pile when it first shows up.
const GRAIN =
  "size-[7px] transition-[translate,opacity] duration-[420ms] ease-[cubic-bezier(0.23,1,0.32,1)] starting:-translate-y-3.5 starting:opacity-0 motion-reduce:transition-none"

// tone picks a run's grain colour from its id, so a grain keeps its colour
// from one poll to the next.
function tone(id: string) {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0
  return TONES[Math.abs(h) % TONES.length]
}

// DayInSand is today as sand: a grain for every run, piled under the hour
// it started. Runs that didn't finish are the rust grains. Hours still to
// come are a dotted floor.
export function DayInSand({ runs, now }: { runs: Run[]; now: number }) {
  const start = new Date(now)
  start.setHours(0, 0, 0, 0)
  const hour = new Date(now).getHours()
  const piles: Run[][] = Array.from({ length: 24 }, () => [])
  for (const r of runs) {
    const t = Date.parse(r.created_at)
    if (t < start.getTime()) continue
    piles[new Date(t).getHours()].push(r)
  }
  // Oldest at the bottom of each pile.
  for (const p of piles) p.reverse()
  const most = Math.max(1, ...piles.map((p) => p.length))
  const per = Math.max(1, Math.ceil(most / (WIDE * TALL))) // runs a grain
  // As tall as the tallest pile, so a quiet day isn't a tall empty band.
  const rows = Math.min(TALL, Math.max(3, Math.ceil(most / per / WIDE)))

  return (
    <figure className="w-full">
      <div className="overflow-x-auto pb-1">
        <div
          role="img"
          aria-label={`Runs today by the hour they started; ${runs.length} so far.`}
          className="grid min-w-[640px] grid-cols-[repeat(24,minmax(0,1fr))] gap-x-2"
        >
          {piles.map((pile, h) => {
            const grains = []
            for (let i = 0; i * per < pile.length; i++) {
              const group = pile.slice(i * per, (i + 1) * per)
              const bad = group.some((r) => r.ended_at && (r.end_reason ?? "destroyed") !== "destroyed")
              const live = group.some((r) => !r.ended_at)
              grains.push(
                <span
                  key={group[0].id}
                  title={group.map(sandboxName).join(", ")}
                  className={cn(GRAIN, live && "grain-live")}
                  style={{ background: bad ? BAD : tone(group[0].id) }}
                />,
              )
            }
            return (
              <div key={h} className="flex flex-col items-center">
                {/* The pile fills from the bottom left, row by row. */}
                <div className="flex w-[36px] flex-wrap-reverse content-start gap-[2px]" style={{ height: rows * 9 }}>
                  {grains}
                </div>
                <span
                  aria-hidden
                  className={cn(
                    "mt-1.5 h-px w-full",
                    h === hour ? "bg-sand/70" : h > hour ? "border-t border-dotted border-faint/40" : "bg-border",
                  )}
                />
                <span className={cn("mt-2 h-4 text-[11px] tabular-nums", h === hour ? "text-sand" : "text-faint")}>
                  {h === hour ? "now" : h % 6 === 0 && Math.abs(h - hour) > 1 ? `${String(h).padStart(2, "0")}:00` : ""}
                </span>
              </div>
            )
          })}
        </div>
      </div>
      {per > 1 && <figcaption className="mt-2 text-xs text-faint">Each grain is {per} runs.</figcaption>}
    </figure>
  )
}
