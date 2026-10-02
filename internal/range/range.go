package httprange

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Request is one parsed byte range within a resource of a known size.
// Start and End are inclusive and always within [0, size-1] for a
// valid request.
type Request struct {
	Start int64
	End   int64 // inclusive
}

// Length is the number of bytes the range covers.
func (r Request) Length() int64 {
	return r.End - r.Start + 1
}

// ContentRange returns the value for the Content-Range response header,
// e.g. "bytes 1000-2000/12345".
func (r Request) ContentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, size)
}

// Errors returned by Parse. ParseNeverSatisfiableError carries the
// size so the caller can emit "bytes */size" per RFC 9110 §14.2.
var (
	// ErrNoRange means the header is absent or empty: serve 200 full.
	ErrNoRange = errors.New("no range header")
	// ErrMalformed means the header is present but not a single valid
	// byte range: serve 200 full (RFC 9110 §14.2 — ignore malformed).
	ErrMalformed = errors.New("malformed range header")
	// ErrUnsatisfiable means the header is a valid range request but
	// every range starts at or past EOF: serve 416.
	ErrUnsatisfiable = errors.New("range not satisfiable")
)

// Parse turns a Range header value into a single Request against a
// resource of the given size. size must be >= 1.
//
// Per the PRD and RFC 9110 §14.1.2, only single byte ranges are
// honored. Multi-range requests ("bytes=0-1,5-6") fall back to a 200
// full response: ErrMalformed tells the caller to do that gracefully.
//
// Accepted forms:
//
//	bytes=1000-2000   explicit, clamped to size-1
//	bytes=1000-       from 1000 to EOF
//	bytes=-1024       last 1024 bytes; if fewer exist, whole file
func Parse(header string, size int64) (Request, error) {
	if size < 1 {
		return Request{}, ErrUnsatisfiable
	}
	if header == "" {
		return Request{}, ErrNoRange
	}
	rest, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return Request{}, ErrMalformed
	}
	if strings.Contains(rest, ",") {
		return Request{}, ErrMalformed // multi-range: fall back to 200
	}
	startS, endS, hyphen := strings.Cut(rest, "-")
	if !hyphen {
		return Request{}, ErrMalformed
	}

	// suffix form: bytes=-N
	if startS == "" {
		n, err := parseCount(endS)
		if err != nil || n < 1 {
			return Request{}, ErrMalformed
		}
		if n > size {
			n = size
		}
		return Request{Start: size - n, End: size - 1}, nil
	}

	start, err := parseCount(startS)
	if err != nil {
		return Request{}, ErrMalformed
	}
	if start >= size {
		return Request{}, ErrUnsatisfiable
	}

	if endS == "" {
		return Request{Start: start, End: size - 1}, nil
	}
	end, err := parseCount(endS)
	if err != nil || end < start {
		return Request{}, ErrMalformed
	}
	if end >= size {
		end = size - 1
	}
	return Request{Start: start, End: end}, nil
}

func parseCount(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty count")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("bad count")
	}
	return n, nil
}
