package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/alyx/aloc/internal/counter"
	"github.com/alyx/aloc/internal/report"
)

// The hand-rolled json/yaml emitters must be byte-identical to the encoders
// they replaced (oracle_test.go) for any string that can reach a report:
// exotic paths, strings YAML would mistype, control bytes, invalid UTF-8,
// and long scalars that trigger yaml.v3's column-80 folding.

func adversarialStrings() []string {
	s := []string{
		"", " ", "  ", "\t", "a b", " leading", "trailing ", "a  b",
		"-", "-x", "- x", "--", "---", "---/x", "-1", "-1.5", "-.inf", "-0b101",
		".", "..", "...", ".../x", "../x", "./x", ".5", ".5e3", ".inf", ".NaN", ".hidden/f.go", ".gitignore",
		"?", "?x", ":", ":x", "x:", "x: y", "a:b", "12:30", "1:2:3", "190:20:30.15",
		"#", "#x", "x #y", "x#y", "C#", "F#", "C++", "C/C++ Header",
		"'", "''", "'quoted'", "\"", "\"x\"", "\\", "\\x", "`", "@", "%", "%x", "&", "&x", "*", "*x",
		"!", "!x", "|", "|x", ">", ">x", "<<", "<", "=", "[", "]", "{", "}", ",", "[x]", "{a: b}", "a,b",
		"y", "Y", "n", "N", "yes", "Yes", "YES", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF",
		"true", "True", "TRUE", "tRue", "false", "False", "FALSE", "null", "Null", "NULL", "nUll", "~",
		"0", "1", "007", "0x1F", "0X1F", "0o17", "017", "0b101", "1_000", "+1", "1.5", "1e5", "1E5", "9e-2",
		"123/456", "0x2f/x", "386Ops.go", "9p/file.go", "2001-12-14", "2001-12-14t21:59:43.10-05:00",
		"2001-12-14 21:59:43", "2001-12-15T02:59:43.1Z",
		"\n", "a\n", "\na", "a\nb", "a\n\nb", "a\r\nb", "\r", "a\rb", "x\u0085y", "x\u2028y", "x\u2029y",
		"\x00", "a\x01b", "\x1f", "\x7f", "a\tb", "\u00a0", "x\u00a0y", "\ufeff", "\ufeffbom",
		"café", "日本語/パス.go", "\U0001f680.go", "é", "\u200b",
		"\xff", "\xff\xfe", "a\x80b", "\xc3\x28", "\xed\xa0\x80", "\xe2\x82", "ok\xffnot",
		"a<b>&c", "<script>", "&amp;", "path with spaces/and 'quotes'/file.go", `back\slash\dir`,
		"a=b;c(d)$e^f", "src/mod~1/f.go",
	}
	// Fold stressors: yaml.v3 wraps at spaces past column 80, and the wrap
	// point depends on the scalar's start column in each context.
	for w := 60; w <= 100; w += 5 {
		s = append(s,
			strings.Repeat("a", w)+" tail",
			strings.Repeat("a", w)+" tail one two three",
			"lead "+strings.Repeat("b", w),
		)
	}
	s = append(s,
		strings.Repeat("x", 300),
		strings.Repeat("word ", 40),
		strings.Repeat("média ", 30),
		strings.Repeat("no-space-", 40),
		strings.Repeat("a b", 60),
		strings.Repeat("é", 120),
		strings.Repeat("a", 200)+"\nsecond line "+strings.Repeat("b", 100),
	)
	return append(s, randomStrings(400)...)
}

func randomStrings(n int) []string {
	rng := rand.New(rand.NewSource(0xa10c))
	pools := []string{
		" \t\"'\\:#-?!|>&*%@`[]{},~+.=<;",
		"abcdefXYZ0123456789._/+-",
		"\x00\x01\x1f\x7f\u0085\u00a0\u2028\u2029\ufeff\u200b",
		"日本語éüñ\U0001f680✓",
		"\n\r",
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i%5 == 4 { // raw bytes: mostly invalid UTF-8
			b := make([]byte, rng.Intn(24))
			for j := range b {
				b[j] = byte(rng.Intn(256))
			}
			out = append(out, string(b))
			continue
		}
		var sb strings.Builder
		pool := []rune(pools[i%len(pools)])
		mix := []rune(pools[(i+1)%len(pools)])
		for j, ln := 0, rng.Intn(40); j < ln; j++ {
			if rng.Intn(3) == 0 {
				sb.WriteRune(mix[rng.Intn(len(mix))])
			} else {
				sb.WriteRune(pool[rng.Intn(len(pool))])
			}
		}
		out = append(out, sb.String())
	}
	return out
}

func adversarialReports() map[string]*report.Report {
	reps := map[string]*report.Report{
		"empty":     report.NewBuilder(false).Build(),
		"nil":       {},
		"nil-langs": {SchemaVersion: 1, Excluded: []report.Excluded{{Path: "p", Detector: "d"}}},
		"rep30k":    rep30k,
	}
	all := report.NewBuilder(true)
	for i, s := range adversarialStrings() {
		one := report.NewBuilder(true)
		one.AddFile(s, s, counter.Stats{Files: 1, Lines: 3, Blank: 1, Comment: 1, Code: 1})
		one.AddFile("Go", "sub/"+s, counter.Stats{Files: 1, Lines: 2, Blank: 0, Comment: 0, Code: 2})
		one.AddExcluded(s, s)
		reps[fmt.Sprintf("nasty-%03d", i)] = one.Build()

		all.AddFile(s, s, counter.Stats{Files: 1, Lines: 3, Blank: 1, Comment: 1, Code: 1})
		all.AddExcluded(s, fmt.Sprintf("det-%d", i))
	}
	reps["nasty-all"] = all.Build()
	return reps
}

