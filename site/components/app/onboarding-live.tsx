"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useState } from "react"

import { Button } from "@/components/ui/button"
import { type Host, type Run, type Token } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { duration, firstName, network, sandboxName, since } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { useAccount } from "./account"
import { CopyLine } from "./copy-line"
import { ENDED } from "./dash"
import { installCommand, joinCommand, useJoinCode } from "./join-code"
import { Label, Waiting } from "./onboarding"
import { SandBox } from "./sand-box"

// Welcome says hello by name once it's known.
export function Welcome() {
  const me = useAccount()
  return <>{me ? `Welcome, ${firstName(me.name, me.login)}.` : "Welcome."}</>
}

// useFresh remembers the ids in the first answer, so what shows up after
// this page opened can be told apart, whatever the clocks say.
function useFresh<T extends { id: string }>(list: T[] | undefined) {
  const [before, setBefore] = useState<Set<string>>()
  if (list && !before) setBefore(new Set(list.map((x) => x.id)))
  return before ? (list ?? []).filter((x) => !before.has(x.id)) : []
}

// ConnectWait waits for ephemera login to be approved: a new device token.
export function ConnectWait() {
  const tokens = useApi<Token[]>("/v1/tokens", 2000)
  const device = useFresh(tokens.data).find((t) => t.kind === "device")
  if (!device) {
    return (
      <Waiting
        title="Waiting for your terminal…"
        box={<SandBox cols={12} rows={8} mode="trickle" className="h-14 w-20" />}
      >
        This page moves on by itself once you approve.
      </Waiting>
    )
  }
  return (
    <div className="flex items-center gap-4 border border-border bg-card px-4 py-3">
      <span aria-hidden className="size-1.5 bg-sand" />
      <p className="flex-1 text-sm">
        <span className="font-medium">Connected:</span>{" "}
        <span className="font-mono text-[13px] text-muted-foreground">{device.name}</span>
      </p>
      <Button asChild>
        <Link href="/onboarding/host">Next: add a host</Link>
      </Button>
    </div>
  )
}

// HostStep makes a join code and waits for a new host to connect with it.
export function HostStep() {
  const join = useJoinCode(true)
  const hosts = useApi<Host[]>("/v1/hosts", 3000)
  const fresh = useFresh(hosts.data)
  const host = fresh.find((h) => h.online) ?? fresh[0]
  return (
    <>
      <div className="mt-10">
        <Label>Run this on that machine</Label>
        {join.code && !join.expired ? (
          <CopyLine command={installCommand(join.code)} />
        ) : (
          <div className="border border-border bg-card px-4 py-3 font-mono text-[13px] text-faint">
            {join.error ?? (join.expired ? "That code expired." : "Making a code…")}
          </div>
        )}
        <p className="mt-3 text-[13px] leading-5 text-muted-foreground">
          It installs Firecracker and the host agent, and joins your account with the code at the end. The code works
          once, for 15 minutes.{" "}
          {host ? (
            <span className="font-mono text-faint">used by {host.name}</span>
          ) : (
            join.code && !join.expired && <span className="font-mono text-faint">{join.clock} left</span>
          )}{" "}
          ·{" "}
          <button
            type="button"
            disabled={join.busy}
            onClick={join.make}
            className="text-foreground underline underline-offset-2"
          >
            Make a new one
          </button>
        </p>
        {join.code && !join.expired && (
          <details className="group mt-4 text-[13px] text-muted-foreground">
            <summary className="cursor-pointer list-none text-foreground underline-offset-2 hover:underline">
              Already have ephemera-host?
            </summary>
            <div className="mt-3">
              <CopyLine command={joinCommand(join.code)} />
            </div>
          </details>
        )}
      </div>

      <div className="mt-8">
        {!host ? (
          <Waiting
            title="Waiting for your host…"
            box={<SandBox cols={12} rows={8} mode="trickle" className="h-14 w-20" />}
          >
            Its first start builds a snapshot, about 10 seconds. This page moves on by itself.
          </Waiting>
        ) : !host.online ? (
          <Waiting
            title={`${host.name} joined. Waiting for it to connect…`}
            box={<SandBox cols={12} rows={8} mode="trickle" className="h-14 w-20" />}
          >
            It connects once the host agent starts.
          </Waiting>
        ) : (
          <div className="border border-border bg-card">
            <div className="flex items-center gap-4 px-4 py-3">
              <span aria-hidden className="size-1.5 bg-sand" />
              <p className="flex-1 text-sm">
                <span className="font-medium">{host.name}</span>{" "}
                <span className="font-mono text-[13px] text-muted-foreground">
                  is online{host.version ? ` · ${host.version}` : ""}
                  {host.capacity ? ` · room for ${host.capacity}` : ""}
                </span>
              </p>
              <Button asChild>
                <Link href="/onboarding/run">Next: first run</Link>
              </Button>
            </div>
            <p className="border-t border-border px-4 py-2.5 font-mono text-xs text-faint [overflow-wrap:anywhere]">
              key {host.fingerprint}, the same one the install printed
            </p>
          </div>
        )}
      </div>
    </>
  )
}

