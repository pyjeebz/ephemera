// Package webui serves the embedded single-page web UI. The SvelteKit app is
// built into internal/webui/dist (by build/build.sh), and go:embed bakes it into
// the daemon binary — so ephemerad ships the UI with it, with no asset directory
// to deploy and no CDN. The daemon only actually serves it on the opt-in -http
// surface; on the unix control socket it is just unused.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// all: is required — the SvelteKit build puts everything under _app, and go:embed
// skips names beginning with _ or . unless told otherwise.
//
//go:embed all:dist
var files embed.FS

// Handler returns an http.Handler for the SPA, or nil when the UI was not built
// into this binary. Static files are served when the path matches a built asset;
// anything else falls back to index.html, so client-side routes (like /box/{id})
// resolve to the app rather than a 404.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil // built without a real UI (placeholder only)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil
	}
	fileServer := http.FileServer(http.FS(sub))

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w)
			return
		}
		// Serve a real file when one exists; otherwise hand the SPA its index so
		// the client router can take the route.
		if info, err := fs.Stat(sub, p); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w)
	})
}
