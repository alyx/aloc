// Package walker traverses the file tree, applies ignore rules and smart
// detection, and fans counting out to parallel workers.
package walker

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	GitObjects     bool // count tracked files, reading clean blobs from git
	Dedup          bool // count only one copy of identical files
	Jobs           int
	ByFile         bool
	Warn           func(format string, args ...any) // may be nil
	Trace          func(format string, args ...any) // per-decision log; may be nil
	TraceFiles     bool                             // also Trace every counted file and its language
}

type job struct {
	abs    string
	prefix string // root as given on the command line, for display
	erel   string // path relative to the scan root, for display
	oid    string // clean index blob; empty means read from the filesystem
	blobs  *gitBatch
}

// display builds the path shown in output and warnings. Callers build it
// only when something will read it: per-file detail, dedup ordering,
// warnings, and traces.
func (j job) display() string { return displayPath(j.prefix, j.erel) }

type result struct {
	language string
	display  string
	stats    counter.Stats
	hash     [sha256.Size]byte // content hash; only filled with Options.Dedup
}

type walker struct {
	opts     Options
	trace    bool // opts.Trace != nil; hot skip paths check it before building tracef args
	useSeen  bool // overlap dedup needed: multiple roots or symlink following
	useUring bool // io_uring read path selected (Linux with kernel support)
	jobs     chan job
	results  chan result

	uringWarn sync.Once // a degraded-to-standard-reads warning prints once, not per worker

	// Bounded pool for parallel directory traversal; nil means the walk is
	// serial. See Run for when parallel traversal is safe.
	walkSem chan struct{}
	walkWG  sync.WaitGroup

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
		trace:   opts.Trace != nil,
		useSeen: len(opts.Roots) > 1 || opts.FollowSymlinks,
		results: make(chan result, 4*jobs),
		seen:    map[string]bool{},
		visited: map[string]bool{},
	}
	// On Linux the io_uring read path is the default when a startup probe
	// confirms kernel support; ALOC_IO=std|uring overrides (see
	// selectUring). The uring workers drain the jobs channel in batches, so
	// it must hold several batches or submissions stay far below the batch
	// size and the I/O depth win evaporates.
	w.useUring = w.selectUring()
	jobsBuf := 4 * jobs
	if w.useUring {
		jobsBuf = max(jobsBuf, 8*uringBatchHint)
	}
	w.jobs = make(chan job, jobsBuf)
	// The single-goroutine walk is the pipeline's bottleneck on wide trees:
	// its ReadDir latency inflates under concurrent worker I/O and starves
	// the workers. A bounded pool of walk goroutines fixes that. The final
	// report is unaffected (Build sorts languages, per-file detail, and
	// exclusions; dedup buffers then sorts), but Warn/Trace arrival order
	// and the symlink-visit outcome would become nondeterministic — so the
	// walk stays serial whenever any of those is observable.
	if opts.Trace == nil && opts.Warn == nil && !opts.FollowSymlinks && jobs > 1 {
		w.walkSem = make(chan struct{}, min(8, jobs))
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
		if opts.Tracked || opts.GitObjects {
			dir := abs
			if !ri.isDir {
				dir = filepath.Dir(abs)
			}
			if opts.GitObjects {
				ri.tracked, err = gitObjects(dir)
			} else {
				ri.tracked, err = gitTracked(dir)
			}
			if err != nil {
				return nil, err
			}
		}
		roots = append(roots, ri)
	}

	// With Dedup, results are buffered and resolved after the walk: workers
	// finish in arbitrary order, so picking the surviving copy on the fly
	// would make output nondeterministic.
	builder := report.NewBuilder(opts.ByFile)
	var buffered []result
	var collect sync.WaitGroup
	collect.Add(1)
	go func() {
		defer collect.Done()
		for res := range w.results {
			if opts.Dedup {
				buffered = append(buffered, res)
			} else {
				builder.AddFile(res.language, res.display, res.stats)
			}
		}
	}()

	var workers sync.WaitGroup
	for i := 0; i < jobs; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if w.useUring && w.uringWorker() {
				return
			}
			// Per-worker scratch buffer, reused across files: content never
			// escapes countFile. Shrunk after an outsized file so one huge
			// blob does not pin memory for the rest of the run.
			buf := make([]byte, initialBufSize)
			for j := range w.jobs {
				buf = w.countFile(j, buf)
				if len(buf) > maxKeepBufSize {
					buf = make([]byte, initialBufSize)
				}
			}
		}()
	}

	for _, r := range roots {
		if r.isDir {
			w.walkDir(r.abs, r.display, "", &ignore.GitStack{}, nil, r.tracked)
			// Drain this root's walk goroutines before starting the next
			// root: overlapping roots (aloc . ./src) must resolve their
			// duplicates in root order, exactly as the serial walk does.
			w.walkWG.Wait()
			continue
		}
		if r.tracked != nil && !r.tracked.files[filepath.Base(r.abs)] {
			w.tracef("skip %s (not tracked by git)", displayPath(r.display, ""))
			continue
		}
		w.dispatchGit(r.abs, r.display, "", r.tracked, filepath.Base(r.abs))
	}
	close(w.jobs)
	workers.Wait()
	for _, r := range roots {
		if r.tracked != nil && r.tracked.blobs != nil {
			r.tracked.blobs.Close()
		}
	}
	close(w.results)
	collect.Wait()

	if opts.Dedup {
		// The lexicographically first path of each identical set survives.
		slices.SortFunc(buffered, func(a, b result) int { return cmp.Compare(a.display, b.display) })
		firstByHash := map[[sha256.Size]byte]string{}
		for _, res := range buffered {
			if first, ok := firstByHash[res.hash]; ok {
				w.tracef("skip %s (duplicate of %s)", res.display, first)
				continue
			}
			firstByHash[res.hash] = res.display
			builder.AddFile(res.language, res.display, res.stats)
		}
	}

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

	// The names slice only feeds detection.
	var names []string
	if w.opts.Detect != nil {
		names = make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
	}

	// A directory whose contents identify it as disposable (a virtualenv, a
	// CMake build dir, Go vendoring) is skipped wholesale — unless it is the
	// scan root itself, which the user asked for explicitly.
	if rel != "" && w.opts.Detect != nil {
		if d := w.opts.Detect.SelfExcludedBy(filepath.Base(abs), names); d != "" {
			disp := displayPath(prefix, rel)
			if w.trace {
				w.tracef("skip %s (smart: %s)", disp, d)
			}
			w.recordExcluded(disp, d)
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

	// abs and rel are clean by construction (filepath.Abs root plus repeated
	// name appends), so plain concatenation replaces the Joins' re-Clean.
	absPre := abs
	if !strings.HasSuffix(absPre, string(filepath.Separator)) {
		absPre += string(filepath.Separator)
	}
	relPre := ""
	if rel != "" {
		relPre = rel + "/"
	}
	// Matchers consume pre-split segments: each entry path is split once
	// here, not per rule set or gitignore stack level. relSegs is shared, so
	// the per-entry append below must always copy (full-capacity slice).
	useGit := w.opts.Gitignore && !gs.Empty()
	needSegs := useGit || scope != nil || !w.opts.Excludes.Empty() || !w.opts.Includes.Empty()
	var relSegs []string
	if needSegs && rel != "" {
		relSegs = strings.Split(rel, "/")
	}
	relSegs = relSegs[:len(relSegs):len(relSegs)]

	for _, e := range entries {
		name := e.Name()
		erel := relPre + name

		var segs []string
		if needSegs {
			segs = append(relSegs, name)
		}

		isDir := e.IsDir()
		childAbs := absPre + name
		if e.Type()&fs.ModeSymlink != 0 {
			if !w.opts.FollowSymlinks {
				if w.trace {
					w.tracef("skip %s (symlink; --follow-symlinks off)", displayPath(prefix, erel))
				}
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
			if w.trace {
				w.tracef("skip %s (vcs metadata)", displayPath(prefix, erel))
			}
			continue
		}
		if !w.opts.Hidden && strings.HasPrefix(name, ".") {
			if w.trace {
				w.tracef("skip %s (hidden; use --hidden to count)", displayPath(prefix, erel))
			}
			continue
		}
		if pat := w.opts.Excludes.MatchedByParts(segs); pat != "" {
			if w.trace {
				w.tracef("skip %s (excluded by pattern %q)", displayPath(prefix, erel), pat)
			}
			continue
		}
		// The tracked filter runs before smart detection: untracked trees
		// are pruned outright, while a *committed* vendor dir survives to be
		// smart-excluded (and attributed) below.
		if tr != nil {
			if isDir && !tr.dirs[erel] {
				if w.trace {
					w.tracef("skip %s (no git-tracked files)", displayPath(prefix, erel))
				}
				continue
			}
			if !isDir && !tr.files[erel] {
				if w.trace {
					w.tracef("skip %s (not tracked by git)", displayPath(prefix, erel))
				}
				continue
			}
		}
		// Smart detection is checked before gitignore so an ecosystem
		// directory (node_modules, target, ...) is always attributed to its
		// detector in the report, even when the project also gitignores it.
		if isDir && scope != nil {
			if d := scope.ExcludedByParts(segs); d != "" {
				disp := displayPath(prefix, erel)
				if w.trace {
					w.tracef("skip %s (smart: %s)", disp, d)
				}
				w.recordExcluded(disp, d)
				continue
			}
		}
		if useGit {
			if w.trace {
				if ignored, src := gs.IgnoredByParts(segs, isDir); ignored {
					w.tracef("skip %s (gitignored by %s)", displayPath(prefix, erel), src)
					continue
				}
			} else if gs.IgnoredParts(segs, isDir) {
				continue
			}
		}

		if isDir {
			// Hand the subtree to a walk goroutine when the pool has room;
			// otherwise recurse inline. gs and scope are immutable once
			// built, so sharing them across goroutines is safe.
			if w.walkSem != nil {
				select {
				case w.walkSem <- struct{}{}:
					w.walkWG.Add(1)
					go func(abs, rel string) {
						defer w.walkWG.Done()
						defer func() { <-w.walkSem }()
						w.walkDir(abs, prefix, rel, gs, scope, tr)
					}(childAbs, erel)
					continue
				default:
				}
			}
			w.walkDir(childAbs, prefix, erel, gs, scope, tr)
			continue
		}
		if !e.Type().IsRegular() && e.Type()&fs.ModeSymlink == 0 {
			continue // sockets, devices, pipes
		}
		if !w.opts.Includes.Empty() && !w.opts.Includes.MatchesParts(segs) {
			if w.trace {
				w.tracef("skip %s (no include pattern matches)", displayPath(prefix, erel))
			}
			continue
		}
		w.dispatchGit(childAbs, prefix, erel, tr, erel)
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

func (w *walker) dispatch(abs, prefix, erel string) {
	w.dispatchGit(abs, prefix, erel, nil, "")
}

func (w *walker) dispatchGit(abs, prefix, erel string, tr *trackedSet, trackedPath string) {
	// Overlapping roots (aloc . ./src) must not double-count. With a single
	// root and no symlink following every path is reached exactly once, so
	// the map and its lock are skipped.
	if w.useSeen {
		w.mu.Lock()
		dup := w.seen[abs]
		w.seen[abs] = true
		w.mu.Unlock()
		if dup {
			if w.trace {
				w.tracef("skip %s (already counted via another root)", displayPath(prefix, erel))
			}
			return
		}
	}

	if len(w.opts.Extensions) > 0 {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(abs)), ".")
		if !w.opts.Extensions[ext] {
			if w.trace {
				w.tracef("skip %s (extension not selected by --ext)", displayPath(prefix, erel))
			}
			return
		}
	}
	j := job{abs: abs, prefix: prefix, erel: erel}
	if tr != nil && tr.blobs != nil {
		j.oid = tr.oids[trackedPath]
		j.blobs = tr.blobs
	}
	w.jobs <- j
}

const (
	// initialBufSize is each worker's scratch buffer; most source files fit.
	initialBufSize = 128 * 1024
	// maxKeepBufSize caps what a worker keeps between files after growing
	// for an unusually large one.
	maxKeepBufSize = 4 << 20
	// uringBatchHint is the io_uring backend's files-per-submission target,
	// defined here (not in the linux-only file) because Run sizes the jobs
	// channel with it on every platform.
	uringBatchHint = 32
)

// sniffable reports whether a file with basename base qualifies for the
// shebang sniff: only extensionless names do, matching tokei and scc. A
// leading dot is not an extension separator (filepath.Ext(".bashrc") is
// ".bashrc", but the name is extensionless), and a bare trailing dot
// ("file.") is not an extension either.
func sniffable(base string) bool {
	ext := filepath.Ext(strings.TrimPrefix(base, "."))
	return ext == "" || ext == "."
}

// countFile reads and counts one file using a single descriptor: the shebang
// sniff (when needed) and the content read share it, replacing the previous
// open/read/close + open/fstat/read/read/close sequence. buf is the worker's
// scratch buffer; the (possibly grown) buffer is returned for reuse.
func (w *walker) countFile(j job, buf []byte) []byte {
	l := w.opts.Registry.ByPath(j.abs)
	if j.oid != "" {
		return w.countGitBlob(j, l, buf)
	}
	var f rawFile
	opened := false
	off := 0
	if l == nil {
		// Unknown by name: a file with an unrecognized extension is skipped
		// without opening it; only extensionless names are sniffed for a
		// shebang, on the same descriptor the content read continues from.
		if !sniffable(filepath.Base(j.abs)) {
			if w.trace {
				w.tracef("skip %s (unknown language)", j.display())
			}
			return buf
		}
		var err error
		f, err = openRaw(j.abs)
		if err != nil {
			w.warnf("cannot read %s: %v", j.display(), err)
			return buf
		}
		opened = true
		n, err := f.read(buf[:256])
		if err != nil {
			f.close()
			w.warnf("cannot read %s: %v", j.display(), err)
			return buf
		}
		l = w.opts.Registry.ByShebang(buf[:n])
		if l == nil {
			f.close()
			if w.trace {
				w.tracef("skip %s (unknown language)", j.display())
			}
			return buf
		}
		off = n
	}
	if len(w.opts.Languages) > 0 && !w.opts.Languages[strings.ToLower(l.Name)] {
		if opened {
			f.close()
		}
		if w.trace {
			w.tracef("skip %s (language %s not selected by --lang)", j.display(), l.Name)
		}
		return buf
	}

	if !opened {
		var err error
		f, err = openRaw(j.abs)
		if err != nil {
			w.warnf("cannot read %s: %v", j.display(), err)
			return buf
		}
	}
	total, buf, err := readFull(f, buf, off)
	f.close()
	if err != nil {
		w.warnf("cannot read %s: %v", j.display(), err)
		return buf
	}
	w.emitCounted(j, l, buf[:total])
	return buf
}

// emitCounted applies the binary check and counts content, sending the
// result. Shared tail of the standard and io_uring read paths, so both
// produce byte-identical reports.
func (w *walker) emitCounted(j job, l *lang.Language, content []byte) {
	if counter.IsBinary(content) {
		w.warnf("skipping binary file %s", j.display())
		return
	}
	if w.opts.TraceFiles {
		w.tracef("count %s (%s)", j.display(), l.Name)
	}
	res := result{language: l.Name, stats: counter.Count(content, l)}
	if w.opts.ByFile || w.opts.Dedup {
		// display is consumed only by per-file detail and dedup's
		// deterministic first-path pick; aggregate runs never read it.
		res.display = j.display()
	}
	if w.opts.Dedup {
		res.hash = sha256.Sum256(content)
	}
	w.results <- res
}

func (w *walker) countGitBlob(j job, l *lang.Language, buf []byte) []byte {
	if l == nil && !sniffable(filepath.Base(j.abs)) {
		if w.trace {
			w.tracef("skip %s (unknown language)", j.display())
		}
		return buf
	}
	content, err := j.blobs.Read(j.oid, buf)
	if err != nil {
		w.warnf("cannot read git object for %s: %v; falling back to filesystem", j.display(), err)
		j.oid = ""
		return w.countFile(j, buf)
	}
	buf = content[:cap(content)]
	if l == nil {
		l = w.opts.Registry.ByShebang(content[:min(256, len(content))])
		if l == nil {
			if w.trace {
				w.tracef("skip %s (unknown language)", j.display())
			}
			return buf
		}
	}
	if len(w.opts.Languages) > 0 && !w.opts.Languages[strings.ToLower(l.Name)] {
		if w.trace {
			w.tracef("skip %s (language %s not selected by --lang)", j.display(), l.Name)
		}
		return buf
	}
	if counter.IsBinary(content) {
		w.warnf("skipping binary file %s", j.display())
		return buf
	}
	if w.opts.TraceFiles {
		w.tracef("count %s (%s)", j.display(), l.Name)
	}
	res := result{language: l.Name, stats: counter.Count(content, l)}
	if w.opts.ByFile || w.opts.Dedup {
		res.display = j.display()
	}
	if w.opts.Dedup {
		res.hash = sha256.Sum256(content)
	}
	w.results <- res
	return buf
}

// readFull reads the rest of the file into buf starting at off, growing the
// buffer as needed, until a 0-byte read confirms EOF. The explicit EOF read
// keeps short reads (FUSE, network filesystems) correct at the cost of one
// extra read syscall per file.
func readFull(f rawFile, buf []byte, off int) (int, []byte, error) {
	total := off
	for {
		if total == len(buf) {
			nb := make([]byte, 2*len(buf))
			copy(nb, buf[:total])
			buf = nb
		}
		m, err := f.read(buf[total:])
		if m > 0 {
			total += m
		}
		if err != nil {
			return total, buf, err
		}
		if m == 0 {
			return total, buf, nil // EOF
		}
	}
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
