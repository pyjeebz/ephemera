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
  - Background upload store on disk (optional `-upload-gb`, under `-data-dir`)
  - Sender authentication (tokens file, pluggable for a hosted accounts service)
  - Link reports and account suspensions (optional `-data-dir`)
  - Rate limiting, metrics, and admin API
- **Receiver**
  - `/:id` page
  - `/:id/status` polling
  - `/:id/file` HTTP range requests
  - `POST /:id/revoke` for the sender: the link's owner key deletes it
  - Native `<video>` playback

## Layers

- **Layer A — live streaming:** The receiver asks for byte ranges; the relay asks the sender; the sender reads only those bytes from disk.
- **Layer B — replay cache:** If the sender opts in, the relay writes served ranges to volatile RAM and can replay them while the sender is offline.
- **Layer C — away prefix:** If the sender opts in, the daemon trickles the first N bytes to the relay, allowing playback to start before the sender returns. Now the fallback for relays that won't keep a full upload.
- **Background upload:** By default the daemon uploads the whole file in the background while the link streams live. The relay keeps it on disk and serves the stored part itself, so receivers only reach the sender for bytes not uploaded yet. Once the relay has every byte the sender exits, and the link keeps working until it expires.

## Persistence model

Uploaded files are the only file bytes the relay keeps on disk: one data file and one metadata file per link in `-data-dir/uploads`, budgeted by `-upload-gb`. They survive a relay restart (the link comes back as soon as the relay does), and they're deleted when the link expires, its sender revokes it, or an admin kills it. Everything else is in memory: share metadata, the optional replay cache and away prefixes, all dropped at expiry. The sender has local resume state for links it is still serving, and keeps each link's owner key until expiry (`~/.config/ephemera/links`) so `ephemera revoke` works after `send` has exited.
