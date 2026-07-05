package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alyx/aloc/internal/counter"
	"github.com/alyx/aloc/internal/report"
)

func sample(byFile bool) *report.Report {
	b := report.NewBuilder(byFile)
	b.AddFile("Go", "b/main.go", counter.Stats{Files: 1, Lines: 12, Blank: 2, Comment: 3, Code: 7})
	b.AddFile("Go", "a/util.go", counter.Stats{Files: 1, Lines: 8, Blank: 1, Comment: 1, Code: 6})
	b.AddFile("Python", "tool.py", counter.Stats{Files: 1, Lines: 30, Blank: 5, Comment: 5, Code: 20})
	b.AddExcluded("node_modules", "node")
	return b.Build()
}

func TestReportOrdering(t *testing.T) {
	r := sample(true)
	if r.Languages[0].Name != "Python" || r.Languages[1].Name != "Go" {
		t.Errorf("languages not sorted by code desc: %v, %v", r.Languages[0].Name, r.Languages[1].Name)
	}
	goDetail := r.Languages[1].Detail
	if goDetail[0].Path != "a/util.go" || goDetail[1].Path != "b/main.go" {
		t.Errorf("files not sorted by path: %+v", goDetail)
	}
	if r.Totals.Files != 3 || r.Totals.Code != 33 || r.Totals.Lines != 50 {
		t.Errorf("totals wrong: %+v", r.Totals)
	}
}

func TestJSONGolden(t *testing.T) {
	f, err := Get("json")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f(&buf, sample(false)); err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema_version": 1,
  "languages": [
    {
      "name": "Python",
      "files": 1,
      "lines": 30,
      "blank": 5,
      "comment": 5,
      "code": 20
    },
    {
      "name": "Go",
      "files": 2,
      "lines": 20,
      "blank": 3,
      "comment": 4,
      "code": 13
    }
  ],
  "totals": {
    "files": 3,
    "lines": 50,
    "blank": 8,
    "comment": 9,
    "code": 33
  },
  "excluded": [
    {
      "path": "node_modules",
      "detector": "node"
    }
  ]
}
`
	if buf.String() != want {
		t.Errorf("json output mismatch:\ngot:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestYAMLGolden(t *testing.T) {
	f, err := Get("yaml")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f(&buf, sample(false)); err != nil {
		t.Fatal(err)
	}
	want := `schema_version: 1
languages:
  - name: Python
    files: 1
    lines: 30
    blank: 5
    comment: 5
    code: 20
  - name: Go
    files: 2
    lines: 20
    blank: 3
    comment: 4
    code: 13
totals:
  files: 3
  lines: 50
  blank: 8
  comment: 9
  code: 33
excluded:
  - path: node_modules
    detector: node
`
	if buf.String() != want {
		t.Errorf("yaml output mismatch:\ngot:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestTable(t *testing.T) {
	f, err := Get("table")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f(&buf, sample(false)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Language", "Python", "Go", "Total"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Header rule, header, rule, 2 languages, rule, total, rule.
	if len(lines) != 8 {
		t.Errorf("table has %d lines, want 8:\n%s", len(lines), out)
	}
	// Total row: 3 files, 8 blank, 9 comment, 33 code.
	total := lines[6]
	for _, cell := range []string{"Total", "3", "8", "9", "33"} {
		if !strings.Contains(total, cell) {
			t.Errorf("total row missing %q: %q", cell, total)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Get("csv"); err == nil || !strings.Contains(err.Error(), "available:") {
		t.Errorf("want unknown-format error listing formats, got %v", err)
	}
}

func TestEmptyReport(t *testing.T) {
	r := report.NewBuilder(false).Build()
	for _, name := range []string{"table", "json", "yaml"} {
		f, _ := Get(name)
		var buf bytes.Buffer
		if err := f(&buf, r); err != nil {
			t.Errorf("%s on empty report: %v", name, err)
		}
	}
	if r.Languages == nil || r.Excluded == nil {
		t.Error("empty report should serialize as [] not null")
	}
}
