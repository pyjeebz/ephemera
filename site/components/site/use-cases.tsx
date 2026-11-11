import type { ReactNode } from "react"

const CARDS: { scene: string; tag: string; title: string; body: ReactNode }[] = [
  {
    scene: "agents",
    tag: "01 agents",
    title: "Parallel coding agents",
    body: (
      <>
        Five agents on one repo get five sandboxes. Each fills, finishes and drains on its own. None of them touch your
        laptop’s CPU or your <code>.env</code>, and none fight over port 3000.
      </>
    ),
  },
  {
    scene: "tests",
    tag: "02 tests",
    title: "Tests and builds",
    body: (
      <>
        <code>ephemera run -- npm test</code> runs the suite in a clean VM. The output and exit code come back as if it ran
        locally.
      </>
    ),
  },
  {
    scene: "untrusted",
    tag: "03 untrusted",
    title: "Code you don’t trust",
    body: (
      <>
        Install that package or run the script from the issue. Anything that tries to phone home is blocked, and when the
        command ends, the VM and its disk are deleted.
      </>
    ),
  },
  {
    scene: "ci",
    tag: "04 ci",
    title: "CI that leaves nothing behind",
    body: (
      <>
        Give CI its own token with a daily limit (
        <code className="wrap">
          <span className="nw">ephemera-server</span> token create ci <span className="nw">--max-minutes</span> 600
        </code>
        ). Every job starts in a fresh box, and nothing carries over to the next one.
      </>
    ),
  },
]

// Each box is a small falling-sand grid run by lib/engine/sand.js, acting out
// its card.
export function UseCases() {
  return (
    <section className="section">
      <div className="section-head">
        <h2>One box per job. Then it’s sand again.</h2>
      </div>
      <div className="cards">
        {CARDS.map((c) => (
          <article key={c.scene} className="card">
            <div className="box">
              <canvas data-scene={c.scene} aria-hidden="true" />
              <span className="tag" data-left>
                {c.tag}
              </span>
              <span className="tag right" data-right />
            </div>
            <h3>{c.title}</h3>
            <p>{c.body}</p>
          </article>
        ))}
      </div>
    </section>
  )
}
