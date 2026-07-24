package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := store.OpenSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	comps, err := store.OpenComputers(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{RunDir: t.TempDir()}, st, snaps, comps, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestListIsAnEmptyArrayNotNull(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/machines")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	// A JSON null would make clients special-case the empty case.
	if !strings.Contains(string(raw), `"machines":[]`) {
		t.Errorf("empty list encoded as %s", raw)
	}
}

func TestUnknownMachineIsNotFound(t *testing.T) {
	srv := newTestServer(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/machines/nope"},
		{http.MethodDelete, "/v1/machines/nope"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestExecOnUnknownMachineIsNotFound(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v1/machines/nope/exec", "application/json",
		strings.NewReader(`{"cmd":["true"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCreateRejectsAMalformedBody(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v1/machines", "application/json", strings.NewReader(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestErrorsCarryAMessage(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/machines/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" {
		t.Error("error response has no message")
	}
}

func TestMethodsAreRouted(t *testing.T) {
	srv := newTestServer(t)
	// PUT is not a method the control plane offers anywhere.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/machines", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 or 405", resp.StatusCode)
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	snaps, _ := store.OpenSnapshots(t.TempDir())
	comps, _ := store.OpenComputers(t.TempDir())
	s := New(Config{}, st, snaps, comps, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if s.cfg.VCPUs == 0 || s.cfg.MemMiB == 0 || s.cfg.BootTimeout == 0 {
		t.Errorf("zero config left unfilled: %+v", s.cfg)
	}
}
