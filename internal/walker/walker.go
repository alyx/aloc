// Package walker traverses the file tree, applies ignore rules and smart
// detection, and fans counting out to parallel workers.
package walker

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/alyx/aloc/internal/counter"
	"github.com/alyx/aloc/internal/detect"
	"github.com/alyx/aloc/internal/ignore"
	"github.com/alyx/aloc/internal/lang"
	"github.com/alyx/aloc/internal/report"
)

// Options configures a run.
type Options struct {
	Roots          []string
	Excludes       *ignore.Set
	Includes       *ignore.Set
	Extensions     map[string]bool // lowercase, no dot; empty = all
	Languages      map[string]bool // lowercase names; empty = all
	Registry       *lang.Registry
	Detect         *detect.Engine // nil = smart exclusion disabled
	Gitignore      bool
	Hidden         bool
	FollowSymlinks bool
	Tracked        bool // count only files tracked by git
	Jobs           int
	ByFile         bool
	Warn           func(format string, args ...any) // may be nil
	Trace          func(format string, args ...any) // per-decision log; may be nil
}

type job struct {
	abs     string
	display string // path shown in output and warnings
}

type result struct {
	language string
	display  string
	stats    counter.Stats
}

type walker struct {
	opts    Options
	jobs    chan job
	results chan result

	logMu sync.Mutex // serializes Warn and Trace callbacks

	mu      sync.Mutex
	seen    map[string]bool // absolute file paths already dispatched
	visited map[string]bool // resolved dirs, for symlink loop safety
	excl    []report.Excluded
}

// Run walks all roots and returns the aggregated report.
func Run(opts Options) (*report.Report, error) {
	if opts.Registry == nil {
		opts.Registry = lang.NewRegistry()
	}
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	w := &walker{
		opts:    opts,
		jobs:    make(chan job, 4*jobs),
		results: make(chan result, 4*jobs),
		seen:    map[string]bool{},
		visited: map[string]bool{},
	}

	// Validate roots up front so a bad argument is a hard error, not a warning.
	type rootInfo struct {
		abs, display string
		isDir        bool
		tracked      *trackedSet
	}
	var roots []rootInfo
	for _, r := range opts.Roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r, err)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", r, err)
		}
		display := filepath.ToSlash(filepath.Clean(r))
		if display == "." {
			display = ""
		}
		ri := rootInfo{abs: abs, display: display, isDir: fi.IsDir()}
		if opts.Tracked {
			dir := abs
			if !ri.isDir {
				dir = filepath.Dir(abs)
			}
			if ri.tracked, err = gitTracked(dir); err != nil {
				return nil, err
			}
		}
		roots = append(roots, ri)
	}

	builder := report.NewBuilder(opts.ByFile)
	var collect sync.WaitGroup
	collect.Add(1)
	go func() {
		defer collect.Done()
		for res := range w.results {
			builder.AddFile(res.language, res.display, res.stats)
		}
	}()

	var workers sync.WaitGroup
	for i := 0; i < jobs; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := range w.jobs {
				w.countFile(j)
			}
		}()
	}

	for _, r := range roots {
		if r.isDir {
			w.walkDir(r.abs, r.display, "", &ignore.GitStack{}, nil, r.tracked)
			continue
		}
		if r.tracked != nil && !r.tracked.files[filepath.Base(r.abs)] {
			w.tracef("skip %s (not tracked by git)", displayPath(r.display, ""))
			continue
		}
		w.dispatch(r.abs, displayPath(r.display, ""))
	}
	close(w.jobs)
	workers.Wait()
	close(w.results)
	collect.Wait()

	for _, e := range w.excl {
		builder.AddExcluded(e.Path, e.Detector)
	}
	return builder.Build(), nil
}

