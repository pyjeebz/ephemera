package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

// desktopHTML is the self-hosted RFB client: a single page that renders the
// box's framebuffer to a canvas and sends input back, over a WebSocket to this
// same process. Embedded so `ephemera` is one binary with no asset directory to
// ship, and no CDN — the page loads entirely from the local box.
//
//go:embed assets/desktop.html
var desktopHTML []byte

// runWebDesktop serves the desktop in a browser: a local HTTP server hands out
// the RFB client page and, at /ws, a WebSocket that proxies the RFB stream to the
// box's desktop vsock port. The daemon stays on its unix socket; this is the one
// spot that faces a browser, and it faces only localhost.
func runWebDesktop(ctx context.Context, vsockPath, label string, port int, open bool) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on local port %d: %w", port, err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(desktopHTML)
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		proxyDesktopWS(w, r, vsockPath)
	})

	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); _ = srv.Close() }()

	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Fprintf(os.Stderr, "ephemera: %s desktop at %s (Ctrl-C to stop)\n", label, url)
	if open {
		openBrowser(url)
	}

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// proxyDesktopWS upgrades to a WebSocket and pumps the RFB stream both ways
// between the browser and the box's desktop vsock port.
func proxyDesktopWS(w http.ResponseWriter, r *http.Request, vsockPath string) {
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	guest, err := dialDesktop(vsockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ephemera: desktop connect failed: %v\n", err)
		return
	}
	defer guest.Close()

	done := make(chan struct{}, 2)
	// guest -> browser: RFB bytes wrapped in binary WebSocket frames.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := guest.Read(buf)
			if n > 0 {
				if werr := ws.WriteBinary(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	// browser -> guest: each WebSocket message's payload, straight to the box.
	go func() {
		for {
			msg, err := ws.ReadBinary()
			if err != nil {
				break
			}
			if _, err := guest.Write(msg); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}

// openBrowser makes a best effort to open a URL in the user's browser. Failure is
// silent on purpose: the URL is already printed, so a headless or unusual
// environment just falls back to copy-and-paste rather than an error.
func openBrowser(url string) {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"open", url}}
	case "windows":
		candidates = [][]string{{"cmd", "/c", "start", url}}
	default: // linux, incl. WSL where wslview/explorer.exe reach the Windows browser
		candidates = [][]string{{"xdg-open", url}, {"wslview", url}, {"explorer.exe", url}}
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }() // reap it; do not block
			return
		}
	}
}
