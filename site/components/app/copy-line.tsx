"use client"

import { Check, Copy } from "lucide-react"

import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// A command to copy, in the terminal style of the landing page.
export function CopyLine({ command, className }: { command: string; className?: string }) {
  const { copied, copy } = useCopy(command)
  return (
    <div
      className={cn(
        "flex items-start gap-3 rounded-lg border border-border/70 bg-card px-4 py-3.5 font-mono text-[13px] leading-5",
        className,
      )}
    >
      <span className="select-none text-faint">$</span>
      <code className="min-w-0 flex-1 break-words text-foreground">{command}</code>
      <button
        type="button"
        onClick={copy}
        aria-label={copied ? "Copied" : "Copy command"}
        className="text-faint transition-colors duration-150 hover:text-foreground active:scale-[0.97]"
      >
        {copied ? <Check className="size-4 text-sand" /> : <Copy className="size-4" />}
      </button>
    </div>
  )
}
