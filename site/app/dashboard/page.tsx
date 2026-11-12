import type { Metadata } from "next"

import {
  Capacity,
  Dashboard,
  HostDot,
  LiveBox,
  MoreLink,
  Network,
  PageTitle,
  Result,
  Section,
  td,
  th,
} from "@/components/app/dash"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { ACCOUNT, HOSTS, LIVE, RUNS, TODAY } from "@/lib/sample"

export const metadata: Metadata = { title: "Overview" }

export default function Overview() {
  const stats = [
    [String(LIVE.length), "running now"],
    [String(TODAY.runs), "runs today"],
    [String(TODAY.minutes), "sandbox-minutes today"],
    [String(TODAY.blocked), "connections blocked today"],
  ] as const
  return (
    <Dashboard active="Overview">
      <PageTitle
        title={`Evening, ${ACCOUNT.name}.`}
        rest={`${LIVE.length} running, ${TODAY.runs - LIVE.length} already gone.`}
      />

      <dl className="mt-10 grid grid-cols-2 border-y border-border md:grid-cols-4">
        {stats.map(([n, label], i) => (
          <div
            key={label}
            className={cn(
              "border-border py-5 pr-5",
              i % 2 === 1 && "border-l pl-5",
              i > 0 && "md:border-l md:pl-6",
              i >= 2 && "border-t md:border-t-0",
            )}
          >
            <dt className="sr-only">{label}</dt>
            <dd>
              <span
                className={`block text-[40px] font-bold leading-none tracking-[-0.03em] ${i === 3 ? "text-sand" : ""}`}
              >
                {n}
              </span>
              <span className="mt-2 block text-[13px] text-muted-foreground">{label}</span>
            </dd>
          </div>
        ))}
      </dl>

      <div className="mt-14 grid grid-cols-1 gap-12 lg:grid-cols-[1fr_340px]">
        <Section title="Running now" aside={`${LIVE.length} sandboxes`}>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border border-border text-sm">
              <thead>
                <tr>
                  <th className={th} />
                  <th className={th}>Sandbox</th>
                  <th className={th}>Host</th>
                  <th className={th}>Token</th>
                  <th className={th}>Running</th>
                  <th className={th} />
                </tr>
              </thead>
              <tbody>
                {LIVE.map((s) => (
                  <tr key={s.id}>
                    <td className={`${td} w-14`}>
                      <LiveBox />
                    </td>
                    <td className={`${td} font-mono text-[13px]`}>{s.id}</td>
                    <td className={td}>{s.host}</td>
                    <td className={`${td} text-muted-foreground`}>{s.token}</td>
                    <td className={`${td} font-mono text-[13px] text-muted-foreground`}>{s.age}</td>
                    <td className={`${td} text-right`}>
                      <Button variant="ghost" size="sm" className="text-muted-foreground">
                        Stop
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>

        <Section title="Hosts" aside={<MoreLink href="/dashboard/hosts">Add a host</MoreLink>}>
          <ul className="border border-border">
            {HOSTS.map((h, i) => (
              <li key={h.id} className={`px-4 py-3.5 ${i > 0 ? "border-t border-border" : ""}`}>
                <div className="flex items-center gap-2.5">
                  <HostDot host={h} />
                  <span className="text-sm font-medium">{h.name}</span>
                  <span className="ml-auto font-mono text-xs text-faint">
                    {h.online ? h.version : `seen ${h.lastSeen}`}
                  </span>
                </div>
                <div className="mt-2.5 pl-4">
                  {h.online ? (
                    <Capacity host={h} />
                  ) : (
                    <span className="text-[13px] text-faint">offline; its sandboxes go elsewhere</span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        </Section>
      </div>

      <Section title="Recent runs" aside={<MoreLink href="/dashboard/runs">All runs</MoreLink>} className="mt-14">
        <div className="overflow-x-auto">
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
              {RUNS.slice(0, 5).map((r) => (
                <tr key={r.id}>
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
              ))}
            </tbody>
          </table>
        </div>
      </Section>
    </Dashboard>
  )
}
