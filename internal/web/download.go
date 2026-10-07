package web

// What a download carries out of this console. Invariant 78.
//
// A download is opened where this console cannot help: a Markdown packet in a
// viewer that renders HTML, a CSV in a spreadsheet that evaluates formulas.
// Several values in them arrive from a file somebody else produced (a FOCUS
// export's ServiceName, x_unit, x_agent_id), so each format gets the one
// escaping it needs, here, and every download goes through these three.

import (
	"encoding/csv"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// setDisposition names the file a browser saves. mime.FormatMediaType quotes
// a filename that needs it, so a quote or a semicolon in a period read from
// the store, or in a desk read from the URL, stays inside the filename rather
// than adding a parameter of its own (a filename* that renames the download).
// It returns "" only for a malformed type, which these constant types are not;
// the bare disposition is the fallback so a download is never left nameless
// AND unguarded.
func setDisposition(w http.ResponseWriter, disposition, filename string) {
	v := mime.FormatMediaType(disposition, map[string]string{"filename": filename})
	if v == "" {
		v = disposition
	}
	w.Header().Set("Content-Disposition", v)
}

// mdText makes a value safe to put in Markdown prose or a table cell.
//
// A line break ends a table row and can start a heading, so CR, LF and the
// Unicode line and paragraph separators become a space. A pipe is a column
// break. <, > and & are written as entities, because a Markdown viewer passes
// raw HTML through and a <script> in a service name would run in it. The
// Markdown punctuation that makes a link, an image, emphasis or code is
// backslash-escaped, so [x](javascript:...) is text and not a link. Other
// control and format characters (zero-width, text-direction overrides) are
// dropped: they render as nothing or reorder what a reader sees. Everything
// else, letters in any script included, is kept as it was.
func mdText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t' || r == ' ' || r == ' ' || r == '\u0085':
			b.WriteByte(' ')
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		case strings.ContainsRune("\\`*_[]()|#~!", r):
			b.WriteByte('\\')
			b.WriteRune(r)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// csvKind is what a CSV column holds, and it decides how a cell that starts
// like a formula is treated.
type csvKind int

const (
	csvText   csvKind = iota // names, states, reasons: anything a person or a file typed
	csvNumber                // an amount or a percentage this console formatted
)

type csvCol struct {
	name string
	kind csvKind
}

func textCol(name string) csvCol   { return csvCol{name, csvText} }
func numberCol(name string) csvCol { return csvCol{name, csvNumber} }

// plainNumber is the shape every number this console writes into a CSV has:
// money.Cents.String, strconv.FormatFloat with a fixed precision, an integer.
var plainNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// csvCell neutralises a cell a spreadsheet would evaluate (OWASP, CSV
// injection): one that starts with = + - @, a tab or a carriage return gets a
// leading apostrophe, which every spreadsheet reads as "this is text" and does
// not display. The value is otherwise kept whole.
//
// The kind decides the one exception. In a number column a plain number is
// left alone, negative ones included: -1234.56 is an amount, and an
// apostrophe would turn every negative variance into text a SUM skips. In a
// text column there is no exception, because a spreadsheet evaluates -2+3 as
// readily as =2+3, and a reason or a name that happens to look numeric is
// still text. A number column that holds something other than a plain number
// gets the text rule.
func csvCell(kind csvKind, v string) string {
	if v == "" || !strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return v
	}
	if kind == csvNumber && plainNumber.MatchString(v) {
		return v
	}
	return "'" + v
}

// writeCSV sends a table as a download.
//
// CRLF because that is what every spreadsheet on every platform opens without
// asking a question, which is the only audience a CSV export has.
func writeCSV(w http.ResponseWriter, filename string, cols []csvCol, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	setDisposition(w, "attachment", filename)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.name
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	_ = cw.Write(header)
	for _, row := range rows {
		out := make([]string, len(row))
		for i, v := range row {
			kind := csvText
			if i < len(cols) {
				kind = cols[i].kind
			}
			out[i] = csvCell(kind, v)
		}
		_ = cw.Write(out)
	}
	cw.Flush()
}
