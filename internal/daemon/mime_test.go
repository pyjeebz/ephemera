package daemon

import "testing"

func TestDetectMIME(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"movie.mp4", "video/mp4"},
		{"movie.MP4", "video/mp4"},
		{"clip.m4v", "video/mp4"},
		{"clip.mov", "video/quicktime"},
		{"clip.webm", "video/webm"},
		{"clip.mkv", "video/x-matroska"},
		{"song.mp3", "audio/mpeg"},
		{"song.m4a", "audio/mp4"},
		{"song.wav", "audio/wav"},
		{"notes.txt", "text/plain; charset=utf-8"},
		{"page.html", "text/html; charset=utf-8"},
		{"readme.md", "text/markdown; charset=utf-8"},
		{"archive.tar.gz", "application/octet-stream"},
		{"no-extension", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tt := range tests {
		if got := DetectMIME(tt.name, nil); got != tt.want {
			t.Errorf("DetectMIME(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