func (w *walker) warnf(format string, args ...any) {
	if w.opts.Warn == nil {
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	w.opts.Warn(format, args...)
}

// tracef logs one skip decision. Traversal decisions arrive in walk order;
// per-file decisions (filters, unknown language) come from workers and may
// interleave.
func (w *walker) tracef(format string, args ...any) {
	if w.opts.Trace == nil {
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	w.opts.Trace(format, args...)
}

// walkDir processes one directory. rel is the path relative to the scan
// root ("" for the root itself); prefix is the root as given on the command
// line, used only for display.
func (w *walker) walkDir(abs, prefix, rel string, gs *ignore.GitStack, scope *detect.Scope, tr *trackedSet) {
	// When following symlinks, refuse to enter any directory twice — this
	// breaks cycles and stops diamond-shaped link layouts double-counting.
	if w.opts.FollowSymlinks && !w.markVisited(abs) {
		return
	}
	// os.ReadDir returns entries sorted by name, which keeps traversal —
	// and therefore warning/trace order — deterministic.
	entries, err := os.ReadDir(abs)
	if err != nil {
		w.warnf("cannot read directory %s: %v", displayPath(prefix, rel), err)
		return
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}

	// A directory whose contents identify it as disposable (a virtualenv, a
	// CMake build dir, Go vendoring) is skipped wholesale — unless it is the
	// scan root itself, which the user asked for explicitly.
	if rel != "" && w.opts.Detect != nil {
		if d := w.opts.Detect.SelfExcludedBy(filepath.Base(abs), names); d != "" {
			w.tracef("skip %s (smart: %s)", displayPath(prefix, rel), d)
			w.recordExcluded(displayPath(prefix, rel), d)
			return
		}
	}

	if w.opts.Gitignore {
		for _, e := range entries {
			if e.Name() == ".gitignore" && e.Type().IsRegular() {
				if content, err := os.ReadFile(filepath.Join(abs, ".gitignore")); err == nil {
					gs = gs.Push(rel, ignore.ParseGitIgnore(content))
				}
				break
			}
		}
	}

	if w.opts.Detect != nil {
		scope = w.opts.Detect.Extend(scope, rel, names)
	}

	for _, e := range entries {
		name := e.Name()
		erel := path.Join(rel, name)

		isDir := e.IsDir()
		childAbs := filepath.Join(abs, name)
		if e.Type()&fs.ModeSymlink != 0 {
			if !w.opts.FollowSymlinks {
				w.tracef("skip %s (symlink; --follow-symlinks off)", displayPath(prefix, erel))
				continue
			}
			fi, err := os.Stat(childAbs)
			if err != nil {
				w.warnf("broken symlink %s: %v", displayPath(prefix, erel), err)
				continue
			}
			isDir = fi.IsDir()
		}

		if isVCSDir(name) && isDir {
			w.tracef("skip %s (vcs metadata)", displayPath(prefix, erel))
			continue
		}
		if !w.opts.Hidden && strings.HasPrefix(name, ".") {
			w.tracef("skip %s (hidden; use --hidden to count)", displayPath(prefix, erel))
			continue
		}
		if pat := w.opts.Excludes.MatchedBy(erel); pat != "" {
			w.tracef("skip %s (excluded by pattern %q)", displayPath(prefix, erel), pat)
			continue
		}
		// The tracked filter runs before smart detection: untracked trees
		// are pruned outright, while a *committed* vendor dir survives to be
		// smart-excluded (and attributed) below.
		if tr != nil {
			if isDir && !tr.dirs[erel] {
				w.tracef("skip %s (no git-tracked files)", displayPath(prefix, erel))
				continue
			}
			if !isDir && !tr.files[erel] {
				w.tracef("skip %s (not tracked by git)", displayPath(prefix, erel))
				continue
			}
		}
		// Smart detection is checked before gitignore so an ecosystem
		// directory (node_modules, target, ...) is always attributed to its
		// detector in the report, even when the project also gitignores it.
		if isDir && scope != nil {
			if d := scope.ExcludedBy(erel); d != "" {
				w.tracef("skip %s (smart: %s)", displayPath(prefix, erel), d)
				w.recordExcluded(displayPath(prefix, erel), d)
				continue
			}
		}
		if w.opts.Gitignore {
			if ignored, src := gs.IgnoredBy(erel, isDir); ignored {
				w.tracef("skip %s (gitignored by %s)", displayPath(prefix, erel), src)
				continue
			}
		}

		if isDir {
			w.walkDir(childAbs, prefix, erel, gs, scope, tr)
			continue
		}
		if !e.Type().IsRegular() && e.Type()&fs.ModeSymlink == 0 {
			continue // sockets, devices, pipes
		}
		if !w.opts.Includes.Empty() && !w.opts.Includes.Matches(erel) {
			w.tracef("skip %s (no include pattern matches)", displayPath(prefix, erel))
			continue
		}
		w.dispatch(childAbs, displayPath(prefix, erel))
	}
}

// markVisited records a directory (by resolved path) and reports whether it
// was new. Used only when following symlinks.
func (w *walker) markVisited(dir string) bool {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.visited[resolved] {
		return false
	}
	w.visited[resolved] = true
	return true
}

func (w *walker) recordExcluded(path, detector string) {
	w.mu.Lock()
	w.excl = append(w.excl, report.Excluded{Path: path, Detector: detector})
	w.mu.Unlock()
}

func (w *walker) dispatch(abs, display string) {
	// Overlapping roots (aloc . ./src) must not double-count.
	w.mu.Lock()
	dup := w.seen[abs]
	w.seen[abs] = true
	w.mu.Unlock()
	if dup {
		w.tracef("skip %s (already counted via another root)", display)
		return
	}

	if len(w.opts.Extensions) > 0 {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(abs)), ".")
		if !w.opts.Extensions[ext] {
			w.tracef("skip %s (extension not selected by --ext)", display)
			return
		}
	}
	w.jobs <- job{abs: abs, display: display}
}

func (w *walker) countFile(j job) {
	l := w.opts.Registry.ByPath(j.abs)
	if l == nil {
		// Unknown by name: sniff a small prefix for a shebang before
		// committing to reading the whole file.
		head, err := readPrefix(j.abs, 256)
		if err != nil {
			w.warnf("cannot read %s: %v", j.display, err)
			return
		}
		l = w.opts.Registry.ByShebang(head)
		if l == nil {
			w.tracef("skip %s (unknown language)", j.display)
			return
		}
	}
	if len(w.opts.Languages) > 0 && !w.opts.Languages[strings.ToLower(l.Name)] {
		w.tracef("skip %s (language %s not selected by --lang)", j.display, l.Name)
		return
	}

	content, err := os.ReadFile(j.abs)
	if err != nil {
		w.warnf("cannot read %s: %v", j.display, err)
		return
	}
	if counter.IsBinary(content) {
		w.warnf("skipping binary file %s", j.display)
		return
	}
	w.results <- result{language: l.Name, display: j.display, stats: counter.Count(content, l)}
}

func readPrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:m], nil
}

func isVCSDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".bzr":
		return true
	}
	return false
}

// displayPath joins the command-line root with a scan-relative path for
// output; the root "." displays as bare relative paths.
func displayPath(prefix, rel string) string {
	switch {
	case prefix == "":
		if rel == "" {
			return "."
		}
		return rel
	case rel == "":
		return prefix
	default:
		return prefix + "/" + rel
	}
}
