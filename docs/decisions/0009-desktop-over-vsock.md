# 0009 — A graphical desktop, streamed out of a box over vsock

- **Status:** Accepted
- **Date:** 2026-07-24
- **Phase:** 5 (web UI + live desktop), Increment 1

## Context

The Phase 4 reframe is that ephemera is a computer you use like a laptop. So far you use it through a
terminal (`eph ssh`). Phase 5 is the rest of the laptop: a graphical desktop you can watch and drive — the
payoff of "use it like a laptop," and the thing that lets you watch an agent work in a browser.

There is one hard constraint that shapes everything: **a Firecracker microVM has no display device.** Its
device model is virtio-block, -net, -vsock, -rng, and a serial console — no GPU, no VGA, no framebuffer, no
PCI graphics at all. This is deliberate on Firecracker's part, not a config we have not found. So the
usual "point a VNC client at the VM's virtual screen" (QEMU + virtio-gpu/SPICE) is simply not available:
there is no screen to point at.

The pixels have to be *manufactured inside the guest* and streamed out over a channel we already have.

## Decision

**Run a headless X server and a VNC server inside the box, and tunnel the RFB stream out over vsock** — the
same private channel exec and the interactive shell already use. The host re-exposes that stream as a plain
local TCP port for any VNC viewer.

```
[ box: Xvfb (RAM framebuffer) + openbox + xterm + x11vnc on 127.0.0.1:5900 ]
                    │  RFB bytes
                    ▼  eph-agent bridges vsock port 1025 ⇄ 127.0.0.1:5900
[ host: `eph desktop <box>` bridges vsock ⇄ 127.0.0.1:<local port> ]
                    │
                    ▼
[ a VNC viewer connects to 127.0.0.1:<local port> ]
```

- The guest boots a **desktop init** (`/sbin/eph-desktop-init`) that starts Xvfb (a framebuffer in RAM,
  since there is no display), a minimal window manager, a terminal, and **x11vnc** bound to localhost, then
  execs the agent as before.
- The agent listens on a **second vsock port** (`1025`) and splices each connection to the local VNC server.
  A box with no desktop simply finds nothing listening on 5900 and drops the connection, so the bridge costs
  nothing when unused.
- `eph desktop <box>` opens a local TCP listener and splices each viewer connection to the box's desktop
  vsock port. Bring your own viewer.

Desktop boxes are a **separate, heavier image** (`rootfs-desktop.ext4`, built with
`build/build-rootfs.sh --desktop`) with more RAM by default. The lean default box keeps its ~1 s boot and
tiny footprint; you opt into pixels.

## Reasons

1. **vsock is the only honest transport here.** x11vnc could bind the guest's network interface, but then
   the desktop is on IP — firewalled, exposed, one misconfiguration from reachable. Over vsock it is exactly
   as private as the shell: no guest NIC required, no open port, reachable only by whoever owns the box. The
   desktop inherits the box's isolation for free.
2. **We own the transport, which is the whole differentiator.** Batteries-included stacks (KasmVNC,
   Guacamole) bundle their own web server and want to serve HTTP straight to the browser — which throws away
   the vsock property. x11vnc is a dumb RFB server we tunnel ourselves. That keeps the isolation model
   intact and keeps the encoder swappable: to chase smoothness later we replace the guest-side server
   (wayvnc, or a WebRTC video pipeline) behind the *same* vsock bridge, and nothing above it moves.
3. **Prove the pipe before marrying an encoder.** Increment 1 uses the simplest possible framebuffer + RFB
   server precisely to measure the load-bearing risk — RFB latency and throughput through the vsock bridge —
   before investing in a codec. A native VNC viewer is the client; the browser (noVNC over a WebSocket
   proxy) is Increment 2, and a video-codec path is a later stretch only if responsiveness demands it.
4. **A separate image keeps the fast box fast.** A desktop stack is heavy; folding it into the default image
   would tax every throwaway box that never wanted pixels. Two images, one shared build path.

## Honest about the number

The roadmap says "60 fps." RFB/VNC gives a *responsive, usable* desktop — good for watching an agent, for
editing and browsing — but it is not smooth 60 fps full-motion video. True 60 fps means capturing frames and
encoding a video codec (VP8/H.264) over WebRTC, and **with no GPU that is software encoding — expensive
CPU.** So the Phase 5 checkpoint is *"a usable browser desktop,"* and the video path is a stretch we take
only if it is worth the cost. Under-promise the number, over-deliver the experience.

## Costs we are accepting

- **No authentication or encryption on the RFB stream yet.** x11vnc runs `-nopw`, bound to localhost. The
  security boundary is the vsock socket (owner-only) and the local bridge (127.0.0.1), the same boundary the
  shell trusts. A remote/multi-user story needs auth; a local-first single-user box does not, yet.
- **Software-only rendering.** No GPU acceleration; heavy graphics will be slow. Fine for a desktop, a
  terminal, a browser watching an agent.
