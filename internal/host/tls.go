package host

import (
	"crypto/tls"
	"errors"

	"github.com/pyjeebz/ephemera/internal/record"
)

// TLSConfig is how the host serves the CLI: TLS 1.3 with a certificate
// made from the host's own key, fresh at every start. The CLI checks that
// key against the one the host joined with, so no certificate authority
// or name is involved.
func (s *Server) TLSConfig() (*tls.Config, error) {
	if s.Key == nil {
		return nil, errors.New("host has no key")
	}
	cert, err := record.Certificate(s.Key)
	if err != nil {
		return nil, err
	}
	// HTTP/1.1 only: ports and exec streams upgrade the connection.
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, nil
}
