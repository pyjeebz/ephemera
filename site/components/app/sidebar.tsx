"use client"

import { BookOpen, Box, ChevronsLeft, ChevronsRight, KeyRound, LayoutGrid, LogOut, Server } from "lucide-react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useState, useSyncExternalStore } from "react"

import { PourMark } from "@/components/site/icons"
import { api, type Host, type Sandbox } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { firstName } from "@/lib/format"
import { DOCS } from "@/lib/site"
import { cn } from "@/lib/utils"
import { useAccount } from "./account"

export const NAV = [
  ["Overview", "/dashboard", LayoutGrid],
  ["Sandboxes", "/dashboard/sandboxes", Box],
  ["Hosts", "/dashboard/hosts", Server],
  ["Tokens", "/dashboard/tokens", KeyRound],
] as const

export type Section = (typeof NAV)[number][0]

const FOLDED = "ephemera:sidebar-folded"

// Whether the sidebar is folded lives in this browser's storage, read the
// way React reads any outside value, so the server renders it unfolded.
function readFolded() {
  try {
    return localStorage.getItem(FOLDED) === "1"
  } catch {
    return false
  }
}
function onFold(change: () => void) {
  window.addEventListener(FOLDED, change)
  window.addEventListener("storage", change)
  return () => {
    window.removeEventListener(FOLDED, change)
    window.removeEventListener("storage", change)
  }
}
function setFolded(v: boolean) {
  try {
    localStorage.setItem(FOLDED, v ? "1" : "0")
  } catch {}
  window.dispatchEvent(new Event(FOLDED))
}

// Sidebar is the dashboard's left rail: the sections, with how many
// sandboxes run and hosts there are, and who is signed in. It folds to
// icons, and stays folded on narrow screens.
export function Sidebar({ active }: { active: Section }) {
  const folded = useSyncExternalStore(onFold, readFolded, () => false)
  const fold = setFolded
  const live = useApi<Sandbox[]>("/v1/sandboxes", 10000)
  const hosts = useApi<Host[]>("/v1/hosts", 10000)
  const counts: Partial<Record<Section, number | undefined>> = {
    Sandboxes: live.data?.length,
    Hosts: hosts.data?.length,
  }

  return (
    <aside
      data-folded={folded}
      className="group/side sticky top-0 flex h-dvh w-16 shrink-0 flex-col border-r border-border md:w-60 md:data-[folded=true]:w-16"
    >
      <div className="flex h-16 items-center justify-center gap-2 md:justify-between md:px-5 md:group-data-[folded=true]/side:justify-center md:group-data-[folded=true]/side:px-0">
        <Link
          href="/"
          aria-label="ephemera home"
          className="pour-host flex items-center md:group-data-[folded=true]/side:hidden"
        >
          <PourMark size={26} />
        </Link>
        <button
          type="button"
          onClick={() => fold(!folded)}
          aria-label={folded ? "Unfold the sidebar" : "Fold the sidebar"}
          className="hidden rounded-md p-1.5 text-faint transition-colors hover:bg-raised/50 hover:text-foreground md:block"
        >
          {folded ? <ChevronsRight className="size-4" /> : <ChevronsLeft className="size-4" />}
        </button>
      </div>

      <nav className="flex flex-1 flex-col gap-0.5 px-3 pt-4">
        <Label>Account</Label>
        {NAV.map(([label, href, Icon]) => (
          <Item key={label} href={href} icon={<Icon />} active={label === active} count={counts[label]}>
            {label}
          </Item>
        ))}
        <Label className="mt-6">Resources</Label>
        <Item href={DOCS} icon={<BookOpen />}>
          Docs
        </Item>
      </nav>

      <Me />
    </aside>
  )
}

function Label({ children, className }: { children: string; className?: string }) {
  return (
    <p className={cn("hidden px-2.5 pb-1.5 text-[13px] text-faint md:group-data-[folded=false]/side:block", className)}>
      {children}
    </p>
  )
}

function Item({
  href,
  icon,
  active,
  count,
  children,
}: {
  href: string
  icon: React.ReactNode
  active?: boolean
  count?: number
  children: string
}) {
  const external = href.startsWith("http")
  const cls = cn(
    "flex h-10 items-center gap-3 rounded-md px-2.5 text-[15px] transition-colors duration-150 [&_svg]:size-[18px] [&_svg]:shrink-0",
    active ? "bg-raised text-foreground" : "text-muted-foreground hover:bg-raised/50 hover:text-foreground",
  )
  const body = (
    <>
      {icon}
      <span className="hidden flex-1 md:group-data-[folded=false]/side:inline">{children}</span>
      {count !== undefined && count > 0 && (
        <span className="hidden text-[13px] tabular-nums text-faint md:group-data-[folded=false]/side:inline">
          {count}
        </span>
      )}
    </>
  )
  return external ? (
    <a href={href} className={cls} title={children}>
      {body}
    </a>
  ) : (
    <Link href={href} aria-current={active ? "page" : undefined} className={cls} title={children}>
      {body}
    </Link>
  )
}

// Who is signed in, and the way out.
function Me() {
  const me = useAccount()
  const router = useRouter()
  const [leaving, setLeaving] = useState(false)
  const out = async () => {
    setLeaving(true)
    await api("/auth/logout", { method: "POST" }).catch(() => {})
    router.replace("/")
  }
  return (
    <div className="flex items-center gap-3 border-t border-border px-4 py-4">
      <span className="hidden size-8 shrink-0 items-center justify-center overflow-hidden rounded-full bg-raised text-xs font-medium md:group-data-[folded=false]/side:flex">
        {me?.avatar_url ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={me.avatar_url} alt="" className="size-full object-cover" />
        ) : (
          (me?.name ?? me?.login ?? "")[0]
        )}
      </span>
      <span className="hidden min-w-0 flex-1 leading-tight md:group-data-[folded=false]/side:block">
        <span className="block truncate text-sm font-medium">{me ? firstName(me.name, me.login) : " "}</span>
        <span className="block truncate text-[13px] text-faint">{me?.login}</span>
      </span>
      <button
        type="button"
        disabled={leaving || !me}
        onClick={out}
        aria-label="Sign out"
        title="Sign out"
        className="rounded-md p-1 text-faint transition-colors hover:text-foreground"
      >
        <LogOut className="size-4" />
      </button>
    </div>
  )
}
