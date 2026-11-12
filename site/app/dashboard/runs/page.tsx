import type { Metadata } from "next"
import { Fragment } from "react"

import { Dashboard, Network, PageTitle, Result, td, th } from "@/components/app/dash"
import { RUNS } from "@/lib/sample"
import { cn } from "@/lib/utils"

export const metadata: Metadata = { title: "Runs" }

const FILTERS = ["All", "Failed", "Something blocked", "laptop", "ci", "claude-code"]

const WHY = {
  exited: "The command finished.",
  destroyed: "Someone stopped it.",
  limit: "Its token used up the day’s sandbox-minutes.",
  gone: "Its host stopped reporting it.",
  expired: "It reached the host’s longest lifetime.",
}

export default function Runs() {
  const open = RUNS.find((r) => r.id === "sbx_e71b0a94")!
  return (
    <Dashboard active="Runs">
      <PageTitle title="Runs." rest="The last 30 days." />
      <div className="mt-8 flex flex-wrap gap-2 text-[13px]">
        {FILTERS.map((f, i) => (
          <button
            key={f}
            type="button"
            className={cn(
              "border px-3 py-1.5 transition-colors duration-150",
              i === 0 ? "border-sand/60 text-foreground" : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {f}
          </button>
        ))}
      </div>

      <div className="mt-6 overflow-x-auto">
        <table className="w-full min-w-[720px] border border-border text-sm">
          <thead>
            <tr>
              <th className={th}>Started</th>
              <th className={th}>Sandbox</th>
              <th className={th}>Lived</th>
              <th className={th}>Result</th>
              <th className={th}>Host</th>
              <th className={th}>Token</th>
              <th className={th}>Network</th>
            </tr>
          </thead>
          <tbody>
            {RUNS.map((r) => (
              <Fragment key={r.id}>
                <tr className={cn("cursor-pointer transition-colors hover:bg-card", r === open && "bg-card")}>
                  <td className={`${td} font-mono text-[13px] text-muted-foreground`}>{r.started}</td>
                  <td className={`${td} font-mono text-[13px]`}>{r.id}</td>
                  <td className={`${td} font-mono text-[13px]`}>{r.lived}</td>
                  <td className={td}>
                    <Result run={r} />
                  </td>
                  <td className={td}>{r.host}</td>
                  <td className={`${td} text-muted-foreground`}>{r.token}</td>
                  <td className={td}>
                    <Network run={r} />
                  </td>
                </tr>
                {r === open && (
                  <tr className="bg-card">
                    <td colSpan={7} className="border-t border-border px-4 pb-6 pt-4">
                      <div className="grid grid-cols-1 gap-8 md:grid-cols-3">
                        <div>
                          <p className="text-xs text-faint">Why it ended</p>
                          <p className="mt-1.5 text-[13px]">
                            {WHY[r.ended]} Exit {r.exit}.
                          </p>
                        </div>
                        <div>
                          <p className="text-xs text-faint">Blocked</p>
                          <ul className="mt-1.5 space-y-1 font-mono text-[13px] text-sand">
                            {r.blockedHosts.map((h) => (
                              <li key={h}>{h}</li>
                            ))}
                          </ul>
                          <p className="mt-2 text-xs text-muted-foreground">
                            Not on its allow list, so they never left the host.
                          </p>
                        </div>
                        <div>
                          <p className="text-xs text-faint">The record</p>
                          <p className="mt-1.5 text-[13px] leading-5 text-muted-foreground">
                            On the machine that ran it, not here. Check it with{" "}
                            <code className="font-mono text-foreground">ephemera verify {r.id}.json</code>
                          </p>
                        </div>
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-4 text-xs text-faint">
        ephemera keeps when each sandbox ran, where, how it ended and what its network did. Never the command, files or
        output.
      </p>
    </Dashboard>
  )
}
