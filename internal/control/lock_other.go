//go:build !unix

package control

import "errors"

func lockFile(string) (func(), error) {
	return nil, errors.New("the central server runs on Linux or another Unix")
}
