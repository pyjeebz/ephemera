package cli_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// selfSigned writes a certificate for 127.0.0.1 and localhost and
// returns its files plus a pool that trusts it.
func selfSigned(t *testing.T) (certPath, keyPath string, roots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ephemera test relay"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	cert, _ := x509.ParseCertificate(der)
	roots = x509.NewCertPool()
	roots.AddCert(cert)
	return certPath, keyPath, roots
}

// TestBinariesTLS runs the relay with a certificate and shares over
// wss://: the sender verifies the relay (trusting the test CA through
// SSL_CERT_FILE) and receivers fetch over https.
func TestBinariesTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary E2E")
	}
	if runtime.GOOS != "linux" {
		t.Skip("SSL_CERT_FILE trust injection is Linux-only")
	}
	ephemeraBin, relayBin := buildBinaries(t)
	certPath, keyPath, roots := selfSigned(t)

	video := filepath.Join(t.TempDir(), "video.mp4")
	content := make([]byte, 256<<10)
	for i := range content {
		content[i] = byte(i % 251)
	}
	os.WriteFile(video, content, 0o644)

	tokensFile := filepath.Join(t.TempDir(), "tokens")
	out, err := exec.Command(relayBin, "token", "-file", tokensFile, "tls").Output()
	if err != nil {
		t.Fatalf("relay token: %v", err)
	}
	token := strings.TrimSpace(string(out))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	relayProc := exec.Command(relayBin, "serve", "-addr", addr, "-tokens", tokensFile, "-tls-cert", certPath, "-tls-key", keyPath)
	relayLog, _ := os.CreateTemp(t.TempDir(), "relay.log")
	relayProc.Stderr = relayLog
	if err := relayProc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { relayProc.Process.Kill(); relayProc.Wait() }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 5 * time.Second}
	base := "https://" + addr
	waitFor(t, 10*time.Second, func() bool {
		resp, err := client.Get(base + "/")
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	}, relayLog.Name())

	sendProc := exec.Command(ephemeraBin, "send", "-relay", "wss://"+addr+"/daemon", "-ttl", "60", video)
	sendProc.Env = append(os.Environ(), "SSL_CERT_FILE="+certPath, "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir(), "EPHEMERA_TOKEN="+token)
	sendLog, _ := os.CreateTemp(t.TempDir(), "send.log")
	sendProc.Stderr = sendLog
	if err := sendProc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sendProc.Process.Kill(); sendProc.Wait() }()

	link, shareID := waitForLink(t, sendLog.Name())
	if link != base+"/"+shareID {
		t.Fatalf("link = %q, want an https link under %s", link, base)
	}

	// live over https once the daemon's wss announce lands
	waitFor(t, 10*time.Second, func() bool {
		resp, err := client.Get(base + "/" + shareID + "/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var st struct{ State string }
		json.NewDecoder(resp.Body).Decode(&st)
		return st.State == "live"
	}, sendLog.Name())

	req, _ := http.NewRequest("GET", base+"/"+shareID+"/file", nil)
	req.Header.Set("Range", "bytes=1000-1999")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(data) != string(content[1000:2000]) {
		t.Fatalf("range over https: %d, %d bytes", resp.StatusCode, len(data))
	}
}

// waitFor polls cond until it holds or the timeout passes, then fails
// with the log at logPath.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, logPath string) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	b, _ := os.ReadFile(logPath)
	t.Fatal(fmt.Sprintf("timed out; log:\n%s", b))
}
