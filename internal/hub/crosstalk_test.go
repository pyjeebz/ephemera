package hub

import (
	"sync"
	"testing"
	"time"

	"ephemera.run/ephemera/internal/protocol"
	"ephemera.run/ephemera/internal/share"
)

// TestConcurrentReadsNoCrossTalk: two concurrent readers on one
// daemon connection. Every read must return exactly its own bytes.
// Before the per-connection read lock, replies crossed and this
// test failed with wrong bytes; it is the regression test for
// IssueRead serialization.
func TestConcurrentReadsNoCrossTalk(t *testing.T) {
	const size = int64(256 << 10)
	reg := share.NewRegistry(time.Hour)
	_, relayR, relayW, ann := startFakeDaemon(t, size, 3600)

	d, err := (&Bridge{Registry: reg}).Accept(&pipeConn{r: relayR, w: relayW})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer d.Close()

	const readers = 4
	const reads = 50
	var wg sync.WaitGroup
	errCh := make(chan error, readers)

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < reads; i++ {
				// each reader picks distinct offsets: bytes at off
				// must equal byte(off%251) or answers crossed
				off := int64((r*reads + i) * 4096 % int(size-4096))
				data, err := d.IssueRead(off, 4096)
				if err != nil {
					errCh <- err
					return
				}
				if len(data) != 4096 {
					errCh <- errShortRead{off, len(data)}
					return
				}
				for j := range data {
					if data[j] != byte((off+int64(j))%251) {
						errCh <- errCrossTalk{off, off + int64(j)}
						return
					}
				}
			}
		}(r)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
	_ = ann.ShareID
}

type errCrossTalk struct{ want, gotAt int64 }

func (e errCrossTalk) Error() string {
	return "cross-talk: wrong bytes at read"
}

type errShortRead struct {
	off int64
	got int
}

func (e errShortRead) Error() string {
	return "short read"
}

var _ = protocol.MaxBody
