//go:build !linux

package snapcrypt

import "os"

func nextData(_ *os.File, off, _ int64) int64 { return off }
