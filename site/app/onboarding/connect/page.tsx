import type { Metadata } from "next"
import Link from "next/link"

import { CopyLine } from "@/components/app/copy-line"
import { Onboarding } from "@/components/app/onboarding"
import { ConnectWait } from "@/components/app/onboarding-live"

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
        <ConnectWait />
      </div>
      <p className="mt-3 text-[13px] text-muted-foreground">
        No browser on that machine?{" "}
        <Link href="/dashboard/tokens" className="text-foreground underline underline-offset-2">
          Make a token instead
        </Link>
      </p>
    </Onboarding>
  )
}
