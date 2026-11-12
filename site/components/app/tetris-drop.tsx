"use client"

import { useEffect, useRef } from "react"

import { cn } from "@/lib/utils"

// A sandbox as a falling block, drawn like a dimensional drawing: an
// isometric tetromino draws itself in, falls along a centerline onto a
// landing plate, runs for a moment, then dissolves into sand that falls
// through the plate. Each loop brings the next piece.
//
// Like the drawings in the features grid, every frame is a pure function
// of time: render(t) sets every attribute from t alone. Strokes draw in
// first and fills follow, so a fill never flashes over unfinished lines.
//
// The scene is one floor that runs under the whole page. "stage" frames all
// of it; "strip" frames just the plate and the drop, for small screens.
// Dispatching DROP_EVENT on window drops the waiting piece now: it moves
// the clock to the start of the fall, so frames stay a pure function of
// time.

const SHAPES: [number, number][][] = [
  // [x, z] cells, z up
  [
    [0, 0],
    [1, 0],
    [2, 0],
    [1, 1],
  ], // T
  [
    [0, 0],
    [1, 0],
    [2, 0],
    [2, 1],
  ], // L
  [
    [0, 0],
    [1, 0],
    [1, 1],
    [2, 1],
  ], // S
  [
    [0, 0],
    [1, 0],
    [2, 0],
    [3, 0],
  ], // I
  [
    [0, 0],
    [1, 0],
    [0, 1],
    [1, 1],
  ], // O
  [
    [0, 0],
    [1, 0],
    [2, 0],
    [0, 1],
  ], // J
  [
    [0, 1],
    [1, 1],
    [1, 0],
    [2, 0],
  ], // Z
]

// Seconds in one loop, and the beats inside it.
const CYCLE = 8.6
const DRAW = [0.1, 1.6] // the piece draws in, up high
const FALL = [1.8, 2.55] // it drops
const LAND = 2.55
const RUN = [2.7, 4.1] // a highlight sweeps its top faces
const DISSOLVE = [4.2, 6.2] // cube by cube, into sand

// Night-desert palette, as on the rest of the site.
const INK = "#EFEDE6"
const DIM = "#8C897F"
const RULE = "#2C2A25"
const SAND = ["#F3DCAE", "#E6C287", "#D9B070", "#C99B5A", "#B98A4D", "#A67840"]
const FACE = { top: "#2A2722", left: "#1E1C19", right: "#161513" }

// The idle loop runs a little slower than the beats above.
const SPEED = 0.85

export const DROP_EVENT = "ephemera:drop"

const NS = "http://www.w3.org/2000/svg"
const S = 44 // one cell
const CX = 470 // the plate's centre, in a 1440 by 900 scene
const PLATE_Y = 560
const DROP = 7 // cells the piece starts above the plate
const VIEW = { stage: "0 0 1440 900", strip: "150 90 640 620" }

const clamp = (v: number) => Math.min(1, Math.max(0, v))
const seg = (t: number, a: number, b: number) => clamp((t - a) / (b - a))
const eio = (p: number) => (p < 0.5 ? 4 * p * p * p : 1 - (-2 * p + 2) ** 3 / 2)
const eo = (p: number) => 1 - (1 - p) ** 3
const ei = (p: number) => p * p

// Isometric projection: x to the right and down, y to the left and down,
// z straight up.
const C30 = Math.cos(Math.PI / 6)
function P(x: number, y: number, z: number, lift = 0): [number, number] {
  return [CX + (x - y) * S * C30, PLATE_Y + (x + y) * S * 0.5 - (z + lift) * S]
}
const path = (pts: [number, number][]) => "M" + pts.map((p) => p[0].toFixed(1) + " " + p[1].toFixed(1)).join("L") + "Z"

// A small seeded random number generator, so every frame of a given loop
// gets the same grains.
function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

type Face = { el: SVGPathElement; cube: number; kind: keyof typeof FACE; at: number }
type Grain = { el: SVGRectElement; cube: number; x: number; y: number; vx: number; delay: number; size: number }

function el<K extends keyof SVGElementTagNameMap>(name: K, attrs: Record<string, string | number>, parent: Element) {
  const e = document.createElementNS(NS, name)
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v))
  parent.appendChild(e)
  return e
}

