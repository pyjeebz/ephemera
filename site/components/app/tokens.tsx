"use client"

import { Plus } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { API, api, type Token } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { ago, date } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { cn } from "@/lib/utils"
import { CopyLine } from "./copy-line"
import { PageHeader, pageAction, Panel, PanelBar, Problem, Quiet, Tabs, td, th } from "./dash"

function limits(t: Token) {
  const parts = []
  if (t.max_sandboxes) parts.push(`${t.max_sandboxes} at once`)
  if (t.max_minutes_per_day) parts.push(`${t.max_minutes_per_day} min a day`)
  return parts.length ? parts.join(" · ") : "no limit"
}

function TokenTable({ tokens, onRevoked }: { tokens: Token[]; onRevoked: () => void }) {
  const now = useNow()
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] table-fixed">
        <colgroup>
          <col className="w-[34%]" />
          <col className="w-[22%]" />
          <col className="w-[14%]" />
          <col className="w-[14%]" />
          <col className="w-[16%]" />
        </colgroup>
        <thead>
          <tr>
            <th className={th}>Name</th>
            <th className={th}>Limits</th>
            <th className={th}>Made</th>
            <th className={th}>Last used</th>
            <th className={th}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td className={td}>
                <span className="block truncate font-medium" title={t.id}>
                  {t.name}
                </span>
              </td>
              <td className={cn(td, "text-muted-foreground")}>{limits(t)}</td>
              <td className={cn(td, "tabular-nums text-muted-foreground")}>{date(t.created)}</td>
              <td className={cn(td, "tabular-nums text-muted-foreground")} title={t.last_used}>
                {t.last_used ? (now ? ago(t.last_used, now) : "") : "never"}
              </td>
              <td className={cn(td, "text-right")}>
                <Revoke token={t} onRevoked={onRevoked} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Revoke({ token, onRevoked }: { token: Token; onRevoked: () => void }) {
  const [state, setState] = useState<"idle" | "sure" | "revoking">("idle")
  return (
    <Button
      variant={state === "sure" ? "destructive" : "ghost"}
      size="sm"
      disabled={state === "revoking"}
      onBlur={() => state === "sure" && setState("idle")}
      onClick={async () => {
        if (state === "idle") return setState("sure")
        setState("revoking")
        try {
          await api(`/v1/tokens/${token.id}`, { method: "DELETE" })
        } finally {
          setState("idle")
          onRevoked()
        }
      }}
      className={state === "idle" ? "text-muted-foreground hover:text-destructive" : undefined}
    >
      {state === "sure" ? "Revoke it?" : state === "revoking" ? "Revoking…" : "Revoke"}
    </Button>
  )
}

const field =
  "w-full rounded-lg border border-border bg-background px-3 py-2.5 text-sm outline-none transition-colors duration-150 placeholder:text-faint focus:border-sand/60"

// A whole number from a form field, or 0 for blank.
function count(v: FormDataEntryValue | null) {
  const n = Number(String(v ?? "").trim() || 0)
  return Number.isInteger(n) && n >= 0 ? n : NaN
}

export function Tokens() {
  const tokens = useApi<Token[]>("/v1/tokens")
  const [made, setMade] = useState<{ name: string; secret: string }>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [making, setMaking] = useState(false)
  const [tab, setTab] = useState<"devices" | "api">("devices")

  const make = async (form: FormData) => {
    const name = String(form.get("name") ?? "").trim()
    const max_sandboxes = count(form.get("max_sandboxes"))
    const max_minutes_per_day = count(form.get("max_minutes_per_day"))
    if (Number.isNaN(max_sandboxes) || Number.isNaN(max_minutes_per_day)) {
      setError("Limits are whole numbers, or blank for none.")
      return
    }
    setBusy(true)
    try {
      const t = await api<Token & { secret: string }>("/v1/tokens", {
        method: "POST",
        body: { name, max_sandboxes, max_minutes_per_day },
      })
      setMade({ name: t.name, secret: t.secret })
      setError(undefined)
      tokens.reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const devices = tokens.data?.filter((t) => t.kind === "device")
  const keys = tokens.data?.filter((t) => t.kind !== "device")
  const list = tab === "devices" ? devices : keys
  return (
    <>
      <Problem error={tokens.error} />
      <PageHeader
        title="Tokens"
        sub="For CI, agents, and any machine that can’t open a browser."
        action={
          <Button size="lg" className={pageAction} onClick={() => (setMaking(true), setTab("api"))}>
            <Plus /> Make a token
          </Button>
        }
      />
      <Panel className="mt-8">
        <PanelBar>
          <Tabs
            tabs={[
              ["devices", "Devices", devices?.length],
              ["api", "API tokens", keys?.length],
            ]}
            value={tab}
            onChange={setTab}
          />
        </PanelBar>
        {making && (
          <div className="mt-6 mb-2 grid grid-cols-1 gap-8 rounded-xl border border-border/70 bg-card/60 px-5 py-5 lg:grid-cols-2">
            <form action={make} className="space-y-4">
              <h2 className="font-medium">Make a token</h2>
              <label className="block">
                <span className="mb-1.5 block text-[13px] text-faint">Name</span>
                <input name="name" required placeholder="github-actions" autoComplete="off" className={field} />
              </label>
              <div className="grid grid-cols-2 gap-3">
                <label className="block">
                  <span className="mb-1.5 block text-[13px] text-faint">Sandboxes at once</span>
                  <input name="max_sandboxes" placeholder="no limit" inputMode="numeric" className={field} />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-[13px] text-faint">Minutes a day</span>
                  <input name="max_minutes_per_day" placeholder="no limit" inputMode="numeric" className={field} />
                </label>
              </div>
              <div className="flex gap-2">
                <Button type="submit" disabled={busy}>
                  {busy ? "Making…" : "Make token"}
                </Button>
                <Button type="button" variant="ghost" onClick={() => (setMaking(false), setMade(undefined))}>
                  {made ? "Done" : "Cancel"}
                </Button>
              </div>
              {error && <p className="text-[13px] text-destructive">{error}</p>}
            </form>
            <div className="lg:pt-[30px]">
              {made ? (
                <>
                  <p className="leading-[21px] text-muted-foreground">
                    <span className="text-foreground">{made.name}</span> is ready. Copy it now: it’s shown this once,
                    and ephemera keeps only a hash.
                  </p>
                  <CopyLine command={`export EPHEMERA_TOKEN=${made.secret}`} className="mt-3 rounded-lg bg-background" />
                </>
              ) : (
                <p className="leading-[21px] text-muted-foreground">
                  A token is shown once, when you make it. ephemera keeps only a hash.
                </p>
              )}
              <p className="mt-3 text-[13px] leading-5 text-faint">
                Set <code className="font-mono text-foreground">EPHEMERA_SERVER={API}</code> next to it, and{" "}
                <code className="font-mono text-foreground">ephemera run</code> works without a login.
              </p>
            </div>
          </div>
        )}
        {!list ? (
          <div className="h-[240px]" />
        ) : list.length === 0 ? (
          <Quiet>
            {tab === "devices" ? (
              <>
                None yet. Run <code className="font-mono text-foreground">ephemera login</code> on a machine with a
                browser.
              </>
            ) : (
              "None yet."
            )}
          </Quiet>
        ) : (
          <TokenTable tokens={list} onRevoked={tokens.reload} />
        )}
      </Panel>
    </>
  )
}
