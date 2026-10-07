package web_test

import (
	"encoding/csv"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The downloads are files a person opens outside this console: a Markdown
// packet in a viewer that renders HTML, a CSV in a spreadsheet. Several of
// their values arrive from a file somebody else produced (a FOCUS export's
// ServiceName, x_unit, x_agent_id), so they are tested here with the values
// such a file can carry. Invariant 78.

// hostileService is one value carrying every way a Markdown table or a
// viewer that renders HTML can be broken by text: a pipe (a new column), a
// newline (a new row, then a heading), a script tag, and a link whose target
// runs script.
const hostileService = "evil | pipe\n# heading <script>alert(1)</script>"
const hostileCause = "[click](javascript:alert(1))"

// plantAnomaly writes one open anomaly with the given text values and an
// excess large enough to sort first, the way the reader's values would reach
// the table after a detection run.
func plantAnomaly(t *testing.T, h *harness, id, service, causedBy, team, reason string, excessCents int64) {
	t.Helper()
	_, err := h.st.DB().Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		 handled_by, state, reason, detected_at)
		VALUES (?, 'ai', ?, ?, '2026-09-01', 'up', 1, 1, ?, 9.9, 'r', 'v1', '',
		        ?, 'agent', '', 'open', ?, '2026-09-02T00:00:00Z')`,
		id, team, service, excessCents, causedBy, reason)
	if err != nil {
		t.Fatal(err)
	}
}

// countUnescapedPipes counts the pipes a Markdown table reads as column breaks.
func countUnescapedPipes(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '|' && (i == 0 || line[i-1] != '\\') {
			n++
		}
	}
	return n
}

func TestTheExecPacketKeepsAnImportedValueAsText(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	plantAnomaly(t, h, "A-hostile", hostileService, hostileCause, "", "", 999_999_999)

	code, body, _ := h.get(t, "/export/exec-packet.md")
	if code != http.StatusOK {
		t.Fatalf("GET /export/exec-packet.md: %d", code)
	}
	if strings.Contains(body, "<script") {
		t.Errorf("the packet carries a raw <script> tag from an imported service name, "+
			"which a viewer that renders HTML runs:\n%s", excerpt(body, "evil"))
	}
	if strings.Contains(body, "](javascript:") {
		t.Errorf("the packet carries a live Markdown link from an imported value:\n%s",
			excerpt(body, "click"))
	}
	var row string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "evil") {
			if row != "" {
				t.Fatalf("the hostile value spans more than one line, so its newline "+
					"started a new row:\n%s", excerpt(body, "evil"))
			}
			row = line
		}
	}
	if row == "" {
		t.Fatalf("the planted anomaly is not in the packet at all:\n%s", body)
	}
	if !strings.HasPrefix(row, "| ") {
		t.Fatalf("the hostile value is not inside a table row: %q", row)
	}
	// | Money | Where | Day | Whose spend | is five column breaks.
	if n := countUnescapedPipes(row); n != 5 {
		t.Errorf("the row has %d column breaks, want 5; a pipe in the value made a "+
			"column of its own: %q", n, row)
	}
	for _, want := range []string{"evil", "pipe", "heading", "&lt;script&gt;", "click"} {
		if !strings.Contains(row, want) {
			t.Errorf("the row lost %q: escaping must keep the value readable, "+
				"not drop it: %q", want, row)
		}
	}
}

func excerpt(body, near string) string {
	i := strings.Index(body, near)
	if i < 0 {
		return body
	}
	lo, hi := i-120, i+200
	if lo < 0 {
		lo = 0
	}
	if hi > len(body) {
		hi = len(body)
	}
	return body[lo:hi]
}

// A download's file name goes into a header, and a value that ends up in it
// (a period read from the store, a desk read from the URL) must stay the
// filename and nothing else. Concatenated, a quote and a semicolon in it add
// parameters of their own; a browser then saves under a name the link chose.
func TestADownloadsFileNameIsOneParameterWhateverItCarries(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")

	// The period is a month the store holds, so a charge on a day whose first
	// seven bytes carry header syntax makes one.
	const period = `9;x="ab` // seven bytes, the width of a month
	if _, err := h.st.DB().Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents) VALUES
		('ai', ?, 'svc', 'unit-a', 'Usage', 100)`, period+"-01"); err != nil {
		t.Fatal(err)
	}
	const source = `aws";filename*=UTF-8''evil.exe`

	cases := []struct{ path, want string }{
		{"/export/exec-packet.md?period=" + url.QueryEscape(period), "exec-packet-" + period + ".md"},
		{"/export/results.md?period=" + url.QueryEscape(period), "results-" + period + ".md"},
		{"/export/allocation.csv?period=" + url.QueryEscape(period), "allocation-" + period + ".csv"},
		{"/export/budget.csv?source=" + url.QueryEscape(source), "budget-vs-actual-" + source + ".csv"},
	}
	for _, c := range cases {
		resp, err := h.c.Get(h.srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		cd := resp.Header.Get("Content-Disposition")
		kind, params, err := mime.ParseMediaType(cd)
		if err != nil {
			t.Errorf("GET %s: Content-Disposition %q does not parse: %v", c.path, cd, err)
			continue
		}
		if kind != "attachment" {
			t.Errorf("GET %s: disposition %q, want attachment", c.path, kind)
		}
		if params["filename"] != c.want {
			t.Errorf("GET %s: filename %q, want %q (header %q)", c.path, params["filename"], c.want, cd)
		}
		if len(params) != 1 {
			t.Errorf("GET %s: the header carries %d parameters, want only the filename: %q",
				c.path, len(params), cd)
		}
	}
}

