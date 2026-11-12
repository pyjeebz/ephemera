import type { Metadata } from "next"
import Link from "next/link"

import { CopyLine } from "@/components/app/copy-line"
import { Label, Onboarding, Waiting } from "@/components/app/onboarding"
import { SandBox } from "@/components/app/sand-box"
import { Button } from "@/components/ui/button"
import { API } from "@/lib/sample"

export const metadata: Metadata = { title: "Add a host" }

// A sample code, in the real format: ej_ and 26 base32 letters.
const CODE = "ej_k3v7q2mxa5t4wd6yr3bn5hc2pe"

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
      <dl className="grid grid-cols-1 gap-x-8 gap-y-3 border-y border-border py-5 sm:grid-cols-3">
        {NEEDS.map(([what, how]) => (
          <div key={what}>
            <dt className="text-sm font-medium">{what}</dt>
            <dd className="mt-0.5 text-[13px] text-muted-foreground">{how}</dd>
          </div>
        ))}
      </dl>

      <div className="mt-10">
        <Label>Run this on that machine</Label>
        <CopyLine command={`curl -fsSL https://ephemera.run/host | sudo sh -s -- ${CODE}`} />
        <p className="mt-3 text-[13px] leading-5 text-muted-foreground">
          It installs Firecracker and the host agent, and joins your account with the code at the end. The code works
          once, for 15 minutes. <span className="font-mono text-faint">14:32 left</span> ·{" "}
          <button type="button" className="text-foreground underline underline-offset-2">
            Make a new one
          </button>
        </p>
        <details className="group mt-4 text-[13px] text-muted-foreground">
          <summary className="cursor-pointer list-none text-foreground underline-offset-2 hover:underline">
            Already have ephemera-host?
          </summary>
          <div className="mt-3">
            <CopyLine command={`sudo ephemera-host join --server ${API} ${CODE}`} />
          </div>
        </details>
      </div>

      <div className="mt-8">
        <Waiting
          title="Waiting for your host…"
          box={<SandBox cols={12} rows={8} mode="trickle" className="h-14 w-20" />}
        >
          Its first start builds a snapshot, about 10 seconds. This page moves on by itself.
        </Waiting>
      </div>

      <p className="mb-2.5 mt-12 font-mono text-xs text-faint">once it connects</p>
      <div className="border border-border bg-card">
        <div className="flex items-center gap-4 px-4 py-3">
          <span aria-hidden className="size-1.5 bg-sand" />
          <p className="flex-1 text-sm">
            <span className="font-medium">hetzner-fsn1</span>{" "}
            <span className="font-mono text-[13px] text-muted-foreground">is online · v0.2.0 · room for 40</span>
          </p>
          <Button asChild>
            <Link href="/onboarding/run">Next: first run</Link>
          </Button>
        </div>
        <p className="border-t border-border px-4 py-2.5 font-mono text-xs text-faint [overflow-wrap:anywhere]">
          key SHA256:q3Zx9kT0lV2b1yN8hK5mEw7cR4sJ6uA0pD9fG2iL1oQ, the same one the install printed
        </p>
      </div>

      <p className="mt-10 max-w-[520px] text-[13px] leading-5 text-faint">
        Your laptop sends files and reads output on the host’s port 7701, so it needs to reach it: the same network, a
        VPN, or an open port. A relay for hosts you can’t reach is on the way.
      </p>
    </Onboarding>
  )
}
