import type { Metadata } from "next"
import { Suspense } from "react"

import { Device } from "@/components/app/device"

export const metadata: Metadata = { title: "Approve the CLI" }

export default function Page() {
  return (
    <Suspense>
      <Device />
    </Suspense>
  )
}
