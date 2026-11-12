import Link from "next/link"
import type { ReactNode } from "react"

import type { Host, Run } from "@/lib/sample"
import { cn } from "@/lib/utils"
import { DashboardHeader } from "./app-header"
import { SandBox } from "./sand-box"

export function Dashboard({
  active,
  children,
}: {
  active: Parameters<typeof DashboardHeader>[0]["active"]
  children: ReactNode
}) {
  return (
    <div className="min-h-dvh">
      <DashboardHeader active={active} />
      <main className="mx-auto w-full max-w-[1200px] px-4 pb-24 pt-12 sm:px-8">{children}</main>
    </div>
  )
}

export function PageTitle({ title, rest, action }: { title: string; rest?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-6">
      <h1 className="text-[30px] font-bold leading-9 tracking-[-0.025em]">
        {title} {rest && <span className="text-muted-foreground">{rest}</span>}
      </h1>
      {action}
    </div>
  )
}

export function Section({
  title,
  aside,
  children,
  className,
}: {
  title: string
  aside?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={className}>
      <div className="mb-3 flex items-baseline justify-between gap-4">
        <h2 className="text-[15px] font-medium">{title}</h2>
        {aside && <div className="text-[13px] text-muted-foreground">{aside}</div>}
      </div>
      {children}
    </section>
  )
}

export function MoreLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link href={href} className="text-muted-foreground transition-colors hover:text-foreground">
      {children} →
    </Link>
  )
}

// How a run ended, in a word, with failures standing out.
export function Result({ run }: { run: Run }) {
  if (run.ended === "exited") {
    return (
      <span className={cn("font-mono text-[13px]", run.exit === 0 ? "text-muted-foreground" : "text-destructive")}>
        exit {run.exit}
      </span>
    )
  }
  const word = {
    destroyed: "stopped",
    limit: "minutes ran out",
    gone: "host lost it",
    expired: "too old",
  }[run.ended]
  return <span className="font-mono text-[13px] text-faint">{word}</span>
}

export function Network({ run }: { run: Pick<Run, "allowed" | "blocked"> }) {
  return (
    <span className="font-mono text-[13px] text-muted-foreground">
      {run.allowed} allowed · <span className={run.blocked ? "text-sand" : undefined}>{run.blocked} blocked</span>
    </span>
  )
}

// A host's state: a lit square when it's connected.
export function HostDot({ host }: { host: Pick<Host, "online"> }) {
  return (
    <span
      aria-label={host.online ? "online" : "offline"}
      className={cn("inline-block size-1.5 shrink-0", host.online ? "bg-sand" : "border border-faint")}
    />
  )
}

export function Capacity({ host }: { host: Host }) {
  return (
    <div className="flex items-center gap-3">
      <div aria-hidden className="h-1 w-20 bg-border">
        <div className="h-full bg-sand" style={{ width: `${(host.running / host.capacity) * 100}%` }} />
      </div>
      <span className="font-mono text-xs text-muted-foreground">
        {host.running} of {host.capacity}
      </span>
    </div>
  )
}

// A running sandbox's little box, pouring and draining on its own.
export function LiveBox() {
  return (
    <span className="block border border-border bg-background">
      <SandBox cols={14} rows={9} mode="loop" className="h-6 w-10" />
    </span>
  )
}

// Table cells with one rhythm everywhere.
export const th = "px-4 py-2.5 text-left text-xs font-normal text-faint"
export const td = "border-t border-border px-4 py-3 align-middle"
