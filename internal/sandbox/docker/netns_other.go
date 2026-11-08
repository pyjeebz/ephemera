//go:build !linux

package docker

import (
	"context"
	"errors"
	"net"
)

func dialInNetns(context.Context, int, string) (net.Conn, error) {
	return nil, errors.New("port forwarding into Docker sandboxes needs a Linux host")
}
