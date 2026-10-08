package connectors

// The three CSV readers that are not the FOCUS reader (rightsizing, budget
// recommendations, SaaS seats) read the same kind of operator-configured
// folder and owe the same two things the FOCUS reader already gives
// (TestRefusalsAreCountedWholeButNamedOnlyForTheFirstFew,
// TestALinkInTheFolderIsNotFollowedAndIsNamed): a file of bad rows is counted
// whole but only its first few rows are named, and a link in the folder is
// neither followed nor passed over in silence. One table of cases, one per
// reader, because the property is one property.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type csvReaderCase struct {
	id     string
	table  string
	header string
	// rowA and rowB are two valid rows with different keys, so a folder that
	// reads both holds two rows and one that reads only rowA holds one.
	rowA, rowB func() []string
	// bad turns a valid row into one the reader refuses.
	bad func([]string) []string
}

func csvReaderCases() []csvReaderCase {
	return []csvReaderCase{
		{
			id: "aws-rightsizing", table: "recommendations", header: awsHeader,
			rowA: awsRowFields,
			rowB: func() []string { f := awsRowFields(); f[1] = "i-0bbbbbbbbbbbbbbbb"; return f },
			bad:  func(f []string) []string { f[6] = "plenty"; return f },
		},
		{
			id: "aws-budgets-recommended", table: "budget_recommendations", header: budgetRecAWSHeader,
			rowA: budgetRecAWSRowFields,
			rowB: func() []string { f := budgetRecAWSRowFields(); f[1] = "research"; return f },
			bad:  func(f []string) []string { f[2] = "september"; return f },
		},
		{
			id: "saas-seats", table: "licences", header: strings.Join(saasSeatsHeaderFields(), ","),
			rowA: saasSeatsRowFields,
			rowB: func() []string { f := saasSeatsRowFields(); f[0] = "Atlassian"; return f },
			bad:  func(f []string) []string { f[2] = "many"; return f },
		},
	}
}

func csvReaderImport(t *testing.T, id, dir string) (string, *sql.DB, error) {
	t.Helper()
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, id, map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, id, false, ImportOptions{})
	return msg, db, err
}

func csvReaderRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEveryCSVReaderCountsRefusedRowsWholeButNamesOnlyTheFirstFew(t *testing.T) {
	for _, c := range csvReaderCases() {
		t.Run(c.id, func(t *testing.T) {
			const bad = 1000
			var b strings.Builder
			b.WriteString(c.header + "\n")
			for i := 0; i < bad; i++ {
				b.WriteString(strings.Join(c.bad(c.rowA()), ",") + "\n")
			}
			b.WriteString(strings.Join(c.rowA(), ",") + "\n")
			dir := t.TempDir()
			writeFocusFile(t, dir, "many.csv", b.String())

			msg, db, err := csvReaderImport(t, c.id, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, fmt.Sprintf("%d rows refused", bad)) {
				t.Errorf("the sentence does not count all %d refused rows: %.300s", bad, msg)
			}
			if n := strings.Count(msg, "many.csv row "); n != focusRefusalsShown {
				t.Errorf("the sentence names %d refused rows, want the first %d", n, focusRefusalsShown)
			}
			if want := fmt.Sprintf("and %d more", bad-focusRefusalsShown); !strings.Contains(msg, want) {
				t.Errorf("the sentence does not say %q: ...%s", want, msg[max(0, len(msg)-200):])
			}
			if len(msg) > 8<<10 {
				t.Errorf("the sentence is %d bytes for one file of bad rows", len(msg))
			}
			if n := csvReaderRows(t, db, c.table); n != 1 {
				t.Errorf("%s holds %d rows, want the 1 good one", c.table, n)
			}
		})
	}
}

func TestEveryCSVReaderNamesALinkItDidNotFollow(t *testing.T) {
	for _, c := range csvReaderCases() {
		t.Run(c.id, func(t *testing.T) {
			outside := t.TempDir()
			writeFocusFile(t, outside, "elsewhere.csv", c.header+"\n"+strings.Join(c.rowB(), ","))

			dir := t.TempDir()
			writeFocusFile(t, dir, "own.csv", c.header+"\n"+strings.Join(c.rowA(), ","))
			if err := os.Symlink(filepath.Join(outside, "elsewhere.csv"), filepath.Join(dir, "link.csv")); err != nil {
				t.Skipf("this filesystem cannot make a symlink: %v", err)
			}
			msg, db, err := csvReaderImport(t, c.id, dir)
			if err != nil {
				t.Fatal(err)
			}
			if n := csvReaderRows(t, db, c.table); n != 1 {
				t.Errorf("%s holds %d rows, want 1: the link's target was read", c.table, n)
			}
			if !strings.Contains(msg, "link.csv") || !strings.Contains(msg, "not followed") {
				t.Errorf("the sentence does not name the link it skipped: %s", msg)
			}

			// A folder of nothing but links says so, rather than "no files".
			only := t.TempDir()
			if err := os.Symlink(filepath.Join(outside, "elsewhere.csv"), filepath.Join(only, "link.csv")); err != nil {
				t.Fatal(err)
			}
			_, _, err = csvReaderImport(t, c.id, only)
			if err == nil || !strings.Contains(err.Error(), "link.csv") {
				t.Errorf("a folder of links only: err = %v, want one naming link.csv", err)
			}

			// And a folder of many links names the first few and counts the rest.
			many := t.TempDir()
			writeFocusFile(t, many, "own.csv", c.header+"\n"+strings.Join(c.rowA(), ","))
			const links = focusRefusalsShown + 5
			for i := 0; i < links; i++ {
				if err := os.Symlink(filepath.Join(outside, "elsewhere.csv"),
					filepath.Join(many, fmt.Sprintf("link-%02d.csv", i))); err != nil {
					t.Fatal(err)
				}
			}
			msg, _, err = csvReaderImport(t, c.id, many)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, fmt.Sprintf("%d files not read", links)) {
				t.Errorf("the sentence does not count all %d links: %s", links, msg)
			}
			if n := strings.Count(msg, "not followed"); n != focusRefusalsShown {
				t.Errorf("the sentence names %d links, want the first %d", n, focusRefusalsShown)
			}
			if !strings.Contains(msg, "and 5 more") {
				t.Errorf("the sentence does not say how many links it did not name: %s", msg)
			}
		})
	}
}

// The tally itself, on its own: counted whole, named up to the bound, and the
// clause says how many it left out.
func TestARefusalTallyCountsEveryRefusalAndNamesTheFirstFew(t *testing.T) {
	var a, b refusalTally
	for i := 0; i < 15; i++ {
		a.add(fmt.Sprintf("a%d", i))
		b.add(fmt.Sprintf("b%d", i))
	}
	a.addAll(b)
	if a.count != 30 || len(a.named) != focusRefusalsShown {
		t.Fatalf("count %d named %d, want 30 and %d", a.count, len(a.named), focusRefusalsShown)
	}
	if got := a.clause(); !strings.HasSuffix(got, "; and 10 more") || !strings.HasPrefix(got, "a0; a1;") {
		t.Errorf("clause = %q", got)
	}
	var small refusalTally
	small.add("only")
	if got := small.clause(); got != "only" {
		t.Errorf("one refusal: clause = %q, want it alone", got)
	}
}
