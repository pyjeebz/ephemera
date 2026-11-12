import type { Metadata } from "next"
import Link from "next/link"

import { CopyLine } from "@/components/app/copy-line"
import { Onboarding, Waiting } from "@/components/app/onboarding"
import { SandBox } from "@/components/app/sand-box"
import { Button } from "@/components/ui/button"

export const metadata: Metadata = { title: "Connect your terminal" }

export default function Connect() {
  return (
    <Onboarding
      step="connect"
      progress={0.4}
      title="Connect your terminal."
      rest="One command, one click."
      lede="Run this in your terminal. Your browser opens a page that asks you to approve it, and the CLI is signed in. No token to copy."
    >
      <CopyLine command="ephemera login" />
      <div className="mt-3">
        <Waiting
          title="Waiting for your terminal…"
          box={<SandBox cols={12} rows={8} mode="trickle" className="h-14 w-20" />}
        >
          This page moves on by itself once you approve.
        </Waiting>
      </div>
      <p className="mt-3 text-[13px] text-muted-foreground">
        No browser on that machine?{" "}
        <Link href="/dashboard/tokens" className="text-foreground underline underline-offset-2">
          Make a token instead
        </Link>
      </p>

      <p className="mb-2.5 mt-12 font-mono text-xs text-faint">once approved</p>
      <div className="flex items-center gap-4 border border-border bg-card px-4 py-3">
        <span aria-hidden className="size-1.5 bg-sand" />
        <p className="flex-1 text-sm">
          <span className="font-medium">Connected:</span>{" "}
          <span className="font-mono text-[13px] text-muted-foreground">jeebz-laptop · linux/amd64</span>
        </p>
        <Button asChild>
          <Link href="/onboarding/host">Next: add a host</Link>
        </Button>
      </div>
    </Onboarding>
  )
}
