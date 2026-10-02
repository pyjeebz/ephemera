package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func roundTrip(t *testing.T, typ uint8, payload []byte) *Frame {
	t.Helper()
	var buf bytes.Buffer
	if typ == TData {
		if err := EncodeData(&buf, payload); err != nil {
			t.Fatalf("EncodeData: %v", err)
		}
	} else {
		if err := writeFrame(&buf, typ, payload); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}
	f, err := ReadFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	return f
}

func TestRoundTripJSON(t *testing.T) {
	s := Send{ShareID: "abc123", Name: "video.mp4", Size: 5 << 30, MIME: "video/mp4", TTLSec: 3600}
	payload, _ := json.Marshal(s)
	f := roundTrip(t, TSend, payload)
	var got Send
	if err := json.Unmarshal(f.JSON, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != s {
		t.Errorf("got %+v, want %+v", got, s)
	}
}

func TestRoundTripData(t *testing.T) {
	data := []byte("raw file bytes, not JSON")
	f := roundTrip(t, TData, data)
	if !bytes.Equal(f.Data, data) {
		t.Errorf("data round trip mismatch: %q", f.Data)
	}
	if f.JSON != nil {
		t.Errorf("TData must not set JSON field")
	}
}

func TestZeroLengthFrame(t *testing.T) {
	f := roundTrip(t, THello, nil)
	if f.Type != THello || f.JSON != nil || f.Data != nil {
		t.Errorf("zero-length frame: %+v", f)
	}
}

func TestBadMagic(t *testing.T) {
	var buf bytes.Buffer
	_ = writeFrame(&buf, TSend, []byte("{}"))
	buf.Bytes()[0] = 'X' // corrupt magic
	_, err := ReadFrame(bufio.NewReader(&buf))
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("err = %v, want ErrBadMagic", err)
	}
}

func TestTooLarge(t *testing.T) {
	huge := bytes.Repeat([]byte{0}, maxBody+1)
	if err := EncodeData(&bytes.Buffer{}, huge); err == nil {
		t.Error("EncodeData oversized: no error")
	}
	var buf bytes.Buffer
	_ = writeFrame(&buf, TData, []byte{})
	b := buf.Bytes()
	b[5] = 0xFF // little-endian len > maxBody: 0xFFFFFFxx
	b[6] = 0xFF
	b[7] = 0xFF
	b[8] = 0x7F // keep it > maxBody but < uint32 overflow
	if _, err := ReadFrame(bufio.NewReader(&buf)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestUnknownType(t *testing.T) {
	var buf bytes.Buffer
	_ = writeFrame(&buf, 99, []byte("{}"))
	if _, err := ReadFrame(bufio.NewReader(&buf)); !errors.Is(err, ErrUnknownType) {
		t.Errorf("err = %v, want ErrUnknownType", err)
	}
}
