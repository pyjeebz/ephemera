// Sample data for the onboarding and dashboard designs, until they read
// from api.ephemera.run. Only what the central server really keeps is here:
// ids, hosts, times, exit codes and network summaries. Never commands,
// files or output.

export const ACCOUNT = {
  id: "gh-48211907",
  login: "pyjeebz",
  name: "Mujeeb",
  avatar: "",
}

export const API = "https://api.ephemera.run"

export type Host = {
  id: string
  name: string
  online: boolean
  lastSeen: string
  version: string
  running: number
  capacity: number
  fingerprint: string
  joined: string
}

export const HOSTS: Host[] = [
  {
    id: "host_7db45053",
    name: "jeebz-pc",
    online: true,
    lastSeen: "4 s ago",
    version: "v0.2.0",
    running: 2,
    capacity: 20,
    fingerprint: "SHA256:RfVqsYqH1wDpSCLQIVE7VPmMqUO6tt/6Ie4tfjE2SqU",
    joined: "Nov 12",
  },
  {
    id: "host_a1c09e42",
    name: "hetzner-fsn1",
    online: true,
    lastSeen: "11 s ago",
    version: "v0.2.0",
    running: 1,
    capacity: 40,
    fingerprint: "SHA256:q3Zx9kT0lV2b1yN8hK5mEw7cR4sJ6uA0pD9fG2iL1oQ",
    joined: "Nov 12",
  },
  {
    id: "host_3e5f7a10",
    name: "old-laptop",
    online: false,
    lastSeen: "2 days ago",
    version: "v0.2.0",
    running: 0,
    capacity: 8,
    fingerprint: "SHA256:Hn2dK9vQ0pL4mX7cW1sT8yB3eR6uA5zJfG0iO2kNqVw",
    joined: "Nov 10",
  },
]

export type Live = {
  id: string
  host: string
  token: string
  age: string
  cpus: number
  memoryMB: number
}

export const LIVE: Live[] = [
  {
    id: "sbx_6abe91d8",
    host: "jeebz-pc",
    token: "laptop",
    age: "41 s",
    cpus: 2,
    memoryMB: 1024,
  },
  {
    id: "sbx_c40f12aa",
    host: "jeebz-pc",
    token: "claude-code",
    age: "6 min",
    cpus: 2,
    memoryMB: 2048,
  },
  {
    id: "sbx_91d2e7b3",
    host: "hetzner-fsn1",
    token: "ci",
    age: "2 min",
    cpus: 4,
    memoryMB: 4096,
  },
]

export type Run = {
  id: string
  started: string
  lived: string
  exit: number | null
  ended: "exited" | "destroyed" | "limit" | "gone" | "expired"
  host: string
  token: string
  allowed: number
  blocked: number
  blockedHosts: string[]
}

export const RUNS: Run[] = [
  {
    id: "sbx_6abe91d8",
    started: "09:41",
    lived: "14.2 s",
    exit: 0,
    ended: "exited",
    host: "jeebz-pc",
    token: "laptop",
    allowed: 14,
    blocked: 1,
    blockedHosts: ["telemetry.example.com:443"],
  },
  {
    id: "sbx_2f8c33d1",
    started: "09:38",
    lived: "1 min 52 s",
    exit: 1,
    ended: "exited",
    host: "hetzner-fsn1",
    token: "ci",
    allowed: 212,
    blocked: 0,
    blockedHosts: [],
  },
  {
    id: "sbx_e71b0a94",
    started: "09:30",
    lived: "38.0 s",
    exit: 0,
    ended: "exited",
    host: "jeebz-pc",
    token: "claude-code",
    allowed: 9,
    blocked: 3,
    blockedHosts: ["pastebin.com:443", "api.openai.com:443", "1.1.1.1:53"],
  },
  {
    id: "sbx_5c0d9f27",
    started: "09:12",
    lived: "22 min",
    exit: null,
    ended: "destroyed",
    host: "jeebz-pc",
    token: "laptop",
    allowed: 48,
    blocked: 0,
    blockedHosts: [],
  },
  {
    id: "sbx_ab3907e6",
    started: "08:57",
    lived: "9.4 s",
    exit: 0,
    ended: "exited",
    host: "hetzner-fsn1",
    token: "ci",
    allowed: 31,
    blocked: 0,
    blockedHosts: [],
  },
  {
    id: "sbx_14ce6b02",
    started: "08:40",
    lived: "10 min",
    exit: null,
    ended: "limit",
    host: "hetzner-fsn1",
    token: "ci",
    allowed: 120,
    blocked: 0,
    blockedHosts: [],
  },
  {
    id: "sbx_77a1f3c8",
    started: "Yesterday",
    lived: "3.1 s",
    exit: 127,
    ended: "exited",
    host: "jeebz-pc",
    token: "laptop",
    allowed: 0,
    blocked: 0,
    blockedHosts: [],
  },
]

export type Token = {
  id: string
  name: string
  kind: "device" | "api"
  created: string
  lastUsed: string
  maxSandboxes?: number
  maxMinutesPerDay?: number
}

export const TOKENS: Token[] = [
  {
    id: "tok_1193fdb8",
    name: "laptop",
    kind: "device",
    created: "Nov 12",
    lastUsed: "now",
  },
  {
    id: "tok_8c2e0b71",
    name: "claude-code",
    kind: "api",
    created: "Nov 12",
    lastUsed: "6 min ago",
    maxSandboxes: 4,
  },
  {
    id: "tok_ead5038c",
    name: "ci",
    kind: "api",
    created: "Nov 12",
    lastUsed: "2 min ago",
    maxSandboxes: 2,
    maxMinutesPerDay: 600,
  },
]

export const TODAY = { runs: 47, minutes: 38, minutesLimit: 600, blocked: 4 }
