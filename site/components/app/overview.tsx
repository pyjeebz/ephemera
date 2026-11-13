"use client"

import Link from "next/link"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { api, type Host, type Run, type Sandbox, type Usage } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { duration, firstName, greeting, isToday, since, started } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { useAccount } from "./account"
import {
  Capacity,
  Empty,
  HostDot,
  LiveBox,
  MoreLink,
  Network,
  PageTitle,
  Problem,
  Result,
  Section,
  td,
  th,
} from "./dash"

// minutes shows short days to a tenth, so a few quick runs aren't 0.
function minutes(m: number) {
  return m < 10 ? m.toFixed(1) : String(Math.round(m))
}

export function Overview() {
  const me = useAccount()
  const now = useNow()
  const live = useApi<Sandbox[]>("/v1/sandboxes", 3000)
  const hosts = useApi<Host[]>("/v1/hosts", 5000)
  const runs = useApi<Run[]>("/v1/runs", 5000)
  const usage = useApi<Usage>("/v1/usage", 10000)

  const running = live.data?.length
  const today = runs.data?.filter((r) => isToday(r.created_at, now))
  // One page of runs is 100; past that, today has at least that many.
  const todayCount = today && (today.length === runs.data?.length && today.length >= 100 ? "100+" : today.length)
  const gone = today?.filter((r) => r.ended_at).length
  const online = hosts.data?.filter((h) => h.online).length

  const stats = [
    [running, "running now"],
    [todayCount, "runs today"],
    [usage.data && minutes(usage.data.minutes_today), "sandbox-minutes today (UTC)"],
    [hosts.data && `${online} of ${hosts.data.length}`, "hosts online"],
  ] as const

  return (
    <>
      <PageTitle
        title={me ? `${greeting(now)}, ${firstName(me.name, me.login)}.` : " "}
        rest={
          running !== undefined && gone !== undefined ? `${running} running, ${gone} already gone today.` : undefined
        }
      />
      <Problem error={live.error ?? hosts.error ?? runs.error ?? usage.error} />

      <dl className="mt-10 grid grid-cols-2 border-y border-border md:grid-cols-4">
        {stats.map(([n, label], i) => (
          <div
            key={label}
            className={cn(
              "border-border py-5 pr-5",
              i % 2 === 1 && "border-l pl-5",
              i > 0 && "md:border-l md:pl-6",
              i >= 2 && "border-t md:border-t-0",
            )}
          >
            <dt className="sr-only">{label}</dt>
            <dd>
              <span className="block text-[40px] font-bold leading-none tracking-[-0.03em]">{n ?? "·"}</span>
              <span className="mt-2 block text-[13px] text-muted-foreground">{label}</span>
            </dd>
          </div>
        ))}
      </dl>

      {hosts.data?.length === 0 && (
        <div className="mt-10 flex flex-wrap items-center justify-between gap-4 border border-sand/40 bg-card px-5 py-4">
          <p className="text-sm">
            <span className="font-medium">No hosts yet.</span>{" "}
            <span className="text-muted-foreground">Sandboxes run on your own machines. Add one to get going.</span>
          </p>
          <Button asChild>
            <Link href="/onboarding/install">Set up</Link>
          </Button>
        </div>
      )}

      <div className="mt-14 grid grid-cols-1 gap-12 lg:grid-cols-[1fr_340px]">
        <Section
          title="Running now"
          aside={running !== undefined && `${running} ${running === 1 ? "sandbox" : "sandboxes"}`}
        >
          {live.data?.length === 0 ? (
            <Empty>Nothing is running. Sandboxes show up here while they live.</Empty>
          ) : (
            <LiveTable sandboxes={live.data ?? []} now={now} onStop={live.reload} />
          )}
        </Section>

        <Section title="Hosts" aside={<MoreLink href="/dashboard/hosts">Add a host</MoreLink>}>
          {hosts.data?.length === 0 ? (
            <Empty>None joined yet.</Empty>
          ) : (
            <ul className="border border-border">
              {hosts.data?.map((h, i) => (
                <li key={h.id} className={`px-4 py-3.5 ${i > 0 ? "border-t border-border" : ""}`}>
                  <div className="flex items-center gap-2.5">
                    <HostDot host={h} />
                    <span className="shrink-0 text-sm font-medium">{h.name}</span>
                    <span className="ml-auto truncate pl-3 font-mono text-xs text-faint" title={h.version}>
                      {h.online ? h.version : "offline"}
                    </span>
                  </div>
                  <div className="mt-2.5 pl-4">
                    {h.online ? (
                      <Capacity host={h} />
                    ) : (
                      <span className="text-[13px] text-faint">offline; its sandboxes go elsewhere</span>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>
      </div>

      <Section title="Recent runs" aside={<MoreLink href="/dashboard/runs">All runs</MoreLink>} className="mt-14">
        {runs.data?.length === 0 ? (
          <Empty>
            No runs yet. Try <code className="font-mono text-foreground">ephemera run -- echo hello</code> in a repo.
          </Empty>
        ) : (
          <RunTable runs={runs.data?.slice(0, 5) ?? []} now={now} />
        )}
      </Section>
    </>
  )
}

function LiveTable({ sandboxes, now, onStop }: { sandboxes: Sandbox[]; now: number; onStop: () => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] border border-border text-sm">
        <thead>
          <tr>
            <th className={th} />
            <th className={th}>Sandbox</th>
            <th className={th}>Host</th>
            <th className={th}>Token</th>
            <th className={th}>Running</th>
            <th className={th} />
          </tr>
        </thead>
        <tbody>
          {sandboxes.map((s) => (
            <tr key={s.id}>
              <td className={`${td} w-14`}>
                <LiveBox />
              </td>
              <td className={`${td} font-mono text-[13px]`}>{s.id}</td>
              <td className={td}>{s.host}</td>
              <td className={`${td} text-muted-foreground`}>{s.owner}</td>
              <td className={`${td} font-mono text-[13px] text-muted-foreground`}>
                {duration(since(s.created_at, now))}
              </td>
              <td className={`${td} text-right`}>
                <Stop id={s.id} onStopped={onStop} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// Stop asks once more before it destroys a sandbox.
function Stop({ id, onStopped }: { id: string; onStopped: () => void }) {
  const [state, setState] = useState<"idle" | "sure" | "stopping" | string>("idle")
  if (state !== "idle" && state !== "sure" && state !== "stopping") {
    return <span className="text-[13px] text-destructive">{state}</span>
  }
  return (
    <Button
      variant="ghost"
      size="sm"
      disabled={state === "stopping"}
      onBlur={() => state === "sure" && setState("idle")}
      onClick={async () => {
        if (state === "idle") return setState("sure")
        setState("stopping")
        try {
          await api(`/v1/sandboxes/${id}`, { method: "DELETE" })
          onStopped()
        } catch (e) {
          setState(e instanceof Error ? e.message : "couldn’t stop it")
        }
      }}
      className={state === "sure" ? "text-destructive" : "text-muted-foreground"}
    >
      {state === "sure" ? "Stop it?" : state === "stopping" ? "Stopping…" : "Stop"}
    </Button>
  )
}

export function RunTable({
  runs,
  now,
  open,
  onOpen,
  detail,
}: {
  runs: Run[]
  now: number
  open?: string
  onOpen?: (id: string | undefined) => void
  detail?: (r: Run) => React.ReactNode
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] border border-border text-sm">
        <thead>
          <tr>
            <th className={th}>Started</th>
            <th className={th}>Sandbox</th>
            <th className={th}>Lived</th>
            <th className={th}>Ended</th>
            <th className={th}>Host</th>
            <th className={th}>Token</th>
            <th className={th}>Network</th>
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
                className={cn(onOpen && "cursor-pointer transition-colors hover:bg-card", isOpen && "bg-card")}
              >
                <td className={`${td} font-mono text-[13px] text-muted-foreground`}>{started(r.created_at, now)}</td>
                <td className={`${td} font-mono text-[13px]`}>{r.id}</td>
                <td className={`${td} font-mono text-[13px]`}>{duration(lived)}</td>
                <td className={td}>
                  <Result run={r} />
                </td>
                <td className={td}>{r.host}</td>
                <td className={`${td} text-muted-foreground`}>{r.owner}</td>
                <td className={td}>
                  <Network run={r} />
                </td>
              </tr>,
              isOpen && detail && (
                <tr key={r.id + "-detail"} className="bg-card">
                  <td colSpan={7} className="border-t border-border px-4 pb-6 pt-4">
                    {detail(r)}
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
