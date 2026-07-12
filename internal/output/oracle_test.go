package output

import (
	"encoding/json"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/alyx/aloc/internal/report"
)

// The reflection-based encoders the hand-rolled emitters replaced, kept as
// oracles: the differential tests assert byte-for-byte equality against them.

func writeJSONStdlib(w io.Writer, r *report.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func writeYAMLv3(w io.Writer, r *report.Report) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return err
	}
	return enc.Close()
}
