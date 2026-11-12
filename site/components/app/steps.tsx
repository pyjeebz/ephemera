import Link from "next/link"

import { cn } from "@/lib/utils"

export const STEPS = [
  { slug: "install", label: "Install" },
  { slug: "connect", label: "Connect" },
  { slug: "host", label: "Add a host" },
  { slug: "run", label: "First run" },
] as const

export type Step = (typeof STEPS)[number]["slug"]

// The four onboarding steps as small square pills: done, current, to come.
export function Steps({ current }: { current: Step | "done" }) {
  const at = current === "done" ? STEPS.length : STEPS.findIndex((s) => s.slug === current)
  return (
    <ol className="flex flex-wrap items-center gap-2 font-mono text-xs">
      {STEPS.map((s, i) => (
        <li key={s.slug} className="flex items-center gap-2">
          {i > 0 && <span aria-hidden className="h-px w-5 bg-border" />}
          <Link
            href={`/onboarding/${s.slug}`}
            aria-current={i === at ? "step" : undefined}
            className={cn(
              "flex items-center gap-1.5 border px-2.5 py-1 transition-colors duration-150",
              i === at ? "border-sand/60 text-foreground" : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {i === at && <span aria-hidden className="size-1.5 bg-sand" />}
            {i + 1} {s.label}
            {i < at && <span className="text-sand"> ✓</span>}
          </Link>
        </li>
      ))}
    </ol>
  )
}
