package counter

// Verbatim copy of the pre-optimization implementation, used as a
// differential-testing oracle and as the "Old" side of benchmarks.

import (
	"bytes"

	"github.com/alyx/aloc/internal/lang"
)

func oldCount(content []byte, l *lang.Language) Stats {
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
		switch oldClassify(line, l, &st) {
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

func oldClassify(line []byte, l *lang.Language, st *state) lineKind {
	if len(bytes.TrimSpace(line)) == 0 {
		return kindBlank
	}

	hasCode := false
	hasComment := false
	i := 0
scan:
	for i < len(line) {
		if st.quotePair != 0 {
			hasCode = true
			var close string
			if st.quoteRaw {
				close = l.RawQuotes[st.quotePair-1][1]
			} else {
				close = l.MultiQuotes[st.quotePair-1][1]
			}
			j := oldIndexDelim(line[i:], close, !st.quoteRaw)
			if j < 0 {
				break scan
			}
			i += j + len(close)
			st.quotePair = 0
			continue
		}

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

		if p := prefixPair(line[i:], l.BlockComments); p >= 0 {
			hasComment = true
			st.blockPair = p + 1
			st.blockDepth = 1
			i += len(l.BlockComments[p][0])
			continue
		}

		for _, lc := range l.LineComments {
			if hasPrefix(line[i:], lc) {
				hasComment = true
				break scan
			}
		}

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

		if p := prefixPair(line[i:], l.Quotes); p >= 0 {
			hasCode = true
			open, close := l.Quotes[p][0], l.Quotes[p][1]
			i += len(open)
			j := oldIndexDelim(line[i:], close, true)
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

func oldIndexDelim(s []byte, delim string, escapes bool) int {
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

// gateOnlyCount is the old implementation plus ONLY the first-byte gate (no
// bytes.Index block scan, no IndexByte-based indexDelim) to decompose wins.
func gateOnlyCount(content []byte, l *lang.Language) Stats {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	cp := compiledFor(l)
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
		switch gateOnlyClassify(line, l, cp, &st) {
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

func gateOnlyClassify(line []byte, l *lang.Language, cp *compiled, st *state) lineKind {
	if len(bytes.TrimSpace(line)) == 0 {
		return kindBlank
	}
	hasCode := false
	hasComment := false
	i := 0
scan:
	for i < len(line) {
		if st.quotePair != 0 {
			hasCode = true
			var close string
			if st.quoteRaw {
				close = l.RawQuotes[st.quotePair-1][1]
			} else {
				close = l.MultiQuotes[st.quotePair-1][1]
			}
			j := oldIndexDelim(line[i:], close, !st.quoteRaw)
			if j < 0 {
				break scan
			}
			i += j + len(close)
			st.quotePair = 0
			continue
		}
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
		if !cp.gate[c] {
			hasCode = true
			for i++; i < len(line) && !cp.gate[line[i]]; i++ {
			}
			continue
		}
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		if p := prefixPair(line[i:], l.BlockComments); p >= 0 {
			hasComment = true
			st.blockPair = p + 1
			st.blockDepth = 1
			i += len(l.BlockComments[p][0])
			continue
		}
		for _, lc := range l.LineComments {
			if hasPrefix(line[i:], lc) {
				hasComment = true
				break scan
			}
		}
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
		if p := prefixPair(line[i:], l.Quotes); p >= 0 {
			hasCode = true
			open, close := l.Quotes[p][0], l.Quotes[p][1]
			i += len(open)
			j := oldIndexDelim(line[i:], close, true)
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
