import type { ReactNode } from "react"

import { SignedIn } from "./account"
import { OnboardingHeader } from "./app-header"
import { Steps, type Step } from "./steps"

// The frame of every onboarding step: header with progress, the step
// pills, and a headline that runs on in a dimmer voice.
export function Onboarding({
  step,
  progress,
  title,
  rest,
  lede,
  children,
}: {
  step: Step | "done"
  progress: number
  title: ReactNode
  rest: string
  lede: ReactNode
  children: ReactNode
}) {
  return (
    <SignedIn>
      <div className="min-h-dvh">
        <OnboardingHeader progress={progress} />
        <main className="mx-auto w-full max-w-[600px] px-6 pb-24 pt-20">
          <Steps current={step} />
          <h1 className="mt-10 text-[34px] font-bold leading-[40px] tracking-[-0.025em]">
            {title} <span className="text-muted-foreground">{rest}</span>
          </h1>
          <p className="mt-4 max-w-[520px] text-[15px] leading-[23px] text-muted-foreground">{lede}</p>
          <div className="mt-10">{children}</div>
        </main>
      </div>
    </SignedIn>
  )
}

// A label over a block, small and plain.
export function Label({ children }: { children: ReactNode }) {
  return <p className="mb-2.5 text-[13px] font-medium text-foreground">{children}</p>
}

// What the page waits for, with a box that trickles while it waits.
export function Waiting({ title, children, box }: { title: string; children: ReactNode; box: ReactNode }) {
  return (
    <div role="status" className="flex items-center gap-5 border border-border bg-card p-4">
      <div className="shrink-0 border border-border bg-background">{box}</div>
      <div>
        <p className="text-sm font-medium">{title}</p>
        <p className="mt-1 text-[13px] text-muted-foreground">{children}</p>
      </div>
    </div>
  )
}
