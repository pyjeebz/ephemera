package bench

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveLoadPrint(t *testing.T) {
	old := &Report{Version: 1, Commit: "aaaaaaaaaaaa", Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Results: []Result{{Name: Restore, Unit: "ms", Value: 60}, {Name: ColdBoot, Unit: "ms", Value: 900}}}
	path := filepath.Join(t.TempDir(), "old.json")
	if err := Save(old, path); err != nil {
		t.Fatal(err)
	}
	prev, err := Load(path)
	if err != nil || prev.Commit != old.Commit || len(prev.Results) != 2 {
		t.Fatalf("load: %+v %v", prev, err)
	}

	now := &Report{Version: 1, Commit: "bbbbbbbbbbbb", Date: time.Now(), Results: []Result{
		{Name: Restore, Unit: "ms", Value: 75, P90: 80, Max: 90, N: 10},
		{Name: ColdBoot, Unit: "ms", Value: 1350, N: 10},
		{Name: DensityMemory, Unit: "MB", Value: 59.5, N: 20},
	}}
	if c, ok := Change(now.Results[0], prev); !ok || c != 25 {
		t.Errorf("change = %v %v, want +25%%", c, ok)
	}
	var out bytes.Buffer
	Print(&out, now, prev)
	for _, want := range []string{"commit bbbbbbb", "compared with commit aaaaaaa", "Restore from snapshot", "75 ms", "+25%", "1.35 s", "+50%", "59.5 MB", "new"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPercentiles(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 10; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	r := timing("x", d, "")
	if r.Value != 5 || r.P90 != 9 || r.Max != 10 || r.N != 10 {
		t.Errorf("timing = %+v", r)
	}
}
