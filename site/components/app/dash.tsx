"use client"

import { Search } from "lucide-react"
import Link from "next/link"
import { useEffect, useRef, type CSSProperties, type ReactNode } from "react"

import type { Host, Run } from "@/lib/api"
import { network } from "@/lib/format"
import { cn } from "@/lib/utils"
import { SignedIn } from "./account"
import { Sidebar, type Section } from "./sidebar"
import { SandBox } from "./sand-box"

// The app rounds what the site keeps square: panels and buttons.
const ROUNDED = { "--radius": "0.375rem" } as CSSProperties

export function Dashboard({ active, children }: { active: Section; children: ReactNode }) {
  return (
    <SignedIn>
      <div className="flex min-h-dvh text-sm" style={ROUNDED}>
        <Sidebar active={active} />
        <main className="min-w-0 flex-1 px-4 pb-24 pt-8 sm:px-8 lg:px-14">
          <div className="mx-auto max-w-[1320px]">{children}</div>
        </main>
      </div>
    </SignedIn>
  )
}

// PageHeader names the page, says something about it underneath, and
// holds its main action on the right.
export function PageHeader({ title, sub, action }: { title: string; sub?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-[22px] font-medium leading-8 tracking-[-0.02em]">{title}</h1>
        <p className="mt-1 min-h-[21px] text-muted-foreground">{sub}</p>
      </div>
      {action}
    </div>
  )
}

// pageAction is a page header's button: quiet, like the page around it.
export const pageAction = "bg-transparent text-muted-foreground hover:bg-raised/60 hover:text-foreground"

// A stat's mark: lit when all is well, red when something needs a look,
// dashed when there's nothing to tell yet.
export type Mark = "good" | "bad" | "none"

// Stats is the strip under a page's header: label, number, a few words,
// and a mark, in columns split by rules.
export function Stats({ items }: { items: { label: string; value: ReactNode; note?: ReactNode; mark?: Mark }[] }) {
  return (
    <dl className="mt-8 grid grid-cols-1 gap-y-6 border-b border-border pb-7 sm:grid-cols-2 lg:grid-cols-4">
      {items.map((s, i) => (
        <div
          key={s.label}
          className={cn(
            "flex items-center gap-4 border-border pr-6",
            i % 2 === 1 && "sm:border-l sm:pl-6",
            i > 0 && "lg:border-l lg:pl-6",
          )}
        >
          <div className="min-w-0 flex-1">
            <dt className="text-[15px] text-muted-foreground">{s.label}</dt>
            <dd className="mt-1.5 text-[32px] font-medium leading-9 tracking-[-0.03em] tabular-nums">
              {s.value ?? <span className="text-faint">–</span>}
            </dd>
            <dd className="mt-1 truncate text-[13px] text-muted-foreground">{s.note ?? "\u00a0"}</dd>
          </div>
          {s.mark && <StatMark mark={s.mark} />}
        </div>
      ))}
    </dl>
  )
}

function StatMark({ mark }: { mark: Mark }) {
  if (mark === "none") return <span aria-hidden className="w-12 shrink-0 border-t border-dashed border-faint/60" />
  return <span aria-hidden className={cn("size-2.5 shrink-0", mark === "good" ? "bg-sand" : "bg-destructive")} />
}

// Card is a titled panel: a name and a link across the top, a list under.
export function Card({
  title,
  action,
  children,
  className,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <Panel className={className}>
      <div className="flex h-[54px] items-center justify-between gap-4 border-b border-border px-5">
        <h2 className="text-[15px] font-medium">{title}</h2>
        {action && <div className="text-sm">{action}</div>}
      </div>
      {children}
    </Panel>
  )
}

// CardLink is a Card's action: a quiet sand link.
export function CardLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link href={href} className="text-sand transition-colors hover:text-foreground">
      {children}
    </Link>
  )
}

// Panel holds a page's list. It has no frame: the rows' hairlines are
// enough, so the page stays quiet.
export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return <section className={className}>{children}</section>
}

// PanelBar runs across the top of a panel: a search or tabs on the left,
// filters or an action on the right.
export function PanelBar({ children, end }: { children: ReactNode; end?: ReactNode }) {
  return (
    <div className="flex min-h-[48px] flex-wrap items-stretch gap-x-6 border-b border-border/60">
      <div className="flex min-w-[240px] flex-1 items-stretch">{children}</div>
      {end && <div className="-mr-3 flex items-center gap-1 overflow-x-auto">{end}</div>}
    </div>
  )
}

// SearchField takes the left of a PanelBar. "/" jumps to it from anywhere.
export function SearchField({
  value,
  onChange,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  placeholder: string
}) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (e.key !== "/" || e.metaKey || e.ctrlKey || t.closest("input, textarea, [contenteditable]")) return
      e.preventDefault()
      ref.current?.focus()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  return (
    <label className="flex min-w-0 flex-1 items-center gap-3">
      <Search aria-hidden className="size-4 shrink-0 text-faint" />
      <input
        ref={ref}
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && (onChange(""), e.currentTarget.blur())}
        placeholder={placeholder}
        aria-label={placeholder}
        className="min-w-0 flex-1 bg-transparent py-3.5 text-sm outline-none placeholder:text-faint [&::-webkit-search-cancel-button]:hidden"
      />
      <kbd className="hidden h-5 min-w-5 place-items-center rounded-sm border border-border px-1 font-sans text-[11px] text-faint sm:grid">
        /
      </kbd>
    </label>
  )
}

