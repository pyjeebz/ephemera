import type { Metadata } from "next"
import Link from "next/link"
import { Suspense } from "react"

import { Onboarding } from "@/components/app/onboarding"
import { DoneSummary } from "@/components/app/onboarding-live"
import { Button } from "@/components/ui/button"
import { MCP } from "@/lib/site"

export const metadata: Metadata = { title: "Your first sandbox" }

export default function Done() {
  return (
    <Onboarding
      step="done"
      progress={1}
      title="Your first sandbox is gone."
      rest="That’s the whole loop."
      lede="The microVM and its disk are deleted. The signed record of what ran is on your laptop."
    >
      <Suspense fallback={<div className="h-[260px] rounded-xl border border-border/70 bg-card/60" />}>
        <DoneSummary />
      </Suspense>
      <div className="mt-10 flex flex-wrap gap-3">
        <Button asChild size="xl">
          <Link href="/dashboard">Open the dashboard</Link>
        </Button>
        <Button asChild size="xl" variant="outline">
          <a href={MCP}>Hand it to your agent</a>
        </Button>
      </div>
    </Onboarding>
  )
}
