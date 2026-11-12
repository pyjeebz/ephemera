import type { Metadata } from "next"
import Link from "next/link"

import { SandBox } from "@/components/app/sand-box"
import { GitHubMark, Mayfly } from "@/components/site/icons"
import { Button } from "@/components/ui/button"
import { API } from "@/lib/sample"

export const metadata: Metadata = { title: "Sign in" }

export default function Login() {
  return (
    <main className="grid min-h-dvh grid-cols-1 md:grid-cols-2">
      <section aria-hidden className="relative hidden overflow-hidden border-r border-border md:block">
        <span className="absolute left-8 top-7 font-mono text-xs text-faint">
          sbx_6abe91d8 · <span className="text-sand">● pour</span>
        </span>
        <SandBox cell={8} mode="loop" className="absolute inset-0 h-full w-full" />
        <p className="absolute inset-x-0 bottom-0 border-t border-border bg-background/90 px-8 py-4 font-mono text-xs text-muted-foreground">
          Every sandbox fills, runs, and drains. Nothing is left in the box.
        </p>
      </section>

      <section className="flex items-center justify-center px-6 py-16">
        <div className="w-full max-w-[360px]">
          <Mayfly width={34} />
          <h1 className="mt-6 text-[28px] font-bold leading-tight tracking-[-0.02em]">Sign in to ephemera</h1>
          <p className="mt-2 text-[15px] leading-[23px] text-muted-foreground">
            Run commands in sandboxes on your own machines. Signing in makes your account.
          </p>

          <Button
            asChild
            size="xl"
            className="mt-8 w-full gap-2.5 bg-foreground text-background hover:bg-foreground/90"
          >
            <a href={`${API}/auth/github`}>
              <GitHubMark size={18} />
              Continue with GitHub
            </a>
          </Button>

          <p className="mt-6 text-[13px] leading-5 text-muted-foreground">
            We ask GitHub only for your id, login, name and avatar, never your repositories. We keep those, your tokens
            as hashes, and when your sandboxes ran. Never your files, output or secrets.
          </p>
          <hr className="my-6 border-border" />
          <p className="text-xs text-faint">
            New here? Read{" "}
            <Link href="/#get-started" className="underline underline-offset-2 hover:text-foreground">
              how it works
            </Link>{" "}
            first.
          </p>
        </div>
      </section>
    </main>
  )
}
