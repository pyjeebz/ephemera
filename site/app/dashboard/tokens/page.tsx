import type { Metadata } from "next"

import { Dashboard } from "@/components/app/dash"
import { Tokens } from "@/components/app/tokens"

export const metadata: Metadata = { title: "Tokens" }

export default function Page() {
  return (
    <Dashboard active="Tokens">
      <Tokens />
    </Dashboard>
  )
}
