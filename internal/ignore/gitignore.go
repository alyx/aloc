package ignore

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"strings"
)

// gitRule is one line of a .gitignore file.
type gitRule struct {
	raw     string // original line, for tracing
	segs    []string
	negate  bool
	dirOnly bool
	rooted  bool // pattern contained a "/" (other than trailing): anchored to the .gitignore dir
}

// GitIgnore holds the parsed rules of a single .gitignore file.
type GitIgnore struct {
	rules []gitRule
}

// ParseGitIgnore parses .gitignore content. Unparseable lines are skipped —
// matching git, which never fails on a bad ignore file.
func ParseGitIgnore(content []byte) *GitIgnore {
	g := &GitIgnore{}
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Trailing spaces are ignored unless escaped; we keep it simple and
		// trim them unconditionally.
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		r := gitRule{raw: line}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		line = strings.TrimPrefix(line, `\!`)
		line = strings.TrimPrefix(line, `\#`)
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.Contains(line, "/") {
			r.rooted = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		r.segs = strings.Split(line, "/")
		g.rules = append(g.rules, r)
	}
	return g
}

// match returns (decided, ignored, rule) for rel — the path relative to the
// directory containing this .gitignore. Later rules win, per git; rule is
// the raw text of the winning rule.
func (g *GitIgnore) match(rel string, isDir bool) (bool, bool, string) {
	parts := splitPath(rel)
	if parts == nil {
		return false, false, ""
	}
	decided, ignored := false, false
	rule := ""
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		var ok bool
		if r.rooted {
			ok = matchSegs(r.segs, parts, false)
		} else {
			// Unrooted: matches the basename at any depth.
			ok = matchSegs(r.segs, parts[len(parts)-1:], false)
		}
		if ok {
			decided, ignored, rule = true, !r.negate, r.raw
		}
	}
	return decided, ignored, rule
}

// GitStack is the chain of .gitignore files in scope for a directory, from
// the scan root down. The nearest file has the final say.
type GitStack struct {
	entries []gitStackEntry
}

type gitStackEntry struct {
	dir string // slash path relative to scan root; "" for the root itself
	g   *GitIgnore
}

// Push returns a new stack with g (from directory dir, relative to the scan
// root) appended. The receiver is not modified, so stacks can be shared
// between sibling subtrees.
func (s *GitStack) Push(dir string, g *GitIgnore) *GitStack {
	entries := make([]gitStackEntry, len(s.entries), len(s.entries)+1)
	copy(entries, s.entries)
	return &GitStack{entries: append(entries, gitStackEntry{dir: dir, g: g})}
}

// Ignored reports whether rel (relative to the scan root) is ignored.
func (s *GitStack) Ignored(rel string, isDir bool) bool {
	ignored, _ := s.IgnoredBy(rel, isDir)
	return ignored
}

// IgnoredBy reports whether rel is ignored and, when it is, describes the
// deciding rule as `path/.gitignore: "pattern"`.
func (s *GitStack) IgnoredBy(rel string, isDir bool) (bool, string) {
	if s == nil {
		return false, ""
	}
	ignored := false
	source := ""
	for _, e := range s.entries {
		sub := rel
		if e.dir != "" {
			if !strings.HasPrefix(rel, e.dir+"/") {
				continue
			}
			sub = rel[len(e.dir)+1:]
		}
		if decided, ig, rule := e.g.match(sub, isDir); decided {
			ignored = ig
			source = fmt.Sprintf("%s: %q", path.Join(e.dir, ".gitignore"), rule)
		}
	}
	return ignored, source
}
