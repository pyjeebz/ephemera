package client

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/record"
)

// hostClients are HTTP clients for hosts, one per host key, so each keeps
// its connections open between calls.
var hostClients sync.Map // fingerprint -> *http.Client

// hostKey works out the key a host must present: the one the central
// server says it joined with, checked against the pin. A host seen for the
// first time is pinned (and OnNewHost told) unless pin is false; in strict
// mode it is refused instead.
func (c *Client) hostKey(id, name, key string, pin bool) (string, error) {
	if key == "" {
		return "", fmt.Errorf("the central server didn't say which key host %s has; it may be older than this CLI", name)
	}
	fp, err := record.Fingerprint(key)
	if err != nil {
		return "", fmt.Errorf("host %s: %w", name, err)
	}
	if c.Pins == nil {
		return fp, nil
	}
	if p, ok := c.Pins.Get(id); ok {
		if p.Fingerprint != fp {
			return "", &HostKeyChangedError{Name: name, Pinned: p.Fingerprint, Now: fp}
		}
		return fp, nil
	}
	if c.Strict {
		return "", fmt.Errorf("%w %s (%s): EPHEMERA_STRICT_HOSTS is set; pin it first with: ephemera hosts trust %s", ErrUnknownHost, name, fp, name)
	}
	if !pin {
		return fp, nil
	}
	if err := c.Pins.Set(id, Pin{Name: name, Fingerprint: fp, FirstSeen: time.Now().UTC()}); err != nil {
		return "", fmt.Errorf("pinning host %s: %w", name, err)
	}
	if c.OnNewHost != nil {
		c.OnNewHost(name, fp)
	}
	return fp, nil
}

// hostHTTP is the HTTP client for talking to a host whose key has the
// fingerprint fp. It speaks TLS 1.3 and HTTP/1.1 (ports and exec upgrade
// the connection), and accepts the host only if its certificate carries
// exactly that key. Names and certificate authorities play no part.
func hostHTTP(fp string) *http.Client {
	if hc, ok := hostClients.Load(fp); ok {
		return hc.(*http.Client)
	}
	t := upgradeTransport.Clone()
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"http/1.1"},
		InsecureSkipVerify: true, // checked below, against the pinned key
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the host sent no certificate")
			}
			got, err := record.FingerprintKey(cs.PeerCertificates[0].PublicKey)
			if err != nil {
				return err
			}
			if got != fp {
				return fmt.Errorf("the host's TLS key is %s, not %s, the key it joined with; something may be between you and the host", got, fp)
			}
			return nil
		},
	}
	hc, _ := hostClients.LoadOrStore(fp, &http.Client{Transport: t})
	return hc.(*http.Client)
}

// forHost returns the HTTP client for a sandbox's host, pinning the host
// on first use.
func (c *Client) forHost(sb *api.Sandbox) (*http.Client, error) {
	if !strings.HasPrefix(sb.HostURL, "https://") {
		return nil, fmt.Errorf("host %s's address %s isn't https; hosts serve TLS with their own key, so set its --url to https://", sb.Host, sb.HostURL)
	}
	fp, err := c.hostKey(sb.HostID, sb.Host, sb.HostKey, true)
	if err != nil {
		return nil, err
	}
	return hostHTTP(fp), nil
}

// HostClient is the HTTP client for a host from the host list, checked
// against its pin but without pinning a host not seen before. The doctor
// uses it to reach hosts.
func (c *Client) HostClient(h api.Host) (*http.Client, error) {
	if !strings.HasPrefix(h.URL, "https://") {
		return nil, fmt.Errorf("its address %s isn't https", h.URL)
	}
	fp, err := c.hostKey(h.ID, h.Name, h.PublicKey, false)
	if err != nil {
		return nil, err
	}
	return hostHTTP(fp), nil
}
