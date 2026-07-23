package vsock

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMux imitates Firecracker's vsock multiplexer: it reads a CONNECT line and
// answers OK or an error, then behaves as a byte pipe to the "guest".
func startFakeMux(t *testing.T, reply string, thenSend string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vsock")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "v.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	})

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
					return
				}
				// Reply and payload go out together, which is exactly the case
				// that loses data if the handshake reader is discarded.
				_, _ = io.WriteString(c, reply+thenSend)
				time.Sleep(50 * time.Millisecond)
			}(c)
		}
	}()
	return sock
}

func TestDialCompletesTheHandshake(t *testing.T) {
	sock := startFakeMux(t, "OK 12345\n", "")

	conn, err := Dial(sock, 1024, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
}

func TestDialSendsTheRequestedPort(t *testing.T) {
	dir, err := os.MkdirTemp("", "vsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "v.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
		_, _ = io.WriteString(c, "OK 1\n")
	}()

	conn, err := Dial(sock, 4242, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	select {
	case line := <-got:
		if strings.TrimSpace(line) != "CONNECT 4242" {
			t.Errorf("sent %q, want %q", strings.TrimSpace(line), "CONNECT 4242")
		}
	case <-time.After(time.Second):
		t.Fatal("multiplexer never received a CONNECT")
	}
}

func TestDialKeepsDataThatArrivedWithTheHandshake(t *testing.T) {
	// The reply and the first guest bytes land in one read. Reading the OK line
	// with a buffered reader consumes them too, so that reader has to stay
	// attached to the connection — otherwise this payload is silently lost.
	const payload = `{"kind":"stdout","data":"aGk="}` + "\n"
	sock := startFakeMux(t, "OK 12345\n", payload)

	conn, err := Dial(sock, 1024, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read after handshake: %v", err)
	}
	if string(buf) != payload {
		t.Errorf("got %q, want %q", buf, payload)
	}
}

func TestDialReportsARefusedGuestPort(t *testing.T) {
	// Firecracker answers with an error line when no guest is listening — the
	// usual case being the agent not up yet.
	sock := startFakeMux(t, "ERROR Connection refused\n", "")

	_, err := Dial(sock, 1024, time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestDialFailsWhenTheSocketIsAbsent(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "nope.sock"), 1024, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error")
	}
}
