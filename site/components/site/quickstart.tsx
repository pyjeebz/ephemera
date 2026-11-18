"use client"

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react"

import { HOW_IT_WORKS, INSTALL, INSTALL_DOCS, MCP, SELF_HOST, SNAPSHOTS } from "@/lib/site"
import { cn } from "@/lib/utils"

type Line = ["$" | "" | "!" | "#", string]

// What each step types. Output lines use the CLI's real wording; ids, counts
// and paths are examples.
const STEPS: { title: string; body: ReactNode; link: [string, string]; script: Line[] }[] = [
  {
    title: "Install the CLI",
    body: (
      <>
        One command on macOS, Linux or Windows. Then <code>ephemera login</code> connects it to your server with an API token.
        No host yet? <a className="inline" href={SELF_HOST}>Set one up</a> on any Linux machine with KVM.
      </>
    ),
    link: ["Quickstart", INSTALL_DOCS],
    script: [
      ["$", INSTALL],
      ["", "ephemera: installed v0.1.1 (linux-amd64) to /usr/local/bin/ephemera"],
      ["$", "ephemera login --server https://ephemera.acme.dev"],
      ["", "API token:"],
      ["", "Logged in to https://ephemera.acme.dev as laptop (saved in ~/.config/ephemera/config.json)"],
    ],
  },
  {
    title: "Run a command",
    body: (
      <>
        Put <code>ephemera run --</code> in front of anything. Your tracked files go in. Ignored files like{" "}
        <code>.env.local</code> stay home unless you pass <code>--include</code>.
      </>
    ),
    link: ["How files get in", HOW_IT_WORKS],
    script: [
      ["$", "ephemera run -- ls -a"],
      ["", ".gitignore  package.json  package-lock.json  src  test"],
      ["$", "ephemera run --include .env.local -- ls -a"],
      ["", ".env.local  .gitignore  package.json  package-lock.json  src  test"],
    ],
  },
  {
    title: "Save the setup",
    body: (
      <>
        <code>--setup &quot;npm ci&quot;</code> saves the prepared sandbox as a snapshot, so later runs skip the install. The
        snapshot is encrypted with a key that stays on your laptop.
      </>
    ),
    link: ["Setup snapshots", SNAPSHOTS],
    script: [
      ["$", 'ephemera run --setup "npm ci" --allow registry.npmjs.org -- npm test'],
      ["", "ephemera: running setup (no snapshot yet)"],
      ["", "ephemera: saved a project snapshot; next time setup is skipped"],
      ["", "  ✓ 41 tests passed"],
      ["", "ephemera: network: 14 allowed, 1 blocked"],
      ["!", "ephemera:   blocked telemetry.example.com:443 (not on the allow list)"],
      ["", "ephemera: signed record saved to ~/.config/ephemera/records/amber-dune-42-sbx_6abe91d8.json"],
      ["$", 'ephemera run --setup "npm ci" --allow registry.npmjs.org -- npm test'],
      ["", "ephemera: started from the project snapshot; setup skipped"],
      ["", "  ✓ 41 tests passed"],
    ],
  },
  {
    title: "Hand it to your agent",
    body: (
      <>
        <code>claude mcp add ephemera -- ephemera mcp</code>. From then on, your agent’s commands run in sandboxes.
      </>
    ),
    link: ["MCP setup guide", MCP],
    script: [
      ["$", "claude mcp add ephemera -- ephemera mcp"],
      ["#", "# from now on, your agent's commands run in sandboxes"],
    ],
  },
]

const TYPE_MS = 24 // per character, capped so long commands still take about a second
const TYPE_MAX = 1100
const OUT_MS = 110 // between output lines
const HOLD_MS = 3800 // how long a finished step stays before the next opens

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

