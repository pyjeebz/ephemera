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

	share_id  string  unguessable 128-bit base64url id
	name      string  file name as the receiver sees it
	size      int64   advisory; relay re-checks the 5GB cap per read
	mime      string  content type for inline play
	ttl_sec   int64   link lifetime; relay sweeps at expiry
	cache     bool    layer B opt-in: relay may replay already-served
	                bytes from volatile RAM while the sender is offline

Everything above is subject to change until the fake-bridge test (M1)
is green.