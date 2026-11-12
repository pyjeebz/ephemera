"use client"

import { useState } from "react"

import { INSTALL } from "@/lib/site"
import { cn } from "@/lib/utils"
import { CopyLine } from "./copy-line"

const RELEASES = "https://github.com/pyjeebz/ephemera/releases/latest"

// How to install the CLI, by system. No go install: the install script
// and the release downloads are the supported ways.
export function InstallTabs() {
  const [tab, setTab] = useState<"unix" | "windows">("unix")
  return (
    <div className="border border-border">
      <div role="tablist" className="flex gap-1 border-b border-border px-2 font-mono text-xs">
        {(
          [
            ["unix", "macOS & Linux"],
            ["windows", "Windows"],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            role="tab"
            aria-selected={tab === id}
            onClick={() => setTab(id)}
            className={cn(
              "-mb-px border-b px-3 py-2.5 transition-colors duration-150",
              tab === id
                ? "border-sand text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {label}
          </button>
        ))}
      </div>
      {tab === "unix" ? (
        <CopyLine command={INSTALL} className="border-0" />
      ) : (
        <p className="px-4 py-3.5 text-[13px] leading-5 text-muted-foreground">
          Download <span className="font-mono text-foreground">ephemera_&lt;version&gt;_windows_amd64.zip</span> (or{" "}
          <span className="font-mono text-foreground">_arm64</span>) from the{" "}
          <a href={RELEASES} className="text-foreground underline underline-offset-2">
            latest release
          </a>
          , and put <span className="font-mono text-foreground">ephemera.exe</span> on your PATH.
        </p>
      )}
    </div>
  )
}
