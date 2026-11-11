"use client"

import { AGENT_PROMPT } from "@/lib/site"
import { useCopy } from "@/lib/use-copy"

// A link-styled button that copies the agent setup prompt.
export function CopyPrompt() {
  const { copied, copy } = useCopy(AGENT_PROMPT)
  return (
    <button type="button" className="link-button" onClick={copy} aria-live="polite">
      {copied ? "copied" : "copy the prompt"}
    </button>
  )
}
