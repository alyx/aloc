// Package counter classifies the lines of a source file as code, comment, or
// blank using a small per-line state machine that tracks block comments and
// string literals across lines.
package counter

import (
	"bytes"
	"sync"

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

// compiled caches per-language scan tables so the per-byte inner loop can
// reject ordinary identifier bytes with a single array load instead of
// prefix-matching every delimiter list.
type compiled struct {
	// gate[b] is true when byte b can start any delimiter (line comment,
	// block comment opener, quote opener) or is ' '/'\t'. Bytes with
	// gate[b]==false can never change scanner state and are plain code.
	gate [256]bool
	// fclass classifies each byte for the fused whole-buffer scan with a
	// single table load. clPlain bytes can never change scanner state,
	// end a line, or affect the blank check.
	fclass [256]byte
	// blockClosers[i] is BlockComments[i][1] as []byte for bytes.Index.
	blockClosers [][]byte
}

// Byte classes for the fused scan. clPlain must be zero: it is the table's
// default and the run loop compares against it.
const (
	clPlain byte = iota // plain code byte, extend the run
	clWS                // ' ' or '\t'
	clNL                // '\n': finalize the line
	clCR                // '\r': stripped before '\n', plain otherwise
	clMaybe             // '\v', '\f', non-ASCII: TrimSpace may strip these
	clMark              // possible delimiter first byte
)

var compiledCache sync.Map // *lang.Language -> *compiled

func compile(l *lang.Language) *compiled {
	c := &compiled{}
	c.gate[' '] = true
	c.gate['\t'] = true
	mark := func(s string) {
		if len(s) > 0 {
			c.gate[s[0]] = true
		}
	}
	for _, lc := range l.LineComments {
		mark(lc)
	}
	for _, p := range l.BlockComments {
		mark(p[0])
		c.blockClosers = append(c.blockClosers, []byte(p[1]))
	}
	for _, p := range l.MultiQuotes {
		mark(p[0])
	}
	for _, p := range l.RawQuotes {
		mark(p[0])
	}
	for _, p := range l.Quotes {
		mark(p[0])
	}
	for b := 0; b < 256; b++ {
		switch {
		case c.gate[b] && b != ' ' && b != '\t':
			c.fclass[b] = clMark
		case b == ' ' || b == '\t':
			c.fclass[b] = clWS
		case b == '\n':
			c.fclass[b] = clNL
		case b == '\r':
			c.fclass[b] = clCR
		case b == '\v' || b == '\f' || b >= 0x80:
			c.fclass[b] = clMaybe
		}
	}
	return c
}

func compiledFor(l *lang.Language) *compiled {
	if c, ok := compiledCache.Load(l); ok {
		return c.(*compiled)
	}
	c, _ := compiledCache.LoadOrStore(l, compile(l))
	return c.(*compiled)
}

// Count classifies every line of content according to l. It never fails:
// malformed input degrades to code lines.
//
// The scan is a single fused pass over the whole buffer: line boundaries are
// events inside the state machine rather than an outer split loop, so plain
// code skips through the gate table without per-line call overhead, and
// multi-line comments and strings jump straight to their closer with one
// bytes.Index over the remaining buffer instead of one failed search per
// line. Per-line semantics (blank via bytes.TrimSpace, one stripped trailing
// \r, line-bounded single-line strings) are preserved exactly; oldCount in
// old_impl_test.go is the differential oracle.
func Count(content []byte, l *lang.Language) Stats {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM

	cp := compiledFor(l)
	var st state
	var s Stats
	s.Files = 1

	n := len(content)
	i, lineStart := 0, 0
	// eol caches the position of the next '\n' at or after i (n when none);
	// valid while eol >= i. Lines with several strings or comment markers
	// reuse it instead of re-scanning to the line end.
	eol := -1
	// Per-line flags. sawText: a byte TrimSpace can never strip was seen, so
	// the line cannot be blank. maybeWS: only bytes TrimSpace *might* strip
	// were seen ('\v', '\f', non-ASCII, interior '\r'); blankness falls back
	// to TrimSpace on the whole line, exactly like the per-line scanner.
	sawText, maybeWS, hasCode, hasComment := false, false, false, false

scan:
	for i < n {
		// Inside a multi-line string: everything is code until the closer.
		// The closer is located once over the remaining buffer (escape
		// parity cannot cross a newline, so this matches the per-line
		// search), then the enclosed line boundaries are replayed.
		if st.quotePair != 0 {
			hasCode, maybeWS = true, true
			var close string
			if st.quoteRaw {
				close = l.RawQuotes[st.quotePair-1][1]
			} else {
				close = l.MultiQuotes[st.quotePair-1][1]
			}
			j := indexDelim(content[i:], close, !st.quoteRaw)
			stop := n
			if j >= 0 {
				stop = i + j
			}
			for {
				k := bytes.IndexByte(content[i:], '\n')
				if j >= 0 && (k < 0 || i+k > stop) {
					i = stop + len(close)
					st.quotePair = 0
					break
				}
				if k < 0 {
					i = n
					break
				}
				s.endLine(content[lineStart:i+k], sawText, maybeWS, hasCode, hasComment)
				i += k + 1
				lineStart = i
				sawText, maybeWS, hasCode, hasComment = false, true, true, false
			}
			continue
		}

		// Inside a block comment: look for the closer (and nested openers).
		if st.blockPair != 0 {
			hasComment, maybeWS = true, true
			pair := l.BlockComments[st.blockPair-1]
			if !l.Nested {
				// Depth is always 1: jump straight to the closer, wherever
				// it is, and replay the line boundaries in between.
				j := bytes.Index(content[i:], cp.blockClosers[st.blockPair-1])
				stop := n
				if j >= 0 {
					stop = i + j
				}
				for {
					k := bytes.IndexByte(content[i:], '\n')
					if j >= 0 && (k < 0 || i+k > stop) {
						i = stop + len(pair[1])
						st.blockDepth = 0
						st.blockPair = 0
						break
					}
					if k < 0 {
						i = n
						break
					}
					s.endLine(content[lineStart:i+k], sawText, maybeWS, hasCode, hasComment)
					i += k + 1
					lineStart = i
					sawText, maybeWS, hasCode, hasComment = false, true, false, true
				}
				continue
			}
			// Nested: openers and closers must be counted byte by byte;
			// markers never contain \n or \r, so unbounded prefix matches
			// cannot cross a line boundary.
			o0, c0 := pair[0][0], pair[1][0]
			for i < n {
				b := content[i]
				if b == '\n' {
					s.endLine(content[lineStart:i], sawText, maybeWS, hasCode, hasComment)
					i++
					lineStart = i
					sawText, maybeWS, hasCode, hasComment = false, true, false, true
					continue
				}
				if b != o0 && b != c0 {
					i++
					continue
				}
				if hasPrefix(content[i:], pair[0]) {
					st.blockDepth++
					i += len(pair[0])
					continue
				}
				if hasPrefix(content[i:], pair[1]) {
					i += len(pair[1])
					st.blockDepth--
					if st.blockDepth == 0 {
						st.blockPair = 0
						break
					}
					continue
				}
				i++
			}
			continue
		}

		// Normal state: scan in a tight loop that re-checks the multi-line
		// states only when a delimiter actually opens one (continue scan).
		for i < n {
			switch cp.fclass[content[i]] {
			case clPlain:
				// Fast path: bytes that cannot start any delimiter, end a
				// line, or confuse the blank check are plain code. Skip the
				// whole run with one table load per byte.
				sawText, hasCode = true, true
				for i++; i < n && cp.fclass[content[i]] == clPlain; i++ {
				}

			case clWS:
				for i++; i < n && cp.fclass[content[i]] == clWS; i++ {
				}

			case clNL:
				// Finalize the line inline; endLine stays for the other exits.
				s.Lines++
				switch {
				case !sawText && (!maybeWS || wsOnly(content[lineStart:i])):
					s.Blank++
				case hasCode || !hasComment:
					s.Code++
				default:
					s.Comment++
				}
				i++
				lineStart = i
				sawText, maybeWS, hasCode, hasComment = false, false, false, false

			case clCR:
				if i+1 == n || content[i+1] == '\n' {
					// The one trailing \r the per-line scanner strips.
					i++
				} else {
					// Interior \r: plain code byte, but TrimSpace-strippable.
					hasCode, maybeWS = true, true
					i++
				}

			case clMaybe:
				// '\v', '\f', or non-ASCII: plain code bytes whose whitespace-
				// ness only TrimSpace can decide at line end.
				hasCode, maybeWS = true, true
				for i++; i < n && cp.fclass[content[i]] == clMaybe; i++ {
				}

			default: // clMark
				// Block comment opener — checked before line comments so that
				// openers sharing a prefix with them (Lua's --[[ vs --, Julia's
				// #= vs #) are not swallowed by the shorter marker.
				if p := prefixPair(content[i:], l.BlockComments); p >= 0 {
					sawText, hasComment = true, true
					st.blockPair = p + 1
					st.blockDepth = 1
					i += len(l.BlockComments[p][0])
					continue scan
				}

				// Line comment: the rest of the line is comment; jump to it.
				if lineComments(content[i:], l) {
					sawText, hasComment = true, true
					if eol < i {
						if k := bytes.IndexByte(content[i:], '\n'); k >= 0 {
							eol = i + k
						} else {
							eol = n
						}
					}
					i = eol
					continue
				}

				// Multi-line string openers (checked before single-line quotes
				// so `"""` wins over `"`).
				if p := prefixPair(content[i:], l.MultiQuotes); p >= 0 {
					sawText, hasCode = true, true
					st.quotePair = p + 1
					st.quoteRaw = false
					i += len(l.MultiQuotes[p][0])
					continue scan
				}
				if p := prefixPair(content[i:], l.RawQuotes); p >= 0 {
					sawText, hasCode = true, true
					st.quotePair = p + 1
					st.quoteRaw = true
					i += len(l.RawQuotes[p][0])
					continue scan
				}

				// Single-line string: skip to the closer so comment markers
				// inside strings are not misread. The search is bounded at the
				// line end: an unterminated string ends at EOL.
				if p := prefixPair(content[i:], l.Quotes); p >= 0 {
					sawText, hasCode = true, true
					open, close := l.Quotes[p][0], l.Quotes[p][1]
					i += len(open)
					if eol < i {
						if k := bytes.IndexByte(content[i:], '\n'); k >= 0 {
							eol = i + k
						} else {
							eol = n
						}
					}
					j := indexDelim(content[i:eol], close, true)
					if j < 0 {
						i = eol
						continue
					}
					i += j + len(close)
					continue
				}

				// A delimiter-looking byte that matched nothing (`/` as
				// division).
				sawText, hasCode = true, true
				i++
			}
		}
	}
	if lineStart < n {
		s.endLine(content[lineStart:n], sawText, maybeWS, hasCode, hasComment)
	}
	return s
}

type lineKind int

const (
	kindCode lineKind = iota
	kindComment
	kindBlank
)

// endLine finalizes one line. seg is the raw line without its terminator
// (a trailing \r may remain; TrimSpace strips it anyway).
func (s *Stats) endLine(seg []byte, sawText, maybeWS, hasCode, hasComment bool) {
	s.Lines++
	if !sawText && (!maybeWS || wsOnly(seg)) {
		s.Blank++
	} else if hasCode || !hasComment {
		s.Code++
	} else {
		s.Comment++
	}
}

// wsOnly reports whether seg is entirely whitespace under the same rules as
// the per-line scanner's blank check.
func wsOnly(seg []byte) bool {
	return len(bytes.TrimSpace(seg)) == 0
}

// lineComments reports whether s starts with any of l's line comments.
func lineComments(s []byte, l *lang.Language) bool {
	for _, lc := range l.LineComments {
		if hasPrefix(s, lc) {
			return true
		}
	}
	return false
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
	if len(delim) == 0 {
		return -1
	}
	if !escapes {
		return bytes.Index(s, []byte(delim))
	}
	if delim[0] == '\\' {
		// A delimiter starting with a backslash is always consumed by the
		// escape rule; preserve the historical scan behavior.
		return indexDelimSlow(s, delim)
	}
	from := 0
	for {
		j := bytes.IndexByte(s[from:], delim[0])
		if j < 0 {
			return -1
		}
		j += from
		if j+len(delim) > len(s) {
			return -1
		}
		if string(s[j:j+len(delim)]) == delim {
			// The forward scan consumes backslashes in pairs, so the
			// occurrence is escaped iff an odd run precedes it.
			n := 0
			for k := j - 1; k >= 0 && s[k] == '\\'; k-- {
				n++
			}
			if n%2 == 0 {
				return j
			}
		}
		from = j + 1
	}
}

func indexDelimSlow(s []byte, delim string) int {
	for i := 0; i+len(delim) <= len(s); i++ {
		if s[i] == '\\' {
			i++ // skip the escaped byte
			continue
		}
		if string(s[i:i+len(delim)]) == delim {
			return i
		}
	}
	return -1
}
