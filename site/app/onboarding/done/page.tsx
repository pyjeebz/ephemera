import type { Metadata } from "next"
import Link from "next/link"

import { Onboarding } from "@/components/app/onboarding"
import { Button } from "@/components/ui/button"
import { MCP } from "@/lib/site"
import { RUNS } from "@/lib/sample"

export const metadata: Metadata = { title: "Your first sandbox" }

export default function Done() {
  const run = RUNS[0]
  const rows: [string, React.ReactNode][] = [
    [
      "Host",
      <span key="h" className="font-mono">
        {run.host}
      </span>,
    ],
    ["Ended", `the command ${run.ended}, exit ${run.exit}`],
    [
      "Network",
      <span key="n" className="font-mono">
        {run.allowed} allowed · {run.blocked} blocked <span className="text-sand">{run.blockedHosts.join(", ")}</span>
      </span>,
    ],
    ["Files, output", "never sent here: they went from your laptop to the host and back"],
  ]
  return (
    <Onboarding
      step="done"
      progress={1}
      title="Your first sandbox is gone."
      rest="That’s the whole loop."
      lede="The microVM and its disk are deleted. The signed record of what ran is on your laptop."
    >
      <div className="border border-border bg-card">
        <div className="flex items-end justify-between gap-6 p-5">
          <div>
            <p className="font-mono text-xs text-faint">{run.id} lived</p>
            <p className="mt-1 text-[40px] font-bold leading-none tracking-[-0.03em]">
              {run.lived}
              <span className="ml-2 font-mono text-sm font-normal tracking-normal text-muted-foreground">
                exit {run.exit}
              </span>
            </p>
          </div>
          <div className="flex h-16 w-24 items-center justify-center border border-border bg-background">
            <span className="font-mono text-[10px] text-faint">nothing here.</span>
          </div>
        </div>
        <dl>
          {rows.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[120px_1fr] gap-4 border-t border-border px-5 py-3 text-[13px]">
              <dt className="text-muted-foreground">{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <p className="mt-4 text-[13px] leading-5 text-muted-foreground">
        Something in it tried to reach <span className="font-mono text-foreground">telemetry.example.com</span>. It was
        blocked, and the attempt is in the record:{" "}
        <code className="font-mono whitespace-normal! text-foreground [overflow-wrap:anywhere]">ephemera verify ~/.config/ephemera/records/{run.id}.json</code>
      </p>
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
