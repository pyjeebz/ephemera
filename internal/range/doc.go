// Package httprange parses and serializes HTTP Range headers.
//
// It is pure: no network, no filesystem. The daemon uses it to serve
// byte ranges from a file on disk; the relay uses it to answer the
// browser's Range requests over the bridge.
package httprange
