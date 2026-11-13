import type { Metadata } from "next"
import Link from "next/link"

import { GitHubButton } from "@/components/app/github-button"
import { TetrisDrop } from "@/components/app/tetris-drop"
import { PourMark } from "@/components/site/icons"
import { API } from "@/lib/api"

export const metadata: Metadata = { title: "Sign in" }

// One stage: the floor runs under the whole page, the plate on the left,
// the sign-in on the right. Below lg, the plate and its drop sit in a
// strip above the form.
export default function Login() {
  return (
    <main className="relative min-h-dvh overflow-hidden">
      <TetrisDrop frame="stage" className="absolute inset-0 hidden h-full w-full lg:block" />
      <div aria-hidden className="relative h-[380px] lg:hidden">
        <TetrisDrop frame="strip" className="absolute inset-0 h-full w-full" />
      </div>

      <section className="relative flex justify-center px-6 pb-16 lg:min-h-dvh lg:items-center lg:justify-end lg:px-0 lg:pb-0 lg:pr-[11vw]">
        {/* A soft dark glow keeps the words readable over the floor; no box. */}
        <div className="relative w-full max-w-[360px] before:absolute before:-inset-16 before:-z-10 before:bg-[radial-gradient(closest-side,var(--ground)_55%,transparent)]">
          <PourMark size={40} />
          <h1 className="mt-6 text-[28px] font-bold leading-tight tracking-[-0.02em]">Sign in to ephemera</h1>
          <p className="mt-2 text-[15px] leading-[23px] text-muted-foreground">
            Run commands in sandboxes on your own machines. Signing in makes your account.
          </p>

          <GitHubButton href={`${API}/auth/github`} />

          <p className="mt-6 text-[13px] leading-5 text-muted-foreground">
            We ask GitHub only for your id, login, name and avatar, never your repositories. We keep those, your tokens
            as hashes, and when your sandboxes ran. Never your files, output or secrets.
          </p>
          <p className="mt-6 text-xs text-faint">
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
