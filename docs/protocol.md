# Ephemera wire protocol — v0 (draft)

Daemon ↔ relay speak length-prefixed JSON frames over a single
WebSocket connection. This file is the versioned source of truth for
the frame format; it is filled in as phase 3 (protocol) lands.

## Frame (planned)

	t uint8      frame type
	len uint32   payload length
	payload     JSON body

## Frame types (planned)

	hello       daemon → relay: identity, version
	send        daemon → relay: announce file (name, size, mime, ttl, cache)
	read        relay → daemon: range request (share id, offset, length)
	data        daemon → relay: raw bytes for last read
	close       either: end of stream, or error

## Send frame fields (v0, live)

	share_id           string  128-bit base64url id, derived from owner_key
	owner_key          string  base64url 32-byte secret; proves ownership
	name               string  file name as the receiver sees it
	size               int64   advisory; relay re-checks the 5GB cap per read
	mime               string  content type for inline play
	ttl_sec            int64   link lifetime; relay sweeps at expiry
	cache              bool    layer B opt-in: relay may replay already-served
	                         bytes from volatile RAM while the sender is offline
	away_prefix_bytes  int64   layer C opt-in: the daemon trickles this many
	                         prefix bytes to the relay post-announce (paced,
	                         ack-driven); the relay serves them offline

## Share ownership (live)

	share_id = base64url(sha256("ephemera share id v1\0" || key)[:16])

	The daemon generates a random 32-byte owner key per share and
	derives the id from it; the key is never part of the link. Every
	send frame carries the key, and the relay recomputes the id and
	refuses a mismatch, so knowing a link never lets anyone else
	announce it. The check is stateless and survives relay restarts.

## Away trickle (layer C, live)

	Each burst is two adjacent frames: TAway (JSON: share_id,
	offset) then TData (the bytes). The relay acks every burst with
	TAck {share_id, have}; a reconnecting daemon resumes from the
	ack it receives at accept. No ack in time: the trickle stops
	quietly (relay full, budgeted out, or layer C disabled).

Everything above is subject to change until the fake-bridge test (M1)
is green.