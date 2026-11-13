import type { Metadata } from "next"

import { Dashboard, PageTitle } from "@/components/app/dash"
import { Tokens } from "@/components/app/tokens"

export const metadata: Metadata = { title: "Tokens" }

export default function Page() {
  return (
    <Dashboard active="Tokens">
      <PageTitle title="Tokens." rest="For CI, agents, and any machine that can’t open a browser." />
      <Tokens />
    </Dashboard>
  )
}
