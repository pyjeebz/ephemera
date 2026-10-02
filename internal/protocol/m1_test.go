package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	rangepkg "ephemera.run/ephemera/internal/range"
)

// TestM1FiveGBRangeOverFakeBridge is the phase-3 gate from the PRD:
// a 5GB file, a Range request served over the daemon<->relay framing,
// and a byte-exact 206 response.
//
// The file is sparse (os.Truncate): 5GB of addressable offsets, near
// zero disk. The relay asks for bounded chunks (the real relay will
// too — it never buffers an unbounded range), so the test verifies
// each range byte-exactly without materializing 5GB in RAM.
//
// Bridge wiring (os.Pipe pairs, same shape the real WS will have):
//
//	relay -> daemon: relayW writes, daemon reads daemonR
//	daemon -> relay: daemonW writes, relay reads relayR
func TestM1FiveGBRangeOverFakeBridge(t *testing.T) {
	const size = int64(5 << 30) // 5GB, the PRD abuse cap

	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	// Sparse markers: one byte per MiB so content is non-uniform and
	// offset shifts are detectable. 5120 one-byte writes -> ~20MB
	// real disk on ext4, the rest of the 5GB stays sparse.
	const miB = int64(1 << 20)
	markerAt := func(off int64) byte { return byte(off / miB % 251) }
	for off := int64(0); off < size; off += miB {
		if _, err := f.WriteAt([]byte{markerAt(off)}, off); err != nil {
			t.Fatal(err)
		}
	}

	const shareID = "share_m1"

	relayR, daemonW, err := os.Pipe() // daemon -> relay
	if err != nil {
		t.Fatal(err)
	}
	daemonR, relayW, err := os.Pipe() // relay -> daemon
	if err != nil {
		t.Fatal(err)
	}
	defer relayR.Close()
	defer relayW.Close()
	defer daemonR.Close()
	defer daemonW.Close()

	daemonIn := bufio.NewReader(daemonR)

	// byteAt reproduces the file's content at offset i: markers every
	// MiB (sparse markers section above), zeros between them.
	byteAt := func(i int64) byte {
		if i%miB == 0 {
			return markerAt(i)
		}
		return 0
	}

	// fake daemon: one Read in, chunked Data out. Same chunking the
	// real daemon will use — every TData <= maxBody.
	serveOne := func() error {
		fr, err := ReadFrame(daemonIn)
		if err != nil {
			return err
		}
		if fr.Type != TRead {
			return fmt.Errorf("expected TRead, got type %d", fr.Type)
		}
		var rd Read
		if err := json.Unmarshal(fr.JSON, &rd); err != nil {
			return err
		}
		if rd.ShareID != shareID {
			return EncodeJSON(daemonW, TErr, Err{ShareID: rd.ShareID, Code: "gone", Message: "unknown share"})
		}
		if rd.Offset < 0 || rd.Length < 1 || rd.Offset+rd.Length > size {
			return EncodeJSON(daemonW, TErr, Err{ShareID: rd.ShareID, Code: "denied", Message: "read out of bounds"})
		}
		buf := make([]byte, rd.Length)
		if _, err := f.ReadAt(buf, rd.Offset); err != nil {
			return err
		}
		for off := 0; off < len(buf); off += maxBody {
			end := off + maxBody
			if end > len(buf) {
				end = len(buf)
			}
			if err := EncodeData(daemonW, buf[off:end]); err != nil {
				return err
			}
		}
		return nil
	}

	done := make(chan error, 1)
	go func() {
		for {
			if err := serveOne(); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		relayW.Close()
		daemonW.Close()
		<-done
	}()

	// relay side: parse a Range header, ask the daemon for that
	// range (bounded to askCap chunks), concatenate TData frames.
	const askCap = int64(maxBody * 4)
	relayOut := bufio.NewReader(relayR)
	relayRead := func(header string) (rangepkg.Request, []byte, error) {
		req, err := rangepkg.Parse(header, size)
		if err != nil {
			return req, nil, err
		}
		askLen := req.Length()
		if askLen > askCap {
			askLen = askCap
		}
		if err := EncodeJSON(relayW, TRead, Read{ShareID: shareID, Offset: req.Start, Length: askLen}); err != nil {
			return req, nil, err
		}
		var out []byte
		for int64(len(out)) < askLen {
			fr, err := ReadFrame(relayOut)
			if err != nil {
				return req, nil, err
			}
			if fr.Type != TData {
				return req, nil, fmt.Errorf("expected TData, got type %d", fr.Type)
			}
			out = append(out, fr.Data...)
		}
		return req, out, nil
	}

	// PRD §9 #1: bytes=1000-2000 -> 206, body == file[1000:2001]
	req, data, err := relayRead("bytes=1000-2000")
	if err != nil {
		t.Fatalf("relayRead: %v", err)
	}
	if len(data) != 1001 {
		t.Fatalf("len = %d, want 1001", len(data))
	}
	for i, b := range data {
		if want := byteAt(req.Start + int64(i)); b != want {
			t.Fatalf("byte %d = %d, want %d", req.Start+int64(i), b, want)
		}
	}
	if got := req.ContentRange(size); got != "bytes 1000-2000/5368709120" {
		t.Errorf("ContentRange = %q", got)
	}

	// PRD §9 #2: bytes=1000- -> 206 to EOF. The relay asks in bounded
	// chunks; verify the first 4 chunks byte-exactly against content.
	req, data, err = relayRead("bytes=1000-")
	if err != nil {
		t.Fatalf("relayRead open-end: %v", err)
	}
	if int64(len(data)) != askCap {
		t.Fatalf("open-end chunk len = %d, want %d", len(data), askCap)
	}
	h := crc32.NewIEEE()
	for i := int64(0); i < askCap; i++ {
		h.Write([]byte{byteAt(1000 + i)})
	}
	if got := crc32.ChecksumIEEE(data); got != h.Sum32() {
		t.Errorf("open-end crc mismatch: got %08x, want %08x", got, h.Sum32())
	}

	// PRD §9 #3: bytes=-1024 -> last 1024 bytes
	req, data, err = relayRead("bytes=-1024")
	if err != nil {
		t.Fatalf("relayRead suffix: %v", err)
	}
	if len(data) != 1024 {
		t.Fatalf("suffix len = %d, want 1024", len(data))
	}
	for i, b := range data {
		if want := byteAt(req.Start + int64(i)); b != want {
			t.Fatalf("suffix byte %d = %d, want %d", req.Start+int64(i), b, want)
		}
	}

	// Edge: the very last byte of the 5GB cap.
	_, data, err = relayRead(fmt.Sprintf("bytes=%d-", size-1))
	if err != nil {
		t.Fatalf("relayRead last byte: %v", err)
	}
	if len(data) != 1 || data[0] != byteAt(size-1) {
		t.Errorf("last byte = %v", data)
	}

	// Edge: out-of-bounds read is refused by the daemon side.
	if err := EncodeJSON(relayW, TRead, Read{ShareID: shareID, Offset: size - 4, Length: 8}); err != nil {
		t.Fatal(err)
	}
	fr, err := ReadFrame(relayOut)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Type != TErr {
		t.Errorf("expected TErr for out-of-bounds read, got type %d", fr.Type)
	}
}
