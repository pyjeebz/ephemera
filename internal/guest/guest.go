// Package guest is the agent's API inside a microVM. The host agent talks to
// it over vsock with the same operations it offers the CLI: upload and
// delete files, run commands with streamed output, and connect to ports.
//
// The VM is the sandbox boundary, so nothing here needs to defend the guest
// from its own user. Uploads are still checked, so a bad tar stream cannot
// write outside the workspace.
package guest

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/stream"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// Port is the vsock port the agent listens on inside the VM.
const Port = 1024

// MaxUpload caps the unpacked size of one upload.
const MaxUpload = 4 << 30

// Server serves the guest API.
type Server struct {
	Workdir string   // where files go and commands run
	Env     []string // base environment for commands
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("PUT /files", s.files)
	mux.HandleFunc("POST /files/delete", s.deleteFiles)
	mux.HandleFunc("POST /exec", s.exec)
	mux.HandleFunc("GET /ports/{port}", s.port)
	return mux
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(api.Error{Error: fmt.Sprintf(format, args...)})
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(worktree.Sanitize(pw, r.Body, MaxUpload)) }()
	err := Extract(s.Workdir, pr)
	pr.CloseWithError(err)
	if err != nil {
		writeError(w, http.StatusBadRequest, "upload: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Extract unpacks a sanitized tar stream into dir. Existing files are
// replaced; a symlink in the way of a file is replaced, never followed.
func Extract(dir string, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			os.Remove(p)
			if err := os.Symlink(hdr.Linkname, p); err != nil {
				return err
			}
		case tar.TypeReg:
			if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
				if err := os.RemoveAll(p); err != nil {
					return err
				}
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			os.Chmod(p, mode) // O_CREATE does not change an existing file's mode
		}
	}
}

func (s *Server) deleteFiles(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteFilesRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	for _, p := range req.Paths {
		if err := worktree.CheckPath(p); err != nil {
			writeError(w, http.StatusBadRequest, "%v", err)
			return
		}
	}
	for _, p := range req.Paths {
		err := os.Remove(filepath.Join(s.Workdir, filepath.FromSlash(p)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "delete %s: %v", p, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var req api.ExecRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if len(req.Argv) == 0 {
		writeError(w, http.StatusBadRequest, "argv is required")
		return
	}
	w.Header().Set("Content-Type", stream.ContentType)
	w.WriteHeader(http.StatusOK)
	sw := stream.NewWriter(w)
	code, err := s.run(r.Context(), req, sw.To(stream.Stdout), sw.To(stream.Stderr))
	if err != nil {
		sw.Fail(err.Error())
		return
	}
	sw.Exit(code)
}

// run starts the command in its own process group, so that stopping it
// (when the request ends) also stops everything it started.
func (s *Server) run(ctx context.Context, req api.ExecRequest, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = s.Workdir
	cmd.Env = append(append([]string{}, s.Env...), req.Env...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 127, nil // like a shell: command not found or not runnable
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func (s *Server) port(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, "invalid port")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), api.PortUpgrade) {
		writeError(w, http.StatusUpgradeRequired, "Upgrade: %s required", api.PortUpgrade)
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 10*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, "nothing is listening on port %d: %v", port, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		writeError(w, http.StatusInternalServerError, "connection cannot be upgraded")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", api.PortUpgrade)
	if err := buf.Flush(); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		upstream.Write(pending)
	}
	go func() {
		io.Copy(upstream, client)
		// The client is done sending; let the server finish its reply.
		if tc, ok := upstream.(*net.TCPConn); ok {
			tc.CloseWrite()
		} else {
			upstream.Close()
		}
	}()
	io.Copy(client, upstream)
	client.Close()
	upstream.Close()
}

// EgressPort is where sandbox programs find the HTTP proxy, on the VM's
// localhost, and the vsock port the host's egress proxy answers on.
const EgressPort = 3128

// ServeEgress accepts proxy connections from programs in the VM and
// carries each one to the host's egress proxy with dial. If the sandbox
// has no network, the host has no proxy for it and every connection fails.
func ServeEgress(ln net.Listener, dial func() (net.Conn, error)) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			defer c.Close()
			up, err := dial()
			if err != nil {
				msg := "ephemera: this sandbox has no network access\n"
				fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
				return
			}
			defer up.Close()
			go func() {
				io.Copy(up, c)
				if cw, ok := up.(interface{ CloseWrite() error }); ok {
					cw.CloseWrite()
				}
			}()
			io.Copy(c, up)
		}()
	}
}
