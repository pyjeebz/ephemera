"use client"

import { useState } from "react"

import { CopyLine } from "./copy-line"
import { Label } from "./onboarding"

// The command people type, and the ephemera run line that wraps it. The
// npm registry is allowed because the default command needs it.
export function FirstRun() {
  const [cmd, setCmd] = useState("npm ci && npm test")
  const quoted = "'" + cmd.replaceAll("'", `'\\''`) + "'"
  return (
    <>
      <Label>Your command</Label>
      <input
        value={cmd}
        onChange={(e) => setCmd(e.target.value)}
        spellCheck={false}
        aria-label="Your command"
        className="w-full rounded-lg border border-border/70 bg-card px-4 py-3 font-mono text-[13px] text-foreground outline-none transition-colors duration-150 focus:border-sand/60"
      />
      <div className="mt-7">
        <Label>Then run, in that repo</Label>
        <CopyLine command={`ephemera run --allow registry.npmjs.org -- sh -c ${quoted}`} />
        <p className="mt-3 text-[13px] text-faint">
          <code className="font-mono text-muted-foreground">--allow</code> lets the package registry through. Every
          other host stays blocked, and every attempt is logged.
        </p>
      </div>
    </>
  )
}
