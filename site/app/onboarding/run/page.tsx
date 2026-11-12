import type { Metadata } from "next"
import Link from "next/link"

import { FirstRun } from "@/components/app/first-run"
import { Onboarding, Waiting } from "@/components/app/onboarding"
import { SandBox } from "@/components/app/sand-box"

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
        <Waiting
          title="Waiting for your first sandbox…"
          box={<SandBox cols={14} rows={9} mode="trickle" className="h-16 w-24" />}
        >
          Usually a second after you press enter. This page updates by itself.
        </Waiting>
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
