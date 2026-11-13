import { useSyncExternalStore } from "react"

// One clock for every age that counts up, ticking each second while
// anything reads it. It reads 0 on the server, where there's no data to
// age yet.
let now = 0
let timer = 0
const readers = new Set<() => void>()

function subscribe(onTick: () => void) {
  readers.add(onTick)
  if (readers.size === 1) {
    timer = window.setInterval(() => {
      now = Date.now()
      readers.forEach((f) => f())
    }, 1000)
  }
  return () => {
    readers.delete(onTick)
    if (readers.size === 0) window.clearInterval(timer)
  }
}

function read() {
  if (!now) now = Date.now()
  return now
}

export function useNow() {
  return useSyncExternalStore(subscribe, read, () => 0)
}
