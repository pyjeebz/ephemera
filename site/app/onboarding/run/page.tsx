import type { Metadata } from "next"
import Link from "next/link"

import { FirstRun } from "@/components/app/first-run"
import { Onboarding } from "@/components/app/onboarding"
import { RunWait } from "@/components/app/onboarding-live"

export const metadata: Metadata = { title: "First run" }

export default function Run() {
  return (
    <Onboarding
      step="run"
      progress={0.8}
      title="Pour your first sandbox."
      rest="Any command works."
      lede="Run it in a repo you’re working on. Your tracked files go in, the command runs in a fresh microVM on your host, and the box drains when it ends."
    >
      <FirstRun />
      <div className="mt-10">
        <RunWait />
      </div>
      <p className="mt-4 text-[13px] text-faint">
        Something went wrong? <code className="font-mono text-muted-foreground">ephemera doctor</code> says which step
        failed, and how to fix it.{" "}
        <Link href="/onboarding/done" className="text-muted-foreground underline underline-offset-2">
          See what’s next
        </Link>
      </p>
    </Onboarding>
  )
}
