//go:build !linux

package walker

import "os"

// selectUring is Linux-only; elsewhere the standard read loop always runs,
// with a warning when io_uring was explicitly requested.
func (w *walker) selectUring() bool {
	if os.Getenv("ALOC_IO") == "uring" {
		w.warnf("io_uring is Linux-only; using standard reads")
	}
	return false
}

// uringWorker never runs: selectUring reports false on this platform.
func (w *walker) uringWorker() bool { return false }
