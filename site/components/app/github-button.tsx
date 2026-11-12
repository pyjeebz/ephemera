"use client"

import { useState } from "react"

import { DROP_EVENT } from "@/components/app/tetris-drop"
import { GitHubMark } from "@/components/site/icons"
import { Button } from "@/components/ui/button"

// Continue with GitHub, as the sign-in's own little sandbox: pressing it
// drops the waiting piece, and the page goes to GitHub as it lands. A
// modified click opens GitHub as usual, and people who prefer reduced
// motion go straight there.
const LAND_MS = 950

export function GitHubButton({ href }: { href: string }) {
  const [going, setGoing] = useState(false)
  return (
    <Button asChild size="xl" className="mt-8 w-full gap-2.5 bg-foreground text-background hover:bg-foreground/90">
      <a
        href={href}
        aria-busy={going}
        onClick={(e) => {
          if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
          if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return
          e.preventDefault()
          if (going) return
          setGoing(true)
          window.dispatchEvent(new Event(DROP_EVENT))
          window.setTimeout(() => window.location.assign(href), LAND_MS)
        }}
      >
        <GitHubMark size={18} />
        {going ? "Opening GitHub…" : "Continue with GitHub"}
      </a>
    </Button>
  )
}
