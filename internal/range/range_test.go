package httprange

import (
	"errors"
	"testing"
)

// PRD §9 acceptance criteria 1-8, range handling, as pure tests
// against a 10-byte resource: "0123456789".
const size = int64(10)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		size    int64
		want    Request
		wantErr error
	}{
		// 1. bytes=1000-2000 -> 206, clamped to EOF
		{name: "explicit in bounds", header: "bytes=1-3", size: size, want: Request{1, 3}},
		{name: "explicit end past EOF clamps", header: "bytes=8-99", size: size, want: Request{8, 9}},
		{name: "explicit whole file", header: "bytes=0-9", size: size, want: Request{0, 9}},

		// 2. bytes=1000- -> 206 to EOF
		{name: "open end to EOF", header: "bytes=4-", size: size, want: Request{4, 9}},
		{name: "open end from zero", header: "bytes=0-", size: size, want: Request{0, 9}},

		// 3. bytes=-1024 -> last 1024 bytes
		{name: "suffix shorter than file", header: "bytes=-4", size: size, want: Request{6, 9}},
		{name: "suffix equals file", header: "bytes=-10", size: size, want: Request{0, 9}},
		{name: "suffix longer than file gives whole file", header: "bytes=-1024", size: size, want: Request{0, 9}},

		// 4. no Range -> 200 + full length
		{name: "absent header", header: "", size: size, wantErr: ErrNoRange},

		// 7. start >= size -> 416
		{name: "start at EOF", header: "bytes=10-", size: size, wantErr: ErrUnsatisfiable},
		{name: "start past EOF", header: "bytes=999999-", size: size, wantErr: ErrUnsatisfiable},
		{name: "explicit start past EOF", header: "bytes=11-20", size: size, wantErr: ErrUnsatisfiable},

		// 8. multi-range -> graceful 200
		{name: "multi-range falls back to 200", header: "bytes=0-1,5-6", size: size, wantErr: ErrMalformed},

		// malformed: ignore, serve 200
		{name: "wrong unit", header: "items=0-1", size: size, wantErr: ErrMalformed},
		{name: "no hyphen", header: "bytes=3", size: size, wantErr: ErrMalformed},
		{name: "reversed", header: "bytes=5-2", size: size, wantErr: ErrMalformed},
		{name: "garbage numbers", header: "bytes=a-b", size: size, wantErr: ErrMalformed},
		{name: "double hyphen", header: "bytes=1--3", size: size, wantErr: ErrMalformed},
		{name: "empty suffix count", header: "bytes=-", size: size, wantErr: ErrMalformed},
		{name: "zero suffix count", header: "bytes=-0", size: size, wantErr: ErrMalformed},
		{name: "whitespace", header: "bytes=1 - 3", size: size, wantErr: ErrMalformed},

		// zero-size resource: any range is unsatisfiable
		{name: "zero size file no range", header: "", size: 0, wantErr: ErrUnsatisfiable},
		{name: "zero size file with range", header: "bytes=0-", size: 0, wantErr: ErrUnsatisfiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.header, tt.size)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Parse(%q, %d) err = %v, want %v", tt.header, tt.size, err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("Parse(%q, %d) unexpected err: %v", tt.header, tt.size, err)
			case got != tt.want:
				t.Fatalf("Parse(%q, %d) = %+v, want %+v", tt.header, tt.size, got, tt.want)
			}
		})
	}
}

func TestRequestHelpers(t *testing.T) {
	r := Request{Start: 1000, End: 2000}
	if r.Length() != 1001 {
		t.Errorf("Length() = %d, want 1001", r.Length())
	}
	if got := r.ContentRange(12345); got != "bytes 1000-2000/12345" {
		t.Errorf("ContentRange(12345) = %q", got)
	}
}
