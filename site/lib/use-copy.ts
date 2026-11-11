"use client"

import { useEffect, useRef, useState } from "react"

// Copies text to the clipboard. copied stays true for a moment after, so the
// button can say so.
export function useCopy(text: string) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => () => clearTimeout(timer.current), [])

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      clearTimeout(timer.current)
      timer.current = setTimeout(() => setCopied(false), 1400)
    } catch {
      // Clipboard refused (an old browser or an embedded view): leave it be.
    }
  }

  return { copied, copy }
}
