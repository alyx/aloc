package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alyx/aloc/internal/report"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

// runCLI executes Main from inside dir and returns (exit, stdout, stderr).
func runCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	testChdir(t, dir)
	var out, errBuf bytes.Buffer
	code := Main(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func decode(t *testing.T, jsonOut string) *report.Report {
	t.Helper()
	var r report.Report
	if err := json.Unmarshal([]byte(jsonOut), &r); err != nil {
		t.Fatalf("bad json output: %v\n%s", err, jsonOut)
	}
	return &r
}

func project(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.go":                       "package main\nfunc main() {}\n",
		"web/package.json":              "{}\n",
		"web/app.js":                    "let x = 1\n",
		"web/node_modules/dep/index.js": "module.exports = 1\n",
	})
	return root
}

func TestEndToEndJSON(t *testing.T) {
	root := project(t)
	code, out, errOut := runCLI(t, root, "-f", "json", "--no-config", ".")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	r := decode(t, out)
	if r.Totals.Files != 3 { // main.go, app.js, package.json — node_modules excluded
		t.Errorf("totals = %+v, want 3 files", r.Totals)
	}
	if len(r.Excluded) != 1 || r.Excluded[0].Path != "web/node_modules" || r.Excluded[0].Detector != "node" {
		t.Errorf("excluded = %+v", r.Excluded)
	}
}

func TestEndToEndNoSmart(t *testing.T) {
	root := project(t)
	code, out, _ := runCLI(t, root, "--format", "json", "--no-config", "--no-smart", ".")
	if code != 0 {
		t.Fatal("nonzero exit")
	}
	if r := decode(t, out); r.Totals.Files != 4 {
		t.Errorf("totals = %+v, want 4 files with smart exclusion off", r.Totals)
	}
}

func TestEndToEndNoDetect(t *testing.T) {
	root := project(t)
	code, out, _ := runCLI(t, root, "-f", "json", "--no-config", "--no-detect", "node", ".")
	if code != 0 {
		t.Fatal("nonzero exit")
	}
	if r := decode(t, out); r.Totals.Files != 4 {
		t.Errorf("totals = %+v, want 4 files with node detector off", r.Totals)
	}
}

func TestEndToEndConfig(t *testing.T) {
	root := project(t)
	writeTree(t, root, map[string]string{
		".aloc.yml": "format: json\nexclude: [web]\n",
	})
	code, out, errOut := runCLI(t, root, ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if r := decode(t, out); r.Totals.Files != 1 {
		t.Errorf("totals = %+v, want only main.go via config excludes", r.Totals)
	}

	// CLI flag overrides config format.
	_, out2, _ := runCLI(t, root, "-f", "yaml", ".")
	if !strings.HasPrefix(out2, "schema_version:") {
		t.Errorf("-f yaml should override config format, got: %.40s", out2)
	}

	// --no-config ignores the file entirely.
	_, out3, _ := runCLI(t, root, "--no-config", ".")
	if !strings.Contains(out3, "Language") {
		t.Errorf("--no-config should fall back to table, got: %.40s", out3)
	}
}

func TestEndToEndConfigCustomLanguageAndDetector(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".aloc.yml": `format: json
detectors:
  custom:
    - name: mytool
      markers: [mytool.lock]
      exclude: [mycache]
definitions:
  FooLang:
    extensions: [foo]
    line_comments: ["%%"]
`,
		"mytool.lock":  "",
		"prog.foo":     "%% comment\nreal code\n",
		"mycache/x.go": "package x\n",
	})
	code, out, errOut := runCLI(t, root, ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	r := decode(t, out)
	found := false
	for _, l := range r.Languages {
		if l.Name == "FooLang" {
			found = true
			if l.Code != 1 || l.Comment != 1 {
				t.Errorf("FooLang = %+v, want 1 code / 1 comment", l)
			}
		}
		if l.Name == "Go" {
			t.Error("mycache should be excluded by the custom detector")
		}
	}
	if !found {
		t.Error("custom language FooLang not counted")
	}
}

