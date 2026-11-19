import Link from "next/link"

import { Button } from "@/components/ui/button"
import { DOCS, GITHUB, SELF_HOST } from "@/lib/site"
import { GitHubMark, PourMark } from "./icons"

const NAV: [string, string][] = [
  ["Docs", DOCS],
  ["Self-host", SELF_HOST],
]

export function Header() {
  return (
    <header className="header">
      <Link className="logo pour-host" href="/" aria-label="ephemera home">
        <PourMark />
        <span>ephemera</span>
      </Link>
      <nav className="nav">
        {NAV.map(([label, href]) => (
          <a key={label} href={href}>
            {label}
          </a>
        ))}
      </nav>
      <div className="actions">
        <a className="link github" href={GITHUB}>
          <GitHubMark />
          GitHub
        </a>
        <Button asChild>
          <a href="/login">Get started</a>
        </Button>
      </div>
    </header>
  )
}
