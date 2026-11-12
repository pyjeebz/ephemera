import type { Metadata } from "next"

import { Mayfly } from "@/components/site/icons"
import { Button } from "@/components/ui/button"

export const metadata: Metadata = { title: "Approve the CLI" }

const ROWS = [
  [
    "Device",
    <span key="d" className="font-mono">
      jeebz-laptop · linux/amd64
    </span>,
  ],
  ["Access", "Run sandboxes on your hosts"],
  ["Lasts", "Until you revoke it, under Tokens"],
] as const

// Where `ephemera login` sends the browser: the person checks the code
// matches their terminal, and approves.
export default function Device() {
  return (
    <main className="flex min-h-dvh items-center justify-center px-6 py-16">
      <div className="w-full max-w-[400px] border border-border bg-card p-7">
        <Mayfly width={30} />
        <h1 className="mt-5 text-[22px] font-bold tracking-[-0.02em]">Connect the ephemera CLI</h1>
        <p className="mt-1.5 text-sm leading-[21px] text-muted-foreground">
          A terminal on jeebz-laptop is asking to sign in as you.
        </p>

        <div className="mt-6 border border-border bg-background px-4 py-5 text-center">
          <p className="font-mono text-[28px] font-semibold tracking-[0.18em] text-sand">SAND-7K2Q</p>
          <p className="mt-1.5 text-xs text-muted-foreground">Check this matches the code in your terminal.</p>
        </div>

        <dl className="mt-5">
          {ROWS.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-4 border-b border-border py-2.5 text-[13px]">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="text-right">{v}</dd>
            </div>
          ))}
        </dl>

        <div className="mt-6 grid grid-cols-[1fr_2fr] gap-2.5">
          <Button variant="outline" size="xl">
            Cancel
          </Button>
          <Button size="xl">Approve</Button>
        </div>
        <p className="mt-4 text-xs leading-[18px] text-faint">
          Didn’t run ephemera login? Cancel. Nobody can use this code without your approval, and it stops working in 10
          minutes.
        </p>
      </div>
    </main>
  )
}
