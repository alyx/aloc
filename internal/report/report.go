// Package report aggregates per-file counts into the final, deterministically
// ordered result.
package report

import (
	"cmp"
	"slices"

	"github.com/alyx/aloc/internal/counter"
)

// FileStats is one counted file.
type FileStats struct {
	Path    string `json:"path" yaml:"path"`
	Lines   int    `json:"lines" yaml:"lines"`
	Blank   int    `json:"blank" yaml:"blank"`
	Comment int    `json:"comment" yaml:"comment"`
	Code    int    `json:"code" yaml:"code"`
}

// LanguageStats aggregates one language.
type LanguageStats struct {
	Name          string `json:"name" yaml:"name"`
	counter.Stats `yaml:",inline"`
	Detail        []FileStats `json:"files_detail,omitempty" yaml:"files_detail,omitempty"`
}

// Excluded records one directory removed by smart detection.
type Excluded struct {
	Path     string `json:"path" yaml:"path"`
	Detector string `json:"detector" yaml:"detector"`
}

// Report is the complete result of a run. It is the schema serialized by the
// json and yaml formatters.
type Report struct {
	SchemaVersion int             `json:"schema_version" yaml:"schema_version"`
	Languages     []LanguageStats `json:"languages" yaml:"languages"`
	Totals        counter.Stats   `json:"totals" yaml:"totals"`
	Excluded      []Excluded      `json:"excluded" yaml:"excluded"`
}

// Builder accumulates results; it is not goroutine-safe — the walker funnels
// results through a single collector.
type Builder struct {
	byLang   map[string]*LanguageStats
	excluded []Excluded
	byFile   bool
}

// NewBuilder returns a Builder; when byFile is set, per-file detail is kept.
func NewBuilder(byFile bool) *Builder {
	return &Builder{byLang: map[string]*LanguageStats{}, byFile: byFile}
}

// AddFile records one counted file.
func (b *Builder) AddFile(language, path string, s counter.Stats) {
	ls, ok := b.byLang[language]
	if !ok {
		ls = &LanguageStats{Name: language}
		b.byLang[language] = ls
	}
	ls.Add(s)
	if b.byFile {
		ls.Detail = append(ls.Detail, FileStats{Path: path, Lines: s.Lines, Blank: s.Blank, Comment: s.Comment, Code: s.Code})
	}
}

// AddExcluded records a smart exclusion.
func (b *Builder) AddExcluded(path, detector string) {
	b.excluded = append(b.excluded, Excluded{Path: path, Detector: detector})
}

// Build produces the final report: languages by code desc then name asc,
// files by path, exclusions by path.
func (b *Builder) Build() *Report {
	r := &Report{
		SchemaVersion: 1,
		Languages:     make([]LanguageStats, 0, len(b.byLang)),
		Excluded:      b.excluded,
	}
	if r.Excluded == nil {
		r.Excluded = []Excluded{}
	}
	for _, ls := range b.byLang {
		slices.SortFunc(ls.Detail, func(a, b FileStats) int { return cmp.Compare(a.Path, b.Path) })
		r.Languages = append(r.Languages, *ls)
		r.Totals.Add(ls.Stats)
	}
	slices.SortFunc(r.Languages, func(a, b LanguageStats) int {
		return cmp.Or(cmp.Compare(b.Code, a.Code), cmp.Compare(a.Name, b.Name))
	})
	slices.SortFunc(r.Excluded, func(a, b Excluded) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Detector, b.Detector))
	})
	return r
}
