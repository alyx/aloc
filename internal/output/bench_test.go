package output

import (
	"fmt"
	"io"
	"testing"

	"github.com/alyx/aloc/internal/counter"
	"github.com/alyx/aloc/internal/report"
)

func synthReport(nLangs, filesPerLang int) *report.Report {
	b := report.NewBuilder(true)
	for l := 0; l < nLangs; l++ {
		lname := fmt.Sprintf("Lang%02d", l)
		for f := 0; f < filesPerLang; f++ {
			path := fmt.Sprintf("proj%02d/pkg%03d/file_%05d.go", l, f%50, f)
			b.AddFile(lname, path, counter.Stats{Files: 1, Lines: 120, Blank: 12, Comment: 20, Code: 88})
		}
	}
	return b.Build()
}

var rep30k = synthReport(7, 4355) // ~30k files, mirrors a large --by-file run

func benchWrite(b *testing.B, f Formatter) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := f(io.Discard, rep30k); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteJSON30k(b *testing.B)       { benchWrite(b, writeJSON) }
func BenchmarkWriteJSONStdlib30k(b *testing.B) { benchWrite(b, writeJSONStdlib) }
func BenchmarkWriteYAML30k(b *testing.B)       { benchWrite(b, writeYAML) }
func BenchmarkWriteYAMLv330k(b *testing.B)     { benchWrite(b, writeYAMLv3) }
