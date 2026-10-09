# Architecture

The high-level architecture diagram is stored as a tldraw file:

```text
docs/architecture.tldr
```

Open it with the tldraw desktop app or import it at tldraw.com.

To regenerate the diagram after changing `scripts/make_architecture_tldraw.py`:

```bash
python3 scripts/make_architecture_tldraw.py
```

## Components

- **Sender device**
  - `ephemera send`
  - Local file on disk
  - Share validation and MIME detection
  - Keep-awake support
  - Local resume state
- **Relay**
  - `/daemon` WebSocket bridge
  - Share registry and TTL sweeper
  - Receiver-facing HTTP surface
  - Layer B volatile cache
  - Layer C away-prefix store
  - Rate limiting, metrics, and admin API
- **Receiver**
  - `/:id` page
  - `/:id/status` polling
  - `/:id/file` HTTP range requests
  - Native `<video>` playback

## Layers

- **Layer A — live streaming:** The receiver asks for byte ranges; the relay asks the sender; the sender reads only those bytes from disk.
- **Layer B — replay cache:** If the sender opts in, the relay writes served ranges to volatile RAM and can replay them while the sender is offline.
- **Layer C — away prefix:** If the sender opts in, the daemon trickles the first N bytes to the relay, allowing playback to start before the sender returns.

## Persistence model

There is no durable file storage on the relay. The sender has local resume state, while the relay has only ephemeral metadata, an optional RAM cache, and an optional away prefix. The TTL sweeper drops all of them at expiry.
