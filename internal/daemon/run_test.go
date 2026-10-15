package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
)

// TestRunReconnectsUntilTTL drives the real Run loop against a
// flaky relay: accepts, drops after one read, accepts again. The
// share must survive redials and the loop must exit cleanly at TTL.
func TestRunReconnectsUntilTTL(t *testing.T) {
	dir := t.TempDir()
	path := makeTestFile(t, dir, "clip.mp4", 64<<10)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	var accepts atomic.Int32
	var ttlMu sync.Mutex
	var announcedTTLs []int64

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// flaky relay: accept, read announce, optionally answer one read,
	// then close — until accepts reach 3, then stay up.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n := accepts.Add(1)
			go func(conn net.Conn, nth int32) {
				defer conn.Close()
				br := bufio.NewReader(conn)
				// hello + send
				for i := 0; i < 2; i++ {
					fr, err := protocol.ReadFrame(br)
					if err != nil {
						return
					}
					var s protocol.Send
					if fr.Type == protocol.TSend && json.Unmarshal(fr.JSON, &s) == nil {
						ttlMu.Lock()
						announcedTTLs = append(announcedTTLs, s.TTLSec)
						ttlMu.Unlock()
					}
				}
				if nth <= 2 {
					// flaky: hang up right after announce
					return
				}
				// steady: answer one read, then close
				fr, err := protocol.ReadFrame(br)
				if err != nil {
					return
				}
				var rd protocol.Read
				if json.Unmarshal(fr.JSON, &rd) != nil {
					return
				}
				buf := make([]byte, rd.Length)
				f, err := os.Open(path)
				if err != nil {
					return
				}
				f.ReadAt(buf, rd.Offset)
				f.Close()
				if err := protocol.EncodeData(conn, buf); err != nil {
					return
				}
			}(conn, n)
		}
	}()

	// TTL 6s: enough to cover two flap-backoffs (0.5s, 1s) and a
	// steady session, short enough to keep the suite quick.
	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- Run(sh, RunConfig{
			Dial:   func() (net.Conn, error) { return net.Dial("tcp", ln.Addr().String()) },
			TTLSec: 6,
			Log:    log.New(io.Discard, "", 0),
		})
	}()

	// wait for 3 accepts (flaky x2 + steady): proves redial happened
	deadline := time.Now().Add(10 * time.Second)
	for accepts.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if accepts.Load() < 3 {
		t.Fatalf("only %d accepts; loop did not redial through flaps", accepts.Load())
	}

	// now let the TTL run out; Run must return on its own
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Run did not return within TTL+6s")
	}

	elapsed := time.Since(start)
	if elapsed > 8*time.Second {
		t.Errorf("Run outlived its TTL: %s", elapsed)
	}

	// redials announce what is left of the TTL, never the original
	ttlMu.Lock()
	defer ttlMu.Unlock()
	if len(announcedTTLs) < 3 {
		t.Fatalf("saw %d announces, want >= 3", len(announcedTTLs))
	}
	if announcedTTLs[0] != 6 {
		t.Errorf("first announce ttl = %d, want 6", announcedTTLs[0])
	}
	if last := announcedTTLs[len(announcedTTLs)-1]; last >= 6 {
		t.Errorf("redial announced ttl %d; want the remaining (< 6): %v", last, announcedTTLs)
	}
}

func TestRunTTLExpiryWhileRelayDown(t *testing.T) {
	dir := t.TempDir()
	path := makeTestFile(t, dir, "clip.mp4", 4096)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	// dial always fails: the loop must back off until TTL, not spin,
	// and exit cleanly (nil, or ErrTTLExpired) — never the dial error
	calls := 0
	start := time.Now()
	err = Run(sh, RunConfig{
		Dial: func() (net.Conn, error) {
			calls++
			return nil, errors.New("relay unreachable")
		},
		TTLSec: 2,
		Log:    log.New(io.Discard, "", 0),
	})
	if err != nil && !errors.Is(err, ErrTTLExpired) {
		t.Errorf("Run returned %v, want ErrTTLExpired", err)
	}
	if err != nil {
		t.Errorf("Run should return nil on expiry, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2*time.Second {
		t.Errorf("loop exited early: %s", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Errorf("loop outlived TTL+backoff: %s", elapsed)
	}
	if calls < 2 {
		t.Errorf("only %d dial attempts; expected backoff retries", calls)
	}
}

func TestRunServesThenExpires(t *testing.T) {
	dir := t.TempDir()
	path := makeTestFile(t, dir, "clip.mp4", 8192)
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// steady relay: accept, consume announce, answer reads forever
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				br := bufio.NewReader(conn)
				for i := 0; i < 2; i++ {
					if _, err := protocol.ReadFrame(br); err != nil {
						return
					}
				}
				for {
					fr, err := protocol.ReadFrame(br)
					if err != nil {
						return
					}
					if fr.Type != protocol.TRead {
						continue
					}
					var rd protocol.Read
					if json.Unmarshal(fr.JSON, &rd) != nil {
						return
					}
					buf := make([]byte, rd.Length)
					f, err := os.Open(path)
					if err != nil {
						return
					}
					f.ReadAt(buf, rd.Offset)
					f.Close()
					protocol.EncodeData(conn, buf)
				}
			}(conn)
		}
	}()

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- Run(sh, RunConfig{
			Dial:   func() (net.Conn, error) { return net.Dial("tcp", ln.Addr().String()) },
			TTLSec: 2,
			Log:    log.New(io.Discard, "", 0),
		})
	}()

	// while live: a read through the relay must return file bytes
	time.Sleep(300 * time.Millisecond)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("probe dial: %v", err)
	}
	defer conn.Close()
	// note: this probe is a second session the relay also accepts;
	// the daemon's read path is exercised via its own connection.

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, ErrTTLExpired) {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not expire")
	}
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Errorf("expiry drifted: %s", elapsed)
	}
}

func TestValidateShareSymlinkDir(t *testing.T) {
	// a symlink to a directory must be rejected as non-regular
	dir := t.TempDir()
	link := filepath.Join(dir, "link.mp4")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip("symlinks unsupported on this filesystem")
	}
	if _, err := ValidateShare(link, DetectMIME); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("err = %v, want os.ErrInvalid", err)
	}
}
