# Ephemera wire protocol — v0 (draft)

Daemon ↔ relay speak length-prefixed JSON frames over a single
WebSocket connection. This file is the versioned source of truth for
the frame format; it is filled in as phase 3 (protocol) lands.

Transport: wss:// to any relay off the sender's machine. Hello and
send carry the sender token and owner key, so the daemon refuses
ws:// and raw tcp:// unless the relay is on localhost (or the sender
passes -insecure on purpose).

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

## Hello frame fields (v0, live)

	version  string  daemon protocol version
	name     string  sender display name, optional
	token    string  sender token (eph_…); relays that require
	                 accounts refuse an empty or unknown one

	Relays check the token before reading the send frame. A missing
	or rejected token is a refusal (TErr denied). If the relay cannot
	check it at all (its accounts service is down) it hangs up with no
	TErr, and the daemon redials as for any dropped connection.

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

	A redial refreshes the share but never moves its expiry later:
	the daemon announces the remaining TTL, and the relay keeps the
	earlier of the two. An admin kill tombstones the id until its
	original expiry; announces for it are refused until then.

## Refusals (live)

	The relay checks every announce against the sender's limits (its
	account's, else relay serve -max-ttl, default 24h, and
	-max-size-mb, default 5 GiB) and its share table. A refused announce gets one TErr before the relay hangs up:

	code     when
	denied   no or unknown sender token, ttl < 1, ttl or size over
	         the sender's limits, or an owner key that does not
	         certify the share id
	gone     the link was killed by an admin

	The message is a reason meant for the sender. A daemon that gets
	TErr in reply to its announce stops sharing instead of redialing:
	none of these get better by trying again.

## Away trickle (layer C, live)

	Each burst is two adjacent frames: TAway (JSON: share_id,
	offset) then TData (the bytes). The relay acks every burst with
	TAck {share_id, have}; a reconnecting daemon resumes from the
	ack it receives at accept. No ack in time: the trickle stops
	quietly (relay full, budgeted out, or layer C disabled).

Everything above is subject to change until the fake-bridge test (M1)
is green.