- **Increment 1 desktop boxes are throwaway.** A *named, persistent* desktop computer (a set-up desktop you
  keep) composes with the persist disk (ADR 0008) but is not wired yet.
- **A bigger image and more RAM per desktop box.** The cost of pixels; you pay it only when you ask for them.

## Verified (Increment 1)

A desktop box boots from the desktop image in ~1.4 s, with Xvfb, openbox, xterm, and x11vnc all running and
x11vnc listening on `127.0.0.1:5900`. Driving `eph desktop <box>` and speaking RFB through the local port,
the handshake completes end-to-end over vsock — ProtocolVersion `RFB 003.008`, security negotiation, and a
ServerInit reporting a **1280×800** desktop named `ephemera:0`. The pixels are flowing out of the box over
the same private channel the shell uses; no guest network, no open port.

**The load-bearing question — is vsock the wall? — is answered: no.** A full **3.91 MiB uncompressed** frame
(1280×800 @ 32 bpp, Raw encoding, cold) came back in ~0.53 s, but first-byte was ~510 ms and the bytes
themselves streamed in ~20 ms — so that half-second is x11vnc *rendering* a cold full-screen Raw frame, not
the transport. vsock moved ~4 MiB in ~20 ms. The cost is encoding, not the pipe, which is exactly what a
real viewer (Tight/ZRLE compression + incremental damage) is built to cut down.

**One bug the live boot caught:** a box with no external network never brought its **loopback** interface
up, so x11vnc could *bind* `127.0.0.1:5900` but the agent's bridge could not *connect* to it
("Network unreachable"). Fixed by raising `lo` in the shared guest mounts — every box now has working
loopback, the way any real computer does.

## Verified (Increment 2 — in the browser)

`eph desktop <box>` now opens the desktop in a browser by default (`--raw` keeps the native-viewer VNC port).
The RFB stream no longer stops at a local TCP port: a small local **web server hosts a self-contained page**
and, at `/ws`, a **WebSocket** that proxies the RFB bytes to the box's desktop vsock port. The daemon stays on
its unix socket; the CLI is the one thing that faces a browser, and it faces only localhost.

Both the WebSocket server and the RFB client are **hand-rolled and dependency-free**, consistent with the rest
of the repo (own Firecracker client, own vsock handshake) — no noVNC vendoring, no CDN, no build step. The page
is a single embedded HTML file: a canvas, a minimal RFB client (no-auth handshake, a forced 32-bpp format so
blitting is a byte reorder, Raw + CopyRect updates, pointer/keyboard input).

Driven headlessly through the browser path: the **WebSocket upgrade** completes with a valid accept token; a
**full RFB handshake** runs through the proxy (ServerInit `1280×800`, `ephemera:0`) — which also exercises the
browser→box direction, since the client's masked frames are unmasked correctly; and a **full 3.91 MiB
framebuffer** pulls through in 32 KiB WebSocket frames, exercising the 16-bit length path. The remaining
verification — pixels on a canvas and live mouse/keyboard — is a real browser in front of a human.

## The video path (5d), and why the swappable encoder paid off

The claim in Decision reason 2 was that owning the transport keeps the encoder swappable — that to chase
smoothness we could replace the guest-side server behind the *same* vsock bridge and nothing above it would
move. That is exactly what happened. A **Crisp / Smooth** toggle now sits on the desktop:

- **Crisp** is the RFB framebuffer above — low latency, exact pixels.
- **Smooth** is **H.264**: the agent runs a per-connection `ffmpeg` capturing the X display, streamed as
  fragmented MP4 over a third vsock port and decoded in the browser with **Media Source** (`avc1.42C01F`,
  baseline). Input still flows through the VNC server — the RFB client gained an *input-only* mode that
  injects pointer/keyboard without ever requesting a framebuffer — so the video desktop stays clickable.

**It is not WebRTC.** WebRTC's machinery (ICE, signaling, SDP) exists to cross NATs, and this is localhost —
so the same codec win comes from MSE-over-WebSocket with a fraction of the parts. The transport, the daemon
route pattern, and the input path are all reused; only the guest-side producer changed. Measured ~9 KiB/s
idle versus RFB's multi-MiB raw frames — the encoder, not the pipe, was always the cost, and now it is a
choice. The trade is CPU (software H.264, no GPU) and a little latency; the toggle exists so a user can feel
which they want rather than have it decided for them.

_Verified: the stream is a valid H.264 fragmented MP4 (`ftyp`+`moov`+`moof`+`mdat`) through the daemon, and
the input-only RFB path handshakes and accepts pointer events without pulling a framebuffer._

**Update:** with a human in front of the browser, Smooth won — so it is now the **default** desktop mode where
the browser can play H.264, falling back to Crisp (RFB) only where it cannot. The toggle stays, because Crisp
is still the better answer for latency-sensitive, typing-heavy work; the default just reflects that for
watching and general use, the video path feels better.
