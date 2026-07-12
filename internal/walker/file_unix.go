//go:build unix

package walker

import (
	"io/fs"
	"syscall"
)

// rawFile is a minimal read-only file handle over a bare descriptor. It
// skips os.File's finalizer, poll-FD setup, and the fstat os.ReadFile
// performs, which measurably matters when opening tens of thousands of
// small files.
type rawFile struct {
	fd   int
	path string
}

// openRaw opens path read-only, retrying EINTR. Errors are *fs.PathError,
// matching os.Open's message text.
func openRaw(path string) (rawFile, error) {
	for {
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return rawFile{fd: -1}, &fs.PathError{Op: "open", Path: path, Err: err}
		}
		return rawFile{fd: fd, path: path}, nil
	}
}

// read reads once into p, retrying EINTR. At EOF it returns (0, nil).
func (f rawFile) read(p []byte) (int, error) {
	for {
		m, err := syscall.Read(f.fd, p)
		if err == syscall.EINTR {
			continue
		}
		if m < 0 {
			m = 0
		}
		if err != nil {
			return m, &fs.PathError{Op: "read", Path: f.path, Err: err}
		}
		return m, nil
	}
}

func (f rawFile) close() {
	syscall.Close(f.fd)
}
