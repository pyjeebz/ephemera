"use client"

import { useState } from "react"

import { Button } from "@/components/ui/button"
import { API, api, type Token } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { date } from "@/lib/format"
import { CopyLine } from "./copy-line"
import { Empty, Problem, Section, td, th } from "./dash"

function limits(t: Token) {
  const parts = []
  if (t.max_sandboxes) parts.push(`${t.max_sandboxes} at once`)
  if (t.max_minutes_per_day) parts.push(`${t.max_minutes_per_day} min a day`)
  return parts.length ? parts.join(" · ") : "no limit"
}

function TokenTable({ tokens, onRevoked }: { tokens: Token[]; onRevoked: () => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[640px] table-fixed border border-border text-sm">
        <colgroup>
          <col className="w-[44%]" />
          <col className="w-[26%]" />
          <col className="w-[14%]" />
          <col className="w-[16%]" />
        </colgroup>
        <thead>
          <tr>
            <th className={th}>Name</th>
            <th className={th}>Limits</th>
            <th className={th}>Made</th>
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
              <td className={`${td} text-[13px] text-muted-foreground`}>{date(t.created)}</td>
              <td className={`${td} text-right`}>
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
      variant="ghost"
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
      className={state === "sure" ? "text-destructive" : "text-muted-foreground hover:text-destructive"}
    >
      {state === "sure" ? "Revoke it?" : state === "revoking" ? "Revoking…" : "Revoke"}
    </Button>
  )
}

const field =
  "w-full border border-border bg-background px-3 py-2.5 text-sm outline-none transition-colors duration-150 focus:border-sand/60"

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
  return (
    <>
      <Problem error={tokens.error} />
      <section className="mt-10 grid grid-cols-1 gap-8 border border-border bg-card p-6 lg:grid-cols-[1fr_1fr]">
        <form action={make} className="space-y-4">
          <h2 className="text-[15px] font-medium">Make a token</h2>
          <label className="block">
            <span className="mb-1.5 block text-xs text-faint">Name</span>
            <input name="name" required placeholder="github-actions" autoComplete="off" className={field} />
          </label>
          <div className="grid grid-cols-2 gap-3">
            <label className="block">
              <span className="mb-1.5 block text-xs text-faint">Sandboxes at once</span>
              <input name="max_sandboxes" placeholder="no limit" inputMode="numeric" className={field} />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-xs text-faint">Minutes a day</span>
              <input name="max_minutes_per_day" placeholder="no limit" inputMode="numeric" className={field} />
            </label>
          </div>
          <Button type="submit" disabled={busy}>
            {busy ? "Making…" : "Make token"}
          </Button>
          {error && <p className="text-[13px] text-destructive">{error}</p>}
        </form>
        <div>
          {made ? (
            <>
              <p className="text-[13px] text-muted-foreground">
                <span className="text-foreground">{made.name}</span> is ready. Copy it now: it’s shown this once, and
                ephemera keeps only a hash.
              </p>
              <CopyLine command={`export EPHEMERA_TOKEN=${made.secret}`} className="mt-3 bg-background" />
            </>
          ) : (
            <p className="text-[13px] text-muted-foreground">
              A token is shown once, when you make it. ephemera keeps only a hash.
            </p>
          )}
          <p className="mt-3 text-xs leading-[18px] text-faint">
            Set <code className="font-mono">EPHEMERA_SERVER={API}</code> next to it, and{" "}
            <code className="font-mono">ephemera run</code> works without a login.
          </p>
        </div>
      </section>

      <Section title="Signed-in devices" aside="from ephemera login" className="mt-12">
        {devices?.length === 0 ? (
          <Empty>None yet. Run ephemera login on a machine with a browser.</Empty>
        ) : (
          <TokenTable tokens={devices ?? []} onRevoked={tokens.reload} />
        )}
      </Section>
      <Section title="API tokens" className="mt-10">
        {keys?.length === 0 ? <Empty>None yet.</Empty> : <TokenTable tokens={keys ?? []} onRevoked={tokens.reload} />}
      </Section>
    </>
  )
}
