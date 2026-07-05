// Package output renders a report. New formats are one entry in the
// formatters map; nothing outside this package changes to add one.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alyx/aloc/internal/report"
)

// Formatter renders a report to w.
type Formatter func(w io.Writer, r *report.Report) error

var formatters = map[string]Formatter{
	"table": writeTable,
	"json":  writeJSON,
	"yaml":  writeYAML,
}

// Get returns the formatter for name, or an error listing valid names.
func Get(name string) (Formatter, error) {
	if f, ok := formatters[name]; ok {
		return f, nil
	}
	names := make([]string, 0, len(formatters))
	for n := range formatters {
		names = append(names, n)
	}
	slices.Sort(names)
	return nil, fmt.Errorf("unknown format %q (available: %s)", name, strings.Join(names, ", "))
}

func writeJSON(w io.Writer, r *report.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func writeYAML(w io.Writer, r *report.Report) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return err
	}
	return enc.Close()
}
