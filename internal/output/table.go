package output

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/alyx/aloc/internal/counter"
	"github.com/alyx/aloc/internal/report"
)

// writeTable renders the classic cloc-style summary table.
func writeTable(w io.Writer, r *report.Report) error {
	// Column widths, widened as needed by long names or large counts.
	nameW, colW := 22, 12
	widen := func(name string, s counter.Stats) {
		nameW = max(nameW, len(name)+2)
		for _, v := range []int{s.Files, s.Blank, s.Comment, s.Code} {
			colW = max(colW, len(strconv.Itoa(v))+2)
		}
	}
	for _, ls := range r.Languages {
		widen(ls.Name, ls.Stats)
	}
	widen("Total", r.Totals)

	var b strings.Builder
	rule := strings.Repeat("-", nameW+4*colW) + "\n"
	row := func(name string, files, blank, comment, code any) {
		fmt.Fprintf(&b, "%-*s%*v%*v%*v%*v\n", nameW, name, colW, files, colW, blank, colW, comment, colW, code)
	}
	stats := func(name string, s counter.Stats) { row(name, s.Files, s.Blank, s.Comment, s.Code) }

	b.WriteString(rule)
	row("Language", "Files", "Blank", "Comment", "Code")
	b.WriteString(rule)
	for _, ls := range r.Languages {
		stats(ls.Name, ls.Stats)
		for _, f := range ls.Detail {
			fmt.Fprintf(&b, "  %-*s%*d%*d%*d\n", nameW+colW-2, f.Path, colW, f.Blank, colW, f.Comment, colW, f.Code)
		}
	}
	b.WriteString(rule)
	stats("Total", r.Totals)
	b.WriteString(rule)

	_, err := io.WriteString(w, b.String())
	return err
}
