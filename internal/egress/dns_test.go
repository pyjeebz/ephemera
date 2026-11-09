package egress

import (
	"encoding/binary"
	"net"
	"time"
)

// fakeDNS is an in-memory DNS "server" that answers every A query with
// 127.0.0.1 and every other query with no records. It speaks DNS over a
// stream (with length prefixes), which Go's resolver accepts from Dial.
func fakeDNS() net.Conn {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		for {
			server.SetDeadline(time.Now().Add(5 * time.Second))
			var lb [2]byte
			if _, err := readFull(server, lb[:]); err != nil {
				return
			}
			q := make([]byte, binary.BigEndian.Uint16(lb[:]))
			if _, err := readFull(server, q); err != nil {
				return
			}
			resp := answer(q)
			out := make([]byte, 2+len(resp))
			binary.BigEndian.PutUint16(out, uint16(len(resp)))
			copy(out[2:], resp)
			if _, err := server.Write(out); err != nil {
				return
			}
		}
	}()
	return &streamConn{client}
}

// streamConn makes Go's resolver use the TCP (length-prefixed) framing.
type streamConn struct{ net.Conn }

func (streamConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (streamConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func answer(q []byte) []byte {
	if len(q) < 12 {
		return q
	}
	// Find the end of the question name.
	i := 12
	for i < len(q) && q[i] != 0 {
		i += int(q[i]) + 1
	}
	if i+5 > len(q) {
		return q
	}
	qtype := binary.BigEndian.Uint16(q[i+1:])
	question := q[12 : i+5]
	resp := make([]byte, 12, 64)
	copy(resp, q[:2])                            // id
	binary.BigEndian.PutUint16(resp[2:], 0x8180) // response, recursion available
	binary.BigEndian.PutUint16(resp[4:], 1)      // one question
	resp = append(resp, question...)
	if qtype == 1 { // A
		binary.BigEndian.PutUint16(resp[6:], 1)
		resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 1)
	}
	return resp
}
