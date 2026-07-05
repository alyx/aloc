// Package cli parses flags, merges them with the config file, and runs the
// pipeline.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/alyx/aloc/internal/config"
	"github.com/alyx/aloc/internal/detect"
	"github.com/alyx/aloc/internal/ignore"
	"github.com/alyx/aloc/internal/lang"
	"github.com/alyx/aloc/internal/output"
	"github.com/alyx/aloc/internal/walker"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Main is the real entry point; it returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("aloc", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		format    = fs.String("format", "", "output format: table, json, yaml")
		outFile   = fs.String("output", "", "write output to `file` instead of stdout")
		excludes  multiFlag
		includes  multiFlag
		exts      = fs.String("ext", "", "count only these comma-separated extensions")
		langs     = fs.String("lang", "", "count only these comma-separated languages")
		byFile    = fs.Bool("by-file", false, "include per-file counts")
		noSmart   = fs.Bool("no-smart", false, "disable smart ecosystem exclusion")
		noDetect  = fs.String("no-detect", "", "disable comma-separated detectors")
		noGitign  = fs.Bool("no-gitignore", false, "do not respect .gitignore files")
		hidden    = fs.Bool("hidden", false, "count hidden files and directories")
		tracked   = fs.Bool("tracked", false, "count only files tracked by git in each path's repository")
		dedup     = fs.Bool("dedup", false, "count only one copy of files with identical content")
		follow    = fs.Bool("follow-symlinks", false, "follow symbolic links")
		jobs      = fs.Int("jobs", 0, "number of parallel workers (0 = CPUs)")
		cfgPath   = fs.String("config", "", "config `file` (default: .aloc.yml, then ~/.config/aloc/config.yml)")
		noConfig  = fs.Bool("no-config", false, "do not load any config file")
		listLangs = fs.Bool("list-languages", false, "list known languages and exit")
		listDet   = fs.Bool("list-detectors", false, "list smart-exclusion detectors and exit")
		verbose   = fs.Bool("verbose", false, "print warnings and applied smart exclusions to stderr")
		trace     = fs.Bool("vv", false, "explain every skip decision — gitignore, patterns, hidden, filters (implies -v)")
		traceAll  = fs.Bool("vvv", false, "also list every counted file and its detected language (implies -vv)")
		version   = fs.Bool("version", false, "print version and exit")
	)
	fs.Var(&excludes, "exclude", "exclude `pattern` (repeatable)")
	fs.Var(&includes, "include", "count only paths matching `pattern` (repeatable)")
	// Short aliases.
	fs.StringVar(format, "f", "", "alias for --format")
	fs.StringVar(outFile, "o", "", "alias for --output")
	fs.Var(&excludes, "e", "alias for --exclude")
	fs.Var(&includes, "i", "alias for --include")
	fs.StringVar(langs, "l", "", "alias for --lang")
	fs.IntVar(jobs, "j", 0, "alias for --jobs")
	fs.BoolVar(verbose, "v", false, "alias for --verbose")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: aloc [flags] [path ...]\n\nCount lines of code, comments, and blanks by language.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(args)); err != nil {
		return 1
	}

	if *version {
		fmt.Fprintf(stdout, "aloc %s (%s)\n", Version, runtime.Version())
		return 0
	}

	fail := func(err error) int {
		fmt.Fprintf(stderr, "aloc: %v\n", err)
		return 1
	}

	cfg := &config.Config{}
	if !*noConfig {
		c, err := config.Load(*cfgPath)
		if err != nil {
			return fail(err)
		}
		cfg = c
	}

	// CLI flags override config; slices concatenate (config first).
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	pickStr := func(flagSet bool, flagVal string, cfgVal *string) string {
		if flagSet || cfgVal == nil {
			return flagVal
		}
		return *cfgVal
	}
	pickBool := func(flagSet bool, flagVal bool, cfgVal *bool) bool {
		if flagSet || cfgVal == nil {
			return flagVal
		}
		return *cfgVal
	}

	formatName := pickStr(set["format"] || set["f"], *format, cfg.Format)
	if formatName == "" {
		formatName = "table"
	}
	useByFile := pickBool(set["by-file"], *byFile, cfg.ByFile)
	useHidden := pickBool(set["hidden"], *hidden, cfg.Hidden)
	useTracked := pickBool(set["tracked"], *tracked, cfg.Tracked)
	useDedup := pickBool(set["dedup"], *dedup, cfg.Dedup)
	useFollow := pickBool(set["follow-symlinks"], *follow, cfg.FollowSymlinks)
	useGitignore := !*noGitign
	if !set["no-gitignore"] && cfg.Gitignore != nil {
		useGitignore = *cfg.Gitignore
	}
	useSmart := !*noSmart
	if !set["no-smart"] && cfg.SmartExclude != nil {
		useSmart = *cfg.SmartExclude
	}
	useJobs := *jobs
	if !(set["jobs"] || set["j"]) && cfg.Jobs != nil {
		useJobs = *cfg.Jobs
	}

	registry := lang.NewRegistry()
	if err := cfg.ApplyDefinitions(registry); err != nil {
		return fail(err)
	}

	if *listLangs {
		for _, n := range registry.Names() {
			fmt.Fprintln(stdout, n)
		}
		return 0
	}

	var engine *detect.Engine
	disabled := slices.Concat(cfg.Detectors.Disable, splitList(*noDetect))
	eng, err := detect.NewEngine(cfg.CustomDetectors(), disabled)
	if err != nil {
		return fail(err)
	}
	if *listDet {
		for _, d := range eng.Detectors() {
			line := d.Name + ":"
			if len(d.Markers) > 0 {
				line += fmt.Sprintf(" markers %v -> exclude %v", d.Markers, d.ExcludeDirs)
			}
			if len(d.SelfMarkers) > 0 {
				line += fmt.Sprintf(" self-markers %v", d.SelfMarkers)
				if d.SelfName != "" {
					line += fmt.Sprintf(" (dirs named %q)", d.SelfName)
				}
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	}
	if useSmart {
		engine = eng
	}

	excludeSet, err := ignore.ParseSet(slices.Concat(cfg.Exclude, excludes))
	if err != nil {
		return fail(err)
	}
	includeSet, err := ignore.ParseSet(slices.Concat(cfg.Include, includes))
	if err != nil {
		return fail(err)
	}

	extFilter := map[string]bool{}
	for _, e := range slices.Concat(cfg.Extensions, splitList(*exts)) {
		extFilter[strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	langFilter := map[string]bool{}
	for _, l := range slices.Concat(cfg.Languages, splitList(*langs)) {
		if registry.Get(l) == nil {
			return fail(fmt.Errorf("unknown language %q (see --list-languages)", l))
		}
		langFilter[strings.ToLower(l)] = true
	}

	// Validate the format before walking so a typo fails fast.
	formatter, err := output.Get(formatName)
	if err != nil {
		return fail(err)
	}

	roots := fs.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	if *traceAll {
		*trace = true
	}
	if *trace {
		*verbose = true
	}
	// All diagnostics go to stderr, terminal or not, so piping or -o always
	// yields a clean report.
	var warn, traceFn func(string, ...any)
	if *verbose {
		warn = func(f string, args ...any) { fmt.Fprintf(stderr, "aloc: "+f+"\n", args...) }
	}
	if *trace {
		traceFn = warn
	}

	rep, err := walker.Run(walker.Options{
		Roots:          roots,
		Excludes:       excludeSet,
		Includes:       includeSet,
		Extensions:     extFilter,
		Languages:      langFilter,
		Registry:       registry,
		Detect:         engine,
		Gitignore:      useGitignore,
		Hidden:         useHidden,
		Tracked:        useTracked,
		Dedup:          useDedup,
		FollowSymlinks: useFollow,
		Jobs:           useJobs,
		ByFile:         useByFile,
		Warn:           warn,
		Trace:          traceFn,
		TraceFiles:     *traceAll,
	})
	if err != nil {
		return fail(err)
	}

	// At -vv every smart exclusion was already traced in walk order; the
	// summary would just repeat it.
	if *verbose && !*trace {
		for _, e := range rep.Excluded {
			fmt.Fprintf(stderr, "aloc: smart-excluded %s (detector: %s)\n", e.Path, e.Detector)
		}
	}

	out := stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		out = f
	}
	if err := formatter(out, rep); err != nil {
		return fail(err)
	}
	return 0
}

// normalizeArgs rewrites GNU-style --long flags to Go's -long so both work.
func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			a = a[1:]
		}
		out = append(out, a)
	}
	return out
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
