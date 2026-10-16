# Ephemera wire protocol — v0

Daemon ↔ relay speak length-prefixed frames over a single WebSocket
connection, one connection per share. This file is the versioned
source of truth for the format; `internal/protocol` implements it.
v0 may still change before 1.0: run a daemon and relay from the same
release.

Transport: wss:// to any relay off the sender's machine. Hello and
send carry the sender token and owner key, so the daemon refuses
ws:// and raw tcp:// unless the relay is on localhost (or the sender
passes -insecure on purpose).

## Frame

All integers little-endian:

	magic  uint32   0x52485045 ("EPHR")
	type   uint8    frame type, below
	len    uint32   payload length in bytes, at most 1 MiB
	payload         JSON body; raw file bytes for data frames

Each frame goes out in a single write, so it maps to one WebSocket
message. A bad magic or an oversized frame ends the connection.
Unknown frame types are skipped, so newer frame types can roll out
ahead of the code that reads them.

## Frame types

	1  hello  daemon → relay   first frame: version, sender token
	2  send   daemon → relay   second frame: announce the share
	3  read   relay → daemon   ask for one byte range
	4  data   daemon → relay   raw bytes answering a read
	5  err    either way       a refusal or a failed read, with reason
	6  away   daemon → relay   layer C prefix header; a data frame follows
	7  ack    relay → daemon   how many away prefix bytes the relay holds

## Hello frame fields

	version  string  daemon protocol version
	name     string  sender display name, optional
	token    string  sender token (eph_…); relays that require
	                 accounts refuse an empty or unknown one

	Relays check the token before reading the send frame. A missing
	or rejected token is a refusal (err, code denied). If the relay
	cannot check it at all (its accounts service is down) it hangs up
	with no err frame, and the daemon redials as for any dropped
	connection.

## Send frame fields

	share_id           string  128-bit base64url id, derived from owner_key
	owner_key          string  base64url 32-byte secret; proves ownership
	name               string  file name as the receiver sees it
	size               int64   bytes; checked against the sender's limit
	mime               string  content type for inline play
	ttl_sec            int64   link lifetime; relay sweeps at expiry
	cache              bool    layer B opt-in: relay may replay already-served
	                           bytes from volatile RAM while the sender is offline
	away_prefix_bytes  int64   layer C opt-in: the daemon trickles this many
	                           prefix bytes to the relay post-announce (paced,
	                           ack-driven); the relay serves them offline

## Reads

	read  {share_id, offset, length}

	The daemon answers each read in the order asked: length bytes as
	one or more consecutive data frames (each at most 1 MiB), or one
	err frame. v0 has no request ids, so the relay keeps one read in
	flight per connection. The daemon re-checks every range against
	the file's current size on disk.

	err codes on a read:
	denied   range out of bounds
	gone     share id the daemon does not serve
	io       the file could not be read

## Share ownership

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

## Refusals

	The relay checks every announce against the sender's limits (its
	account's, else relay serve -max-ttl, default 24h, and
	-max-size-mb, default 5 GiB) and its share table. A refused
	announce gets one err frame before the relay hangs up:

	code     when
	denied   no or unknown sender token, a suspended account, ttl < 1,
	         ttl or size over the sender's limits, or an owner key
	         that does not certify the share id
	gone     the link was killed by an admin

	The message is a reason meant for the sender. A daemon that gets
	err in reply to its announce stops sharing instead of redialing:
	none of these get better by trying again.

## Away trickle (layer C)

	Each burst is two adjacent frames: away (JSON: share_id, offset)
	then data (the bytes). The relay acks every burst with ack
	{share_id, have}; a reconnecting daemon resumes from the ack it
	receives at accept. No ack in time: the trickle stops quietly
	(relay full, budgeted out, or layer C disabled).
