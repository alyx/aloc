// Package counter classifies the lines of a source file as code, comment, or
// blank using a small per-line state machine that tracks block comments and
// string literals across lines.
package counter

import (
	"bytes"

	"github.com/alyx/aloc/internal/lang"
)

// Stats are the counts for one file (or an aggregate).
type Stats struct {
	Files   int `json:"files" yaml:"files"`
	Lines   int `json:"lines" yaml:"lines"`
	Blank   int `json:"blank" yaml:"blank"`
	Comment int `json:"comment" yaml:"comment"`
	Code    int `json:"code" yaml:"code"`
}

// Add accumulates other into s.
func (s *Stats) Add(o Stats) {
	s.Files += o.Files
	s.Lines += o.Lines
	s.Blank += o.Blank
	s.Comment += o.Comment
	s.Code += o.Code
}

// IsBinary sniffs content (typically the first few KB of a file) for a NUL
// byte, the same heuristic git uses.
func IsBinary(content []byte) bool {
	const sniffLen = 8192
	if len(content) > sniffLen {
		content = content[:sniffLen]
	}
	return bytes.IndexByte(content, 0) >= 0
}

// state carries scanner context across lines.
type state struct {
	blockPair  int // 1-based index into lang.BlockComments; 0 = none
	blockDepth int
	quotePair  int  // 1-based index into the multi-line quote set; 0 = none
	quoteRaw   bool // active multi-line quote is a raw quote (no escapes)
}

// Count classifies every line of content according to l. It never fails:
// malformed input degrades to code lines.
func Count(content []byte, l *lang.Language) Stats {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM

	var st state
	var s Stats
	s.Files = 1
	for len(content) > 0 {
		var line []byte
		if i := bytes.IndexByte(content, '\n'); i >= 0 {
			line = content[:i]
			content = content[i+1:]
		} else {
			line = content
			content = nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})

		s.Lines++
		switch classify(line, l, &st) {
		case kindBlank:
			s.Blank++
		case kindComment:
			s.Comment++
		default:
			s.Code++
		}
	}
	return s
}

type lineKind int

const (
	kindCode lineKind = iota
	kindComment
	kindBlank
)

func classify(line []byte, l *lang.Language, st *state) lineKind {
	if len(bytes.TrimSpace(line)) == 0 {
		return kindBlank
	}

	hasCode := false
	hasComment := false
	i := 0
scan:
	for i < len(line) {
		// Inside a multi-line string: everything is code until the closer.
		if st.quotePair != 0 {
			hasCode = true
			var close string
			if st.quoteRaw {
				close = l.RawQuotes[st.quotePair-1][1]
			} else {
				close = l.MultiQuotes[st.quotePair-1][1]
			}
			j := indexDelim(line[i:], close, !st.quoteRaw)
			if j < 0 {
				break scan
			}
			i += j + len(close)
			st.quotePair = 0
			continue
		}

		// Inside a block comment: look for the closer (and nested openers).
		if st.blockPair != 0 {
			hasComment = true
			pair := l.BlockComments[st.blockPair-1]
			for i < len(line) {
				if l.Nested && hasPrefix(line[i:], pair[0]) {
					st.blockDepth++
					i += len(pair[0])
					continue
				}
				if hasPrefix(line[i:], pair[1]) {
					i += len(pair[1])
					st.blockDepth--
					if st.blockDepth == 0 {
						st.blockPair = 0
						continue scan
					}
					continue
				}
				i++
			}
			break scan
		}

		c := line[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}

		// Block comment opener — checked before line comments so that
		// openers sharing a prefix with them (Lua's --[[ vs --, Julia's #=
		// vs #) are not swallowed by the shorter marker.
		if p := prefixPair(line[i:], l.BlockComments); p >= 0 {
			hasComment = true
			st.blockPair = p + 1
			st.blockDepth = 1
			i += len(l.BlockComments[p][0])
			continue
		}

		// Line comment: rest of the line is comment.
		for _, lc := range l.LineComments {
			if hasPrefix(line[i:], lc) {
				hasComment = true
				break scan
			}
		}

		// Multi-line string openers (checked before single-line quotes so
		// `"""` wins over `"`).
		if p := prefixPair(line[i:], l.MultiQuotes); p >= 0 {
			hasCode = true
			st.quotePair = p + 1
			st.quoteRaw = false
			i += len(l.MultiQuotes[p][0])
			continue
		}
		if p := prefixPair(line[i:], l.RawQuotes); p >= 0 {
			hasCode = true
			st.quotePair = p + 1
			st.quoteRaw = true
			i += len(l.RawQuotes[p][0])
			continue
		}

		// Single-line string: skip to the closer so comment markers inside
		// strings are not misread. An unterminated string ends at EOL.
		if p := prefixPair(line[i:], l.Quotes); p >= 0 {
			hasCode = true
			open, close := l.Quotes[p][0], l.Quotes[p][1]
			i += len(open)
			j := indexDelim(line[i:], close, true)
			if j < 0 {
				break scan
			}
			i += j + len(close)
			continue
		}

		hasCode = true
		i++
	}

	if hasCode {
		return kindCode
	}
	if hasComment {
		return kindComment
	}
	return kindCode
}

// prefixPair returns the index of the first pair whose opener is a prefix of
// s, or -1.
func prefixPair(s []byte, pairs [][2]string) int {
	for p, pair := range pairs {
		if hasPrefix(s, pair[0]) {
			return p
		}
	}
	return -1
}

func hasPrefix(s []byte, prefix string) bool {
	return len(prefix) > 0 && len(s) >= len(prefix) && string(s[:len(prefix)]) == prefix
}

// indexDelim finds delim in s, skipping backslash-escaped occurrences when
// escapes is true. Returns -1 if not found.
func indexDelim(s []byte, delim string, escapes bool) int {
	for i := 0; i+len(delim) <= len(s); i++ {
		if escapes && s[i] == '\\' {
			i++ // skip the escaped byte
			continue
		}
		if string(s[i:i+len(delim)]) == delim {
			return i
		}
	}
	return -1
}
