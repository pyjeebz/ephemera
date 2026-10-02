// Package metrics is the relay's counter floor: process-wide
// atomic counters with a JSON snapshot for /admin/metrics and the
// build-in-public receipts (bytes-served, thesis ratio).
package metrics

import (
	"encoding/json"
	"expvar"
)

// Counters exported for the admin surface and the byte-ratio thesis.
var (
	// SharesStarted counts accepted daemon announcements.
	SharesStarted = expvar.NewInt("shares_started")

	// BytesServed counts body bytes written to receivers.
	BytesServed = expvar.NewInt("bytes_served")

	// DarkLinkHits counts file reads that found the sender offline —
	// the layer-A failure metric.
	DarkLinkHits = expvar.NewInt("dark_link_hits")

	// CacheHits / CacheMisses count layer-B replay reads. The
	// hit-rate is the cache's whole reason to exist.
	CacheHits   = expvar.NewInt("cache_hits")
	CacheMisses = expvar.NewInt("cache_misses")

	// BytesCachedReplay counts bytes served from the cache while
	// the sender was offline — the "replay while away" receipt.
	BytesCachedReplay = expvar.NewInt("bytes_cached_replay")

	// CacheBytesHeld is a gauge, not a counter: the LRU's current
	// byte occupancy, set by the cache on every mutation.
	CacheBytesHeld = expvar.NewInt("cache_bytes_held")

	// Requests counts receiver-facing requests (page, status, file).
	Requests = expvar.NewInt("requests")

	// RangeRequests counts requests that arrived with a Range
	// header — the thesis in action: browsers asking for slices.
	RangeRequests = expvar.NewInt("range_requests")

	// StatusPolls counts /status hits (receiver pages polling).
	StatusPolls = expvar.NewInt("status_polls")

	// RateLimited429 counts requests refused by the limiter.
	RateLimited429 = expvar.NewInt("rate_limited_429")

	// BytesRatioSnapshot is a derived value, computed on read.
	BytesRatioSnapshot = expvar.NewFloat("bytes_ratio")

	// AdminKills counts /admin/kill invocations.
	AdminKills = expvar.NewInt("admin_kills")
)

// ThesisRatio returns bytes-served divided by bytes-announced, the
// publishable "we moved X% of what a full upload would have"
// number. Announced bytes are counted by the relay at announce time.
var BytesAnnounced = expvar.NewInt("bytes_announced")

// UpdateRatio recomputes the snapshot ratio. Called after announce
// and after each served body; cheap enough to call sparingly.
func UpdateRatio() {
	a := BytesAnnounced.Value()
	if a == 0 {
		BytesRatioSnapshot.Set(0)
		return
	}
	BytesRatioSnapshot.Set(float64(BytesServed.Value()) / float64(a))
}

// Snapshot renders all counters as pretty JSON for /admin/metrics.
func Snapshot() []byte {
	snapshot := map[string]any{}
	expvar.Do(func(kv expvar.KeyValue) {
		var v any
		if err := json.Unmarshal([]byte(kv.Value.String()), &v); err != nil {
			return
		}
		snapshot[kv.Key] = v
	})
	// ratio derives fresh on every snapshot
	a := BytesAnnounced.Value()
	if a > 0 {
		snapshot["bytes_ratio"] = float64(BytesServed.Value()) / float64(a)
	}
	b, _ := json.MarshalIndent(snapshot, "", "  ")
	return b
}