func TestEndToEndErrors(t *testing.T) {
	root := t.TempDir()
	if code, _, errOut := runCLI(t, root, "--no-config", "/definitely/missing"); code != 1 || !strings.Contains(errOut, "cannot read") {
		t.Errorf("missing root: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, root, "--no-config", "-f", "csv", "."); code != 1 || !strings.Contains(errOut, "unknown format") {
		t.Errorf("bad format: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, root, "--no-config", "--no-detect", "bogus", "."); code != 1 || !strings.Contains(errOut, "unknown detector") {
		t.Errorf("bad detector: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, root, "--no-config", "-l", "Klingon", "."); code != 1 || !strings.Contains(errOut, "unknown language") {
		t.Errorf("bad language: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, root, "--config", "nope.yml", "."); code != 1 || !strings.Contains(errOut, "not found") {
		t.Errorf("missing config: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, root, "--no-config", "-e", "../bad", "."); code != 1 || !strings.Contains(errOut, "scan root") {
		t.Errorf("bad pattern: code %d, stderr %q", code, errOut)
	}
}

func TestListingsAndVersion(t *testing.T) {
	root := t.TempDir()
	if code, out, _ := runCLI(t, root, "--no-config", "--list-languages"); code != 0 || !strings.Contains(out, "Go\n") {
		t.Errorf("--list-languages: code %d, out %.60q", code, out)
	}
	if code, out, _ := runCLI(t, root, "--no-config", "--list-detectors"); code != 0 || !strings.Contains(out, "composer:") {
		t.Errorf("--list-detectors: code %d, out %.60q", code, out)
	}
	if code, out, _ := runCLI(t, root, "--version"); code != 0 || !strings.Contains(out, "aloc") {
		t.Errorf("--version: code %d, out %q", code, out)
	}
}

func TestResolveVersion(t *testing.T) {
	for _, tt := range []struct {
		name, stamped, module, want string
	}{
		{"ldflags wins", "1.2.0", "v1.1.0", "1.2.0"},
		{"trim stamped v", "v1.2.0", "", "1.2.0"},
		{"go install module", "dev", "v1.2.0", "1.2.0"},
		{"local build", "dev", "(devel)", "dev"},
		{"missing metadata", "", "", "dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.stamped, tt.module); got != tt.want {
				t.Errorf("resolveVersion(%q, %q) = %q, want %q", tt.stamped, tt.module, got, tt.want)
			}
		})
	}
}

func TestVerboseWarnings(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"ok.go": "package ok\n"})
	if err := os.WriteFile(filepath.Join(root, "bin.go"), []byte("\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI(t, root, "--no-config", "-v", ".")
	if code != 0 {
		t.Fatal("nonzero exit")
	}
	if !strings.Contains(errOut, "binary") {
		t.Errorf("verbose should warn about binary files, stderr: %q", errOut)
	}
}

func TestEndToEndTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable:", err)
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"tracked.go":   "package a\n",
		"untracked.go": "package b\n",
	})
	for _, args := range [][]string{{"init", "-q"}, {"add", "-f", "tracked.go"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	code, out, errOut := runCLI(t, root, "--no-config", "--tracked", "-f", "json", ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if r := decode(t, out); r.Totals.Files != 1 {
		t.Errorf("totals = %+v, want only the tracked file", r.Totals)
	}

	outside := t.TempDir()
	if code, _, errOut := runCLI(t, outside, "--no-config", "--tracked", "."); code != 1 || !strings.Contains(errOut, "git") {
		t.Errorf("--tracked outside a repo: code %d, stderr %q", code, errOut)
	}
}

func TestEndToEndGitObjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable:", err)
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"clean.go":     "package clean\n",
		"modified.go":  "package before\n",
		"untracked.go": "package untracked\n",
	})
	for _, args := range [][]string{{"init", "-q"}, {"add", "clean.go", "modified.go"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "modified.go"), []byte("package after\n\n// dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, tracked, trackedErr := runCLI(t, root, "--no-config", "--tracked", "--by-file", "-f", "json", ".")
	code, objects, errOut := runCLI(t, root, "--no-config", "--git", "--by-file", "-f", "json", ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if trackedErr != "" || errOut != "" {
		t.Fatalf("unexpected diagnostics: tracked=%q git=%q", trackedErr, errOut)
	}
	if objects != tracked {
		t.Errorf("--git output differs from --tracked\ngit: %s\ntracked: %s", objects, tracked)
	}
}

func TestEndToEndDedup(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"one.go": "package same\n",
		"two.go": "package same\n",
	})
	code, out, errOut := runCLI(t, root, "--no-config", "--dedup", "-f", "json", "-vv", ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if r := decode(t, out); r.Totals.Files != 1 {
		t.Errorf("totals = %+v, want duplicates collapsed to 1", r.Totals)
	}
	if !strings.Contains(errOut, "skip two.go (duplicate of one.go)") {
		t.Errorf("-vv should attribute the duplicate, stderr: %q", errOut)
	}
}

func TestExtraVerboseTracesEveryDecision(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":                    "*.gen.go\n",
		"package.json":                  "{}\n",
		"main.go":                       "package main\n",
		"types.gen.go":                  "package main\n",
		"web/node_modules/dep/index.js": "x\n",
		".secret/hidden.go":             "package hidden\n",
		"skipme/a.go":                   "package a\n",
		"photo.xyz":                     "not a language\n",
	})
	code, _, errOut := runCLI(t, root, "--no-config", "-vv", "-e", "skipme", ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		`skip types.gen.go (gitignored by .gitignore: "*.gen.go")`,
		`skip web/node_modules (smart: node)`,
		`skip .secret (hidden; use --hidden to count)`,
		`skip skipme (excluded by pattern "skipme")`,
		`skip photo.xyz (unknown language)`,
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("-vv output missing %q\ngot:\n%s", want, errOut)
		}
	}
	// -vv replaces the -v smart summary rather than duplicating it.
	if strings.Contains(errOut, "smart-excluded") {
		t.Errorf("-vv should not repeat the smart-exclusion summary:\n%s", errOut)
	}
}

func TestTripleVerboseListsCountedFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.go":   "package main\n",
		"tool.py":   "x = 1\n",
		"skip.xyz":  "?\n",
		"README.md": "# hi\n",
	})
	code, out, errOut := runCLI(t, root, "--no-config", "-vvv", ".")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"count main.go (Go)",
		"count tool.py (Python)",
		"count README.md (Markdown)",
		"skip skip.xyz (unknown language)", // -vvv implies -vv
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("-vvv stderr missing %q\ngot:\n%s", want, errOut)
		}
	}
	// Diagnostics stay on stderr; stdout is only the report.
	if strings.Contains(out, "count main.go") {
		t.Error("file listing leaked to stdout")
	}
}

func TestOutputFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	dest := filepath.Join(root, "out.json")
	if code, _, errOut := runCLI(t, root, "--no-config", "-f", "json", "-o", dest, "."); code != 0 {
		t.Fatalf("exit: %s", errOut)
	}
	content, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if r := decode(t, string(content)); r.Totals.Files != 1 {
		t.Errorf("file output totals = %+v", r.Totals)
	}
}
