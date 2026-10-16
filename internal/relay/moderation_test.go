package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/abuse"
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/share"
)

// moderationServer is a relay with reports and suspensions on, and
// two of mallory's links and one of alice's live.
func moderationServer(t *testing.T, cfg ServerConfig) (*httptest.Server, *share.Registry, *abuse.Suspensions) {
	t.Helper()
	reg := share.NewRegistry(time.Hour)
	soon := time.Now().Add(time.Hour)
	for id, acct := range map[string]string{"mal1": "mallory", "mal2": "mallory", "ali1": "alice"} {
		reg.Register(share.Info{ID: id, Name: id + ".mp4", Size: 3, MIME: "video/mp4", Account: acct, Expires: soon}, &memHandle{data: []byte{1, 2, 3}})
	}
	reports, _ := abuse.NewReports("")
	susp, _ := abuse.NewSuspensions("")
	cfg.Registry, cfg.Reports, cfg.Suspensions, cfg.AdminToken = reg, reports, susp, "sekrit"
	ts := httptest.NewServer(New(cfg))
	t.Cleanup(ts.Close)
	return ts, reg, susp
}

func post(t *testing.T, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func adminGet(t *testing.T, url string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer sekrit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(v)
	return resp.StatusCode
}

// TestReportFlow: a receiver reports a link; the admin list shows it
// with a snapshot of the share. Bad reasons and unknown links are
// refused, and nothing reaches the queue without the admin token.
func TestReportFlow(t *testing.T) {
	ts, _, _ := moderationServer(t, ServerConfig{})

	if resp, _ := post(t, ts.URL+"/mal1/report", "", map[string]string{"reason": "malware", "note": "  fake installer  "}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("report: %d", resp.StatusCode)
	}
	if resp, body := post(t, ts.URL+"/mal1/report", "", map[string]string{"reason": "spam"}); resp.StatusCode != http.StatusBadRequest || body["reasons"] == nil {
		t.Fatalf("bad reason: %d %v", resp.StatusCode, body)
	}
	if resp, _ := post(t, ts.URL+"/nope/report", "", map[string]string{"reason": "other"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown link: %d", resp.StatusCode)
	}

	var list []abuse.Report
	if status := adminGet(t, ts.URL+"/admin/reports", &list); status != http.StatusOK || len(list) != 1 {
		t.Fatalf("admin reports: %d %+v", status, list)
	}
	got := list[0]
	if got.ShareID != "mal1" || got.Reason != "malware" || got.Note != "fake installer" || got.Account != "mallory" || got.Name != "mal1.mp4" {
		t.Fatalf("report snapshot = %+v", got)
	}

	resp, err := http.Get(ts.URL + "/admin/reports")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reports without token: %d", resp.StatusCode)
	}
}

// TestReportRateLimit: reports have their own, stricter per-IP limit.
func TestReportRateLimit(t *testing.T) {
	ts, _, _ := moderationServer(t, ServerConfig{ReportLimiter: ratelimit.New(1.0/60, 2)})
	for i, want := range []int{http.StatusAccepted, http.StatusAccepted, http.StatusTooManyRequests} {
		resp, _ := post(t, ts.URL+"/ali1/report", "", map[string]string{"reason": "other"})
		if resp.StatusCode != want {
			t.Fatalf("report %d: %d, want %d", i+1, resp.StatusCode, want)
		}
		if want == http.StatusTooManyRequests && resp.Header.Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
}

// TestKillAndSuspend: kill -suspend takes the link down, suspends its
// account and kills that account's other links, and leaves others.
func TestKillAndSuspend(t *testing.T) {
	ts, reg, susp := moderationServer(t, ServerConfig{})

	resp, body := post(t, ts.URL+"/admin/kill", "sekrit", map[string]any{"id": "mal1", "suspend": true})
	if resp.StatusCode != http.StatusOK || body["killed"] != true || body["suspended"] != "mallory" {
		t.Fatalf("kill+suspend: %d %v", resp.StatusCode, body)
	}
	if also, _ := body["also_killed"].([]any); len(also) != 1 || also[0] != "mal2" {
		t.Fatalf("also_killed = %v, want [mal2]", body["also_killed"])
	}
	if !susp.Suspended("mallory") {
		t.Fatal("mallory not suspended")
	}
	if _, _, err := reg.Lookup("ali1"); err != nil {
		t.Fatalf("alice's link hit by mallory's takedown: %v", err)
	}

	var list []string
	if adminGet(t, ts.URL+"/admin/suspended", &list); len(list) != 1 || list[0] != "mallory" {
		t.Fatalf("suspended list = %v", list)
	}
	if resp, _ := post(t, ts.URL+"/admin/unsuspend", "sekrit", map[string]string{"account": "mallory"}); resp.StatusCode != http.StatusOK || susp.Suspended("mallory") {
		t.Fatalf("unsuspend: %d", resp.StatusCode)
	}
}

// TestKillSuspendAfterExpiry: when the link is already gone, the
// account comes from its report; with no report there is nothing to
// suspend, and the relay says so instead of guessing.
func TestKillSuspendAfterExpiry(t *testing.T) {
	ts, reg, susp := moderationServer(t, ServerConfig{})

	post(t, ts.URL+"/mal1/report", "", map[string]string{"reason": "illegal"})
	reg.Kill("mal1") // gone before review
	resp, body := post(t, ts.URL+"/admin/kill", "sekrit", map[string]any{"id": "mal1", "suspend": true})
	if resp.StatusCode != http.StatusOK || body["suspended"] != "mallory" || !susp.Suspended("mallory") {
		t.Fatalf("suspend from report: %d %v", resp.StatusCode, body)
	}

	reg.Kill("ali1")
	resp, body = post(t, ts.URL+"/admin/kill", "sekrit", map[string]any{"id": "ali1", "suspend": true})
	if resp.StatusCode != http.StatusConflict || susp.Suspended("alice") {
		t.Fatalf("no account known: %d %v", resp.StatusCode, body)
	}
}
