"use client"

import { Plus } from "lucide-react"
import Link from "next/link"
import type { ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { type Host, type Run, type Sandbox, type Usage } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { ago, duration, firstName, greeting, isToday, network, sandboxName, since } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { useAccount } from "./account"
import { HostDot, Meter, Problem, Result } from "./dash"
import { DayInSand } from "./day-in-sand"
import { FirstSandbox, Stop } from "./sandboxes"
import { SandBox } from "./sand-box"

const today = new Intl.DateTimeFormat(undefined, { weekday: "long", month: "long", day: "numeric" })

// minutes shows short days to a tenth, so a few quick runs aren't 0.
function minutes(m: number) {
  return m === 0 ? "0" : m < 10 ? m.toFixed(1) : String(Math.round(m))
}

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`

// B is a number in a sentence: brighter than the words around it.
function B({ children }: { children: ReactNode }) {
  return <span className="text-foreground tabular-nums">{children}</span>
}

// Overview is the dashboard's home, kept quiet: today told in a sentence
// and drawn as sand, the sandboxes alive right now as little boxes, and
// short lists of what ended and where things run.
export function Overview() {
  const me = useAccount()
  const now = useNow()
  const live = useApi<Sandbox[]>("/v1/sandboxes", 3000)
  const hosts = useApi<Host[]>("/v1/hosts", 5000)
  const runs = useApi<Run[]>("/v1/runs", 5000)
  const usage = useApi<Usage>("/v1/usage", 10000)

  const todays = runs.data?.filter((r) => isToday(r.created_at, now))
  const unfinished = todays?.filter((r) => r.ended_at && (r.end_reason ?? "destroyed") !== "destroyed").length ?? 0
  const online = hosts.data?.filter((h) => h.online).length ?? 0
  const fresh = runs.data?.length === 0

  return (
    <>
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="text-[13px] text-faint">{today.format(now)}</p>
          <h1 className="mt-1 text-[22px] font-medium tracking-[-0.02em]">
            {me ? `${greeting(now)}, ${firstName(me.name, me.login)}.` : " "}
          </h1>
        </div>
        <Button asChild variant="ghost" className="text-muted-foreground">
          <Link href="/dashboard/hosts#add">
            <Plus /> Add a host
          </Link>
        </Button>
      </header>
      <Problem error={live.error ?? hosts.error ?? runs.error ?? usage.error} />

      {fresh && hosts.data ? (
        <section className="mt-10 rounded-xl border border-border/70">
          <FirstSandbox hasHost={hosts.data.length > 0} />
        </section>
      ) : (
        <>
          <section className="mt-10">
            <p className="min-h-[28px] max-w-[760px] text-[17px] leading-[28px] text-muted-foreground">
              {live.data && hosts.data && todays && usage.data && (
                <>
                  {live.data.length ? (
                    <>
                      <B>{plural(live.data.length, "sandbox", "sandboxes")}</B> running now
                    </>
                  ) : (
                    "Nothing is running right now"
                  )}
                  , on <B>{online}</B> of {plural(hosts.data.length, "host")}. Today, <B>{todays.length}</B>{" "}
                  {todays.length === 1 ? "run" : "runs"}
                  {unfinished > 0 && (
                    <>
                      , <span className="text-destructive tabular-nums">{unfinished}</span> that didn’t finish
                    </>
                  )}
                  , and <B>{minutes(usage.data.minutes_today)}</B> sandbox-minutes.
                </>
              )}
            </p>
            <div className="mt-6">{todays && <DayInSand runs={todays} now={now} />}</div>
          </section>

          <section className="mt-14">
            <Heading href="/dashboard/sandboxes" link="All sandboxes">
              Running now
            </Heading>
            {live.data?.length === 0 ? (
              <p className="rounded-xl border border-dashed border-border px-5 py-8 text-center text-muted-foreground">
                Nothing is running. Sandboxes show up here, pouring, while they live.
              </p>
            ) : (
              <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {live.data?.map((s) => (
                  <Tile key={s.id} sandbox={s} now={now} onStop={() => (live.reload(), runs.reload())} />
                ))}
              </ul>
            )}
          </section>

          <div className="mt-14 grid grid-cols-1 gap-14 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
            <section>
              <Heading href="/dashboard/sandboxes" link="History">
                Ended lately
              </Heading>
              <ul>
                {runs.data
                  ?.filter((r) => r.ended_at)
                  .slice(0, 6)
                  .map((r) => (
                    <li
                      key={r.id}
                      className="grid grid-cols-[minmax(0,1fr)_auto_auto] items-baseline gap-x-6 border-t border-border/60 py-3 first:border-t-0"
                    >
                      <span className="truncate" title={r.id}>
                        {sandboxName(r)}
                        <span className="ml-3 text-[13px]">
                          <Result run={r} />
                        </span>
                      </span>
                      <span className="text-right tabular-nums text-muted-foreground">
                        {duration(since(r.created_at, Date.parse(r.ended_at!)))}
                      </span>
                      <span className="w-[84px] text-right text-[13px] tabular-nums text-faint">
                        {ago(r.ended_at!, now)}
                      </span>
                    </li>
                  ))}
              </ul>
            </section>
            <section>
              <Heading href="/dashboard/hosts" link="Manage">
                Hosts
              </Heading>
              {hosts.data?.length === 0 ? (
                <p className="text-muted-foreground">
                  None yet.{" "}
                  <Link href="/dashboard/hosts#add" className="text-sand">
                    Add one
                  </Link>
                  .
                </p>
              ) : (
                <ul>
                  {hosts.data?.map((h) => (
                    <li key={h.id} className="border-t border-border/60 py-3 first:border-t-0">
                      <div className="flex items-center gap-2.5">
                        <HostDot host={h} />
                        <span>{h.name}</span>
                        <span className="ml-auto text-[13px] text-faint">
                          {h.online
                            ? `${h.running} of ${h.capacity || "?"}`
                            : h.last_seen && Date.parse(h.last_seen) > 0
                              ? `offline ${ago(h.last_seen, now)}`
                              : "not connected"}
                        </span>
                      </div>
                      {h.online && <Meter used={h.running} of={h.capacity || 0} className="mt-2.5 pl-4" />}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>
        </>
      )}
    </>
  )
}

function Heading({ children, href, link }: { children: string; href: string; link: string }) {
  return (
    <div className="mb-4 flex items-baseline justify-between gap-4">
      <h2 className="text-[13px] text-faint">{children}</h2>
      <Link href={href} className="text-[13px] text-muted-foreground transition-colors hover:text-sand">
        {link} →
      </Link>
    </div>
  )
}

// Tile is a running sandbox as what it is: a box, pouring.
function Tile({ sandbox: s, now, onStop }: { sandbox: Sandbox; now: number; onStop: () => void }) {
  return (
    <li className="group/tile overflow-hidden rounded-xl border border-border/70 bg-card/60 transition-colors duration-200 hover:border-border">
      <div className="relative h-20 border-b border-border/60 bg-box">
        <SandBox cell={4} mode="loop" className="absolute inset-0 h-full w-full opacity-80" label={`${sandboxName(s)} is running`} />
        <span className="absolute right-3 top-2.5 text-xs tabular-nums text-faint">
          {duration(since(s.created_at, now))}
        </span>
      </div>
      <div className="flex items-center gap-3 px-4 py-3">
        <div className="min-w-0 flex-1">
          <p className="truncate font-medium" title={s.id}>
            {sandboxName(s)}
          </p>
          <p className="mt-0.5 truncate text-[13px] text-faint">
            {s.host} · {s.owner} ·{" "}
            <span className={s.spec.network === "open" ? "text-sand" : undefined}>{network(s.spec)}</span>
          </p>
        </div>
        <div className="opacity-60 transition-opacity duration-150 group-focus-within/tile:opacity-100 group-hover/tile:opacity-100">
          <Stop run={s} onStopped={onStop} />
        </div>
      </div>
    </li>
  )
}
