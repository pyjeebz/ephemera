// Package playback is the relay's view of how watching actually
// went: what receiver pages report about each playback (time to first
// frame, stalls, seeks), next to what the relay saw of the sender
// (how fast it delivered live reads, how much came off the relay's
// own disk instead). In memory only, the latest views, no addresses.
package playback

import (
	"math"
	"slices"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/metrics"
)

// Report is what a receiver page sends about one playback.
type Report struct {
	View       string  `json:"view"`         // random per page view
	State      string  `json:"state"`        // link state when play was pressed
	TTFFMs     int64   `json:"ttff_ms"`      // play pressed to first frame
	Stalls     int64   `json:"stalls"`       // waits for data mid-play, not after seeks
	StallMs    int64   `json:"stall_ms"`     // time spent in those waits
	Seeks      int64   `json:"seeks"`        // seeks after playback started
	SeekWaitMs int64   `json:"seek_wait_ms"` // time from seeking to seeked, summed
	WatchedS   float64 `json:"watched_s"`    // seconds of media played
	DurationS  float64 `json:"duration_s"`   // media length, if the browser knows it
}

// Valid reports whether r looks like something a real page sent:
// a view id, a known state, and numbers inside sane bounds.
func (r Report) Valid() bool {
	const day = 24 * 60 * 60
	inMs := func(v int64) bool { return v >= 0 && v <= day*1000 }
	switch r.State {
	case "live", "available", "offline":
	default:
		return false
	}
	return r.View != "" && len(r.View) <= 64 &&
		r.TTFFMs > 0 && inMs(r.TTFFMs) && inMs(r.StallMs) && inMs(r.SeekWaitMs) &&
		r.Stalls >= 0 && r.Stalls <= 100000 && r.Seeks >= 0 && r.Seeks <= 100000 &&
		r.WatchedS >= 0 && r.WatchedS <= day && r.DurationS >= 0 && r.DurationS <= 10*day
}

// View is one playback as the relay keeps it: the page's report
// plus what the relay knows about the link and its sender.
type View struct {
	Report
	Share string    `json:"share"`
	At    time.Time `json:"at"`
	Size  int64     `json:"size"`

	// BitrateBps is the file's average bitrate: size over duration.
	// SenderBps is how fast the sender delivered live reads, 0 if
	// it served none. FromRelayPct is the share of bytes receivers got
	// from the relay's own disk or memory rather than live.
	BitrateBps   int64 `json:"bitrate_bps,omitempty"`
	SenderBps    int64 `json:"sender_bps,omitempty"`
	FromRelayPct int   `json:"from_relay_pct"`
}

// Log keeps the latest views, newest last. A page that reports twice
// (hidden, shown again, hidden) replaces its earlier report.
type Log struct {
	// OnAdd, if set, sees every view as it's added: the relay logs it.
	OnAdd func(View)

	mu    sync.Mutex
	max   int
	views []View
}

// NewLog keeps up to max views.
func NewLog(max int) *Log { return &Log{max: max} }

// Add records a view.
func (l *Log) Add(v View) {
	l.mu.Lock()
	if i := slices.IndexFunc(l.views, func(o View) bool { return o.View == v.View && o.Share == v.Share }); i >= 0 {
		l.views = slices.Delete(l.views, i, i+1)
	} else {
		metrics.PlaybackViews.Add(1)
	}
	l.views = append(l.views, v)
	if len(l.views) > l.max {
		l.views = slices.Delete(l.views, 0, len(l.views)-l.max)
	}
	l.mu.Unlock()
	if l.OnAdd != nil {
		l.OnAdd(v)
	}
}

// Views returns up to limit of the latest views, newest first.
// limit <= 0 returns them all.
func (l *Log) Views(limit int) []View {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.views)
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Summary is the numbers that say whether it feels instant.
type Summary struct {
	Views int `json:"views"`

	TTFFMsP50 int64 `json:"ttff_ms_p50"`
	TTFFMsP90 int64 `json:"ttff_ms_p90"`

	// StallFreePct: views that never waited for data mid-play.
	// RebufferPct: time spent stalled, over time spent watching.
	StallFreePct int     `json:"stall_free_pct"`
	RebufferPct  float64 `json:"rebuffer_pct"`

	SeekWaitMsP50 int64 `json:"seek_wait_ms_p50"` // per seek

	BitrateBpsP50 int64 `json:"bitrate_bps_p50"`
	SenderBpsP50  int64 `json:"sender_bps_p50"`
	// SlowSenderPct: views whose sender delivered live reads slower
	// than the file plays, among views that had both numbers.
	SlowSenderPct int `json:"slow_sender_pct"`

	// ByState breaks the views down by the link's state at play.
	ByState map[string]int `json:"by_state"`
}

