package connectors

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// x_unit is a name somebody else's file chose, and it travels on: into
// charges.team, onto /chargeback and /allocation, into a CSV a spreadsheet
// opens and a statement a team reads. So it is held to the same rule a unit
// name is held to everywhere else a person can see it, and a row that breaks
// it is refused by name rather than carried. Invariant 78.

const xUnitCol = 24 // x_unit's position in focusHeader

// focusFileWith writes one file whose rows are focusRowFields with x_unit set
// to each given value, through encoding/csv so a newline or a quote in a value
// is written the way a real export would quote it.
func focusFileWith(t *testing.T, units ...string) string {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(strings.Split(focusHeader, ","))
	for i, u := range units {
		f := focusRowFields()
		f[xUnitCol] = u
		f[15] = fmt.Sprintf("run-%d", i) // x_run_id, so rows differ
		_ = w.Write(f)
	}
	w.Flush()
	dir := t.TempDir()
	writeFocusFile(t, dir, "units.csv", buf.String())
	return dir
}

func teamsInCharges(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT DISTINCT COALESCE(team, '<null>') FROM charges ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestXUnitIsRefusedUnlessItIsAPlainBoundedName(t *testing.T) {
	refused := []struct{ name, unit, says string }{
		{"one byte over the bound", strings.Repeat("u", xUnitMaxBytes+1), "129 bytes"},
		{"a megabyte", strings.Repeat("u", 1<<20), "bytes"},
		{"a control character", "acme\x07corp", "control"},
		{"a newline inside", "acme\ncorp", "control"},
		{"a line separator", "acme\u2028corp", "separator"},
		{"a zero-width space", "acme\u200bcorp", "format"},
		{"a text-direction override", "\u202eprocrema", "format"},
		{"bytes that are not text", "acme\xffcorp", "not valid text"},
		{"a formula", "=HYPERLINK(\"x\")", "formula"},
		{"a plus", "+cmd", "formula"},
		{"a minus", "-2+3", "formula"},
		{"an at sign", "@SUM(1)", "formula"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			msg, db, err := importFrom(t, focusFileWith(t, c.unit))
			if err != nil {
				t.Fatalf("Import returned a hard error rather than refusing the row: %v", err)
			}
			if !strings.Contains(msg, "x_unit") || !strings.Contains(msg, c.says) {
				t.Errorf("the refusal does not name x_unit and say %q: %.300s", c.says, msg)
			}
			if !strings.Contains(msg, "1 row refused") {
				t.Errorf("the row was not refused: %.300s", msg)
			}
			assertNoRows(t, db)
		})
	}

	t.Run("exactly at the bound, padded, empty and ordinary names are kept", func(t *testing.T) {
		atBound := strings.Repeat("u", xUnitMaxBytes)
		msg, db, err := importFrom(t, focusFileWith(t, atBound, "  acme-corp  ", "", "Київ east"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(msg, "refused") {
			t.Fatalf("a plain name was refused: %s", msg)
		}
		got := strings.Join(teamsInCharges(t, db), "|")
		for _, want := range []string{atBound, "acme-corp", "<null>", "Київ east"} {
			if !strings.Contains(got, want) {
				t.Errorf("charges.team lacks %q; has %q", want, got)
			}
		}
	})
}

// A folder with many bad rows produced a sentence with one clause per row, so
// a file of a million bad rows built a message of a million clauses, held in
// memory and then rendered onto the connector page. The count is kept whole;
// the clauses are kept for the first few.
func TestRefusalsAreCountedWholeButNamedOnlyForTheFirstFew(t *testing.T) {
	var b strings.Builder
	b.WriteString(focusHeader + "\n")
	const bad = 1000
	for i := 0; i < bad; i++ {
		f := focusRowFields()
		f[2] = "EUR"
		b.WriteString(strings.Join(f, ",") + "\n")
	}
	b.WriteString(strings.Join(focusRowFields(), ",") + "\n")
	dir := t.TempDir()
	writeFocusFile(t, dir, "many.csv", b.String())

	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, fmt.Sprintf("%d rows refused", bad)) {
		t.Errorf("the sentence does not count all %d refused rows: %.400s", bad, msg)
	}
	if n := strings.Count(msg, "EUR"); n != focusRefusalsShown {
		t.Errorf("the sentence names %d refusals, want the first %d", n, focusRefusalsShown)
	}
	if want := fmt.Sprintf("and %d more", bad-focusRefusalsShown); !strings.Contains(msg, want) {
		t.Errorf("the sentence does not say %q: %.400s", want, msg[len(msg)-200:])
	}
	if len(msg) > 8<<10 {
		t.Errorf("the sentence is %d bytes for one file of bad rows", len(msg))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ai_calls holds %d rows, want the 1 good one", n)
	}
}

// The folder is the operator's, and a link in it points wherever its author
// chose: another user's file, /dev/zero (read forever), a FIFO (blocks). A
// link is not followed and a file that is not a regular one is not opened;
// either is named, so an operator is told rather than shown fewer rows.
func TestALinkInTheFolderIsNotFollowedAndIsNamed(t *testing.T) {
	outside := t.TempDir()
	writeFocusFile(t, outside, "elsewhere.csv", focusHeader+"\n"+strings.Join(focusRowFields(), ","))

	dir := t.TempDir()
	f := focusRowFields()
	f[15] = "run-own"
	writeFocusFile(t, dir, "own.csv", focusHeader+"\n"+strings.Join(f, ","))
	if err := os.Symlink(filepath.Join(outside, "elsewhere.csv"), filepath.Join(dir, "link.csv")); err != nil {
		t.Skipf("this filesystem cannot make a symlink: %v", err)
	}

	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ai_calls holds %d rows, want 1: the link's target was read", n)
	}
	if !strings.Contains(msg, "link.csv") || !strings.Contains(msg, "not followed") {
		t.Errorf("the sentence does not name the link it skipped: %s", msg)
	}

	// A folder holding nothing but a link says so, rather than "no files".
	only := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "elsewhere.csv"), filepath.Join(only, "link.csv")); err != nil {
		t.Fatal(err)
	}
	st := openFocusStore(t)
	configureFocus(t, st.DB(), only)
	_, err = Import(st.DB(), "tokenfuse-focus", false, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "link.csv") {
		t.Errorf("a folder of links only: err = %v, want one naming link.csv", err)
	}
}