// RunWait waits for a new sandbox, and moves on once it's gone.
export function RunWait() {
  const router = useRouter()
  const now = useNow()
  const runs = useApi<Run[]>("/v1/runs", 2000)
  const run = useFresh(runs.data).at(-1)
  useEffect(() => {
    if (run?.ended_at) router.push(`/onboarding/done?id=${encodeURIComponent(run.id)}`)
  }, [run?.ended_at, run?.id, router])
  return (
    <Waiting
      title={run ? `Running on ${run.host}…` : "Waiting for your first sandbox…"}
      box={<SandBox cols={14} rows={9} mode={run ? "loop" : "trickle"} className="h-16 w-24" />}
    >
      {run
        ? `${run.id}, ${duration(since(run.created_at, now))} so far. This page moves on when it ends.`
        : "Usually a second after you press enter. This page updates by itself."}
    </Waiting>
  )
}

// DoneSummary shows the run that just ended, or the newest one.
export function DoneSummary() {
  const params = useSearchParams()
  const id = params.get("id")
  const runs = useApi<Run[]>("/v1/runs")
  const now = useNow()
  const run = runs.data?.find((r) => r.id === id) ?? runs.data?.[0]
  if (!runs.data) return <div className="h-[260px] border border-border bg-card" />
  if (!run) {
    return (
      <p className="border border-border bg-card px-5 py-6 text-[13px] text-muted-foreground">
        No runs yet.{" "}
        <Link href="/onboarding/run" className="text-foreground underline underline-offset-2">
          Go back
        </Link>{" "}
        and pour one.
      </p>
    )
  }
  const lived = since(run.created_at, run.ended_at ? Date.parse(run.ended_at) : now)
  const rows: [string, React.ReactNode][] = [
    [
      "Host",
      <span key="h" className="font-mono">
        {run.host}
      </span>,
    ],
    ["Ended", run.ended_at ? ENDED[run.end_reason ?? "destroyed"][1] : "It’s still running."],
    [
      "Network",
      <span key="n" className="font-mono">
        {network(run.spec)}
      </span>,
    ],
    ["Files, output", "never sent here: they went from your laptop to the host and back"],
  ]
  return (
    <>
      <div className="border border-border bg-card">
        <div className="flex items-end justify-between gap-6 p-5">
          <div>
            <p className="font-mono text-xs text-faint">{run.id} lived</p>
            <p className="mt-1 text-[40px] font-bold leading-none tracking-[-0.03em]">{duration(lived)}</p>
          </div>
          <div className="flex h-16 w-24 items-center justify-center border border-border bg-background">
            <span className="font-mono text-[10px] text-faint">nothing here.</span>
          </div>
        </div>
        <dl>
          {rows.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[120px_1fr] gap-4 border-t border-border px-5 py-3 text-[13px]">
              <dt className="text-muted-foreground">{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <p className="mt-4 text-[13px] leading-5 text-muted-foreground">
        The record of what ran, and what its network tried, is on your laptop. Check it with{" "}
        <code className="font-mono whitespace-normal! text-foreground [overflow-wrap:anywhere]">
          ephemera verify {sandboxName(run)}
        </code>
      </p>
    </>
  )
}
