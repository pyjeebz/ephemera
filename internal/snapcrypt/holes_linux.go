package snapcrypt

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// nextData returns the offset of the next byte of data at or after off,
// skipping holes without reading them, or size if there is none.
func nextData(f *os.File, off, size int64) int64 {
	n, err := unix.Seek(int(f.Fd()), off, unix.SEEK_DATA)
	if errors.Is(err, unix.ENXIO) {
		return size // only a hole is left
	}
	if err != nil {
		return off // no hole support: read everything
	}
	return n
}