func mustEqual(t *testing.T, label, enc string, want, got []byte) {
	t.Helper()
	if bytes.Equal(want, got) {
		return
	}
	d := 0
	for d < len(want) && d < len(got) && want[d] == got[d] {
		d++
	}
	lo := max(0, d-80)
	t.Errorf("%s/%s: first mismatch at byte %d\nwant ...%q\ngot  ...%q",
		label, enc, d, want[lo:min(len(want), d+80)], got[lo:min(len(got), d+80)])
}

func TestJSONMatchesStdlib(t *testing.T) {
	for label, r := range adversarialReports() {
		var want, got bytes.Buffer
		if err := writeJSONStdlib(&want, r); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(&got, r); err != nil {
			t.Fatal(err)
		}
		mustEqual(t, label, "json", want.Bytes(), got.Bytes())
	}
}

func TestYAMLMatchesYAMLv3(t *testing.T) {
	for label, r := range adversarialReports() {
		var want, got bytes.Buffer
		if err := writeYAMLv3(&want, r); err != nil {
			t.Fatal(err)
		}
		if err := writeYAML(&got, r); err != nil {
			t.Fatal(err)
		}
		mustEqual(t, label, "yaml", want.Bytes(), got.Bytes())
	}
}

// realTreeReport builds a by-file report over this repository's own tree so
// the differential tests also cover organically occurring path strings.
func realTreeReport(t *testing.T) *report.Report {
	b := report.NewBuilder(true)
	root := "../.."
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		lang := "Other"
		if strings.HasSuffix(rel, ".go") {
			lang = "Go"
		}
		b.AddFile(lang, filepath.ToSlash(rel), counter.Stats{Files: 1, Lines: n % 997, Blank: n % 13, Comment: n % 7, Code: n % 89})
		n++
		if n >= 4000 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || n == 0 {
		t.Skipf("cannot walk repo tree: %v (n=%d)", err, n)
	}
	return b.Build()
}

func TestEmittersMatchOnRealTree(t *testing.T) {
	r := realTreeReport(t)
	var wj, gj, wy, gy bytes.Buffer
	if err := writeJSONStdlib(&wj, r); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(&gj, r); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "realtree", "json", wj.Bytes(), gj.Bytes())
	if err := writeYAMLv3(&wy, r); err != nil {
		t.Fatal(err)
	}
	if err := writeYAML(&gy, r); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "realtree", "yaml", wy.Bytes(), gy.Bytes())
}

// TestRoundTrip decodes the emitters' output back into Report with the
// stock decoders; a renamed struct tag or reordered field breaks this.
func TestRoundTrip(t *testing.T) {
	b := report.NewBuilder(true)
	b.AddFile("Go", "a dir/with space/f.go", counter.Stats{Files: 1, Lines: 10, Blank: 1, Comment: 2, Code: 7})
	b.AddFile("Go", "-leading/dash: colon 'q' #h.go", counter.Stats{Files: 1, Lines: 4, Blank: 0, Comment: 0, Code: 4})
	b.AddFile("C#", "日本語/パス.cs", counter.Stats{Files: 1, Lines: 8, Blank: 2, Comment: 2, Code: 4})
	b.AddFile("YAML", "true", counter.Stats{Files: 1, Lines: 1, Blank: 0, Comment: 0, Code: 1})
	b.AddFile("Text", strings.Repeat("long segment ", 15)+"tail.txt", counter.Stats{Files: 1, Lines: 1, Blank: 0, Comment: 0, Code: 1})
	b.AddExcluded("node_modules", "node")
	b.AddExcluded("2001-12-14", "looks-like-a-date")
	src := b.Build()

	var jbuf bytes.Buffer
	if err := writeJSON(&jbuf, src); err != nil {
		t.Fatal(err)
	}
	var fromJSON report.Report
	if err := json.Unmarshal(jbuf.Bytes(), &fromJSON); err != nil {
		t.Fatalf("json round-trip: %v", err)
	}
	if !reflect.DeepEqual(*src, fromJSON) {
		t.Errorf("json round-trip mismatch:\nsrc: %+v\ngot: %+v", *src, fromJSON)
	}

	var ybuf bytes.Buffer
	if err := writeYAML(&ybuf, src); err != nil {
		t.Fatal(err)
	}
	var fromYAML report.Report
	if err := yaml.Unmarshal(ybuf.Bytes(), &fromYAML); err != nil {
		t.Fatalf("yaml round-trip: %v", err)
	}
	if !reflect.DeepEqual(*src, fromYAML) {
		t.Errorf("yaml round-trip mismatch:\nsrc: %+v\ngot: %+v", *src, fromYAML)
	}
}
