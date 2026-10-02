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
	send        daemon → relay: announce file (name, size, mime, ttl)
	read        relay → daemon: range request (share id, offset, length)
	data        daemon → relay: raw bytes for last read
	close       either: end of stream, or error

Everything above is subject to change until the fake-bridge test (M1)
is green.