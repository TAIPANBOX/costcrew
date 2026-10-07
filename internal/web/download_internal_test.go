package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The boundaries of the CSV rule, cell by cell: what is neutralised in a text
// column, what is left alone in a number column, and that the rule never
// touches a value that does not start a formula.
func TestCSVCellNeutralisesByColumnKind(t *testing.T) {
	cases := []struct {
		kind     csvKind
		in, want string
	}{
		// OWASP's six openers, in a text column.
		{csvText, "=1+1", "'=1+1"},
		{csvText, "+1", "'+1"},
		{csvText, "-1", "'-1"},
		{csvText, "@SUM(A1)", "'@SUM(A1)"},
		{csvText, "\tx", "'\tx"},
		{csvText, "\rx", "'\rx"},
		// Text that merely contains one later is not a formula.
		{csvText, "a=b", "a=b"},
		{csvText, "ml-platform", "ml-platform"},
		{csvText, "", ""},
		{csvText, " =1", " =1"},
		// A number column keeps a plain number, the negative ones included.
		{csvNumber, "-1234.56", "-1234.56"},
		{csvNumber, "-0.01", "-0.01"},
		{csvNumber, "0", "0"},
		{csvNumber, "-7", "-7"},
		{csvNumber, "", ""},
		// ...and anything else in it gets the text rule.
		{csvNumber, "-1+1", "'-1+1"},
		{csvNumber, "=1", "'=1"},
		{csvNumber, "-", "'-"},
		{csvNumber, "-1.", "'-1."},
		{csvNumber, "+5", "'+5"},
		{csvNumber, "@1", "'@1"},
	}
	for _, c := range cases {
		if got := csvCell(c.kind, c.in); got != c.want {
			t.Errorf("csvCell(%v, %q) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}

func TestMDTextKeepsAValueReadableAndInert(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Amazon EC2", "Amazon EC2"},
		{"a|b", `a\|b`},
		{"line\nbreak\r\nend", "line break  end"},
		{"sep\u2028para\u2029", "sep para "},
		{"<script>alert(1)</script>", "&lt;script&gt;alert\\(1\\)&lt;/script&gt;"},
		{"R&D", "R&amp;D"},
		{"[x](javascript:alert(1))", `\[x\]\(javascript:alert\(1\)\)`},
		{"![img](http://x)", `\!\[img\]\(http://x\)`},
		{"*bold* _it_ `code` ~s~ #h", `\*bold\* \_it\_ \` + "`" + `code\` + "`" + ` \~s\~ \#h`},
		{`back\slash`, `back\\slash`},
		{"zero\u200bwidth\u202eflip", "zerowidthflip"},
		{"\x00nul\x1b[31m", "nul\\[31m"},
		{"Київ-東京", "Київ-東京"},
		{"", ""},
	}
	for _, c := range cases {
		if got := mdText(c.in); got != c.want {
			t.Errorf("mdText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// One place names a download. A second, concatenated Content-Disposition
// anywhere in this package is the defect this file exists to stop, so the
// literal header name may appear in exactly one non-test file, the helper's.
func TestEveryDownloadIsNamedThroughOneHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var where []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(b), `"Content-Disposition"`); n > 0 {
			where = append(where, f+" x"+string(rune('0'+n)))
		}
	}
	if len(where) != 1 || where[0] != "download.go x1" {
		t.Errorf("the Content-Disposition header is set in %v; want only download.go, "+
			"once, inside setDisposition", where)
	}
}
