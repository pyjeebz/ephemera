import type { Metadata } from "next"

import { Onboarding } from "@/components/app/onboarding"
import { HostStep } from "@/components/app/onboarding-live"

export const metadata: Metadata = { title: "Add a host" }

const NEEDS = [
  ["x86_64 Linux", "Ubuntu 24.04 is what we test on"],
  ["KVM", "check with ls /dev/kvm"],
  ["2 GB of disk", "for the guest image, plus what sandboxes write"],
] as const

export default function AddHost() {
  return (
    <Onboarding
      step="host"
      progress={0.6}
      title="Add a host."
      rest="The machine your sandboxes run on."
      lede="A spare PC, a VPS, or a cloud VM with nested virtualization. It connects out to ephemera, so it needs no open ports for us, and your code goes from your laptop straight to it."
    >
      <dl className="grid grid-cols-1 gap-x-8 gap-y-3 border-y border-border/60 py-5 sm:grid-cols-3">
        {NEEDS.map(([what, how]) => (
          <div key={what}>
            <dt className="text-sm font-medium">{what}</dt>
            <dd className="mt-0.5 text-[13px] text-muted-foreground">{how}</dd>
          </div>
        ))}
      </dl>

      <HostStep />

      <p className="mt-10 max-w-[520px] text-[13px] leading-5 text-faint">
        Your laptop sends files and reads output on the host’s port 7701, so it needs to reach it: the same network, a
        VPN, or an open port. A relay for hosts you can’t reach is on the way.
      </p>
    </Onboarding>
  )
}
