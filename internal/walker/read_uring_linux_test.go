//go:build linux

package walker

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alyx/aloc/internal/report"
)

// TestUringParity runs the same tree through the standard and io_uring read
// paths and requires identical reports. The tree exercises the uring
// worker's special cases: an empty file, an extensionless shebang script, a
// file larger than the slot buffer (sync-fallback reread), an unknown
// extension (skipped unopened), and enough files for multi-file batches.
func TestUringParity(t *testing.T) {
	if ok, err := uringAvailable(); !ok {
		t.Skipf("io_uring unavailable: %v", err)
	}

	dir := t.TempDir()
	write := func(name string, content []byte) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("empty.go", nil)
	write("script", []byte("#!/bin/sh\necho hi\n# comment\n"))
	write("noext", []byte("no shebang here\n"))
	write("data.xyzzy", []byte("unknown extension\n"))
	big := bytes.Repeat([]byte("// filler line for the fallback path\n"), (uringBufSize/38)+100)
	write("big.go", append([]byte("package big\n"), big...))
	for i := 0; i < 100; i++ {
		write(filepath.Join("pkg", string(rune('a'+i%5)), "f"+string(rune('0'+i%10))+".go"),
			[]byte("package p\n\n// doc\nfunc F() int {\n\treturn 1 // inline\n}\n"))
	}

	run := func(mode string) *report.Report {
		t.Helper()
		t.Setenv("ALOC_IO", mode)
		rep, err := Run(Options{Roots: []string{dir}, ByFile: true})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	std, uring := run("std"), run("uring")
	if !reflect.DeepEqual(std, uring) {
		t.Errorf("reports differ\nstd:   %+v\nuring: %+v", std, uring)
	}
}
