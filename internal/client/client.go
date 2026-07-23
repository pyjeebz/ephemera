// Package client talks to ephemerad.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/api"
)

// DefaultAddr matches ephemerad's default listen address.
const DefaultAddr = "unix://run/ephemerad.sock"

// Client is a handle on a daemon.
type Client struct {
	addr string
	http *http.Client
}

// New returns a client for a unix:// or tcp:// address.
//
// For a Unix socket the host in the URL is meaningless, so requests are sent to
// a placeholder host and the transport's dialer routes them to the socket —
// the same shape as the Firecracker client.
func New(addr string) *Client {
	c := &Client{addr: addr, http: &http.Client{}}
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		c.http.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}
	}
	return c
}

func (c *Client) url(path string) string {
	if host, ok := strings.CutPrefix(c.addr, "tcp://"); ok {
		return "http://" + host + path
	}
	return "http://ephemerad" + path
}

// Create boots a machine and returns once it is ready to run commands.
func (c *Client) Create(ctx context.Context, req api.CreateRequest) (api.MachineResponse, error) {
	var out api.MachineResponse
	err := c.do(ctx, http.MethodPost, "/v1/machines", req, &out)
	return out, err
}

// List returns every machine the daemon owns.
func (c *Client) List(ctx context.Context) ([]api.MachineResponse, error) {
	var out struct {
		Machines []api.MachineResponse `json:"machines"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/machines", nil, &out)
	return out.Machines, err
}

// Get returns one machine.
func (c *Client) Get(ctx context.Context, id string) (api.MachineResponse, error) {
	var out api.MachineResponse
	err := c.do(ctx, http.MethodGet, "/v1/machines/"+id, nil, &out)
	return out, err
}

// Destroy tears a machine down.
func (c *Client) Destroy(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/machines/"+id, nil, nil)
}

// Exec runs a command in a machine, writing its output as it arrives.
//
// The returned status is the command's own; a non-zero value comes back with a
// nil error, because a failing command is a result rather than a broken call.
func (c *Client) Exec(ctx context.Context, id string, req agent.ExecRequest, stdout, stderr io.Writer) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return -1, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/v1/machines/"+id+"/exec"), bytes.NewReader(body))
	if err != nil {
		return -1, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return -1, fmt.Errorf("client: exec: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, decodeError(resp)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var f agent.Frame
		if err := dec.Decode(&f); err != nil {
			if err == io.EOF {
				return -1, fmt.Errorf("client: stream ended without an exit status")
			}
			return -1, fmt.Errorf("client: read frame: %w", err)
		}
		switch f.Kind {
		case agent.FrameStdout:
			if stdout != nil {
				if _, err := stdout.Write(f.Data); err != nil {
					return -1, err
				}
			}
		case agent.FrameStderr:
			if stderr != nil {
				if _, err := stderr.Write(f.Data); err != nil {
					return -1, err
				}
			}
		case agent.FrameExit:
			return f.Code, nil
		case agent.FrameError:
			return -1, fmt.Errorf("client: %s", f.Error)
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s %s: %w (is ephemerad running?)", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("client: decode response: %w", err)
		}
	}
	return nil
}

// decodeError turns the daemon's JSON error body into a Go error, falling back
// to the status line when the body is not what we expect.
func decodeError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return fmt.Errorf("ephemerad: %s", e.Error)
	}
	return fmt.Errorf("ephemerad: %s: %s", resp.Status, bytes.TrimSpace(raw))
}