// Summary sums up the views in the log.
func (l *Log) Summary() Summary {
	views := l.Views(0)
	s := Summary{Views: len(views), ByState: map[string]int{}}
	if len(views) == 0 {
		return s
	}
	var ttff, seekWait, bitrate, sender []int64
	var stallFree, slow, both int
	var stalledMs, watchedMs float64
	for _, v := range views {
		s.ByState[v.State]++
		ttff = append(ttff, v.TTFFMs)
		if v.Stalls == 0 {
			stallFree++
		}
		stalledMs += float64(v.StallMs)
		watchedMs += v.WatchedS * 1000
		if v.Seeks > 0 {
			seekWait = append(seekWait, v.SeekWaitMs/v.Seeks)
		}
		if v.BitrateBps > 0 {
			bitrate = append(bitrate, v.BitrateBps)
		}
		if v.SenderBps > 0 {
			sender = append(sender, v.SenderBps)
		}
		if v.BitrateBps > 0 && v.SenderBps > 0 {
			both++
			if v.SenderBps < v.BitrateBps {
				slow++
			}
		}
	}
	s.TTFFMsP50, s.TTFFMsP90 = pct(ttff, 50), pct(ttff, 90)
	s.StallFreePct = stallFree * 100 / len(views)
	if watchedMs+stalledMs > 0 {
		s.RebufferPct = math.Round(stalledMs/(watchedMs+stalledMs)*1000) / 10
	}
	s.SeekWaitMsP50 = pct(seekWait, 50)
	s.BitrateBpsP50, s.SenderBpsP50 = pct(bitrate, 50), pct(sender, 50)
	if both > 0 {
		s.SlowSenderPct = slow * 100 / both
	}
	return s
}

// pct is the p-th percentile of xs by nearest rank; 0 for none.
func pct(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	xs = slices.Clone(xs)
	slices.Sort(xs)
	i := (len(xs)*p + 99) / 100
	return xs[max(i-1, 0)]
}

// Senders tracks, per link, how fast its sender delivers live reads
// and how many bytes receivers got from the relay instead.
type Senders struct {
	mu    sync.Mutex
	stats map[string]*senderStats
}

type senderStats struct {
	liveBytes  int64
	liveTime   time.Duration
	relayBytes int64
}

// NewSenders starts an empty table.
func NewSenders() *Senders { return &Senders{stats: make(map[string]*senderStats)} }

func (s *Senders) get(id string) *senderStats {
	st := s.stats[id]
	if st == nil {
		st = &senderStats{}
		s.stats[id] = st
	}
	return st
}

// Live records one read the sender answered: n bytes in d, measured
// from asking to having them all, round trip included. That is the
// rate a receiver sees while the relay has nothing stored.
func (s *Senders) Live(id string, n int64, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.get(id)
	st.liveBytes += n
	st.liveTime += d
}

// FromRelay records n bytes served without the sender: off the
// upload store, the replay cache or the away prefix.
func (s *Senders) FromRelay(id string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(id).relayBytes += n
}

// Stats returns the sender's live delivery rate in bits per second
// (0 if it served no live reads) and the percent of bytes served
// from the relay.
func (s *Senders) Stats(id string) (senderBps int64, fromRelayPct int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats[id]
	if st == nil {
		return 0, 0
	}
	if st.liveTime > 0 {
		senderBps = int64(float64(st.liveBytes*8) / st.liveTime.Seconds())
	}
	if total := st.liveBytes + st.relayBytes; total > 0 {
		fromRelayPct = int(st.relayBytes * 100 / total)
	}
	return senderBps, fromRelayPct
}

// Drop forgets a link (expiry, kill).
func (s *Senders) Drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.stats, id)
}
