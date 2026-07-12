package counter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alyx/aloc/internal/lang"
)

func benchCount(b *testing.B, data []byte, langName string, fn func([]byte, *lang.Language) Stats) {
	l := lang.NewRegistry().Get(langName)
	if l == nil {
		b.Fatalf("no lang %s", langName)
	}
	base := fn(data, l)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := fn(data, l)
		if got != base {
			b.Fatalf("mismatch: %+v vs %+v", got, base)
		}
	}
}

func gorootFile(b *testing.B, rel string) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join(runtime.GOROOT(), rel))
	if err != nil {
		b.Skipf("missing GOROOT file: %v", err)
	}
	return data
}

var pySynthetic = []byte(strings.Repeat(`# a comment
def f(x):
    """docstring
    over lines"""
    s = 'string # not comment'
    return x + 1

`, 200))

var jsSynthetic = []byte(strings.Repeat("// comment\nconst s = `template\nstring`;\nfunction f(x) { /* c */ return x + 1; }\n\n", 200))

func BenchmarkCount(b *testing.B) {
	cases := []struct {
		name, lang string
		data       []byte
	}{
		{"GoServer", "Go", gorootFile(b, "src/net/http/server.go")},
		{"GoZerrors", "Go", gorootFile(b, "src/syscall/zerrors_linux_amd64.go")},
		{"Python", "Python", pySynthetic},
		{"JavaScript", "JavaScript", jsSynthetic},
	}
	for _, c := range cases {
		b.Run(c.name+"/old", func(b *testing.B) { benchCount(b, c.data, c.lang, oldCount) })
		b.Run(c.name+"/prev", func(b *testing.B) { benchCount(b, c.data, c.lang, prevCount) })
		b.Run(c.name+"/new", func(b *testing.B) { benchCount(b, c.data, c.lang, Count) })
	}
}
