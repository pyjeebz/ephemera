import { Check } from "lucide-react"
import Link from "next/link"

import { cn } from "@/lib/utils"

export const STEPS = [
  { slug: "install", label: "Install" },
  { slug: "connect", label: "Connect" },
  { slug: "host", label: "Add a host" },
  { slug: "run", label: "First run" },
] as const

export type Step = (typeof STEPS)[number]["slug"]

// The four onboarding steps, quiet: done ones ticked in sand, the current
// one lit, the rest dim.
export function Steps({ current }: { current: Step | "done" }) {
  const at = current === "done" ? STEPS.length : STEPS.findIndex((s) => s.slug === current)
  return (
    <ol className="flex flex-wrap items-center gap-2 text-[13px]">
      {STEPS.map((s, i) => (
        <li key={s.slug} className="flex items-center gap-2">
          {i > 0 && <span aria-hidden className="h-px w-4 bg-border" />}
          <Link
            href={`/onboarding/${s.slug}`}
            aria-current={i === at ? "step" : undefined}
            className={cn(
              "flex items-center gap-1.5 rounded-full px-3 py-1 transition-colors duration-150",
              i === at ? "bg-raised text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {i < at ? (
              <Check aria-label="done" className="size-3.5 text-sand" strokeWidth={2.25} />
            ) : (
              <span aria-hidden className={cn("tabular-nums", i === at ? "text-sand" : "text-faint")}>
                {i + 1}
              </span>
            )}
            {s.label}
          </Link>
        </li>
      ))}
    </ol>
  )
}
