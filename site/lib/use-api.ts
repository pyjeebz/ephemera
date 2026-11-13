import { useCallback, useEffect, useRef, useState } from "react"

import { api, signIn, SignedOut } from "./api"

// useApi reads path, again every `every` ms while the tab is visible.
// Signed-out readers go to sign in.
export function useApi<T>(path: string | null, every = 0) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<string>()
  const [nonce, setNonce] = useState(0)
  const alive = useRef(true)

  useEffect(() => {
    if (!path) return
    alive.current = true
    const load = async () => {
      if (document.hidden) return
      try {
        const d = await api<T>(path)
        if (alive.current) {
          setData(d)
          setError(undefined)
        }
      } catch (e) {
        if (e instanceof SignedOut) signIn()
        else if (alive.current) setError(e instanceof Error ? e.message : String(e))
      }
    }
    load()
    const id = every ? window.setInterval(load, every) : 0
    const onShow = () => !document.hidden && load()
    document.addEventListener("visibilitychange", onShow)
    return () => {
      alive.current = false
      window.clearInterval(id)
      document.removeEventListener("visibilitychange", onShow)
    }
  }, [path, every, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { data, error, reload }
}
