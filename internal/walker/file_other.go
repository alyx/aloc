//go:build !unix

package walker

import (
	"io"
	"os"
)

// rawFile falls back to os.File on platforms without POSIX read syscalls.
type rawFile struct {
	f *os.File
}

func openRaw(path string) (rawFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return rawFile{}, err
	}
	return rawFile{f: f}, nil
}

// read reads once into p. At EOF it returns (0, nil), matching the unix
// implementation's contract.
func (f rawFile) read(p []byte) (int, error) {
	m, err := f.f.Read(p)
	if err == io.EOF {
		return m, nil
	}
	return m, err
}

func (f rawFile) close() {
	f.f.Close()
}
