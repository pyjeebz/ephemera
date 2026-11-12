import type { Metadata } from "next"
import Link from "next/link"

import { InstallTabs } from "@/components/app/install-tabs"
import { Label, Onboarding } from "@/components/app/onboarding"
import { Button } from "@/components/ui/button"
import { ACCOUNT } from "@/lib/sample"

export const metadata: Metadata = { title: "Install the CLI" }

export default function Install() {
  return (
    <Onboarding
      step="install"
      progress={0.2}
      title={`Welcome, ${ACCOUNT.name}.`}
      rest="Let’s pour your first sandbox."
      lede="A few minutes from here. Your code stays on your laptop; when you run something, only the files git tracks go in."
    >
      <Label>Install the CLI</Label>
      <InstallTabs />
      <p className="mt-3 text-[13px] text-faint">
        Already have it? Check with <code className="font-mono text-muted-foreground">ephemera version</code>.
      </p>
      <div className="mt-10 flex flex-wrap items-center gap-5">
        <Button asChild size="xl">
          <Link href="/onboarding/connect">I’ve installed it</Link>
        </Button>
        <p className="text-[13px] text-muted-foreground">
          Setting up CI or an agent?{" "}
          <Link href="/dashboard/tokens" className="text-foreground underline underline-offset-2">
            Make a token instead
          </Link>
        </p>
      </div>
    </Onboarding>
  )
}
