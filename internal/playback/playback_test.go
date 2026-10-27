package playback

import (
	"reflect"
	"testing"
	"time"
)

func rep(view string, ttff int64) Report {
	return Report{View: view, State: "live", TTFFMs: ttff, WatchedS: 60, DurationS: 120}
}

func TestValid(t *testing.T) {
	if !rep("v1", 800).Valid() {
		t.Fatal("a plain report is invalid")
	}
	for name, r := range map[string]Report{
		"no view":        {State: "live", TTFFMs: 1},
		"unknown state":  {View: "v", State: "weird", TTFFMs: 1},
		"never played":   {View: "v", State: "live"},
		"negative stall": {View: "v", State: "live", TTFFMs: 1, StallMs: -5},
		"absurd ttff":    {View: "v", State: "live", TTFFMs: 1 << 40},
		"long view id":   {View: string(make([]byte, 65)), State: "live", TTFFMs: 1},
	} {
		if r.Valid() {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLogReplacesAndCaps(t *testing.T) {
	l := NewLog(3)
	var seen int
	l.OnAdd = func(View) { seen++ }
	l.Add(View{Report: rep("a", 100), Share: "s"})
	l.Add(View{Report: rep("b", 200), Share: "s"})
	l.Add(View{Report: rep("a", 150), Share: "s"}) // same page, later report
	if got := l.Views(0); len(got) != 2 || got[0].View != "a" || got[0].TTFFMs != 150 {
		t.Fatalf("views = %+v, want a's later report first", got)
	}
	l.Add(View{Report: rep("c", 1), Share: "s"})
	l.Add(View{Report: rep("d", 1), Share: "s"})
	if got := l.Views(0); len(got) != 3 || got[2].View != "a" {
		t.Fatalf("views = %+v, want the 3 newest with a oldest", got)
	}
	if seen != 5 {
		t.Fatalf("OnAdd saw %d views, want 5", seen)
	}
}

func TestSummary(t *testing.T) {
	l := NewLog(100)
	for i, ttff := range []int64{100, 200, 300, 400, 1000} {
		v := View{Report: rep(string(rune('a'+i)), ttff), Share: "s", BitrateBps: 4e6}
		switch i {
		case 0:
			v.Stalls, v.StallMs = 2, 3000 // 3s stalled over 60s watched
			v.SenderBps = 2e6             // slower than the file plays
		case 1:
			v.Seeks, v.SeekWaitMs = 2, 1000
			v.SenderBps = 8e6
		}
		l.Add(v)
	}
	s := l.Summary()
	want := Summary{
		Views: 5, TTFFMsP50: 300, TTFFMsP90: 1000,
		StallFreePct: 80, RebufferPct: 1.0, // 3s of 303s
		SeekWaitMsP50: 500,
		BitrateBpsP50: 4e6, SenderBpsP50: 2e6,
		SlowSenderPct: 50,
		ByState:       map[string]int{"live": 5},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("summary =\n %+v\nwant\n %+v", s, want)
	}
	if empty := NewLog(1).Summary(); empty.Views != 0 || empty.TTFFMsP50 != 0 {
		t.Fatalf("empty summary = %+v", empty)
	}
}

func TestSenders(t *testing.T) {
	s := NewSenders()
	s.Live("x", 1<<20, time.Second) // 1 MiB in a second
	s.Live("x", 1<<20, time.Second)
	s.FromRelay("x", 6<<20)
	bps, rel := s.Stats("x")
	if bps != 8<<20 || rel != 75 {
		t.Fatalf("stats = %d bps, %d%% from relay; want %d, 75", bps, rel, 8<<20)
	}
	s.Drop("x")
	if bps, rel := s.Stats("x"); bps != 0 || rel != 0 {
		t.Fatal("dropped link still has stats")
	}
}
