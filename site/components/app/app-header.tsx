import Link from "next/link"

import { Mayfly } from "@/components/site/icons"
import { ACCOUNT } from "@/lib/sample"
import { cn } from "@/lib/utils"

const NAV = [
  ["Overview", "/dashboard"],
  ["Runs", "/dashboard/runs"],
  ["Hosts", "/dashboard/hosts"],
  ["Tokens", "/dashboard/tokens"],
] as const

function Logo() {
  return (
    <Link
      href="/"
      className="flex items-center gap-2.5 text-[17px] font-bold tracking-tight text-foreground"
      aria-label="ephemera home"
    >
      <Mayfly />
      <span className="hidden sm:inline">ephemera</span>
    </Link>
  )
}

// The onboarding header: the logo, a thin sand line for progress, and a
// way out.
export function OnboardingHeader({ progress }: { progress: number }) {
  return (
    <header className="relative flex h-14 items-center justify-between border-b border-border px-4 sm:px-8">
      <Logo />
      <Link href="/dashboard" className="text-sm text-muted-foreground transition-colors hover:text-foreground">
        Skip setup
      </Link>
      <span
        aria-hidden
        className="absolute -bottom-px left-0 h-px bg-sand transition-[width] duration-500 ease-out"
        style={{ width: `${Math.round(progress * 100)}%` }}
      />
    </header>
  )
}

// The dashboard header, with the section that's open.
export function DashboardHeader({ active }: { active: (typeof NAV)[number][0] }) {
  return (
    <header className="flex h-14 items-center gap-5 border-b border-border px-4 sm:gap-10 sm:px-8">
      <Logo />
      <nav className="flex h-full items-stretch gap-4 text-sm sm:gap-7">
        {NAV.map(([label, href]) => (
          <Link
            key={label}
            href={href}
            aria-current={label === active ? "page" : undefined}
            className={cn(
              "-mb-px flex items-center border-b transition-colors duration-150",
              label === active
                ? "border-sand text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {label}
          </Link>
        ))}
      </nav>
      <div className="ml-auto flex items-center gap-3 text-sm text-muted-foreground">
        <span className="hidden font-mono text-xs sm:inline">{ACCOUNT.login}</span>
        <span
          aria-hidden
          className="flex size-7 items-center justify-center border border-border bg-card text-xs font-medium text-foreground"
        >
          {ACCOUNT.name[0]}
        </span>
      </div>
    </header>
  )
}