function TermLine({ line, typed }: { line: Line; typed?: string }) {
  const [kind, text] = line
  if (kind === "$") {
    return (
      <span className="tl cmd">
        <span className="p">$ </span>
        <span className="c">{typed ?? text}</span>
      </span>
    )
  }
  if (kind === "!") {
    const i = text.indexOf("blocked")
    return (
      <span className="tl out">
        {text.slice(0, i)}
        <span className="warn">blocked</span>
        {text.slice(i + 7)}
      </span>
    )
  }
  return <span className={kind === "#" ? "tl note" : "tl out"}>{text}</span>
}

// An accordion of the steps beside a terminal that types out the open one.
// It walks through the steps on its own while on screen, until someone picks one.
export function Quickstart() {
  const [open, setOpen] = useState(-1)
  const [shown, setShown] = useState(0) // lines fully shown
  const [typed, setTyped] = useState("") // the command being typed
  const [done, setDone] = useState(false)
  const picked = useRef(false)
  const visible = useRef(false)
  const root = useRef<HTMLDivElement>(null)

  const reduce = typeof window !== "undefined" && matchMedia("(prefers-reduced-motion: reduce)").matches

  // Type the open step. A new step cancels the old run.
  useEffect(() => {
    if (open < 0) return
    let cancelled = false
    const script = STEPS[open].script
    ;(async () => {
      setDone(false)
      setShown(0)
      setTyped("")
      if (reduce) {
        setShown(script.length)
        setDone(true)
        return
      }
      for (let i = 0; i < script.length; i++) {
        const [kind, text] = script[i]
        if (kind === "$") {
          const step = Math.min(TYPE_MS, TYPE_MAX / text.length)
          for (let n = 1; n <= text.length; n++) {
            if (cancelled) return
            setTyped(text.slice(0, n))
            await wait(step)
          }
          await wait(260)
        } else {
          await wait(OUT_MS)
        }
        if (cancelled) return
        setShown(i + 1)
        setTyped("")
      }
      setDone(true)
    })()
    return () => {
      cancelled = true
    }
  }, [open, reduce])

  // When a step finishes, move on to the next one, unless someone picked.
  useEffect(() => {
    if (!done || picked.current || reduce) return
    const t = setTimeout(() => {
      if (visible.current && !picked.current) setOpen((o) => (o + 1) % STEPS.length)
    }, HOLD_MS)
    return () => clearTimeout(t)
  }, [done, reduce])

  // Start when the section comes into view.
  useEffect(() => {
    const el = root.current
    if (!el) return
    const io = new IntersectionObserver(
      ([e]) => {
        visible.current = e.isIntersecting
        if (e.isIntersecting) setOpen((o) => (o < 0 ? 0 : o))
      },
      { threshold: 0.3 },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [])

  const pick = useCallback((i: number) => {
    picked.current = true
    setOpen(i)
  }, [])

  const script = open >= 0 ? STEPS[open].script : []

  return (
    <div className="qs" ref={root}>
      <ol className="qs-steps">
        {STEPS.map((s, i) => (
          <li key={s.title} className={cn("qs-step", open === i && "open")}>
            <button type="button" className="qs-head" aria-expanded={open === i} aria-controls={`qs-${i}`} onClick={() => pick(i)}>
              <span className="num">{String(i + 1).padStart(2, "0")}</span>
              <span className="qs-title">{s.title}</span>
            </button>
            <div className="qs-body" id={`qs-${i}`}>
              <div>
                <p>{s.body}</p>
                <a className="qs-link" href={s.link[1]}>
                  {s.link[0]}
                </a>
              </div>
            </div>
          </li>
        ))}
      </ol>
      <div className="terminal">
        <div className="terminal-bar">
          <span className="dots" aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          ~/acme
        </div>
        <pre aria-live="polite">
          {script.slice(0, shown).map((l, i) => (
            <TermLine key={`${open}-${i}`} line={l} />
          ))}
          {shown < script.length && script[shown][0] === "$" && typed && <TermLine line={script[shown]} typed={typed} />}
          {done && (
            <span className="tl cmd">
              <span className="p">$ </span>
              <span className="caret"> </span>
            </span>
          )}
        </pre>
      </div>
    </div>
  )
}
