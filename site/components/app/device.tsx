"use client"

import { useSearchParams } from "next/navigation"
import { useCallback, useEffect, useState } from "react"

import { PourMark } from "@/components/site/icons"
import { Button } from "@/components/ui/button"
import { api, signIn, SignedOut, type DeviceInfo } from "@/lib/api"

type State =
  | { at: "loading" }
  | { at: "ask" } // no code, or a code that matched nothing
  | { at: "check"; info: DeviceInfo }
  | { at: "done"; approved: boolean }

// Where ephemera login sends the browser: the person checks the code
// matches their terminal, and approves.
export function Device() {
  const params = useSearchParams()
  const [code, setCode] = useState(params.get("code") ?? "")
  const [state, setState] = useState<State>({ at: params.get("code") ? "loading" : "ask" })
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  // fetchInfo looks a code up, and sets state only when it answers.
  const fetchInfo = useCallback((c: string) => {
    api<DeviceInfo>(`/v1/device?code=${encodeURIComponent(c)}`)
      .then((info) => {
        setState({ at: "check", info })
        setError(undefined)
      })
      .catch((e) => {
        if (e instanceof SignedOut) return signIn()
        setError(e instanceof Error ? e.message : String(e))
        setState({ at: "ask" })
      })
      .finally(() => setBusy(false))
  }, [])
  const look = (c: string) => {
    setBusy(true)
    fetchInfo(c)
  }
  // The code the page opened with is looked up by itself, once.
  const first = params.get("code")
  useEffect(() => {
    if (first) fetchInfo(first)
  }, [first, fetchInfo])

  const decide = async (approve: boolean) => {
    if (state.at !== "check") return
    setBusy(true)
    try {
      await api("/v1/device/approve", { method: "POST", body: { user_code: state.info.user_code, approve } })
      setState({ at: "done", approved: approve })
    } catch (e) {
      if (e instanceof SignedOut) return signIn()
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center px-6 py-16">
      <div className="w-full max-w-[400px] border border-border bg-card p-7">
        <PourMark size={34} />
        {state.at === "done" ? (
          <>
            <h1 className="mt-5 text-[22px] font-bold tracking-[-0.02em]">
              {state.approved ? "Your terminal is signed in." : "Cancelled."}
            </h1>
            <p className="mt-1.5 text-sm leading-[21px] text-muted-foreground">
              {state.approved
                ? "Go back to it; ephemera login finishes by itself. You can close this tab."
                : "Nothing was signed in. If that was you after all, run ephemera login again."}
            </p>
          </>
        ) : state.at === "check" ? (
          <>
            <h1 className="mt-5 text-[22px] font-bold tracking-[-0.02em]">Connect the ephemera CLI</h1>
            <p className="mt-1.5 text-sm leading-[21px] text-muted-foreground">
              A terminal on <span className="font-mono text-foreground">{state.info.device}</span> is asking to sign in
              as you.
            </p>
            <div className="mt-6 border border-border bg-background px-4 py-5 text-center">
              <p className="font-mono text-[28px] font-semibold tracking-[0.18em] text-sand">{state.info.user_code}</p>
              <p className="mt-1.5 text-xs text-muted-foreground">Check this matches the code in your terminal.</p>
            </div>
            <dl className="mt-5">
              {(
                [
                  [
                    "Device",
                    <span key="d" className="font-mono">
                      {state.info.device}
                    </span>,
                  ],
                  ["Access", "Run sandboxes on your hosts"],
                  ["Lasts", "Until you revoke it, under Tokens"],
                ] as const
              ).map(([k, v]) => (
                <div key={k} className="flex justify-between gap-4 border-b border-border py-2.5 text-[13px]">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="text-right">{v}</dd>
                </div>
              ))}
            </dl>
            <div className="mt-6 grid grid-cols-[1fr_2fr] gap-2.5">
              <Button variant="outline" size="xl" disabled={busy} onClick={() => decide(false)}>
                Cancel
              </Button>
              <Button size="xl" disabled={busy} onClick={() => decide(true)}>
                {busy ? "Approving…" : "Approve"}
              </Button>
            </div>
            {error && <p className="mt-3 text-[13px] text-destructive">{error}</p>}
            <p className="mt-4 text-xs leading-[18px] text-faint">
              Didn’t run ephemera login? Cancel. Nobody can use this code without your approval, and it stops working 10
              minutes after it was made.
            </p>
          </>
        ) : (
          <>
            <h1 className="mt-5 text-[22px] font-bold tracking-[-0.02em]">Connect the ephemera CLI</h1>
            <p className="mt-1.5 text-sm leading-[21px] text-muted-foreground">
              Type the code ephemera login shows in your terminal.
            </p>
            <form
              className="mt-6"
              onSubmit={(e) => {
                e.preventDefault()
                if (code.trim()) look(code.trim())
              }}
            >
              <input
                value={code}
                onChange={(e) => setCode(e.target.value.toUpperCase())}
                placeholder="XXXX-XXXX"
                aria-label="Code"
                autoComplete="off"
                spellCheck={false}
                disabled={state.at === "loading"}
                className="w-full border border-border bg-background px-4 py-4 text-center font-mono text-[24px] tracking-[0.18em] text-sand outline-none transition-colors duration-150 placeholder:text-faint focus:border-sand/60"
              />
              <Button type="submit" size="xl" className="mt-4 w-full" disabled={busy || state.at === "loading"}>
                {busy || state.at === "loading" ? "Checking…" : "Continue"}
              </Button>
            </form>
            {error && <p className="mt-3 text-[13px] text-destructive">{error}</p>}
          </>
        )}
      </div>
    </main>
  )
}
