//go:build darwin || linux

package walker

import "syscall"

// mapRaw returns a private read-only mapping for a sufficiently large regular
// file plus the stat size for exact fallback allocation. Failure is
// deliberately non-fatal: callers fall back to read(2).
func mapRaw(f rawFile, minSize int) ([]byte, int) {
	var st syscall.Stat_t
	if err := syscall.Fstat(f.fd, &st); err != nil || st.Size < 0 || st.Size > int64(int(^uint(0)>>1)) {
		return nil, 0
	}
	size := int(st.Size)
	if size < minSize {
		return nil, size
	}
	b, err := syscall.Mmap(f.fd, 0, size, syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, size
	}
	return b, size
}

func unmapRaw(b []byte) { _ = syscall.Munmap(b) }
