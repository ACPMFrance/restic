//go:build linux || darwin || freebsd || netbsd || openbsd

package index

import (
	"os"

	"golang.org/x/sys/unix"
)

const diskSupported = true

// mapTempFile creates an unlinked temporary file of the given size in dir and
// maps it read-write. The file is sparse: disk space is only used for the pages
// that are written. The mapping keeps the file alive until it is unmapped.
func mapTempFile(dir string, size int) ([]byte, error) {
	f, err := os.CreateTemp(dir, "restic-index-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := os.Remove(f.Name()); err != nil {
		return nil, err
	}
	if err := f.Truncate(int64(size)); err != nil {
		return nil, err
	}
	b, err := unix.Mmap(int(f.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	// lookups are random: no read-ahead
	_ = unix.Madvise(b, unix.MADV_RANDOM)
	return b, nil
}

func unmapRegion(b []byte) error { return unix.Munmap(b) }
