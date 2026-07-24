package wsutil

import "net"

// Proxy pumps bytes both ways between a WebSocket and a byte stream until either
// side closes: the stream's bytes go out as binary WebSocket messages, and each
// inbound message's payload is written straight to the stream. It is the whole of
// carrying a raw protocol (RFB, a pty) to a browser once the socket is upgraded.
func Proxy(ws *Conn, conn net.Conn) {
	done := make(chan struct{}, 2)
	// stream -> browser: raw bytes wrapped in binary WebSocket frames.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
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
	// browser -> stream: each message's payload, straight through.
	go func() {
		for {
			msg, err := ws.ReadBinary()
			if err != nil {
				break
			}
			if _, err := conn.Write(msg); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}
