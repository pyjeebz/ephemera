"use client"

import { useState } from "react"

import { Button } from "@/components/ui/button"
import { api, type Host } from "@/lib/api"
import { useApi } from "@/lib/use-api"
import { ago, date } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { CopyLine } from "./copy-line"
import { Capacity, Empty, HostDot, Problem, Section } from "./dash"
import { installCommand, joinCommand, useJoinCode } from "./join-code"

export function Hosts() {
  const now = useNow()
  const hosts = useApi<Host[]>("/v1/hosts", 5000)
  const join = useJoinCode(false)

  return (
    <>
      <Problem error={hosts.error} />
      <section className="mt-10 border border-sand/40 bg-card p-6">
        <div className="flex flex-wrap items-baseline justify-between gap-4">
          <h2 className="text-[15px] font-medium">Add a host</h2>
          {join.code && (
            <p className="font-mono text-xs text-faint">
              {join.expired ? "code expired" : `code works once · ${join.clock} left`}
            </p>
          )}
        </div>
        <p className="mt-1.5 max-w-[620px] text-[13px] leading-5 text-muted-foreground">
          On any x86_64 Linux machine with KVM. It installs the host agent and joins this account. The host connects out
          to ephemera, so it needs no open ports for us.
        </p>
        {join.code && !join.expired ? (
          <>
            <CopyLine command={installCommand(join.code)} className="mt-4 bg-background" />
            <p className="mt-3 text-[13px] text-muted-foreground">
              Has ephemera-host already?{" "}
              <code className="font-mono whitespace-normal! text-foreground [overflow-wrap:anywhere]">
                {joinCommand(join.code)}
              </code>
            </p>
          </>
        ) : (
          <Button className="mt-4" disabled={join.busy} onClick={join.make}>
            {join.busy ? "Making a code…" : join.expired ? "Make a new code" : "Make a join code"}
          </Button>
        )}
        {join.error && <p className="mt-3 text-[13px] text-destructive">{join.error}</p>}
      </section>

      <Section
        title="Joined"
        aside={hosts.data && `${hosts.data.filter((h) => h.online).length} of ${hosts.data.length} online`}
        className="mt-12"
      >
        {hosts.data?.length === 0 ? (
          <Empty>No hosts yet. Make a join code above and run it on the machine.</Empty>
        ) : (
          <ul className="border border-border">
            {hosts.data?.map((h, i) => (
              <li
                key={h.id}
                className={`grid grid-cols-1 items-center gap-4 px-5 py-4 md:grid-cols-[220px_180px_1fr_auto] ${i > 0 ? "border-t border-border" : ""}`}
              >
                <div>
                  <div className="flex items-center gap-2.5">
                    <HostDot host={h} />
                    <span className="font-medium">{h.name}</span>
                  </div>
                  <p className="mt-1 pl-4 font-mono text-xs text-faint">{h.id}</p>
                </div>
                <div className="text-[13px] text-muted-foreground">
                  {h.online ? (
                    <Capacity host={h} />
                  ) : h.last_seen && Date.parse(h.last_seen) > 0 ? (
                    <>offline, seen {ago(h.last_seen, now)}</>
                  ) : (
                    <>joined, hasn’t connected</>
                  )}
                </div>
                <div className="min-w-0">
                  <p className="truncate font-mono text-xs text-muted-foreground" title={h.fingerprint}>
                    {h.fingerprint}
                  </p>
                  <p className="mt-1 text-xs text-faint">
                    {h.version ? `${h.version} · ` : ""}joined {date(h.joined)}
                  </p>
                </div>
                <Remove host={h} onRemoved={hosts.reload} />
              </li>
            ))}
          </ul>
        )}
        <p className="mt-4 max-w-[680px] text-xs leading-[18px] text-faint">
          The fingerprint is the host’s key; ephemera-host join printed the same one. Removing a host destroys its
          sandboxes and it can’t sign in again until it joins with a new code.
        </p>
      </Section>
    </>
  )
}

// Remove asks once more: a removed host has to join again.
function Remove({ host, onRemoved }: { host: Host; onRemoved: () => void }) {
  const [state, setState] = useState<"idle" | "sure" | "removing">("idle")
  const [error, setError] = useState<string>()
  return (
    <div className="justify-self-start md:justify-self-end">
      <Button
        variant="ghost"
        size="sm"
        disabled={state === "removing"}
        onBlur={() => state === "sure" && setState("idle")}
        onClick={async () => {
          if (state === "idle") return setState("sure")
          setState("removing")
          try {
            await api(`/v1/hosts/${host.id}`, { method: "DELETE" })
            onRemoved()
          } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
            setState("idle")
          }
        }}
        className={state === "sure" ? "text-destructive" : "text-muted-foreground hover:text-destructive"}
      >
        {state === "sure" ? `Remove ${host.name}?` : state === "removing" ? "Removing…" : "Remove"}
      </Button>
      {error && <p className="mt-1 text-xs text-destructive">{error}</p>}
    </div>
  )
}
