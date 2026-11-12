"use client"

import { useEffect, useRef } from "react"

import { cn } from "@/lib/utils"

// A small falling-sand box for the app pages. Each cell is one pixel of a
// tiny canvas, scaled up with crisp edges.
//
//   loop     pours, holds, drains through a gap in the floor, sits empty
//   trickle  a grain now and then, falling through: waiting for something
//   pile     a settled pile that doesn't move: done
//
// It only animates while on screen, and shows one still frame to people
// who prefer reduced motion.
type Mode = "loop" | "trickle" | "pile"

const TONES = ["#F3DCAE", "#E6C287", "#D9B070", "#C99B5A", "#B98A4D", "#A67840"].map((h) => [
  parseInt(h.slice(1, 3), 16),
  parseInt(h.slice(3, 5), 16),
  parseInt(h.slice(5, 7), 16),
])

export function SandBox({
  cols: fixedCols = 48,
  rows: fixedRows = 36,
  cell,
  mode = "loop",
  className,
  label,
}: {
  cols?: number
  rows?: number
  // cell, in CSS pixels, sizes the grid from the box's own size instead,
  // so grains stay square whatever shape the box is.
  cell?: number
  mode?: Mode
  className?: string
  label?: string
}) {
  const ref = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = ref.current
    const ctx = canvas?.getContext("2d")
    if (!canvas || !ctx) return
    let cols = fixedCols
    let rows = fixedRows
    if (cell) {
      const box = canvas.getBoundingClientRect()
      cols = Math.max(8, Math.round(box.width / cell))
      rows = Math.max(8, Math.round(box.height / cell))
    }
    canvas.width = cols
    canvas.height = rows
    const grid = new Uint8Array(cols * rows) // 0 empty, 1..6 a tone
    const img = ctx.createImageData(cols, rows)
    const at = (x: number, y: number) => y * cols + x
    const tone = () => 1 + Math.floor(Math.random() * TONES.length)

    const pile = () => {
      // A cone, as if poured and settled.
      const peak = Math.floor(rows * 0.45)
      for (let y = peak; y < rows; y++) {
        const half = Math.floor(((y - peak) / (rows - peak)) * (cols / 2)) + 1
        for (let x = Math.max(0, cols / 2 - half); x < Math.min(cols, cols / 2 + half); x++)
          grid[at(Math.floor(x), y)] = tone()
      }
    }

    // Grains fall straight if they can, else slide diagonally to a random
    // side. Rows go bottom up, each in a random direction.
    const step = (drain: boolean) => {
      if (drain) {
        const gap = Math.max(2, Math.floor(cols / 8))
        for (let x = Math.floor(cols / 2 - gap / 2); x < cols / 2 + gap / 2; x++) grid[at(x, rows - 1)] = 0
      }
      for (let y = rows - 2; y >= 0; y--) {
        const ltr = Math.random() < 0.5
        for (let i = 0; i < cols; i++) {
          const x = ltr ? i : cols - 1 - i
          const g = grid[at(x, y)]
          if (!g) continue
          if (!grid[at(x, y + 1)]) {
            grid[at(x, y + 1)] = g
            grid[at(x, y)] = 0
            continue
          }
          const d = Math.random() < 0.5 ? -1 : 1
          for (const dx of [d, -d]) {
            const nx = x + dx
            if (nx >= 0 && nx < cols && !grid[at(nx, y + 1)]) {
              grid[at(nx, y + 1)] = g
              grid[at(x, y)] = 0
              break
            }
          }
        }
      }
    }

    const draw = () => {
      for (let i = 0; i < grid.length; i++) {
        const g = grid[i]
        const o = i * 4
        if (g) {
          const [r, gg, b] = TONES[g - 1]
          img.data[o] = r
          img.data[o + 1] = gg
          img.data[o + 2] = b
          img.data[o + 3] = 255
        } else {
          img.data[o + 3] = 0
        }
      }
      ctx.putImageData(img, 0, 0)
    }

    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches
    if (mode === "pile" || still) {
      if (mode !== "trickle") pile()
      draw()
      return
    }

    // loop phases, in frames at about 30 a second
    const POUR = rows * 9
    const HOLD = 60
    const DRAIN = rows * 6
    const EMPTY = 45
    // Start partway through a pour, with a pile already there, so the box
    // never opens on nothing.
    let frame = 0
    if (mode === "loop") {
      pile()
      frame = Math.floor(POUR * 0.7)
    }
    let raf = 0
    let visible = true
    let last = 0

    const tick = (t: number) => {
      raf = requestAnimationFrame(tick)
      if (!visible || t - last < 33) return
      last = t
      if (mode === "trickle") {
        if (frame % 9 === 0) grid[at(Math.floor(cols / 2), 0)] = tone()
        // Grains fall through the floor.
        for (let x = 0; x < cols; x++) grid[at(x, rows - 1)] = 0
        step(false)
      } else {
        const p = frame % (POUR + HOLD + DRAIN + EMPTY)
        if (p < POUR) {
          const mid = Math.floor(cols / 2)
          for (let w = -1; w <= 1; w++) if (Math.random() < 0.7) grid[at(mid + w, 0)] = tone()
        }
        step(p >= POUR + HOLD && p < POUR + HOLD + DRAIN)
        if (p === POUR + HOLD + DRAIN) grid.fill(0)
      }
      frame++
      draw()
    }
    const io = new IntersectionObserver(([e]) => (visible = e.isIntersecting))
    io.observe(canvas)
    raf = requestAnimationFrame(tick)
    return () => {
      cancelAnimationFrame(raf)
      io.disconnect()
    }
  }, [fixedCols, fixedRows, cell, mode])

  return (
    <canvas
      ref={ref}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className={cn("block [image-rendering:pixelated]", className)}
    />
  )
}
