"use client"

import Link from "next/link"

import { PourMark } from "@/components/site/icons"

export function Logo() {
  return (
    <Link
      href="/"
      className="pour-host flex items-center gap-2.5 text-[17px] font-bold tracking-tight text-foreground"
      aria-label="ephemera home"
    >
      <PourMark size={26} />
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
