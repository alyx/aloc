package counter

import (
	"strings"
	"testing"

	"github.com/alyx/aloc/internal/lang"
)

func mustLang(t *testing.T, name string) *lang.Language {
	t.Helper()
	l := lang.NewRegistry().Get(name)
	if l == nil {
		t.Fatalf("language %q not found", name)
	}
	return l
}

func TestCount(t *testing.T) {
	tests := []struct {
		name    string
		lang    string
		src     string
		blank   int
		comment int
		code    int
	}{
		{
			name: "go basics", lang: "Go",
			src:   "package main\n\n// comment\nfunc main() {}\n",
			blank: 1, comment: 1, code: 2,
		},
		{
			name: "block comment spans lines", lang: "Go",
			src:   "/*\nlicense\n*/\npackage main\n",
			blank: 0, comment: 3, code: 1,
		},
		{
			name: "comment marker inside string", lang: "Go",
			src:   "s := \"// not a comment\"\n",
			blank: 0, comment: 0, code: 1,
		},
		{
			name: "code after block comment close", lang: "Go",
			src:   "/* c */ x := 1\n",
			blank: 0, comment: 0, code: 1,
		},
		{
			name: "block comment after code", lang: "Go",
			src:   "x := 1 /* trailing\nstill comment */\n",
			blank: 0, comment: 1, code: 1,
		},
		{
			name: "raw string hides comment and spans lines", lang: "Go",
			src:   "s := `\n// inside raw string\n`\n",
			blank: 0, comment: 0, code: 3,
		},
		{
			name: "string with escaped quote", lang: "Go",
			src:   "s := \"a\\\"b\" // real comment\nx := 1\n",
			blank: 0, comment: 0, code: 2,
		},
		{
			name: "python docstring is code", lang: "Python",
			src:   "def f():\n    \"\"\"docstring\n    # not a comment\n    \"\"\"\n    return 1\n",
			blank: 0, comment: 0, code: 5,
		},
		{
			name: "python hash comments and shebang", lang: "Python",
			src:   "#!/usr/bin/env python3\n# comment\nx = 1\n",
			blank: 0, comment: 2, code: 1,
		},
		{
			name: "rust nested block comments", lang: "Rust",
			src:   "/* outer /* inner */ still comment */\nfn main() {}\n",
			blank: 0, comment: 1, code: 1,
		},
		{
			name: "haskell nested", lang: "Haskell",
			src:   "{- a {- b -} c -}\nmain = return ()\n",
			blank: 0, comment: 1, code: 1,
		},
		{
			name: "crlf line endings", lang: "Go",
			src:   "package main\r\n\r\n// c\r\n",
			blank: 1, comment: 1, code: 1,
		},
		{
			name: "utf8 bom", lang: "Go",
			src:   "\xEF\xBB\xBFpackage main\n",
			blank: 0, comment: 0, code: 1,
		},
		{
			name: "no trailing newline", lang: "Go",
			src:   "package main\nx := 1",
			blank: 0, comment: 0, code: 2,
		},
		{
			name: "empty file", lang: "Go",
			src: "", blank: 0, comment: 0, code: 0,
		},
		{
			name: "only blank lines", lang: "Go",
			src:   "\n   \n\t\n",
			blank: 3, comment: 0, code: 0,
		},
		{
			name: "comment-only file", lang: "Go",
			src:   "// a\n// b\n",
			blank: 0, comment: 2, code: 0,
		},
		{
			name: "blank line inside block comment is blank", lang: "Go",
			src:   "/*\n\n*/\n",
			blank: 1, comment: 2, code: 0,
		},
		{
			name: "html comment", lang: "HTML",
			src:   "<!-- hi -->\n<p>x</p>\n",
			blank: 0, comment: 1, code: 1,
		},
		{
			name: "markdown counts prose as code", lang: "Markdown",
			src:   "# Title\n\ntext\n",
			blank: 1, comment: 0, code: 2,
		},
		{
			name: "json has no comments", lang: "JSON",
			src:   "{\n  \"a\": 1\n}\n",
			blank: 0, comment: 0, code: 3,
		},
		{
			name: "device tree comments and strings", lang: "Device Tree",
			src:   "/dts-v1/;\n\n// board\nnode { compatible = \"vendor,//not-comment\"; /* tail */ };\n/* block\ncomment */\n",
			blank: 1, comment: 3, code: 2,
		},
		{
			name: "restructured text counts prose and directives as code", lang: "reStructuredText",
			src:   "Title\n=====\n\n.. note:: text\n",
			blank: 1, comment: 0, code: 3,
		},
		{
			name: "shell", lang: "Shell",
			src:   "#!/bin/sh\n# comment\necho 'hi # not comment'\n",
			blank: 0, comment: 2, code: 1,
		},
		{
			name: "unterminated string does not leak state", lang: "Go",
			src:   "s := \"unterminated\n// next line comment\n",
			blank: 0, comment: 1, code: 1,
		},
		{
			name: "lua long comment", lang: "Lua",
			src:   "--[[ block\ncomment ]]\nprint('x')\n",
			blank: 0, comment: 2, code: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := mustLang(t, tt.lang)
			got := Count([]byte(tt.src), l)
			if got.Blank != tt.blank || got.Comment != tt.comment || got.Code != tt.code {
				t.Errorf("Count() = blank %d, comment %d, code %d; want %d, %d, %d",
					got.Blank, got.Comment, got.Code, tt.blank, tt.comment, tt.code)
			}
			wantLines := tt.blank + tt.comment + tt.code
			if got.Lines != wantLines {
				t.Errorf("Lines = %d, want %d", got.Lines, wantLines)
			}
		})
	}
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("hello\nworld\n")) {
		t.Error("text misdetected as binary")
	}
	if !IsBinary([]byte("ELF\x00\x01\x02")) {
		t.Error("NUL bytes not detected as binary")
	}
	if !IsBinary(append(make([]byte, 4096), append([]byte{0}, make([]byte, 8192)...)...)) {
		t.Error("NUL inside sniff window not detected")
	}
}

func TestCountLargeLine(t *testing.T) {
	// A single very long line (minified JS style) must not blow up.
	src := "var x = 1;" + strings.Repeat("x=1;", 100000) + "\n"
	got := Count([]byte(src), mustLang(t, "JavaScript"))
	if got.Code != 1 || got.Lines != 1 {
		t.Errorf("got %+v, want 1 code line", got)
	}
}
