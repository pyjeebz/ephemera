// The central server. The browser calls it directly with the session
// cookie it set at sign-in; nothing goes through this site's server.
export const API = process.env.NEXT_PUBLIC_EPHEMERA_API ?? "https://api.ephemera.run"

export type Account = { id: string; login: string; name?: string; avatar_url?: string }

export type Host = {
  id: string
  name: string
  public_key: string
  fingerprint?: string
  last_seen: string
  url: string
  online: boolean
  capacity?: number
  version?: string
  running: number
  joined: string
}

export type Spec = {
  image?: string
  cpus?: number
  memory_mb?: number
  network?: "none" | "allow" | "open"
  allow?: string[]
  max_lifetime_seconds?: number
}

export type Sandbox = {
  id: string
  host: string
  host_id: string
  spec: Spec
  created_at: string
  owner?: string
}

export type Run = {
  id: string
  host: string
  host_id: string
  owner: string
  spec: Spec
  created_at: string
  ended_at?: string
  end_reason?: "destroyed" | "limit" | "gone" | "failed"
}

export type Usage = { day: string; minutes_today: number; running: number }

export type Token = {
  id: string
  name: string
  kind: "device" | "api"
  created: string
  max_sandboxes?: number
  max_minutes_per_day?: number
}

export type JoinCode = { code: string; expires: string }

export type DeviceInfo = { user_code: string; device: string; created: string }

// SignedOut means the session is missing or over.
export class SignedOut extends Error {}

export async function api<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(API + path, {
    method: init?.method ?? "GET",
    credentials: "include",
    headers: init?.body === undefined ? undefined : { "Content-Type": "application/json" },
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
  })
  if (res.status === 401) throw new SignedOut("not signed in")
  if (!res.ok) {
    const e = await res.json().catch(() => null)
    throw new Error(e?.error ?? `${res.status} ${res.statusText}`)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

// signIn sends the browser to sign in, and back here afterwards. It's a
// full page load, as signing in leaves the site anyway.
export function signIn() {
  const here = window.location.pathname + window.location.search
  window.location.replace(`/login?next=${encodeURIComponent(here)}`)
}
