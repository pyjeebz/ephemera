package cli

import (
	"strings"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/playback"
)

func TestPrintStats(t *testing.T) {
	var b strings.Builder
	printStats(&b, playback.Summary{}, nil, nil, map[string]float64{"uploads_completed": 2, "upload_bytes_held": 50e6})
	if got := b.String(); !strings.Contains(got, "uploads: 2 finished, 50 MB") || !strings.Contains(got, "none reported yet") {
		t.Fatalf("empty stats:\n%s", got)
	}

	b.Reset()
	s := playback.Summary{
		Views: 3, TTFFMsP50: 420, TTFFMsP90: 1500, StallFreePct: 66, RebufferPct: 2.5,
		BitrateBpsP50: 4e6, SenderBpsP50: 12e6, SlowSenderPct: 33,
		ByState: map[string]int{"live": 2, "available": 1},
	}
	views := []playback.View{{
		Report: playback.Report{View: "v", State: "live", TTFFMs: 420, Stalls: 1, StallMs: 1500, Seeks: 2, SeekWaitMs: 600},
		Share:  "abc", At: time.Now(), BitrateBps: 4e6, SenderBps: 12e6, FromRelayPct: 40,
	}}
	printStats(&b, s, views, map[string]playback.FirstByteStats{
		"relay":  {Requests: 40, P50Ms: 1.5, P90Ms: 4},
		"sender": {Requests: 12, P50Ms: 180, P90Ms: 1200},
	}, nil)
	got := b.String()
	for _, want := range []string{
		"first byte, any client: from the relay's copy 1.5ms median, 4.0ms p90 (40 requests); live from the sender 180ms median, 1.2s p90 (12 requests)",
		"playbacks: 3 (live 2, available 1)",
		"first frame: 420ms median, 1.5s p90",
		"66% of playbacks never stalled; 2.5% of watch time",
		"files play at 4.0 Mbps median, senders deliver live at 12.0 Mbps median; sender slower than the file in 33%",
		"1 (1.5s)", "300ms", "40%",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stats missing %q:\n%s", want, got)
		}
	}
}
