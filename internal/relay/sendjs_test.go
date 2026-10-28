package relay

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// TestWebAppRoutes: the browser sender's page, script and stylesheet
// are served with their types and a policy that lets nothing else run.
func TestWebAppRoutes(t *testing.T) {
	ts := httptest.NewServer(New(ServerConfig{Registry: share.NewRegistry(time.Hour)}))
	defer ts.Close()
	for path, ctype := range map[string]string{
		"/send": "text/html", "/send.js": "text/javascript", "/app.css": "text/css", "/view.js": "text/javascript",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), ctype) {
			t.Errorf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "connect-src 'self'") {
			t.Errorf("%s: CSP %q", path, csp)
		}
	}
}

// TestSendJSSpeaksTheProtocol runs the browser sender's protocol code
// under Node against the Go implementation: the same share id from
// the same owner key, Go's frames parsed by JavaScript (split at odd
// places, as WebSocket messages may be), and JavaScript's frames read
// by Go. Skipped where Node isn't installed.
func TestSendJSSpeaksTheProtocol(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	wantID, _ := protocol.ShareIDFor(base64.RawURLEncoding.EncodeToString(raw))

	var goFrames bytes.Buffer
	protocol.EncodeJSON(&goFrames, protocol.TWelcome, protocol.Welcome{ShareID: wantID, Link: "https://ephemera.run/" + wantID, Upload: true, Have: 42})
	protocol.EncodeJSON(&goFrames, protocol.TRead, protocol.Read{ShareID: wantID, Offset: 1 << 33, Length: 262144})
	protocol.EncodeData(&goFrames, []byte("hello"))
	protocol.EncodeJSON(&goFrames, protocol.TAck, protocol.Ack{ShareID: wantID, Have: 1 << 20})

	script := `
const p = require(process.argv[1]);
(async () => {
	const raw = Uint8Array.from(Buffer.from(process.argv[2], "hex"));
	const id = await p.shareIdFor(raw);
	const r = new p.FrameReader(), all = Buffer.from(process.argv[3], "hex"), got = [];
	for (let i = 0; i < all.length; i += 7) for (const f of r.push(all.subarray(i, i + 7))) got.push(f.json ?? Buffer.from(f.payload).toString());
	const out = Buffer.concat([
		p.jsonFrame(p.T.SEND, {share_id: id, owner_key: p.b64url(raw), name: "a.bin", size: 5, ttl_sec: 60, upload: true}),
		p.jsonFrame(p.T.AWAY, {share_id: id, offset: 0}),
		p.frame(p.T.DATA, new TextEncoder().encode("bytes")),
	]);
	console.log(JSON.stringify({id, got, out: out.toString("hex")}));
})();`
	sendJS, _ := filepath.Abs("receiver/send.js")
	cmd := exec.Command(node, "-e", script, sendJS, hex.EncodeToString(raw), hex.EncodeToString(goFrames.Bytes()))
	outb, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var res struct {
		ID  string
		Got []any
		Out string
	}
	if err := json.Unmarshal(outb, &res); err != nil {
		t.Fatalf("node output %q: %v", outb, err)
	}

	if res.ID != wantID {
		t.Fatalf("share id: JS %s, Go %s", res.ID, wantID)
	}
	if len(res.Got) != 4 || res.Got[2] != "hello" {
		t.Fatalf("JS parsed Go's frames as %v", res.Got)
	}
	if w := res.Got[0].(map[string]any); w["upload"] != true || w["have"] != float64(42) {
		t.Fatalf("welcome = %v", w)
	}
	if rd := res.Got[1].(map[string]any); rd["offset"] != float64(1<<33) || rd["length"] != float64(262144) {
		t.Fatalf("read = %v", rd)
	}

	b, _ := hex.DecodeString(res.Out)
	r := bufio.NewReader(bytes.NewReader(b))
	var send protocol.Send
	fr, err := protocol.ReadFrame(r)
	if err != nil || fr.Type != protocol.TSend || json.Unmarshal(fr.JSON, &send) != nil {
		t.Fatalf("Go read JS's send frame: %+v, %v", fr, err)
	}
	if !protocol.OwnsShare(send.ShareID, send.OwnerKey) || !send.Upload || send.Size != 5 {
		t.Fatalf("send = %+v: the relay would refuse it", send)
	}
	if fr, err := protocol.ReadFrame(r); err != nil || fr.Type != protocol.TAway {
		t.Fatalf("away frame: %+v, %v", fr, err)
	}
	if fr, err := protocol.ReadFrame(r); err != nil || fr.Type != protocol.TData || string(fr.Data) != "bytes" {
		t.Fatalf("data frame: %+v, %v", fr, err)
	}
}