// csvExport fetches one CSV download and returns its header and rows.
func csvExport(t *testing.T, h *harness, path string) ([]string, [][]string) {
	t.Helper()
	code, body, _ := h.get(t, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, code)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("GET %s does not parse as CSV: %v", path, err)
	}
	if len(recs) == 0 {
		t.Fatalf("GET %s: empty", path)
	}
	return recs[0], recs[1:]
}

func column(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("no column %q in %v", name, header)
	return -1
}

// formulaStart is what a spreadsheet reads as the start of a formula (OWASP's
// list for CSV injection).
func formulaStart(s string) bool {
	return s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0]))
}

// A cell that begins like a formula is run by the spreadsheet that opens the
// file, so every text cell that does is neutralised; a number, negative ones
// included, is a number and is written as one.
func TestACSVExportNeutralisesAFormulaAndKeepsANegativeNumber(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	plantAnomaly(t, h, "A-formula", `=HYPERLINK("http://x.test","y")`, "@SUM(1+1)",
		"+cmd", "-2+3", -123456)
	plantAnomaly(t, h, "A-tab", "\tlooks-blank", "\rcarriage", "plain-team", "ordinary", 5)

	header, rows := csvExport(t, h, "/export/results.csv")
	excess := column(t, header, "excess_usd")
	textCols := []string{"source", "team", "service", "caused_by", "handled_by", "reason"}
	found := 0
	for _, r := range rows {
		if r[0] != "A-formula" && r[0] != "A-tab" {
			continue
		}
		found++
		for _, name := range textCols {
			v := r[column(t, header, name)]
			if formulaStart(v) {
				t.Errorf("%s row %s: text cell %s = %q starts a formula", "results.csv", r[0], name, v)
			}
		}
		if r[0] == "A-formula" {
			if got := r[excess]; got != "-1234.56" {
				t.Errorf("excess_usd = %q, want -1234.56: a negative amount in a number "+
					"column is a number, and neutralising it turns it into text", got)
			}
			if got := r[column(t, header, "service")]; got != `'=HYPERLINK("http://x.test","y")` {
				t.Errorf("service = %q, want the value kept whole behind a leading quote", got)
			}
			if got := r[column(t, header, "reason")]; got != "'-2+3" {
				t.Errorf("reason = %q: in a TEXT column even something number-like starting "+
					"with a minus is neutralised, because a spreadsheet evaluates -2+3", got)
			}
		}
	}
	if found != 2 {
		t.Fatalf("found %d of the 2 planted rows in results.csv", found)
	}

	// The same through a second export whose values come from charges.team,
	// which the FOCUS reader fills from x_unit.
	p := latestPeriod(t, h)
	if _, err := h.st.DB().Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, provenance) VALUES
		('ai', ?, 'svc', '=1+1', 'Usage', 100, 'tokenfuse-focus')`, p+"-02"); err != nil {
		t.Fatal(err)
	}
	header, rows = csvExport(t, h, "/export/allocation.csv?period="+p)
	team := column(t, header, "team")
	saw := false
	for _, r := range rows {
		if strings.Contains(r[team], "=1+1") {
			saw = true
			if r[team] != "'=1+1" {
				t.Errorf("allocation.csv team = %q, want '=1+1", r[team])
			}
		}
		for i, v := range r {
			if formulaStart(v) && !numberCell.MatchString(v) {
				t.Errorf("allocation.csv %s = %q starts a formula", header[i], v)
			}
		}
	}
	if !saw {
		t.Fatalf("the planted team is not in allocation.csv for %s", p)
	}
}

var numberCell = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

func latestPeriod(t *testing.T, h *harness) string {
	t.Helper()
	var p string
	if err := h.st.DB().QueryRow(`SELECT MAX(substr(day,1,7)) FROM charges`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}
