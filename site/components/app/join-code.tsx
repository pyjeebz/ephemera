"use client"

import { useCallback, useEffect, useState } from "react"

import { API, api, type JoinCode } from "@/lib/api"
import { useNow } from "@/lib/use-now"

// useJoinCode makes a one-time code for adding a host, and says how long
// it has left.
export function useJoinCode(auto: boolean) {
  const [code, setCode] = useState<JoinCode>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(auto)
  const now = useNow()

  const fetchCode = useCallback(() => {
    api<JoinCode>("/v1/join-codes", { method: "POST" })
      .then((c) => {
        setCode(c)
        setError(undefined)
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false))
  }, [])
  const make = useCallback(() => {
    setBusy(true)
    fetchCode()
  }, [fetchCode])
  useEffect(() => {
    if (auto) fetchCode()
  }, [auto, fetchCode])

  const left = code ? Math.max(0, Math.floor((Date.parse(code.expires) - now) / 1000)) : 0
  const clock = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`
  return { code: code?.code, left, clock, expired: !!code && left === 0, make, busy, error }
}

export function installCommand(code: string) {
  return `curl -fsSL https://ephemera.run/host | sudo sh -s -- ${code}`
}

export function joinCommand(code: string) {
  return `sudo ephemera-host join --server ${API} ${code}`
}
