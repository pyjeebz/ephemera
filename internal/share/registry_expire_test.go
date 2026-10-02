package share

import (
	"testing"
	"time"
)

func TestOnExpireFiresOnSweepAndKill(t *testing.T) {
	r := NewRegistry(time.Hour)

	fired := make(chan string, 4)
	r.OnExpire(func(id string) { fired <- id })

	// sweep path
	r.Register(Info{ID: "dead", Expires: time.Now().Add(-time.Second)}, &fakeHandle{})
	r.Register(Info{ID: "alive", Expires: time.Now().Add(time.Hour)}, &fakeHandle{})
	r.Sweep()

	select {
	case id := <-fired:
		if id != "dead" {
			t.Errorf("fired %q, want dead", id)
		}
	case <-time.After(time.Second):
		t.Fatal("OnExpire did not fire on sweep")
	}

	// kill path
	r.Kill("alive")
	select {
	case id := <-fired:
		if id != "alive" {
			t.Errorf("fired %q, want alive", id)
		}
	case <-time.After(time.Second):
		t.Fatal("OnExpire did not fire on kill")
	}
}
