"use client"

import { useState } from "react"
import { Check, Copy } from "lucide-react"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { INSTALL } from "@/lib/site"
import { useCopy } from "@/lib/use-copy"

// The install line with a copy button. The tooltip says "Copy" on hover and
// "Copied" for a moment after a click.
export function InstallCommand({ className }: { className?: string }) {
  const [hover, setHover] = useState(false)
  const { copied, copy } = useCopy(INSTALL)

  return (
    <div className={cn("install", className)}>
      <span className="prompt">$</span>
      <code>{INSTALL}</code>
      <Tooltip open={hover || copied} onOpenChange={setHover}>
        <TooltipTrigger asChild>
          <button type="button" className={cn("copy", copied && "copied")} onClick={copy} aria-label="Copy install command">
            {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6}>
          {copied ? "Copied" : "Copy"}
        </TooltipContent>
      </Tooltip>
    </div>
  )
}
