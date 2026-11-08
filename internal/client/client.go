// Package client talks to the central server and, for files and commands,
// directly to the host running a sandbox.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/stream"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// Client is an ephemera API client.
type Client struct {
	Server string // central server URL
	Token  string
	HTTP   *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) do(ctx context.Context, method, url, token string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, &StatusError{Code: resp.StatusCode, Msg: e.Error}
	}
	return resp, nil
}

// StatusError is a non-2xx API response.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return e.Msg }

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func (c *Client) server() string { return strings.TrimRight(c.Server, "/") }

// Create asks the central server for a sandbox.
func (c *Client) Create(ctx context.Context, spec api.Spec) (*api.Sandbox, error) {
	resp, err := c.do(ctx, http.MethodPost, c.server()+"/v1/sandboxes", c.Token,
		jsonBody(api.CreateSandboxRequest{Spec: spec}), "application/json")
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer resp.Body.Close()
	var sb api.Sandbox
	if err := json.NewDecoder(resp.Body).Decode(&sb); err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return &sb, nil
}

// WhoAmI checks the token and returns its name.
func (c *Client) WhoAmI(ctx context.Context) (api.WhoAmI, error) {
	var w api.WhoAmI
	resp, err := c.do(ctx, http.MethodGet, c.server()+"/v1/whoami", c.Token, nil, "")
	if err != nil {
		return w, err
	}
	defer resp.Body.Close()
	return w, json.NewDecoder(resp.Body).Decode(&w)
}

// List returns the sandboxes the central server knows about.
func (c *Client) List(ctx context.Context) ([]api.Sandbox, error) {
	resp, err := c.do(ctx, http.MethodGet, c.server()+"/v1/sandboxes", c.Token, nil, "")
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	defer resp.Body.Close()
	var out []api.Sandbox
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Destroy asks the central server to destroy a sandbox.
func (c *Client) Destroy(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.server()+"/v1/sandboxes/"+id, c.Token, nil, "")
	if err != nil {
		return fmt.Errorf("destroy %s: %w", id, err)
	}
	resp.Body.Close()
	return nil
}

// Upload sends the worktree files straight to the sandbox's host.
func (c *Client) Upload(ctx context.Context, sb *api.Sandbox, dir string, files []string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(worktree.WriteTar(pw, dir, files)) }()
	resp, err := c.do(ctx, http.MethodPut, sb.HostURL+"/v1/sandboxes/"+sb.ID+"/files", sb.Token, pr, "application/x-tar")
	pr.Close()
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	resp.Body.Close()
	return nil
}

// DeleteFiles removes files from the sandbox's workspace.
func (c *Client) DeleteFiles(ctx context.Context, sb *api.Sandbox, paths []string) error {
	resp, err := c.do(ctx, http.MethodPost, sb.HostURL+"/v1/sandboxes/"+sb.ID+"/files/delete", sb.Token,
		jsonBody(api.DeleteFilesRequest{Paths: paths}), "application/json")
	if err != nil {
		return fmt.Errorf("delete files: %w", err)
	}
	resp.Body.Close()
	return nil
}

// upgradeTransport speaks HTTP/1.1 only, since connection upgrades do not
// exist in HTTP/2.
var upgradeTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return t
}()

// DialPort opens a raw TCP connection to a port inside the sandbox,
// through its host.
func (c *Client) DialPort(ctx context.Context, sb *api.Sandbox, port int) (io.ReadWriteCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sandboxes/%s/ports/%d", sb.HostURL, sb.ID, port), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sb.Token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", api.PortUpgrade)
	resp, err := (&http.Client{Transport: upgradeTransport}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, &StatusError{Code: resp.StatusCode, Msg: e.Error}
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("port: connection was not upgraded")
	}
	return rwc, nil
}

// ErrStreamCut means the command's output stopped without an exit code.
var ErrStreamCut = errors.New("the connection to the sandbox ended before the command finished")

// Exec runs a command in the sandbox and copies its output to stdout and
// stderr as it arrives. It returns the command's exit code.
func (c *Client) Exec(ctx context.Context, sb *api.Sandbox, req api.ExecRequest, stdout, stderr io.Writer) (int, error) {
	resp, err := c.do(ctx, http.MethodPost, sb.HostURL+"/v1/sandboxes/"+sb.ID+"/exec", sb.Token, jsonBody(req), "application/json")
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	defer resp.Body.Close()
	for {
		f, err := stream.Read(resp.Body)
		if err == io.EOF {
			return 0, ErrStreamCut
		}
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, fmt.Errorf("exec: %w", err)
		}
		switch f.Kind {
		case stream.Stdout:
			stdout.Write(f.Data)
		case stream.Stderr:
			stderr.Write(f.Data)
		case stream.Exit:
			return f.ExitCode(), nil
		case stream.Error:
			return 0, fmt.Errorf("exec: %s", f.Data)
		}
	}
}
