package walker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alyx/aloc/internal/detect"
	"github.com/alyx/aloc/internal/ignore"
	"github.com/alyx/aloc/internal/report"
)

// fixture builds a small multi-ecosystem repo and returns its root.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":            "package main\n\n// entry\nfunc main() {}\n", // 2 code, 1 comment, 1 blank
		"README.md":          "# hi\n",                                     // 1 code
		"ignored.go":         "package ignored\n",                          // gitignored
		".gitignore":         "ignored.go\n",
		".hidden/secret.go":  "package secret\n", // hidden
		"app/composer.json":  "{}\n",
		"app/index.php":      "<?php\necho 1;\n", // 2 code
		"app/vendor/lib.php": "<?php\n",          // smart-excluded
		"env/pyvenv.cfg":     "home = /usr\n",
		"env/lib/pkg.py":     "x = 1\n",                         // inside venv
		"tool.py":            "#!/usr/bin/env python3\nx = 1\n", // 1 comment(shebang is #), 1 code
		"noext":              "#!/bin/bash\necho hi\n",          // Shell via shebang
	}
	for path, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A "Go source file" that is actually binary.
	if err := os.WriteFile(filepath.Join(root, "bin.go"), []byte("GIF89a\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func run(t *testing.T, opts Options) *report.Report {
	t.Helper()
	if opts.Detect == nil {
		e, err := detect.NewEngine(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		opts.Detect = e
	}
	opts.Gitignore = true
	rep, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func langStats(r *report.Report, name string) *report.LanguageStats {
	for i := range r.Languages {
		if r.Languages[i].Name == name {
			return &r.Languages[i]
		}
	}
	return nil
}

func TestWalkDefaults(t *testing.T) {
	root := fixture(t)
	// Excluded paths are reported relative to the root as given on the
	// command line, so scan from inside the fixture.
	t.Chdir(root)
	rep := run(t, Options{Roots: []string{"."}})

	if g := langStats(rep, "Go"); g == nil || g.Files != 1 || g.Code != 2 {
		// ignored.go is gitignored, .hidden/ skipped, bin.go is binary.
		t.Errorf("Go = %+v, want 1 file / 2 code", g)
	}
	if p := langStats(rep, "PHP"); p == nil || p.Files != 1 {
		t.Errorf("PHP = %+v, want 1 file (vendor smart-excluded)", p)
	}
	if py := langStats(rep, "Python"); py == nil || py.Files != 1 {
		t.Errorf("Python = %+v, want only tool.py (venv self-excluded)", py)
	}
	if sh := langStats(rep, "Shell"); sh == nil || sh.Files != 1 {
		t.Errorf("Shell = %+v, want noext detected via shebang", sh)
	}

	wantExcluded := map[string]string{"app/vendor": "composer", "env": "venv"}
	if len(rep.Excluded) != len(wantExcluded) {
		t.Fatalf("excluded = %+v, want %v", rep.Excluded, wantExcluded)
	}
	for _, e := range rep.Excluded {
		if wantExcluded[e.Path] != e.Detector {
			t.Errorf("unexpected exclusion %+v", e)
		}
	}
}

func TestWalkFlags(t *testing.T) {
	root := fixture(t)

	rep := run(t, Options{Roots: []string{root}, Hidden: true})
	if g := langStats(rep, "Go"); g == nil || g.Files != 2 {
		t.Errorf("with Hidden: Go files = %+v, want 2 (secret.go now counted)", g)
	}

	// Gitignore off is passed directly to Run (run() forces it on).
	e, _ := detect.NewEngine(nil, nil)
	rep2, err := Run(Options{Roots: []string{root}, Detect: e, Gitignore: false})
	if err != nil {
		t.Fatal(err)
	}
	if g := langStats(rep2, "Go"); g == nil || g.Files != 2 {
		t.Errorf("without gitignore: Go files = %+v, want 2 (ignored.go counted)", g)
	}

	// Smart detection off: vendor and the venv are counted.
	rep3, err := Run(Options{Roots: []string{root}, Detect: nil, Gitignore: true})
	if err != nil {
		t.Fatal(err)
	}
	if p := langStats(rep3, "PHP"); p == nil || p.Files != 2 {
		t.Errorf("without smart: PHP files = %+v, want 2", p)
	}
	if py := langStats(rep3, "Python"); py == nil || py.Files != 2 {
		t.Errorf("without smart: Python files = %+v, want 2", py)
	}
	if len(rep3.Excluded) != 0 {
		t.Errorf("without smart: excluded = %+v, want none", rep3.Excluded)
	}
}

func TestWalkExcludeInclude(t *testing.T) {
	root := fixture(t)

	ex, err := ignore.ParseSet([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	rep := run(t, Options{Roots: []string{root}, Excludes: ex})
	if p := langStats(rep, "PHP"); p != nil {
		t.Errorf("exclude app: PHP still counted: %+v", p)
	}

	inc, err := ignore.ParseSet([]string{"*.go"})
	if err != nil {
		t.Fatal(err)
	}
	rep2 := run(t, Options{Roots: []string{root}, Includes: inc})
	if len(rep2.Languages) != 1 || rep2.Languages[0].Name != "Go" {
		t.Errorf("include *.go: languages = %+v, want only Go", rep2.Languages)
	}

	rep3 := run(t, Options{Roots: []string{root}, Extensions: map[string]bool{"php": true}})
	if len(rep3.Languages) != 1 || rep3.Languages[0].Name != "PHP" {
		t.Errorf("ext filter: languages = %+v, want only PHP", rep3.Languages)
	}

	rep4 := run(t, Options{Roots: []string{root}, Languages: map[string]bool{"python": true}})
	if len(rep4.Languages) != 1 || rep4.Languages[0].Name != "Python" {
		t.Errorf("lang filter: languages = %+v, want only Python", rep4.Languages)
	}
}

func TestWalkSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	// A cycle: real/loop -> root.
	if err := os.Symlink(root, filepath.Join(root, "real", "loop")); err != nil {
		t.Fatal(err)
	}

	rep := run(t, Options{Roots: []string{root}})
	if g := langStats(rep, "Go"); g == nil || g.Files != 1 {
		t.Errorf("symlinks off: Go files = %+v, want 1", g)
	}

	rep2 := run(t, Options{Roots: []string{root}, FollowSymlinks: true})
	if g := langStats(rep2, "Go"); g == nil || g.Files != 1 {
		t.Errorf("symlinks on with cycle: Go files = %+v, want exactly 1 (no dupes, no hang)", g)
	}
}

func TestWalkSmartExclusionOutranksGitignore(t *testing.T) {
	// Real-world layout: projects gitignore node_modules themselves. The
	// directory must still be attributed to the node detector in the
	// report, not silently dropped by the gitignore rule.
	root := t.TempDir()
	files := map[string]string{
		".gitignore":                "/node_modules\n",
		"package.json":              "{}\n",
		"app.js":                    "let x = 1\n",
		"node_modules/dep/index.js": "module.exports = 1\n",
	}
	for path, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	rep := run(t, Options{Roots: []string{"."}})
	if js := langStats(rep, "JavaScript"); js == nil || js.Files != 1 {
		t.Errorf("JavaScript = %+v, want 1 file", js)
	}
	if len(rep.Excluded) != 1 || rep.Excluded[0].Path != "node_modules" || rep.Excluded[0].Detector != "node" {
		t.Errorf("excluded = %+v, want node_modules attributed to the node detector", rep.Excluded)
	}
}

func TestWalkTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable:", err)
	}
	root := t.TempDir()
	files := map[string]string{
		"main.go":                       "package main\n",
		"untracked.go":                  "package untracked\n",
		"scratch/notes.py":              "x = 1\n",
		"composer.json":                 "{}\n",
		"vendor/lib.php":                "<?php\n",
		"web/node_modules/dep/index.js": "x\n",
	}
	for path, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Track everything except untracked.go, scratch/, and node_modules —
	// including vendor/, to prove smart exclusion still applies after the
	// tracked filter.
	for _, args := range [][]string{
		{"init", "-q"},
		// -f so a developer's global gitignore can't break the fixture.
		{"add", "-f", "main.go", "composer.json", "vendor/lib.php"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	t.Chdir(root)
	rep := run(t, Options{Roots: []string{"."}, Tracked: true})

	if g := langStats(rep, "Go"); g == nil || g.Files != 1 {
		t.Errorf("Go = %+v, want only tracked main.go", g)
	}
	if py := langStats(rep, "Python"); py != nil {
		t.Errorf("untracked scratch/ should be pruned, got %+v", py)
	}
	if p := langStats(rep, "PHP"); p != nil {
		t.Errorf("committed vendor/ should still be smart-excluded, got %+v", p)
	}
	if len(rep.Excluded) != 1 || rep.Excluded[0] != (report.Excluded{Path: "vendor", Detector: "composer"}) {
		t.Errorf("excluded = %+v, want committed vendor attributed to composer", rep.Excluded)
	}

	// A root outside any git repository is a hard error.
	if _, err := Run(Options{Roots: []string{t.TempDir()}, Tracked: true}); err == nil {
		t.Error("--tracked outside a git repo should fail")
	}
}

func TestWalkDedup(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"b/copy.go":  "package dup\nvar x = 1\n",
		"a/orig.go":  "package dup\nvar x = 1\n", // identical to b/copy.go
		"unique.go":  "package unique\n",
		"same.css":   "b { color: red }\n",
		"twin.scss":  "b { color: red }\n", // identical bytes, different language
		"empty_a.py": "",
		"empty_b.py": "", // empty files dedup too
	}
	for path, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rep := run(t, Options{Roots: []string{root}, Dedup: true, ByFile: true})

	if g := langStats(rep, "Go"); g == nil || g.Files != 2 {
		t.Errorf("Go = %+v, want 2 files (one copy of the pair, plus unique.go)", g)
	}
	// The lexicographically first path survives.
	if g := langStats(rep, "Go"); g != nil {
		var paths []string
		for _, f := range g.Detail {
			paths = append(paths, filepath.Base(f.Path))
		}
		if len(paths) != 2 || paths[0] != "orig.go" {
			t.Errorf("surviving Go files = %v, want a/orig.go first", paths)
		}
	}
	// Dedup is by content alone, across languages: same.css beats twin.scss.
	if c := langStats(rep, "CSS"); c == nil || c.Files != 1 {
		t.Errorf("CSS = %+v, want 1 file", c)
	}
	if s := langStats(rep, "SCSS"); s != nil {
		t.Errorf("SCSS = %+v, want none (identical to same.css)", s)
	}
	if py := langStats(rep, "Python"); py == nil || py.Files != 1 {
		t.Errorf("Python = %+v, want empty files deduped to one", py)
	}

	// Without the flag, everything is counted.
	rep2 := run(t, Options{Roots: []string{root}})
	if rep2.Totals.Files != len(files) {
		t.Errorf("without dedup: %d files, want %d", rep2.Totals.Files, len(files))
	}
}

func TestWalkOverlappingRoots(t *testing.T) {
	root := fixture(t)
	rep := run(t, Options{Roots: []string{root, filepath.Join(root, "app")}})
	if p := langStats(rep, "PHP"); p == nil || p.Files != 1 {
		t.Errorf("overlapping roots: PHP files = %+v, want 1 (deduped)", p)
	}
}

func TestWalkDeterminism(t *testing.T) {
	root := fixture(t)
	opts := Options{Roots: []string{root}, ByFile: true, Dedup: true}
	a, _ := json.Marshal(run(t, opts))
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(run(t, opts))
		if string(a) != string(b) {
			t.Fatalf("output not deterministic:\n%s\nvs\n%s", a, b)
		}
	}
}

func TestWalkMissingRoot(t *testing.T) {
	_, err := Run(Options{Roots: []string{"/does/not/exist-aloc-test"}})
	if err == nil {
		t.Fatal("missing root should be a hard error")
	}
}

func TestWalkFileRoot(t *testing.T) {
	root := fixture(t)
	rep := run(t, Options{Roots: []string{filepath.Join(root, "main.go")}})
	if g := langStats(rep, "Go"); g == nil || g.Files != 1 || rep.Totals.Files != 1 {
		t.Errorf("file root: %+v", rep.Totals)
	}
}
