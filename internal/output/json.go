package output

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"

	"github.com/alyx/aloc/internal/report"
)

// writeJSON emits the fixed Report schema directly; encoding/json's Encoder
// with SetIndent materializes the document twice (compact, then re-indented).
// Bytes are identical to that encoder, including HTML escaping and the
// trailing newline: strings outside the always-verbatim ASCII set defer to
// json.Marshal (jsonString).
func writeJSON(w io.Writer, r *report.Report) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	var scratch [20]byte
	num := func(prefix string, v int) {
		bw.WriteString(prefix)
		bw.Write(strconv.AppendInt(scratch[:0], int64(v), 10))
	}
	str := func(prefix, s string) {
		bw.WriteString(prefix)
		jsonString(bw, s)
	}
	bw.WriteString("{\n")
	num("  \"schema_version\": ", r.SchemaVersion)
	switch {
	case r.Languages == nil:
		bw.WriteString(",\n  \"languages\": null")
	case len(r.Languages) == 0:
		bw.WriteString(",\n  \"languages\": []")
	default:
		bw.WriteString(",\n  \"languages\": [")
		for i := range r.Languages {
			l := &r.Languages[i]
			if i > 0 {
				bw.WriteByte(',')
			}
			str("\n    {\n      \"name\": ", l.Name)
			num(",\n      \"files\": ", l.Files)
			num(",\n      \"lines\": ", l.Lines)
			num(",\n      \"blank\": ", l.Blank)
			num(",\n      \"comment\": ", l.Comment)
			num(",\n      \"code\": ", l.Code)
			if len(l.Detail) > 0 {
				bw.WriteString(",\n      \"files_detail\": [")
				for j := range l.Detail {
					f := &l.Detail[j]
					if j > 0 {
						bw.WriteByte(',')
					}
					str("\n        {\n          \"path\": ", f.Path)
					num(",\n          \"lines\": ", f.Lines)
					num(",\n          \"blank\": ", f.Blank)
					num(",\n          \"comment\": ", f.Comment)
					num(",\n          \"code\": ", f.Code)
					bw.WriteString("\n        }")
				}
				bw.WriteString("\n      ]")
			}
			bw.WriteString("\n    }")
		}
		bw.WriteString("\n  ]")
	}
	bw.WriteString(",\n  \"totals\": {")
	num("\n    \"files\": ", r.Totals.Files)
	num(",\n    \"lines\": ", r.Totals.Lines)
	num(",\n    \"blank\": ", r.Totals.Blank)
	num(",\n    \"comment\": ", r.Totals.Comment)
	num(",\n    \"code\": ", r.Totals.Code)
	bw.WriteString("\n  }")
	switch {
	case r.Excluded == nil:
		bw.WriteString(",\n  \"excluded\": null")
	case len(r.Excluded) == 0:
		bw.WriteString(",\n  \"excluded\": []")
	default:
		bw.WriteString(",\n  \"excluded\": [")
		for i := range r.Excluded {
			e := &r.Excluded[i]
			if i > 0 {
				bw.WriteByte(',')
			}
			str("\n    {\n      \"path\": ", e.Path)
			str(",\n      \"detector\": ", e.Detector)
			bw.WriteString("\n    }")
		}
		bw.WriteString("\n  ]")
	}
	bw.WriteString("\n}\n")
	return bw.Flush()
}

// jsonString quotes s exactly as encoding/json does. ASCII needing no
// escapes is written verbatim; anything else (controls, quotes, HTML-escaped
// <>&, non-ASCII incl. U+2028/U+2029 and invalid UTF-8) defers to
// json.Marshal for identical escape sequences.
func jsonString(bw *bufio.Writer, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x80 || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			b, _ := json.Marshal(s)
			bw.Write(b)
			return
		}
	}
	bw.WriteByte('"')
	bw.WriteString(s)
	bw.WriteByte('"')
}
