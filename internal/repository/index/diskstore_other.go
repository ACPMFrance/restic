//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package index

const diskSupported = false

func mapTempFile(string, int) ([]byte, error) { return nil, errMmapUnsupported }

func unmapRegion([]byte) error { return nil }
