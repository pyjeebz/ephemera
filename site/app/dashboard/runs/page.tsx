import type { Metadata } from "next"

import { Dashboard, PageTitle } from "@/components/app/dash"
import { Runs } from "@/components/app/runs"

export const metadata: Metadata = { title: "Runs" }

export default function Page() {
  return (
    <Dashboard active="Runs">
      <PageTitle title="Runs." rest="Newest first." />
      <Runs />
    </Dashboard>
  )
}
