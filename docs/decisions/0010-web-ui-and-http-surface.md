# 0010 — A web UI, and an opt-in HTTP surface to serve it

- **Status:** Accepted
- **Date:** 2026-07-24
- **Phase:** 5 (web UI + live desktop), Increment 3

## Context

ephemera should be drivable from a browser: list your boxes, spin one up, stop or fork it, watch its desktop
— without the CLI. Two things stand in the way of just "serving a web UI from the daemon":

1. **The daemon lives on a Unix socket.** `ephemerad` listens on `run/ephemerad.sock` (mode 0600) by design
   — a local-first daemon has no reason to be on the network, and file permissions are a better access
   control than an open port ([0002](0002-networking-and-isolation-model.md)'s spirit). A browser cannot
   speak to a Unix socket.
2. **A browser needs static assets.** A single-page app is HTML/JS/CSS that has to come from somewhere the
   browser can fetch.

## Decision

**Add an opt-in TCP surface to the daemon (`-http`), off by default and meant for loopback, that serves the
same handler — the JSON API, the desktop WebSocket, and an embedded single-page web UI.** The UI is
**SvelteKit**, built to a static SPA and baked into the binary with `go:embed`.

- **`ephemerad -http 127.0.0.1:PORT`** starts a second listener on TCP. It serves the *same* `http.Handler`
  as the Unix socket, so a browser reaches the whole API plus the UI plus `…/desktop/ws` from one origin.
  Off by default: the control socket stays the only surface unless you ask for the browser one.
- **The UI is embedded, not deployed.** `web/` is a SvelteKit app (`adapter-static`, SPA); its build lands
  in `internal/webui/dist` and `go:embed` bakes it into `ephemerad`. One binary ships the UI — no asset
  directory, no CDN, no separate web server. The built output is committed so `go build` needs no Node.
- **The web UI is the catch-all route.** The API patterns (`/v1/…`, `/healthz`) are specific and win; every
  other path falls to the SPA, which serves its index and routes on the client.

## Reasons

1. **Opt-in keeps the safe default safe.** The TCP surface exposes the *full control plane* — creating and
   destroying boxes — guarded only by the address it binds, which is a weaker boundary than a 0600 socket.
   So it is off unless asked for, defaults to loopback, and warns when bound anywhere routable. A box owner
   who wants the browser UI turns it on; nobody else pays for it.
2. **Embedding matches how the rest of ephemera ships.** The whole project is one-binary, self-contained, no
   CDN (the desktop page, the RFB client, the WebSocket server are all hand-rolled and embedded). The web UI
   plays by the same rules: baked in, served from the local box, works offline.
3. **One origin, one handler.** Serving the API and the UI from the same listener means the SPA uses
   relative paths — no base URL, no CORS, no second port. The desktop WebSocket the daemon already grew for
   the browser ([0009](0009-desktop-over-vsock.md)) is reused unchanged.
4. **SvelteKit was the stated plan.** The roadmap named it; a static SPA build embeds cleanly. The cost —
   a Node toolchain and a build step, against a repo that had none — is real and acknowledged; it is paid at
   build time only, and the committed `dist` keeps `go build` and the Go tests Node-free.

## Costs we are accepting

- **A Node/npm build step enters the repo.** The one place ephemera is not hand-rolled from scratch. Bounded
  to `web/`, and the committed build output means the Go side never needs Node.
- **Committed build artifacts.** `internal/webui/dist` is generated yet tracked, so the binary always has a
  UI and `go build` is standalone. It must be rebuilt and re-committed when the UI changes (build.sh does the
  rebuild when Node is present).
- **The control plane can be reached over TCP.** Only when `-http` is set, only where it binds. On a
  single-user local box that is the owner already; a multi-user or exposed host wants a real auth story
  before turning it on, which is noted at the flag and warned at bind.

## Verified

With `ephemerad -http 127.0.0.1:8080`: the SPA is served (`GET /` → the app; `_app` assets load; a client
route like `/box/{id}` falls back to index so the router takes it); the JSON API answers on the same origin
(so the dashboard's list/new/stop/fork calls work); and the desktop WebSocket handshakes through the daemon
(`RFB 003.008`). The dashboard and the in-UI desktop viewer (the RFB client ported to a Svelte component)
build and serve; their live rendering and interactivity are a real browser in front of a human. The
in-browser **terminal** is the remaining piece of the UI.
