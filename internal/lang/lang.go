// Package lang holds language definitions and detects the language of a file
// from its extension, filename, or shebang line.
package lang

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Language describes how to recognize and count one language.
type Language struct {
	Name          string
	Extensions    []string    // without leading dot, lowercase
	Filenames     []string    // exact basenames, e.g. "Makefile"
	Shebangs      []string    // interpreter names, e.g. "python"
	LineComments  []string    // e.g. "//", "#"
	BlockComments [][2]string // e.g. {"/*", "*/"}
	Quotes        [][2]string // single-line string delimiters
	MultiQuotes   [][2]string // multi-line string delimiters, backslash escapes apply
	RawQuotes     [][2]string // multi-line string delimiters, no escapes (Go backticks)
	Nested        bool        // block comments nest (Rust, Haskell, ...)
}

// Registry resolves files to languages.
type Registry struct {
	languages map[string]*Language // by name
	byExt     map[uint64]*Language // packed ASCII extensions up to 8 bytes
	byLongExt map[string]*Language // uncommon long or non-ASCII extensions
	byName    map[string]*Language // by basename
	byShebang map[string]*Language
}

// NewRegistry builds a registry from the built-in definitions.
func NewRegistry() *Registry {
	r := &Registry{
		languages: map[string]*Language{},
		byExt:     map[uint64]*Language{},
		byLongExt: map[string]*Language{},
		byName:    map[string]*Language{},
		byShebang: map[string]*Language{},
	}
	for i := range builtin {
		r.Add(&builtin[i])
	}
	for i := range builtinExpanded {
		r.Add(&builtinExpanded[i])
	}
	return r
}

// Add registers a language, replacing any previous definition with the same
// name and taking over any extensions/filenames/shebangs it claims.
func (r *Registry) Add(l *Language) {
	if old, ok := r.languages[l.Name]; ok {
		r.remove(old)
	}
	r.languages[l.Name] = l
	for _, e := range l.Extensions {
		if key, ok := extensionKey(e); ok {
			r.byExt[key] = l
		} else {
			r.byLongExt[strings.ToLower(e)] = l
		}
	}
	for _, n := range l.Filenames {
		r.byName[n] = l
	}
	for _, s := range l.Shebangs {
		r.byShebang[s] = l
	}
}

func (r *Registry) remove(l *Language) {
	for _, e := range l.Extensions {
		if key, ok := extensionKey(e); ok {
			if r.byExt[key] == l {
				delete(r.byExt, key)
			}
		} else {
			ext := strings.ToLower(e)
			if r.byLongExt[ext] == l {
				delete(r.byLongExt, ext)
			}
		}
	}
	for _, n := range l.Filenames {
		if r.byName[n] == l {
			delete(r.byName, n)
		}
	}
	for _, s := range l.Shebangs {
		if r.byShebang[s] == l {
			delete(r.byShebang, s)
		}
	}
	delete(r.languages, l.Name)
}

// Get returns a language by name, case-insensitively.
func (r *Registry) Get(name string) *Language {
	if l, ok := r.languages[name]; ok {
		return l
	}
	for n, l := range r.languages {
		if strings.EqualFold(n, name) {
			return l
		}
	}
	return nil
}

// Names returns all language names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.languages))
	for n := range r.languages {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ByPath detects a language from a file path alone (filename, then
// extension). Returns nil if unknown.
func (r *Registry) ByPath(path string) *Language {
	base := filepath.Base(path)
	if l, ok := r.byName[base]; ok {
		return l
	}
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	if ext == "" {
		return nil
	}
	if key, ok := extensionKey(ext); ok {
		return r.byExt[key]
	}
	return r.byLongExt[strings.ToLower(ext)]
}

// extensionKey packs the overwhelmingly common short ASCII extension into a
// scalar map key while folding case. This avoids allocating a lowercase string
// and keeps the expanded language registry cache-friendly on large walks.
func extensionKey(ext string) (uint64, bool) {
	if len(ext) == 0 || len(ext) > 8 {
		return 0, false
	}
	var key uint64
	for i := 0; i < len(ext); i++ {
		c := ext[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		} else if c >= 0x80 {
			return 0, false
		}
		key |= uint64(c) << (8 * i)
	}
	return key, true
}

// ByShebang detects a language from the first line of content. Returns nil
// if there is no recognizable shebang.
func (r *Registry) ByShebang(content []byte) *Language {
	if !bytes.HasPrefix(content, []byte("#!")) {
		return nil
	}
	line := content[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return nil
	}
	interp := filepath.Base(fields[0])
	if interp == "env" {
		// #!/usr/bin/env [-S] [VAR=val ...] interpreter
		interp = ""
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
				continue
			}
			interp = filepath.Base(f)
			break
		}
		if interp == "" {
			return nil
		}
	}
	if l, ok := r.byShebang[interp]; ok {
		return l
	}
	// python3.11 -> python
	trimmed := strings.TrimRight(interp, "0123456789.")
	if l, ok := r.byShebang[trimmed]; ok {
		return l
	}
	return nil
}

// Detect resolves a language for path; content (may be a prefix of the file)
// is consulted for a shebang only when the path alone is inconclusive.
func (r *Registry) Detect(path string, content []byte) *Language {
	if l := r.ByPath(path); l != nil {
		return l
	}
	return r.ByShebang(content)
}

// Validate reports an error for an unusable definition.
func (l *Language) Validate() error {
	if l.Name == "" {
		return fmt.Errorf("language with empty name")
	}
	if len(l.Extensions) == 0 && len(l.Filenames) == 0 && len(l.Shebangs) == 0 {
		return fmt.Errorf("language %q has no extensions, filenames, or shebangs", l.Name)
	}
	for _, b := range l.BlockComments {
		if b[0] == "" || b[1] == "" {
			return fmt.Errorf("language %q has a block comment with an empty delimiter", l.Name)
		}
	}
	return nil
}
