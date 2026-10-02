// Package daemon is the sender-side of Ephemera: it shares one file,
// announces it to a relay, and serves ranged reads off the local disk
// for the lifetime of the share. Nothing leaves the machine except
// the bytes a receiver actually asks for.
package daemon

import (
	"mime"
	"path/filepath"
	"strings"
)

// DetectMIME guesses the content type for a share announcement.
//
// Browsers need the right type to play video inline, so for the
// formats the PRD cares about (.mp4 .m4v .mov .webm) the answer must
// be a real video/* type, not a generic octet-stream. Extension
// first (name is what the receiver sees), sniffing as fallback.
func DetectMIME(name string, head []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	}
	if len(head) > 0 {
		if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
	return "application/octet-stream"
}
