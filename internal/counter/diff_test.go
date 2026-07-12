package counter

import (
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alyx/aloc/internal/lang"
)

// TestDifferentialGOROOT runs the old and new Count over every recognized
// file under GOROOT/src and requires identical Stats.
func TestDifferentialGOROOT(t *testing.T) {
	root := filepath.Join(runtime.GOROOT(), "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("GOROOT/src unavailable: %v", err)
	}
	reg := lang.NewRegistry()
	files, mismatches := 0, 0
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		l := reg.ByPath(path)
		if l == nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || IsBinary(data) {
			return nil
		}
		files++
		if got, want := Count(data, l), oldCount(data, l); got != want {
			mismatches++
			if mismatches <= 10 {
				t.Errorf("%s (%s): new %+v old %+v", path, l.Name, got, want)
			}
		}
		return nil
	})
	t.Logf("compared %d files, %d mismatches", files, mismatches)
	if files < 1000 {
		t.Fatalf("too few files compared: %d", files)
	}
}

// TestDifferentialEdgeCases hits the precedence and escape corners for every
// builtin language with synthetic nasty inputs.
func TestDifferentialEdgeCases(t *testing.T) {
	inputs := []string{
		"",
		"\n",
		"   \n\t\n",
		"x = 1\n",
		"--[[ block\nstill ]] code\n",
		"#= nested #= inner =# still =# code\n",
		"/* c */ code // tail\n",
		"/* unterminated\nmore\n*/ done\n",
		"s = \"str \\\" with escape\" # tail\n",
		"s = \"unterminated\ncode\n",
		"m = \"\"\"multi\nline\"\"\" tail\n",
		"m = \"\"\"esc \\\"\"\" not closed?\nrest\"\"\"\n",
		"r = `raw \\` + \"q\"\n",
		"r = '''raw\ntoml \\'''\n",
		"(* ocaml (* nested *) still *) code\n",
		"{- haskell {- nest -} -} x\n",
		"code /* open\n\n   \n*/ /* again */ end\n",
		"=begin\nruby block\n=end\n",
		"<!-- html\ncomment --> <b>x</b>\n",
		"\\\"escaped quote first\n",
		"a = 'x' -- lua comment --[[ not block\n",
		"\xEF\xBB\xBF# bom then comment\n",
		"tab\tafter\ttabs // c\r\n",
		"\"\\\\\" + \"\\\\\\\"\" // escapes\n",
		"'''\npython doc\n'''\n",
		"x = \"\"\"a\"\"\" + \"\"\"b\nc\"\"\"\n",
		"\v\n\f\n \v \n",                      // TrimSpace-only blanks
		"\xc2\xa0\n",                          // NBSP-only line is blank
		"\xc2\xa0// not reached\n",            // NBSP then marker
		"日本語 = \"テスト\" // コメント\n",   // multi-byte text
		"\rx\n\r// c\n\r\r\n",                 // interior \r torture
		"a\r\r\n\r\n",                         // \r runs against CRLF
		"/* a\n\n \x0b \n*/ x\n",              // blank lines inside block comment
		"m = \"\"\"\n\n  \n\xc2\xa0\nend\"\"\"\n", // blanks inside multi-line string
		"s = \"x\\\r\n\" y\n",                 // backslash before CRLF in string
		"no trailing newline",
		"ends with cr\r",
		"\"\"\"\r\nx\r\n\"\"\"\r\n",           // CRLF multi-line string
	}
	reg := lang.NewRegistry()
	for _, name := range reg.Names() {
		l := reg.Get(name)
		for _, in := range inputs {
			if got, want := Count([]byte(in), l), oldCount([]byte(in), l); got != want {
				t.Errorf("%s input %q: new %+v old %+v", name, in, got, want)
			}
		}
	}
}

// TestDifferentialRandom feeds seeded random soup, dense in delimiter bytes,
// to both implementations for every builtin language. This is the guard
// against fast-path divergence on inputs no curated list anticipates.
func TestDifferentialRandom(t *testing.T) {
	// Alphabet skewed heavily toward delimiter and escape bytes, plus the
	// whitespace-ambiguous bytes the fused scan special-cases: \v, \f,
	// non-ASCII (NBSP as \xc2\xa0, and a lone continuation byte), and \r in
	// positions other than before \n.
	alphabet := `"'` + "`" + `\/*#-=<>![]{}() ` + "\t\r\n" + `abc123` + "\v\f\xc2\xa0\x80"
	reg := lang.NewRegistry()
	rng := rand.New(rand.NewSource(1))
	for _, name := range reg.Names() {
		l := reg.Get(name)
		for trial := 0; trial < 200; trial++ {
			var sb strings.Builder
			n := 1 + rng.Intn(400)
			for i := 0; i < n; i++ {
				sb.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
			in := sb.String()
			if got, want := Count([]byte(in), l), oldCount([]byte(in), l); got != want {
				t.Fatalf("%s trial %d input %q: new %+v old %+v", name, trial, in, got, want)
			}
		}
	}
}
