"use client"

import { useEffect } from "react"

// Starts the sand and the isometric drawings once the page is in the DOM.
// The engines are plain modules that find their canvases and svgs by
// selector; each returns a stop function for unmount.
export function Effects() {
  useEffect(() => {
    let gone = false
    const stops: (() => void)[] = []
    Promise.all([import("@/lib/engine/sand"), import("@/lib/engine/iso")]).then(([sand, iso]) => {
      if (gone) return
      stops.push(sand.mountSand(), iso.mountIso())
    })
    return () => {
      gone = true
      for (const stop of stops) stop()
    }
  }, [])
  return null
}
