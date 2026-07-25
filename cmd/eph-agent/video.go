package main

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// The video bridge streams the desktop as encoded H.264 (fragmented MP4) rather
// than raw framebuffer updates. On each connection it starts an ffmpeg capturing
// the X display and pipes the encoded stream straight out the vsock — a
// per-connection encoder, so a box pays nothing for it until a viewer asks. The
// browser plays the stream through Media Source Extensions.
//
// ffmpeg args, and why:
//
//	-f x11grab -i :0     capture the headless Xvfb display (with the cursor)
//	-framerate 30        30 fps is smooth and affordable on a couple of vCPUs
//	libx264 ultrafast    software encode, no GPU here; ultrafast keeps up in real time
//	-tune zerolatency    no B-frames, no lookahead — the point is low latency
//	-profile baseline    the most universally MSE-decodable H.264 profile
//	-g 30                a keyframe every second, so MSE can start and recover quickly
//	fragmented mp4       empty_moov + frag_keyframe streams over a pipe with no seeking
var ffmpegArgs = []string{
	"-loglevel", "error", "-nostdin",
	"-f", "x11grab", "-draw_mouse", "1", "-framerate", "30", "-video_size", "1280x800", "-i", ":0",
	"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
	"-profile:v", "baseline", "-level", "3.1", "-pix_fmt", "yuv420p",
	"-g", "30", "-keyint_min", "30",
	"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof",
	"-",
}

// bridgeVideo accepts connections on the video vsock port and streams an encoded
// capture to each. Like the desktop bridge, an accept error must not be fatal —
// the agent is usually PID 1.
func bridgeVideo(lfd int) {
	for {
		cfd, _, err := unix.Accept4(lfd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			return
		}
		go serveVideo(cfd)
	}
}

// serveVideo runs one ffmpeg for the life of one connection, piping its encoded
// output to the viewer and killing it the moment the viewer goes away — so a
// closed tab does not leave an encoder pinning a CPU.
func serveVideo(fd int) {
	f := os.NewFile(uintptr(fd), "vsock-video")
	defer f.Close()

	cmd := exec.Command("ffmpeg", ffmpegArgs...)
	cmd.Stdout = f // encoded stream straight to the viewer
	// DISPLAY=:0 is inherited from the desktop init that started the agent.
	if err := cmd.Start(); err != nil {
		return // no ffmpeg (not a desktop box, or it is missing) — just drop
	}

	// The viewer sends nothing; a read on the connection blocks until it closes,
	// which is our signal to stop encoding. vsock is full-duplex, so reading here
	// while ffmpeg writes above is fine.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := f.Read(buf); err != nil {
				break
			}
		}
		_ = cmd.Process.Kill()
	}()

	_ = cmd.Wait()
}
