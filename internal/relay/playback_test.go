package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/playback"
	"ephemera.run/ephemera/internal/share"
)

// TestPlaybackReports: a page's report is filed with the file's
// bitrate and what the relay measured of the sender; bad reports and
// unknown links are turned away; admins get the summary.
func TestPlaybackReports(t *testing.T) {
	body := patterned(1 << 20)
	reg := share.NewRegistry(time.Hour)
	reg.Register(share.Info{ID: "pb", Name: "v.mp4", Size: int64(len(body)), MIME: "video/mp4", Expires: time.Now().Add(time.Hour)}, &memHandle{data: body})
	log, senders := playback.NewLog(10), playback.NewSenders()
	ts := httptest.NewServer(New(ServerConfig{Registry: reg, Playback: log, Senders: senders, AdminToken: "adm"}))
	defer ts.Close()

	// a live read gives the sender a measured rate
	if code, _, _ := get(t, ts.URL+"/pb/file", "bytes=0-65535"); code != 206 {
		t.Fatalf("live read = %d", code)
	}

	post := func(id, js string) int {
		resp, err := http.Post(ts.URL+"/"+id+"/playback", "application/json", strings.NewReader(js))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	good := `{"view":"v1","state":"live","ttff_ms":850,"stalls":1,"stall_ms":1200,"watched_s":30,"duration_s":8}`
	if code := post("pb", good); code != http.StatusNoContent {
		t.Fatalf("good report = %d", code)
	}
	if code := post("pb", `{"view":"v2","state":"live","ttff_ms":-1}`); code != http.StatusBadRequest {
		t.Fatalf("bad report = %d, want 400", code)
	}
	if code := post("nope", good); code != http.StatusNotFound {
		t.Fatalf("unknown link = %d, want 404", code)
	}

	views := log.Views(0)
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if v.Share != "pb" || v.TTFFMs != 850 || v.BitrateBps != int64(len(body))*8/8 || v.SenderBps <= 0 || v.FromRelayPct != 0 {
		t.Fatalf("view = %+v", v)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/admin/playback", nil)
	req.Header.Set("Authorization", "Bearer adm")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Summary playback.Summary
		Views   []playback.View
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Summary.Views != 1 || out.Summary.TTFFMsP50 != 850 || len(out.Views) != 1 {
		t.Fatalf("admin playback = %+v", out)
	}
}

// TestFromRelayShare: bytes served off the upload store count as
// coming from the relay, live reads as coming from the sender.
func TestFromRelayShare(t *testing.T) {
	body := patterned(64 << 10)
	_, reg, st := uploadServer(t, body, 48<<10, &memHandle{data: body})
	senders := playback.NewSenders()
	ts := httptest.NewServer(New(ServerConfig{Registry: reg, Uploads: st, Senders: senders, ChunkSize: 16 << 10}))
	defer ts.Close()
	if code, data, _ := get(t, ts.URL+"/up/file", ""); code != 200 || len(data) != len(body) {
		t.Fatalf("full read = %d, %d bytes", code, len(data))
	}
	if bps, rel := senders.Stats("up"); rel != 75 || bps <= 0 {
		t.Fatalf("stats = %d bps, %d%% from relay; want 75%% and a live rate", bps, rel)
	}
}
