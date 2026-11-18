import type { Metadata } from "next"

import { Dashboard } from "@/components/app/dash"
import { Sandboxes } from "@/components/app/sandboxes"

export const metadata: Metadata = { title: "Sandboxes" }

export default function Page() {
  return (
    <Dashboard active="Sandboxes">
      <Sandboxes />
    </Dashboard>
  )
}
