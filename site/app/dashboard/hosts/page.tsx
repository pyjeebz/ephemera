import type { Metadata } from "next"

import { Dashboard, PageTitle } from "@/components/app/dash"
import { Hosts } from "@/components/app/hosts"

export const metadata: Metadata = { title: "Hosts" }

export default function Page() {
  return (
    <Dashboard active="Hosts">
      <PageTitle title="Hosts." rest="Your machines, where sandboxes run." />
      <Hosts />
    </Dashboard>
  )
}
