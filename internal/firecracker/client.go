package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client speaks Firecracker's REST API.
//
// The API is plain HTTP, but served over a Unix domain socket rather than TCP.
// The standard library handles this cleanly: we keep using net/http, and only
// swap the transport's dialer so every request goes to the socket. The host part
// of the URL is therefore meaningless — "localhost" is a placeholder that keeps
// the URL well-formed.
type Client struct {
	sock string
	http *http.Client
}

// NewClient returns a client bound to one VMM's API socket. It does not connect;
// the socket need not exist yet.
func NewClient(sock string) *Client {
	return &Client{
		sock: sock,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
		},
	}
}

// SetBootSource selects the kernel and its command line. Must be called before
// Start.
func (c *Client) SetBootSource(ctx context.Context, b BootSource) error {
	return c.do(ctx, http.MethodPut, "/boot-source", b, nil)
}

// SetDrive attaches a virtio-blk device. Must be called before Start.
func (c *Client) SetDrive(ctx context.Context, d Drive) error {
	return c.do(ctx, http.MethodPut, "/drives/"+d.DriveID, d, nil)
}

// SetMachineConfig sets vCPU count and memory. Must be called before Start.
func (c *Client) SetMachineConfig(ctx context.Context, m MachineConfig) error {
	return c.do(ctx, http.MethodPut, "/machine-config", m, nil)
}

// SetVsock attaches the virtio-vsock device. Must be called before Start.
func (c *Client) SetVsock(ctx context.Context, v Vsock) error {
	return c.do(ctx, http.MethodPut, "/vsock", v, nil)
}

// Start boots the configured machine. The call returns as soon as the VMM has
// accepted the action — the guest kernel is still starting at that point.
func (c *Client) Start(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/actions", action{ActionType: "InstanceStart"}, nil)
}

// Info reports the VMM's state. It succeeds as soon as the API server is up,
// which is why WaitReady uses it as a readiness probe.
func (c *Client) Info(ctx context.Context) (*InstanceInfo, error) {
	var out InstanceInfo
	if err := c.do(ctx, http.MethodGet, "/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaitReady blocks until the API socket is accepting requests or ctx is done.
//
// Firecracker creates the socket a moment after the process starts, so the first
// few dials legitimately fail. Polling here keeps that race out of the callers.
func (c *Client) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if _, err := c.Info(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("firecracker api at %s never became ready: %w (last error: %v)", c.sock, ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

// do issues one request, encoding body as JSON when non-nil and decoding the
// response into out when non-nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(enc)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, rdr)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}

	// Successful mutations answer 204 No Content; only GETs carry a body.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var f apiFault
		if json.Unmarshal(raw, &f) == nil && f.FaultMessage != "" {
			return fmt.Errorf("%s %s: firecracker returned %s: %s", method, path, resp.Status, f.FaultMessage)
		}
		return fmt.Errorf("%s %s: firecracker returned %s: %s", method, path, resp.Status, bytes.TrimSpace(raw))
	}

	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}
