package daemon

import (
	"errors"
	"log"
	"math/rand"
	"net"
	"time"
)

// RunConfig drives the share lifetime loop.
type RunConfig struct {
	// Dial returns a fresh relay connection. Returning an error
	// triggers backoff and a retry until the TTL expires.
	Dial func() (net.Conn, error)

	// TTLSec is the share lifetime. Reconnect attempts stop once
	// the deadline has passed.
	TTLSec int64

	// Log is optional; nil means logs are discarded.
	Log *log.Logger
}

// Backoff schedule for reconnects: capped exponential with jitter,
// bounded so a flapping network never turns into a spin loop.
const (
	backoffBase = 500 * time.Millisecond
	backoffCap  = 8 * time.Second
)

// errExpired is the internal "TTL used up" signal: wait turns it into
// a clean return, Run never surfaces it to callers.
var errExpired = errors.New("daemon: share TTL expired")

// Run shares one file until its TTL expires. It connects, announces,
// serves until the connection drops, and redials with backoff until
// the deadline. It returns nil when the TTL is up.
func Run(sh Share, cfg RunConfig) error {
	logf := cfg.Log
	if logf == nil {
		logf = log.New(discard{}, "", 0)
	}

	deadline := time.Now().Add(time.Duration(cfg.TTLSec) * time.Second)
	attempt := 0

	// stop fires at the deadline so a live session ends with its TTL
	// even if the relay never hangs up.
	stop := make(chan struct{})
	time.AfterFunc(time.Until(deadline), func() { close(stop) })

	for {
		if time.Now().After(deadline) {
			return nil
		}

		conn, err := cfg.Dial()
		if err != nil {
			if wait(logf, deadline, attempt, err) {
				return nil
			}
			attempt++
			continue
		}

		sess, err := NewSession(conn, sh, cfg.TTLSec)
		if err != nil {
			conn.Close()
			if wait(logf, deadline, attempt, err) {
				return nil
			}
			attempt++
			continue
		}

		if err := sess.Announce(); err != nil {
			sess.Close()
			if wait(logf, deadline, attempt, err) {
				return nil
			}
			attempt++
			continue
		}

		logf.Printf("share %s live: %s (%d bytes, %s)", sh.ID, sh.Name, sh.Size, sh.MIME)
		_ = sess.Serve(stop)
		sess.Close()

		if time.Now().After(deadline) {
			return nil
		}
		// the relay hung up: a short beat before redial
		if wait(logf, deadline, 0, errors.New("relay closed")) {
			return nil
		}
		attempt = 0
	}
}

// wait sleeps for the current backoff window. It returns true when
// the TTL ran out (now or while sleeping), meaning the share is over.
func wait(logf *log.Logger, deadline time.Time, attempt int, cause error) (expired bool) {
	d := backoffBase << uint(min(attempt, 4))
	if d > backoffCap {
		d = backoffCap
	}
	// jitter: sleep somewhere in [d/2, d)
	d = d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
	remain := time.Until(deadline)
	switch {
	case remain <= 0:
		return true
	case d >= remain:
		time.Sleep(remain)
		return true
	}
	logf.Printf("share reconnect in %s: %v", d, cause)
	time.Sleep(d)
	return false
}

// ErrTTLExpired is kept for callers that want to distinguish a
// TTL-elapsed exit from other conditions; Run itself returns nil
// on expiry.
var ErrTTLExpired = errExpired

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
