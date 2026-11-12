import type { Metadata } from "next"

import { CopyLine } from "@/components/app/copy-line"
import { Dashboard, PageTitle, Section, td, th } from "@/components/app/dash"
import { Button } from "@/components/ui/button"
import { TOKENS, type Token } from "@/lib/sample"

export const metadata: Metadata = { title: "Tokens" }

function limits(t: Token) {
  const parts = []
  if (t.maxSandboxes) parts.push(`${t.maxSandboxes} at once`)
  if (t.maxMinutesPerDay) parts.push(`${t.maxMinutesPerDay} min a day`)
  return parts.length ? parts.join(" · ") : "no limit"
}

function TokenTable({ tokens }: { tokens: Token[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] table-fixed border border-border text-sm">
        <colgroup>
          <col className="w-[38%]" />
          <col className="w-[24%]" />
          <col className="w-[13%]" />
          <col className="w-[13%]" />
          <col className="w-[12%]" />
        </colgroup>
        <thead>
          <tr>
            <th className={th}>Name</th>
            <th className={th}>Limits</th>
            <th className={th}>Made</th>
            <th className={th}>Last used</th>
            <th className={th} />
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td className={td}>
                <span className="font-medium">{t.name}</span>{" "}
                <span className="ml-2 font-mono text-xs text-faint">{t.id}</span>
              </td>
              <td className={`${td} text-[13px] text-muted-foreground`}>{limits(t)}</td>
              <td className={`${td} text-[13px] text-muted-foreground`}>{t.created}</td>
              <td className={`${td} font-mono text-[13px] text-muted-foreground`}>{t.lastUsed}</td>
              <td className={`${td} text-right`}>
                <Button variant="ghost" size="sm" className="text-muted-foreground hover:text-destructive">
                  Revoke
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const field =
  "w-full border border-border bg-background px-3 py-2.5 text-sm outline-none transition-colors duration-150 focus:border-sand/60"

export default function Tokens() {
  return (
    <Dashboard active="Tokens">
      <PageTitle title="Tokens." rest="For CI, agents, and any machine that can’t open a browser." />

      <section className="mt-10 grid grid-cols-1 gap-8 border border-border bg-card p-6 lg:grid-cols-[1fr_1fr]">
        <form className="space-y-4">
          <h2 className="text-[15px] font-medium">Make a token</h2>
          <label className="block">
            <span className="mb-1.5 block text-xs text-faint">Name</span>
            <input defaultValue="github-actions" className={field} />
          </label>
          <div className="grid grid-cols-2 gap-3">
            <label className="block">
              <span className="mb-1.5 block text-xs text-faint">Sandboxes at once</span>
              <input defaultValue="4" inputMode="numeric" className={field} />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-xs text-faint">Minutes a day</span>
              <input placeholder="no limit" inputMode="numeric" className={field} />
            </label>
          </div>
          <Button type="button">Make token</Button>
        </form>
        <div>
          <p className="text-[13px] text-muted-foreground">
            <span className="text-foreground">github-actions</span> is ready. Copy it now: it’s shown this once, and
            ephemera keeps only a hash.
          </p>
          <CopyLine
            command="export EPHEMERA_TOKEN=eph_Qm9vbGVhbi1zYW5kLWlzLW5vdC1yZWFs"
            className="mt-3 bg-background"
          />
          <p className="mt-3 text-xs leading-[18px] text-faint">
            Set <code className="font-mono">EPHEMERA_SERVER=https://api.ephemera.run</code> next to it, and{" "}
            <code className="font-mono">ephemera run</code> works without a login.
          </p>
        </div>
      </section>

      <Section title="Signed-in devices" aside="from ephemera login" className="mt-12">
        <TokenTable tokens={TOKENS.filter((t) => t.kind === "device")} />
      </Section>
      <Section title="API tokens" className="mt-10">
        <TokenTable tokens={TOKENS.filter((t) => t.kind === "api")} />
      </Section>
    </Dashboard>
  )
}
