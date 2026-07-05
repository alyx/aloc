// Package detect implements smart ecosystem detection: recognizing project
// types from marker files and excluding their dependency/build directories.
package detect

import (
	"fmt"
	"path"
	"strings"
)

// Detector recognizes one ecosystem.
type Detector struct {
	Name string
	// Markers are file basenames (globs allowed) whose presence in a
	// directory marks it as a project root of this ecosystem.
	Markers []string
	// ExcludeDirs are directory names — or slash-separated relative paths
	// like "vendor/bundle" — excluded anywhere in the subtree rooted at a
	// marked directory. Globs allowed in each segment.
	ExcludeDirs []string
	// SelfMarkers are file basenames whose presence marks the *containing*
	// directory as disposable (build output, virtualenv), regardless of its
	// name.
	SelfMarkers []string
	// SelfName, when set, restricts SelfMarkers to directories with this
	// basename (e.g. Go vendoring: "modules.txt" inside "vendor").
	SelfName string
}

// Builtin is the default detector set.
var Builtin = []Detector{
	{Name: "node", Markers: []string{"package.json"}, ExcludeDirs: []string{"node_modules", ".pnpm-store", "bower_components"}},
	{Name: "composer", Markers: []string{"composer.json"}, ExcludeDirs: []string{"vendor"}},
	{Name: "python", Markers: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"},
		ExcludeDirs: []string{"__pycache__", ".venv", "venv", ".tox", ".nox", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".eggs", "*.egg-info"}},
	{Name: "venv", SelfMarkers: []string{"pyvenv.cfg"}},
	{Name: "rust", Markers: []string{"Cargo.toml"}, ExcludeDirs: []string{"target"}},
	{Name: "go", SelfMarkers: []string{"modules.txt"}, SelfName: "vendor"},
	{Name: "maven", Markers: []string{"pom.xml"}, ExcludeDirs: []string{"target"}},
	{Name: "gradle", Markers: []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}, ExcludeDirs: []string{"build", ".gradle"}},
	{Name: "ruby", Markers: []string{"Gemfile"}, ExcludeDirs: []string{".bundle", "vendor/bundle"}},
	{Name: "dotnet", Markers: []string{"*.sln", "*.csproj", "*.fsproj"}, ExcludeDirs: []string{"bin", "obj"}},
	{Name: "elixir", Markers: []string{"mix.exs"}, ExcludeDirs: []string{"_build", "deps", ".elixir_ls"}},
	{Name: "dart", Markers: []string{"pubspec.yaml"}, ExcludeDirs: []string{".dart_tool", "build"}},
	{Name: "swift", Markers: []string{"Package.swift"}, ExcludeDirs: []string{".build"}},
	{Name: "terraform", Markers: []string{"*.tf"}, ExcludeDirs: []string{".terraform"}},
	{Name: "cmake", SelfMarkers: []string{"CMakeCache.txt"}},
	{Name: "zig", Markers: []string{"build.zig"}, ExcludeDirs: []string{"zig-cache", ".zig-cache", "zig-out"}},
	{Name: "haskell", Markers: []string{"stack.yaml", "*.cabal"}, ExcludeDirs: []string{".stack-work", "dist-newstyle"}},
}

// Engine evaluates a set of detectors during traversal.
type Engine struct {
	detectors []Detector
}

// NewEngine returns an engine over Builtin plus custom, minus disabled.
// Unknown names in disabled are reported as an error so typos surface.
func NewEngine(custom []Detector, disabled []string) (*Engine, error) {
	all := make([]Detector, 0, len(Builtin)+len(custom))
	all = append(all, Builtin...)
	all = append(all, custom...)

	known := map[string]bool{}
	for _, d := range all {
		known[d.Name] = true
	}
	skip := map[string]bool{}
	for _, name := range disabled {
		if !known[name] {
			return nil, fmt.Errorf("unknown detector %q (see --list-detectors)", name)
		}
		skip[name] = true
	}

	e := &Engine{}
	for _, d := range all {
		if !skip[d.Name] {
			e.detectors = append(e.detectors, d)
		}
	}
	return e, nil
}

// Detectors returns the active detectors.
func (e *Engine) Detectors() []Detector { return e.detectors }

// Scope is the set of subtree exclusion rules active for a directory. Scopes
// are immutable; Extend produces a child scope shared by a whole subtree.
type Scope struct {
	parent *Scope
	dir    string // subtree root, relative to scan root ("" = root)
	rules  []scopeRule
}

type scopeRule struct {
	detector string
	segs     []string // relative-path segments of the excluded dir
}

// Extend evaluates detectors against a directory listing and returns the
// scope for that directory's subtree. dir is relative to the scan root;
// names are the directory's entry basenames.
func (e *Engine) Extend(parent *Scope, dir string, names []string) *Scope {
	var rules []scopeRule
	for _, d := range e.detectors {
		if len(d.Markers) == 0 {
			continue
		}
		if !anyNameMatches(names, d.Markers) {
			continue
		}
		for _, ex := range d.ExcludeDirs {
			rules = append(rules, scopeRule{detector: d.Name, segs: strings.Split(ex, "/")})
		}
	}
	if len(rules) == 0 {
		return parent
	}
	return &Scope{parent: parent, dir: dir, rules: rules}
}

// ExcludedBy returns the name of the detector excluding directory rel
// (relative to the scan root), or "" if none does. A rule like "vendor"
// matches any directory of that name inside its scope; "vendor/bundle"
// matches that relative path at any depth inside its scope.
func (s *Scope) ExcludedBy(rel string) string {
	parts := strings.Split(rel, "/")
	for sc := s; sc != nil; sc = sc.parent {
		sub := parts
		if sc.dir != "" {
			prefix := strings.Split(sc.dir, "/")
			if len(parts) <= len(prefix) || !segsEqual(prefix, parts[:len(prefix)]) {
				continue
			}
			sub = parts[len(prefix):]
		}
		for _, r := range sc.rules {
			if len(sub) < len(r.segs) {
				continue
			}
			// The rule matches if the trailing segments of sub equal it —
			// i.e. this directory is a "vendor" (or "vendor/bundle")
			// anywhere inside the scope.
			tail := sub[len(sub)-len(r.segs):]
			if segsMatch(r.segs, tail) {
				return r.detector
			}
		}
	}
	return ""
}

// SelfExcludedBy returns the detector that marks a directory (with basename
// base and entries names) as disposable, or "".
func (e *Engine) SelfExcludedBy(base string, names []string) string {
	for _, d := range e.detectors {
		if len(d.SelfMarkers) == 0 {
			continue
		}
		if d.SelfName != "" && d.SelfName != base {
			continue
		}
		if anyNameMatches(names, d.SelfMarkers) {
			return d.Name
		}
	}
	return ""
}

func anyNameMatches(names, globs []string) bool {
	for _, g := range globs {
		for _, n := range names {
			if ok, _ := path.Match(g, n); ok {
				return true
			}
		}
	}
	return false
}

func segsEqual(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func segsMatch(pat, parts []string) bool {
	for i := range pat {
		if ok, _ := path.Match(pat[i], parts[i]); !ok {
			return false
		}
	}
	return true
}
