import { Button } from "@/components/ui/button"
import { BENCHMARKS, CHANGELOG, DOCS, GITHUB, LICENSE, SECURITY, SELF_HOST } from "@/lib/site"
import { InstallCommand } from "./install-command"

const COLUMNS: [string, [string, string][]][] = [
  [
    "Product",
    [
      ["Docs", DOCS],
      ["Self-host", SELF_HOST],
      ["Security", SECURITY],
      ["Benchmarks", BENCHMARKS],
      ["Changelog", CHANGELOG],
    ],
  ],
  [
    "Project",
    [
      ["GitHub", GITHUB],
      ["License", LICENSE],
    ],
  ],
]

// The closing call to action lives in the footer, above the wordmark. The
// wordmark is its own sandbox (lib/engine/sand.js): it fills, holds, drains.
export function Footer() {
  return (
    <footer className="footer">
      <div className="footer-top">
        <div className="footer-cta">
          <h2>Run your next command in a box that won’t stick around</h2>
          <p>
            Install the CLI, connect it to a host, and put <code>ephemera run --</code> in front of the command you trust least.
          </p>
          <div className="buttons">
            <Button asChild size="xl">
              <a href="/login">Get started</a>
            </Button>
            <Button asChild size="xl" variant="outline">
              <a href={SELF_HOST}>Set up a host</a>
            </Button>
          </div>
          <InstallCommand />
        </div>
        <div className="footer-cols">
          {COLUMNS.map(([label, links]) => (
            <div key={label}>
              <span className="label">{label}</span>
              {links.map(([text, href]) => (
                <a key={text} href={href}>
                  {text}
                </a>
              ))}
            </div>
          ))}
        </div>
      </div>
      <div className="pit" id="pit" role="img" aria-label="ephemera, written in sand">
        <canvas id="pit-sand" aria-hidden="true" />
      </div>
    </footer>
  )
}
