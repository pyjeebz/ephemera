package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/share"
)

// TestFileSafetyHeaders: plain media plays inline, anything that could
// run in a browser downloads instead under a sandbox policy, every
// file response carries nosniff, and the file name survives for
// curl -OJ.
func TestFileSafetyHeaders(t *testing.T) {
	reg := share.NewRegistry(time.Hour)
	ts := httptest.NewServer(New(ServerConfig{Registry: reg}))
	defer ts.Close()
	for id, tc := range map[string]struct {
		name, mime, want string
	}{
		"vid": {"clip.mp4", "video/mp4", `inline; filename=clip.mp4`},
		"txt": {"notes.txt", "text/plain; charset=utf-8", `inline; filename=notes.txt`},
		"htm": {"login.html", "text/html", `attachment; filename=login.html`},
		"svg": {"logo.svg", "image/svg+xml", `attachment; filename=logo.svg`},
		"bin": {"tool.sh", "application/x-sh", `attachment; filename=tool.sh`},
		"dir": {"../../etc/passwd", "application/octet-stream", `attachment; filename=passwd`},
		"utf": {"café menu.pdf", "application/pdf", `inline; filename*=utf-8''caf%C3%A9%20menu.pdf`},
		"spc": {"two words.tar.gz", "application/gzip", `attachment; filename="two words.tar.gz"`},
	} {
		body := []byte("0123456789")
		reg.Register(share.Info{ID: id, Name: tc.name, Size: int64(len(body)), MIME: tc.mime, Expires: time.Now().Add(time.Hour)}, &memHandle{data: body})
		resp, err := http.Get(ts.URL + "/" + id + "/file")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		if got := h.Get("Content-Disposition"); got != tc.want {
			t.Errorf("%s: Content-Disposition %q, want %q", tc.name, got, tc.want)
		}
		wantCSP := ""
		if strings.HasPrefix(tc.want, "attachment") {
			wantCSP = "sandbox"
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != wantCSP {
			t.Errorf("%s: nosniff %q, CSP %q (want %q)", tc.name, h.Get("X-Content-Type-Options"), h.Get("Content-Security-Policy"), wantCSP)
		}
	}
}
