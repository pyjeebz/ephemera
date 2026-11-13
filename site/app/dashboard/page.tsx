import type { Metadata } from "next"

import { Dashboard } from "@/components/app/dash"
import { Overview } from "@/components/app/overview"

export const metadata: Metadata = { title: "Overview" }

export default function Page() {
  return (
    <Dashboard active="Overview">
      <Overview />
    </Dashboard>
  )
}
