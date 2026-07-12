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

// Engine evaluates a set of detectors during traversal. Marker patterns are
// split at construction into exact names (one map lookup per directory
// entry) and true globs (path.Match); most builtin markers are exact.
type Engine struct {
	detectors  []Detector
	markerLit  map[string][]int // literal marker -> detector indices
	selfLit    map[string][]int
	markerGlob []globMarker
	selfGlob   []globMarker
	exSegs     [][][]string // per detector, pre-split ExcludeDirs
}

type globMarker struct {
	pat    string
	suffix string // when pat is "*<literal>", matched via HasSuffix
	idx    int    // detector index
}

func (e *Engine) index() {
	e.markerLit = map[string][]int{}
	e.selfLit = map[string][]int{}
	e.exSegs = make([][][]string, len(e.detectors))
	add := func(lit map[string][]int, globs *[]globMarker, m string, i int) {
		if !strings.ContainsAny(m, `*?[\`) {
			lit[m] = append(lit[m], i)
			return
		}
		g := globMarker{pat: m, idx: i}
		if strings.HasPrefix(m, "*") && !strings.ContainsAny(m[1:], `*?[\`) {
			g.suffix = m[1:]
		}
		*globs = append(*globs, g)
	}
	for i, d := range e.detectors {
		for _, m := range d.Markers {
			add(e.markerLit, &e.markerGlob, m, i)
		}
		for _, m := range d.SelfMarkers {
			add(e.selfLit, &e.selfGlob, m, i)
		}
		for _, ex := range d.ExcludeDirs {
			e.exSegs[i] = append(e.exSegs[i], strings.Split(ex, "/"))
		}
	}
}

// markerHits returns one flag per detector for markers matching any of
// names, or nil when none match. Names never contain a separator, so a
// "*<suffix>" glob reduces to HasSuffix. No shared scratch: the walker calls
// this from concurrent walk goroutines.
func (e *Engine) markerHits(names []string, lit map[string][]int, globs []globMarker) []bool {
	var hits []bool
	mark := func(i int) {
		if hits == nil {
			hits = make([]bool, len(e.detectors))
		}
		hits[i] = true
	}
	for _, n := range names {
		if idxs, ok := lit[n]; ok {
			for _, i := range idxs {
				mark(i)
			}
		}
		for _, g := range globs {
			if hits != nil && hits[g.idx] {
				continue
			}
			var ok bool
			if g.suffix != "" {
				ok = strings.HasSuffix(n, g.suffix)
			} else {
				ok, _ = path.Match(g.pat, n)
			}
			if ok {
				mark(g.idx)
			}
		}
	}
	return hits
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
	e.index()
	return e, nil
}

// Detectors returns the active detectors.
func (e *Engine) Detectors() []Detector { return e.detectors }

// Scope is the set of subtree exclusion rules active for a directory. Scopes
// are immutable; Extend produces a child scope shared by a whole subtree.
type Scope struct {
	parent  *Scope
	dir     string   // subtree root, relative to scan root ("" = root)
	dirSegs []string // dir pre-split; nil for the root
	rules   []scopeRule
}

type scopeRule struct {
	detector string
	segs     []string // relative-path segments of the excluded dir
}

// Extend evaluates detectors against a directory listing and returns the
// scope for that directory's subtree. dir is relative to the scan root;
// names are the directory's entry basenames.
func (e *Engine) Extend(parent *Scope, dir string, names []string) *Scope {
	hits := e.markerHits(names, e.markerLit, e.markerGlob)
	if hits == nil {
		return parent
	}
	var rules []scopeRule
	for i, d := range e.detectors {
		if !hits[i] {
			continue
		}
		for _, segs := range e.exSegs[i] {
			rules = append(rules, scopeRule{detector: d.Name, segs: segs})
		}
	}
	if len(rules) == 0 {
		return parent
	}
	var dirSegs []string
	if dir != "" {
		dirSegs = strings.Split(dir, "/")
	}
	return &Scope{parent: parent, dir: dir, dirSegs: dirSegs, rules: rules}
}

// ExcludedBy returns the name of the detector excluding directory rel
// (relative to the scan root), or "" if none does. A rule like "vendor"
// matches any directory of that name inside its scope; "vendor/bundle"
// matches that relative path at any depth inside its scope.
func (s *Scope) ExcludedBy(rel string) string {
	return s.ExcludedByParts(strings.Split(rel, "/"))
}

// ExcludedByParts is ExcludedBy on pre-split path segments, so a caller
// checking every directory entry splits each path once.
func (s *Scope) ExcludedByParts(parts []string) string {
	for sc := s; sc != nil; sc = sc.parent {
		sub := parts
		if len(sc.dirSegs) > 0 {
			if len(parts) <= len(sc.dirSegs) || !segsEqual(sc.dirSegs, parts[:len(sc.dirSegs)]) {
				continue
			}
			sub = parts[len(sc.dirSegs):]
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
	hits := e.markerHits(names, e.selfLit, e.selfGlob)
	if hits == nil {
		return ""
	}
	for i, d := range e.detectors {
		if !hits[i] {
			continue
		}
		if d.SelfName != "" && d.SelfName != base {
			continue
		}
		return d.Name
	}
	return ""
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
