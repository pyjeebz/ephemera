import type { Metadata } from "next"

import { Dashboard } from "@/components/app/dash"
import { Hosts } from "@/components/app/hosts"

export const metadata: Metadata = { title: "Hosts" }

export default function Page() {
  return (
    <Dashboard active="Hosts">
      <Hosts />
    </Dashboard>
  )
}
