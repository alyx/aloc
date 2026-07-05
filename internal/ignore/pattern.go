// Package ignore implements aloc's path matching: user include/exclude
// patterns and .gitignore files. All paths handled here are slash-separated
// and relative to the scan root.
package ignore

import (
	"fmt"
	"path"
	"strings"
)

// Pattern is one user-supplied include or exclude pattern. Semantics:
//
//   - "./x/y" or any pattern containing "/": anchored at the scan root,
//     matching that subtree. Segments may use *, ?, [...]; "**" matches any
//     number of segments.
//   - no "/": broad name match — matches any single path component anywhere
//     in the tree (glob allowed).
type Pattern struct {
	raw      string
	anchored bool
	segs     []string
}

// ParsePattern validates and compiles a pattern.
func ParsePattern(raw string) (Pattern, error) {
	if raw == "" {
		return Pattern{}, fmt.Errorf("empty pattern")
	}
	p := Pattern{raw: raw}
	s := strings.TrimSuffix(raw, "/")
	if strings.HasPrefix(s, "./") || strings.Contains(s, "/") {
		p.anchored = true
		s = strings.TrimPrefix(s, "./")
		s = path.Clean(s)
		if s == "." || s == ".." || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/") {
			return Pattern{}, fmt.Errorf("pattern %q must name a path inside the scan root", raw)
		}
	}
	p.segs = strings.Split(s, "/")
	for _, seg := range p.segs {
		if seg != "**" {
			if _, err := path.Match(seg, "x"); err != nil {
				return Pattern{}, fmt.Errorf("pattern %q: bad glob segment %q", raw, seg)
			}
		}
	}
	return p, nil
}

func (p Pattern) String() string { return p.raw }

// Matches reports whether rel (slash-separated, relative to the scan root)
// is matched by p — either directly or because an ancestor of rel matches,
// which gives subtree semantics.
func (p Pattern) Matches(rel string) bool {
	parts := splitPath(rel)
	if parts == nil {
		return false
	}
	if !p.anchored {
		// Broad name: any component matches.
		for _, part := range parts {
			if ok, _ := path.Match(p.segs[0], part); ok {
				return true
			}
		}
		return false
	}
	return matchSegs(p.segs, parts, true)
}

// matchSegs matches pattern segments against path segments; "**" spans any
// number of segments. With allowPrefix, matching a leading prefix of parts
// counts (subtree semantics); otherwise pat must consume all of parts.
func matchSegs(pat, parts []string, allowPrefix bool) bool {
	if len(pat) == 0 {
		return allowPrefix || len(parts) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if matchSegs(pat[1:], parts[i:], allowPrefix) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], parts[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], parts[1:], allowPrefix)
}

func splitPath(rel string) []string {
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return nil
	}
	return strings.Split(rel, "/")
}

// Set is an ordered collection of patterns.
type Set struct {
	patterns []Pattern
}

// ParseSet compiles all patterns, failing on the first invalid one.
func ParseSet(raw []string) (*Set, error) {
	s := &Set{}
	for _, r := range raw {
		p, err := ParsePattern(r)
		if err != nil {
			return nil, err
		}
		s.patterns = append(s.patterns, p)
	}
	return s, nil
}

// Empty reports whether the set has no patterns.
func (s *Set) Empty() bool { return s == nil || len(s.patterns) == 0 }

// Matches reports whether any pattern matches rel.
func (s *Set) Matches(rel string) bool {
	return s.MatchedBy(rel) != ""
}

// MatchedBy returns the raw text of the first pattern matching rel, or "".
func (s *Set) MatchedBy(rel string) string {
	if s == nil {
		return ""
	}
	for _, p := range s.patterns {
		if p.Matches(rel) {
			return p.raw
		}
	}
	return ""
}
