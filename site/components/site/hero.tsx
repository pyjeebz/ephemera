import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { DOCS, GITHUB, MCP } from "@/lib/site"
import { CopyPrompt } from "./copy-prompt"
import { InstallCommand } from "./install-command"

const TITLE = ["Sandboxes", "that", "empty", "themselves"]

// The hero is a sandbox. Elements marked .solid catch the sand that rains
// behind the copy; the headline is split into words so the sand lands on the
// real letters.
export function Hero() {
  return (
    <section className="hero" id="hero">
      <canvas className="hero-sand" id="hero-sand" aria-hidden="true" />
      <div className="hero-copy">
        <Badge asChild variant="outline" className="mb-8 gap-2 bg-card px-2.5 py-0.5 text-xs text-muted-foreground [a]:hover:text-foreground">
          <a href={GITHUB}>
            <span className="size-1.5 bg-sand" aria-hidden="true" />
            Open source
          </a>
        </Badge>
        <h1 className="solid" id="hero-title" data-split="1">
          {TITLE.map((word, i) => (
            <span key={word}>
              {word}
              {i < TITLE.length - 1 ? " " : ""}
            </span>
          ))}
        </h1>
        <p className="lede">
          Run a fleet of agents and test suites side by side, each in a throwaway microVM that can’t touch your laptop, your
          secrets or anything you didn’t allow.
        </p>
        <div className="cta">
          <div className="buttons">
            <Button asChild size="xl" className="solid">
              <a href="/login">Get started</a>
            </Button>
            <Button asChild size="xl" variant="outline" className="solid">
              <a href={DOCS}>Read the docs</a>
            </Button>
          </div>
          <InstallCommand className="solid" />
          <p className="agent-line">
            Or set it up with your agent: <CopyPrompt /> · <a href={MCP}>MCP setup guide</a>
          </p>
        </div>
      </div>
    </section>
  )
}
