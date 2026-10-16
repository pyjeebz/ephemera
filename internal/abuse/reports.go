// Package abuse holds the relay's moderation state: reports that
// receivers file against links, and the sender accounts an operator
// has suspended.
//
// Both outlive the links they are about (links expire in hours;
// review can take longer), so both can persist to a file. Neither
// holds file bytes or anything about the reporter.
package abuse

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"time"
	"unicode/utf8"
)

// Reasons a receiver can pick when reporting a link.
var Reasons = []string{"illegal", "copyright", "malware", "harassment", "other"}

// MaxNote caps a report's free-text note, in characters.
const MaxNote = 1000

// maxReports bounds what a relay keeps in memory; the oldest drop
// first. The file, when set, keeps everything.
const maxReports = 10000

// ErrBadReason and ErrNoteTooLong are the ways a report is refused.
var (
	ErrBadReason   = errors.New("abuse: unknown report reason")
	ErrNoteTooLong = errors.New("abuse: report note too long")
)

// Report is one receiver's report against a link, with a snapshot of
// the share as it was, since the link may expire before review.
type Report struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	ShareID string    `json:"share_id"`
	Reason  string    `json:"reason"`
	Note    string    `json:"note,omitempty"`

	// snapshot of the share at report time
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	MIME    string `json:"mime"`
	Account string `json:"account,omitempty"` // empty on open relays
}

// Reports is the relay's report queue. The zero value is not usable;
// call NewReports.
type Reports struct {
	mu      sync.Mutex
	reports []Report // oldest first
	path    string   // JSONL file; "" = memory only
}

// NewReports opens the queue. With a path, existing reports load from
// it and new ones are appended; with "", reports live in memory only.
func NewReports(path string) (*Reports, error) {
	r := &Reports{path: path}
	if path == "" {
		return r, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		var rep Report
		if json.Unmarshal(sc.Bytes(), &rep) == nil && rep.ID != "" {
			r.reports = append(r.reports, rep)
		}
	}
	if len(r.reports) > maxReports {
		r.reports = r.reports[len(r.reports)-maxReports:]
	}
	return r, sc.Err()
}

// Add validates and files a report, stamping its id and time.
func (r *Reports) Add(rep Report) (Report, error) {
	if !slices.Contains(Reasons, rep.Reason) {
		return Report{}, ErrBadReason
	}
	if utf8.RuneCountInString(rep.Note) > MaxNote {
		return Report{}, ErrNoteTooLong
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Report{}, err
	}
	rep.ID = "rep_" + hex.EncodeToString(b[:])
	rep.At = time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path != "" {
		line, err := json.Marshal(rep)
		if err != nil {
			return Report{}, err
		}
		if err := appendLine(r.path, line); err != nil {
			return Report{}, err
		}
	}
	r.reports = append(r.reports, rep)
	if len(r.reports) > maxReports {
		r.reports = r.reports[1:]
	}
	return rep, nil
}

// List returns up to limit reports, newest first (limit <= 0: all).
func (r *Reports) List(limit int) []Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.reports)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Report, 0, limit)
	for i := n - 1; i >= n-limit; i-- {
		out = append(out, r.reports[i])
	}
	return out
}

// AccountFor returns the account behind a share id from its newest
// report, for takedowns after the link has already expired.
func (r *Reports) AccountFor(shareID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.reports) - 1; i >= 0; i-- {
		if r.reports[i].ShareID == shareID {
			return r.reports[i].Account
		}
	}
	return ""
}

// appendLine appends one line to path, creating it private to the
// relay's user.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