// Segments are a PanelBar's filters: plain words, the chosen one lit.
export function Segments<T extends string>({
  options,
  value,
  onChange,
}: {
  options: readonly (T | [T, ReactNode])[]
  value: T
  onChange: (v: T) => void
}) {
  return (
    <div role="radiogroup" className="flex items-center">
      {options.map((o) => {
        const [key, label] = Array.isArray(o) ? o : [o, o]
        return (
          <button
            key={key}
            type="button"
            role="radio"
            aria-checked={key === value}
            onClick={() => onChange(key)}
            className={cn(
              "rounded-md px-3 py-1.5 text-sm transition-colors duration-150",
              key === value ? "text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {label}
          </button>
        )
      })}
    </div>
  )
}

// Tabs are a PanelBar's left side when a panel holds two lists.
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: [T, string, number | undefined][]
  value: T
  onChange: (v: T) => void
}) {
  return (
    <div role="tablist" className="flex items-stretch gap-6">
      {tabs.map(([key, label, count]) => (
        <button
          key={key}
          type="button"
          role="tab"
          aria-selected={key === value}
          onClick={() => onChange(key)}
          className={cn(
            "-mb-px flex items-center gap-2 border-b text-sm transition-colors duration-150",
            key === value
              ? "border-foreground text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {label}
          <span className="tabular-nums text-faint">{count ?? ""}</span>
        </button>
      ))}
    </div>
  )
}

// EmptyState fills a panel that has nothing in it yet, with what to do.
export function EmptyState({ title, children, actions }: { title: string; children?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mt-6 flex min-h-[380px] flex-col items-center justify-center rounded-xl border border-dashed border-border px-6 py-16 text-center">
      <SandBox cols={14} rows={9} mode="trickle" className="h-12 w-[72px]" />
      <h2 className="mt-6 text-[22px] font-medium tracking-[-0.02em]">{title}</h2>
      {children && <p className="mt-2 max-w-[400px] text-sm leading-[21px] text-muted-foreground">{children}</p>}
      {actions && <div className="mt-8 flex w-full max-w-[288px] flex-col gap-3">{actions}</div>}
    </div>
  )
}

// Quiet is a short line in place of a list: nothing matched.
export function Quiet({ children }: { children: ReactNode }) {
  return <p className="px-5 py-10 text-center text-sm text-muted-foreground">{children}</p>
}

// How a run ended, in a word, with the ones that didn't finish standing
// out. The CLI destroys a sandbox when its command ends, so "destroyed"
// is the usual end.
export const ENDED = {
  destroyed: ["Ended", "The CLI destroyed it when the command ended, or someone stopped it."],
  limit: ["Out of minutes", "Its token used up the day’s sandbox-minutes."],
  gone: ["Host lost it", "Its host stopped reporting it, so it’s gone."],
  failed: ["Didn’t start", "The host couldn’t make it."],
} as const

export function Result({ run }: { run: Run }) {
  if (!run.ended_at) {
    return (
      <span className="inline-flex items-center gap-2 text-sand">
        <span aria-hidden className="size-1.5 rounded-full bg-sand" />
        Running
      </span>
    )
  }
  const reason = run.end_reason ?? "destroyed"
  const code = run.exit_code
  return (
    <span className={reason === "destroyed" ? "text-muted-foreground" : "text-destructive"}>
      {ENDED[reason][0]}
      {code !== undefined && (
        <span className={code === 0 ? "text-muted-foreground" : "text-destructive"}> · exit {code}</span>
      )}
    </span>
  )
}

export function Network({ run }: { run: Pick<Run, "spec"> }) {
  return (
    <span className={run.spec.network === "open" ? "text-sand" : "text-muted-foreground"}>{network(run.spec)}</span>
  )
}

// A host's state: a lit dot when it's connected.
export function HostDot({ host }: { host: Pick<Host, "online"> }) {
  return (
    <span
      aria-label={host.online ? "online" : "offline"}
      className={cn(
        "inline-block size-1.5 shrink-0 rounded-full",
        host.online ? "bg-sand" : "border border-faint",
      )}
    />
  )
}

export function Capacity({ host }: { host: Host }) {
  return (
    <div className="flex items-center gap-3">
      <Meter used={host.running} of={host.capacity || 0} />
      <span className="tabular-nums text-muted-foreground">
        {host.running} of {host.capacity || "?"}
      </span>
    </div>
  )
}

// Meter is a host's room as a row of grains, the taken ones sand.
export function Meter({ used, of, className }: { used: number; of: number; className?: string }) {
  const n = Math.min(of, 32)
  const lit = of ? Math.round((Math.min(used, of) / of) * n) : 0
  return (
    <div aria-hidden className={cn("flex gap-[3px]", className)}>
      {Array.from({ length: n }, (_, i) => (
        <span key={i} className={cn("size-[5px]", i < lit ? "bg-sand" : "bg-border")} />
      ))}
    </div>
  )
}

// Problem shows that the API couldn't be read, and keeps trying.
export function Problem({ error }: { error?: string }) {
  if (!error) return null
  return (
    <p role="alert" className="mb-6 rounded-lg border border-destructive/40 px-4 py-3 text-[13px] text-destructive">
      Couldn’t reach ephemera: {error}. Trying again.
    </p>
  )
}

// A running sandbox's little box, pouring and draining on its own.
export function LiveBox() {
  return (
    <span className="block overflow-hidden rounded-[3px] border border-border bg-box">
      <SandBox cols={14} rows={9} mode="loop" className="h-6 w-10" />
    </span>
  )
}

// Table cells with one rhythm everywhere.
export const th = "px-3 py-3 text-left text-[13px] font-normal text-faint whitespace-nowrap first:pl-0 last:pr-0"
export const td = "h-[52px] border-t border-border/60 px-3 py-2 align-middle whitespace-nowrap first:pl-0 last:pr-0"
