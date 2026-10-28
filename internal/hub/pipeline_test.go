package hub

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// TestReadsOverlap: several reads go out before the first reply comes
// back. The fake daemon answers nothing until it holds four reads,
// then answers them in order; with one read at a time this would
// never finish.
func TestReadsOverlap(t *testing.T) {
	ann := newAnnounce(60, 1<<20)
	conn, daemonW, daemonR := uploadDaemon(t, ann)
	d, err := (&Bridge{Registry: share.NewRegistry(time.Hour)}).Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	go func() {
		var held []protocol.Read
		for {
			fr, err := protocol.ReadFrame(daemonR)
			if err != nil {
				return
			}
			if fr.Type != protocol.TRead {
				continue // the welcome
			}
			var rd protocol.Read
			json.Unmarshal(fr.JSON, &rd)
			if held = append(held, rd); len(held) < 4 {
				continue
			}
			for _, rd := range held {
				protocol.EncodeData(daemonW, bytes.Repeat([]byte{byte(rd.Offset)}, int(rd.Length)))
			}
			held = nil
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := range 4 {
		wg.Go(func() {
			data, err := d.IssueRead(int64(i), 1000)
			if err == nil && (len(data) != 1000 || data[0] != byte(i) || data[999] != byte(i)) {
				err = errCrossTalk{int64(i), 0}
			}
			errs <- err
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reads never overlapped: the daemon waited for four and got fewer")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestReadErrorFailsOnlyThatRead: a daemon refusing one read fails
// that read; the connection and the reads around it carry on.
func TestReadErrorFailsOnlyThatRead(t *testing.T) {
	ann := newAnnounce(60, 1<<20)
	conn, daemonW, daemonR := uploadDaemon(t, ann)
	d, err := (&Bridge{Registry: share.NewRegistry(time.Hour)}).Accept(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	go func() {
		for {
			fr, err := protocol.ReadFrame(daemonR)
			if err != nil {
				return
			}
			if fr.Type != protocol.TRead {
				continue
			}
			var rd protocol.Read
			json.Unmarshal(fr.JSON, &rd)
			if rd.Offset == 666 {
				protocol.EncodeJSON(daemonW, protocol.TErr, protocol.Err{ShareID: rd.ShareID, Code: "denied", Message: "read out of bounds"})
				continue
			}
			protocol.EncodeData(daemonW, make([]byte, rd.Length))
		}
	}()

	if _, err := d.IssueRead(0, 10); err != nil {
		t.Fatalf("read before: %v", err)
	}
	if _, err := d.IssueRead(666, 10); err == nil || !strings.Contains(err.Error(), "out of bounds") {
		t.Fatalf("refused read: %v, want the daemon's reason", err)
	}
	if data, err := d.IssueRead(20, 10); err != nil || len(data) != 10 {
		t.Fatalf("read after: %d bytes, %v", len(data), err)
	}
}
