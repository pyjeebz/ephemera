package metrics

import (
	"encoding/json"
	"testing"
)

func TestSnapshotRenders(t *testing.T) {
	SharesStarted.Add(1)
	BytesAnnounced.Add(1000)
	BytesServed.Add(40)
	UpdateRatio()

	snap := Snapshot()
	var m map[string]any
	if err := json.Unmarshal(snap, &m); err != nil {
		t.Fatalf("snapshot json: %v", err)
	}
	if m["shares_started"] != float64(1) {
		t.Errorf("shares_started = %v", m["shares_started"])
	}
	if m["bytes_ratio"] != 0.04 {
		t.Errorf("bytes_ratio = %v, want 0.04", m["bytes_ratio"])
	}
}

func TestUpdateRatioZeroAnnounced(t *testing.T) {
	// expvar is process-global: force the zero case explicitly
	BytesAnnounced.Set(0)
	defer BytesAnnounced.Add(1000) // restore for other tests
	UpdateRatio()
	if BytesRatioSnapshot.Value() != 0 {
		t.Errorf("ratio = %v, want 0", BytesRatioSnapshot.Value())
	}
}
