import type { Spec } from "./api"

// duration says a length of time the way a person would: 14.2 s, 1 min 52 s.
export function duration(seconds: number) {
  const s = Math.max(0, seconds)
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`
  const m = Math.floor(s / 60)
  if (m < 10) return `${m} min ${Math.round(s % 60)} s`
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60)
  return m % 60 ? `${h} h ${m % 60} min` : `${h} h`
}

export function since(iso: string, now: number) {
  return (now - Date.parse(iso)) / 1000
}

export function ago(iso: string, now: number) {
  const s = since(iso, now)
  if (s < 60) return `${Math.max(0, Math.round(s))} s ago`
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  const d = Math.round(s / 86400)
  return d === 1 ? "a day ago" : `${d} days ago`
}

const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" })
const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })

export function date(iso: string) {
  return day.format(new Date(iso))
}

// started is a time today, "Yesterday", or a date.
export function started(iso: string, now: number) {
  const t = new Date(iso)
  const today = new Date(now)
  today.setHours(0, 0, 0, 0)
  if (t >= today) return clock.format(t)
  if (t.getTime() >= today.getTime() - 86400_000) return "Yesterday"
  return day.format(t)
}

export function isToday(iso: string, now: number) {
  const today = new Date(now)
  today.setHours(0, 0, 0, 0)
  return Date.parse(iso) >= today.getTime()
}

export function greeting(now: number) {
  const h = new Date(now).getHours()
  return h < 5 ? "Evening" : h < 12 ? "Morning" : h < 18 ? "Afternoon" : "Evening"
}

export function network(spec: Spec) {
  switch (spec.network) {
    case "allow": {
      const n = spec.allow?.length ?? 0
      return `allow · ${n} ${n === 1 ? "host" : "hosts"}`
    }
    case "open":
      return "open"
    default:
      return "no network"
  }
}

export function firstName(name: string | undefined, login: string) {
  return name?.trim().split(/\s+/)[0] || login
}