export function TetrisDrop({ frame = "stage", className }: { frame?: keyof typeof VIEW; className?: string }) {
  const ref = useRef<SVGSVGElement>(null)

  useEffect(() => {
    const svg = ref.current
    if (!svg) return

    // --- The parts that stay: floor, plate and centerline -----------------
    const fixed = el("g", {}, svg)
    // The floor: the same cell grid as the plate, out across the page,
    // fading away from the middle.
    const uid = Math.random().toString(36).slice(2)
    const defs = el("defs", {}, svg)
    const fade = el(
      "radialGradient",
      { id: `fade-${uid}`, cx: CX + 200, cy: PLATE_Y, r: 820, gradientUnits: "userSpaceOnUse" },
      defs,
    )
    el("stop", { offset: 0, "stop-color": "#fff", "stop-opacity": 0.9 }, fade)
    el("stop", { offset: 1, "stop-color": "#fff", "stop-opacity": 0 }, fade)
    const mask = el("mask", { id: `floor-${uid}` }, defs)
    el("rect", { x: -2000, y: -2000, width: 6000, height: 6000, fill: `url(#fade-${uid})` }, mask)
    const floor = el("g", { stroke: RULE, "stroke-width": 0.6, mask: `url(#floor-${uid})` }, fixed)
    for (let i = -24; i <= 24; i++) {
      el("path", { d: `M${P(i, -24, 0).join(" ")}L${P(i, 24, 0).join(" ")}` }, floor)
      el("path", { d: `M${P(-24, i, 0).join(" ")}L${P(24, i, 0).join(" ")}` }, floor)
    }
    // The plate: a 6 by 3 floor, dashed, with a faint cell grid.
    const px0 = -3,
      px1 = 3,
      py0 = -1,
      py1 = 2
    const plate = el(
      "path",
      {
        d: path([P(px0, py0, 0), P(px1, py0, 0), P(px1, py1, 0), P(px0, py1, 0)]),
        fill: "none",
        stroke: DIM,
        "stroke-width": 1,
        "stroke-dasharray": "5 4",
      },
      fixed,
    )
    const grid = el("g", { stroke: RULE, "stroke-width": 0.7 }, fixed)
    for (let x = px0 + 1; x < px1; x++) el("path", { d: `M${P(x, py0, 0).join(" ")}L${P(x, py1, 0).join(" ")}` }, grid)
    for (let y = py0 + 1; y < py1; y++) el("path", { d: `M${P(px0, y, 0).join(" ")}L${P(px1, y, 0).join(" ")}` }, grid)
    // The flash when a piece lands.
    const flash = el(
      "path",
      {
        d: path([P(px0, py0, 0), P(px1, py0, 0), P(px1, py1, 0), P(px0, py1, 0)]),
        fill: "none",
        stroke: SAND[1],
        "stroke-width": 1.6,
        opacity: 0,
      },
      fixed,
    )
    // The drop path, dash-dot like a centerline.
    const [cx, cy] = P(0.5, 0.5, 0)
    const center = el(
      "path",
      {
        d: `M${cx} ${cy - (DROP + 3.4) * S}L${cx} ${cy + 40}`,
        stroke: DIM,
        "stroke-width": 0.6,
        "stroke-dasharray": "14 4 2 4",
        opacity: 0,
      },
      fixed,
    )
    const cross = el(
      "path",
      { d: `M${cx - 10} ${cy}L${cx + 10} ${cy}`, stroke: DIM, "stroke-width": 0.8, opacity: 0 },
      fixed,
    )
    // --- The piece, rebuilt for each loop's shape ------------------------
    // Dust sits behind the piece, so only what kicks out past it shows.
    const dust = el("g", {}, svg)
    const piece = el("g", {}, svg)
    const ghost = el("g", { fill: "none", stroke: DIM, "stroke-width": 0.8, "stroke-dasharray": "3 3" }, svg)
    const grains = el("g", {}, svg)
    let faces: Face[] = []
    let sand: Grain[] = []
    let puffs: Grain[] = []
    let cubes: [number, number][] = []
    let built = -1

    const build = (loop: number) => {
      built = loop
      for (const g of [piece, ghost, dust, grains]) g.replaceChildren()
      const shape = SHAPES[loop % SHAPES.length]
      const w = Math.max(...shape.map((c) => c[0])) + 1
      const off = Math.floor(w / 2) - (w % 2 === 0 ? 1 : 0)
      cubes = shape.map(([x, z]) => [x - off, z])
      const has = (x: number, z: number) => cubes.some((c) => c[0] === x && c[1] === z)
      // Back to front, so nearer faces paint over farther ones.
      const order = cubes.map((c, i) => i).sort((a, b) => cubes[a][0] + cubes[a][1] - (cubes[b][0] + cubes[b][1]))
      faces = []
      let n = 0
      for (const i of order) {
        const [x, z] = cubes[i]
        const y = 0
        const quads: [keyof typeof FACE, [number, number, number][]][] = []
        // Faces against a neighbour are inside the solid; leave them out.
        if (!has(x, z + 1))
          quads.push([
            "top",
            [
              [x, y, z + 1],
              [x + 1, y, z + 1],
              [x + 1, y + 1, z + 1],
              [x, y + 1, z + 1],
            ],
          ])
        if (!has(x + 1, z))
          quads.push([
            "right",
            [
              [x + 1, y, z],
              [x + 1, y + 1, z],
              [x + 1, y + 1, z + 1],
              [x + 1, y, z + 1],
            ],
          ])
        quads.push([
          "left",
          [
            [x, y + 1, z],
            [x + 1, y + 1, z],
            [x + 1, y + 1, z + 1],
            [x, y + 1, z + 1],
          ],
        ])
        for (const [kind, q] of quads) {
          const d = path(q.map(([a, b, c]) => P(a, b, c)))
          const e = el(
            "path",
            {
              d,
              fill: FACE[kind],
              "fill-opacity": 0,
              stroke: INK,
              "stroke-width": 1.3,
              "stroke-linejoin": "round",
              pathLength: 1,
              "stroke-dasharray": "1 2",
            },
            piece,
          )
          faces.push({ el: e, cube: i, kind, at: n++ })
          el("path", { d }, ghost)
        }
      }
      // Grains each cube breaks into, and the dust a landing kicks up.
      const r = rng(loop * 7919 + 17)
      sand = []
      cubes.forEach(([x, z], i) => {
        for (let k = 0; k < 22; k++) {
          const [gx, gy] = P(x + r(), r(), z + r())
          const size = 3 + Math.floor(r() * 3)
          const e = el(
            "rect",
            { width: size, height: size, fill: SAND[Math.floor(r() * SAND.length)], opacity: 0 },
            grains,
          )
          sand.push({ el: e, cube: i, x: gx, y: gy, vx: (r() - 0.5) * 30, delay: r() * 0.35, size })
        }
      })
      // Dust starts where the piece's base meets the plate, along its front
      // edges, and kicks out a little to either side.
      puffs = []
      const base = cubes.filter((c) => c[1] === 0)
      for (let k = 0; k < 16; k++) {
        const [bx] = base[k % base.length]
        const side = k % 2 ? 1 : -1
        const [x, y] = P(bx + r(), 1, 0)
        const e = el("rect", { width: 2, height: 2, fill: SAND[1 + (k % 4)], opacity: 0 }, dust)
        puffs.push({ el: e, cube: 0, x, y, vx: side * (24 + r() * 40), delay: r() * 0.06, size: 2 })
      }
    }

    // Window for each cube's dissolve: the top cells go first.
    const dissolveAt = (i: number) => {
      const z = cubes[i][1]
      const rank = cubes.filter((c) => c[1] > z || (c[1] === z && c[0] < cubes[i][0])).length
      return DISSOLVE[0] + rank * 0.28
    }

    const render = (time: number) => {
      const loop = Math.floor(time / CYCLE)
      const t = time - loop * CYCLE
      if (loop !== built) build(loop)
      const first = loop === 0

      // Plate and centerline draw in once, then stay.
      const base = first ? eo(seg(t, 0, 0.8)) : 1
      floor.setAttribute("opacity", String(base))
      plate.setAttribute("opacity", String(base))
      grid.setAttribute("opacity", String(base * 0.9))
      center.setAttribute("opacity", String(base * 0.55))
      cross.setAttribute("opacity", String(base))

      // The fall: gravity, so ease-in, from DROP cells up to the plate.
      const fall = ei(seg(t, FALL[0], FALL[1]))
      const lift = DROP * (1 - fall)
      // A small give on landing, then still.
      const give = t > LAND && t < LAND + 0.18 ? Math.sin(((t - LAND) / 0.18) * Math.PI) * 3 : 0
      piece.setAttribute("transform", `translate(0 ${(-lift * S + give).toFixed(2)})`)

      for (const f of faces) {
        const a = DRAW[0] + f.at * ((DRAW[1] - DRAW[0] - 0.5) / Math.max(1, faces.length))
        const drawn = eio(seg(t, a, a + 0.5))
        const d0 = dissolveAt(f.cube)
        const gone = eio(seg(t, d0, d0 + 0.55))
        // Strokes draw in, then un-draw from the same end as they dissolve.
        f.el.setAttribute("stroke-dashoffset", String(gone > 0 ? -gone : 1 - drawn))
        const fill = eo(seg(t, a + 0.3, a + 0.8)) * (1 - eo(seg(t, d0, d0 + 0.35)))
        let color = FACE[f.kind]
        // While it runs, a sand highlight passes across the top faces.
        if (f.kind === "top") {
          const x = cubes[f.cube][0]
          const sweep = seg(t, RUN[0], RUN[1]) * 6 - 1.5
          const near = Math.max(0, 1 - Math.abs(sweep - x - 1))
          if (near > 0 && t < DISSOLVE[0]) color = mix(FACE.top, SAND[1], near * 0.55)
        }
        f.el.setAttribute("fill", color)
        f.el.setAttribute("fill-opacity", fill.toFixed(3))
      }

      // The ghost waits on the plate while the piece is up high.
      const ghostOn = eo(seg(t, 0.9, 1.5)) * (1 - eo(seg(t, FALL[1] - 0.2, LAND)))
      ghost.setAttribute("opacity", (ghostOn * 0.7).toFixed(3))

      // Landing: the plate's edge lights up, and dust kicks out sideways.
      flash.setAttribute("opacity", (t > LAND ? 1 - eo(seg(t, LAND, LAND + 0.7)) : 0).toFixed(3))
      for (const g of puffs) {
        const k = t - LAND - g.delay
        const on = k > 0 && k < 0.6
        g.el.setAttribute("opacity", on ? (1 - k / 0.6).toFixed(3) : "0")
        if (on) {
          g.el.setAttribute("x", (g.x + g.vx * eo(k / 0.6)).toFixed(1))
          g.el.setAttribute("y", (g.y - 8 * Math.sin((k / 0.6) * Math.PI)).toFixed(1))
        }
      }

      // Dissolve: each cube's grains appear where it was and fall through
      // the plate, fading as they go.
      for (const g of sand) {
        const k = t - dissolveAt(g.cube) - 0.15 - g.delay
        const on = k > 0 && k < 1.4
        g.el.setAttribute("opacity", on ? (eo(seg(k, 0, 0.12)) * (1 - seg(k, 0.6, 1.4))).toFixed(3) : "0")
        if (on) {
          g.el.setAttribute("x", (g.x + g.vx * k).toFixed(1))
          g.el.setAttribute("y", (g.y + 0.5 * 520 * k * k).toFixed(1))
        }
      }
    }

    // People who prefer reduced motion get the piece landed, at rest.
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      render(3.2)
      return
    }

    let raf = 0
    let visible = true
    let start = performance.now()
    let paused = 0
    const at = (now: number) => ((now - start) / 1000) * SPEED
    // Drop now: a piece still up high falls at once; after a landing, the
    // next piece comes in already drawn and falls.
    const drop = () => {
      const now = performance.now()
      const time = at(now)
      const loop = Math.floor(time / CYCLE)
      const t = time - loop * CYCLE
      let target = -1
      if (t < FALL[0]) target = loop * CYCLE + FALL[0]
      else if (t > LAND + 0.3) target = (loop + 1) * CYCLE + FALL[0]
      if (target >= 0) start = now - (target / SPEED) * 1000
    }
    window.addEventListener(DROP_EVENT, drop)
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting && !visible) start += performance.now() - paused
      if (!e.isIntersecting) paused = performance.now()
      visible = e.isIntersecting
    })
    io.observe(svg)
    const tick = (now: number) => {
      raf = requestAnimationFrame(tick)
      if (visible) render(at(now))
    }
    raf = requestAnimationFrame(tick)
    return () => {
      cancelAnimationFrame(raf)
      io.disconnect()
      window.removeEventListener(DROP_EVENT, drop)
      svg.replaceChildren()
    }
  }, [])

  return (
    <svg
      ref={ref}
      viewBox={VIEW[frame]}
      preserveAspectRatio="xMidYMid slice"
      aria-hidden
      className={cn("block", className)}
    />
  )
}

// Blends two hex colours, a share p of the way from a to b.
function mix(a: string, b: string, p: number) {
  const ch = (h: string, i: number) => parseInt(h.slice(1 + i * 2, 3 + i * 2), 16)
  const out = [0, 1, 2].map((i) => Math.round(ch(a, i) + (ch(b, i) - ch(a, i)) * p))
  return "#" + out.map((v) => v.toString(16).padStart(2, "0")).join("")
}
