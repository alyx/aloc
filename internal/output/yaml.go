package output

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alyx/aloc/internal/report"
)

// writeYAML emits the fixed Report schema directly instead of driving
// yaml.v3's reflection encoder, which dominates --by-file output time on
// large trees. Bytes are identical to yaml.v3 with SetIndent(2): scalars not
// provably plain-safe are rendered by yaml.v3 inside a minimal document with
// the same nesting and spliced in (yamlScalar).
func writeYAML(w io.Writer, r *report.Report) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	var scratch [20]byte
	num := func(prefix string, v int) {
		bw.WriteString(prefix)
		bw.Write(strconv.AppendInt(scratch[:0], int64(v), 10))
		bw.WriteByte('\n')
	}
	str := func(prefix, v string, ctx yamlCtx) error {
		bw.WriteString(prefix)
		if yamlPlainOK(v) {
			bw.WriteString(v)
			bw.WriteByte('\n')
			return nil
		}
		rendered, err := yamlScalar(ctx, v)
		if err != nil {
			return err
		}
		bw.WriteString(rendered)
		return nil
	}
	num("schema_version: ", r.SchemaVersion)
	// yaml.v3 renders nil and empty slices alike as [].
	if len(r.Languages) == 0 {
		bw.WriteString("languages: []\n")
	} else {
		bw.WriteString("languages:\n")
		for i := range r.Languages {
			l := &r.Languages[i]
			if err := str("  - name: ", l.Name, yamlCtxName); err != nil {
				return err
			}
			num("    files: ", l.Files)
			num("    lines: ", l.Lines)
			num("    blank: ", l.Blank)
			num("    comment: ", l.Comment)
			num("    code: ", l.Code)
			if len(l.Detail) > 0 {
				bw.WriteString("    files_detail:\n")
				for j := range l.Detail {
					f := &l.Detail[j]
					if err := str("      - path: ", f.Path, yamlCtxDetailPath); err != nil {
						return err
					}
					num("        lines: ", f.Lines)
					num("        blank: ", f.Blank)
					num("        comment: ", f.Comment)
					num("        code: ", f.Code)
				}
			}
		}
	}
	bw.WriteString("totals:\n")
	num("  files: ", r.Totals.Files)
	num("  lines: ", r.Totals.Lines)
	num("  blank: ", r.Totals.Blank)
	num("  comment: ", r.Totals.Comment)
	num("  code: ", r.Totals.Code)
	if len(r.Excluded) == 0 {
		bw.WriteString("excluded: []\n")
	} else {
		bw.WriteString("excluded:\n")
		for i := range r.Excluded {
			e := &r.Excluded[i]
			if err := str("  - path: ", e.Path, yamlCtxExcludedPath); err != nil {
				return err
			}
			if err := str("    detector: ", e.Detector, yamlCtxDetector); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

// yamlPlainOK reports whether yaml.v3 provably emits s verbatim as a
// single-line plain scalar in this schema's block contexts. Conservative:
// false negatives only reroute through yamlScalar.
//   - The charset excludes everything that can force quoting (':', tabs,
//     breaks, non-ASCII, flow/block indicators) and spaces, which admit line
//     folding past column 80.
//   - The first byte must not start anything resolve() could type as
//     non-string (numbers, timestamps, '.inf'/'.nan') and must not be an
//     indicator; digit- or dot-led strings are allowed only with a '/',
//     which no int/float/timestamp form survives.
//   - "..." would read as a document-end marker.
//   - Bool/null words are rejected case-insensitively, a superset of
//     yaml.v3's exact-case bool, old-bool, and null sets.
func yamlPlainOK(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == '/' || c == '+' || c == '#':
		default:
			return false
		}
	}
	switch c := s[0]; {
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '/':
	case strings.IndexByte(s, '/') >= 0 && !strings.HasPrefix(s, "..."):
		// c is a digit, '.', '-', '+', or '#'; only digit- and dot-led
		// strings are worth the slash test, but all are safe with one.
		if c == '-' || c == '#' {
			return false // leading "- " / comment indicators: not worth modeling
		}
	default:
		return false
	}
	switch len(s) {
	case 1:
		return !strings.EqualFold(s, "y") && !strings.EqualFold(s, "n")
	case 2:
		return !strings.EqualFold(s, "no") && !strings.EqualFold(s, "on")
	case 3:
		return !strings.EqualFold(s, "yes") && !strings.EqualFold(s, "off")
	case 4:
		return !strings.EqualFold(s, "true") && !strings.EqualFold(s, "null")
	case 5:
		return !strings.EqualFold(s, "false")
	}
	return true
}

// yamlCtx identifies where in the Report schema a fallback scalar sits; the
// wrapper documents below reproduce that nesting exactly.
type yamlCtx int

const (
	yamlCtxName yamlCtx = iota
	yamlCtxDetailPath
	yamlCtxExcludedPath
	yamlCtxDetector
)

type yamlKeyName struct {
	Name string `yaml:"name"`
}

type yamlKeyPath struct {
	Path string `yaml:"path"`
}

type yamlKeyPathDetector struct {
	Path     string `yaml:"path"`
	Detector string `yaml:"detector"`
}

type yamlNameDoc struct {
	Languages []yamlKeyName `yaml:"languages"`
}

type yamlDetailLang struct {
	Detail []yamlKeyPath `yaml:"files_detail"`
}

type yamlDetailDoc struct {
	Languages []yamlDetailLang `yaml:"languages"`
}

type yamlExcludedPathDoc struct {
	Excluded []yamlKeyPath `yaml:"excluded"`
}

type yamlDetectorDoc struct {
	Excluded []yamlKeyPathDetector `yaml:"excluded"`
}

// yamlScalar renders one scalar exactly as yaml.v3 would inside the full
// report: quoting, escaping, folding, and block-scalar layout all depend
// only on the scalar's start column and indent, so encoding it in a wrapper
// document with identical nesting and stripping the fixed prefix yields the
// exact bytes. The result includes the trailing newline and any
// continuation lines.
func yamlScalar(ctx yamlCtx, v string) (string, error) {
	var doc any
	var prefix string
	switch ctx {
	case yamlCtxName:
		doc = yamlNameDoc{Languages: []yamlKeyName{{Name: v}}}
		prefix = "languages:\n  - name: "
	case yamlCtxDetailPath:
		doc = yamlDetailDoc{Languages: []yamlDetailLang{{Detail: []yamlKeyPath{{Path: v}}}}}
		prefix = "languages:\n  - files_detail:\n      - path: "
	case yamlCtxExcludedPath:
		doc = yamlExcludedPathDoc{Excluded: []yamlKeyPath{{Path: v}}}
		prefix = "excluded:\n  - path: "
	default:
		doc = yamlDetectorDoc{Excluded: []yamlKeyPathDetector{{Path: "x", Detector: v}}}
		prefix = "excluded:\n  - path: x\n    detector: "
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	out := buf.String()
	if !strings.HasPrefix(out, prefix) {
		return "", fmt.Errorf("yaml emitter: unexpected rendering for %q", v)
	}
	return out[len(prefix):], nil
}
