package web

// A period reaches the team, desk and service pages' breakdown queries from
// the URL (?period=). Until 2026-10-08 it was concatenated into the SQL text,
// and what stopped a hostile one was a check somewhere else: s.period only
// hands back a month the store already holds. These tests call the query
// builder directly, with that check out of the way, so what they prove is that
// the period is a bound parameter and not text in the statement.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func periodStore(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		source, day, service, team string
		cents                      int64
	}{
		{"aws", "2026-01-15", "EC2", "alpha", 100},
		{"aws", "2026-02-15", "EC2", "alpha", 200},
		{"gcp", "2026-01-15", "GKE", "beta", 9999},
	} {
		if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
			VALUES (?,?,?,?,'Usage',?)`, r.source, r.day, r.service, r.team, r.cents); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestABreakdownReadsOnlyTheNamedPeriod(t *testing.T) {
	db := periodStore(t)
	rows, total, err := periodBreakdown(db, "team", "alpha", "2026-01", "service")
	if err != nil {
		t.Fatal(err)
	}
	if total != 100 || len(rows) != 1 || rows[0].Key != "EC2" {
		t.Errorf("alpha in 2026-01: rows %+v, total %d, want EC2 alone at 100", rows, total)
	}
}

// Every shape below is a string an attacker could put in ?period= if the
// month check in s.period were ever relaxed. Bound as a parameter, each one is
// a month that no charge carries, so the answer is empty and the store is
// untouched. Concatenated, the first widens the WHERE to every row of every
// team, which is the leak this test names.
func TestAHostilePeriodIsAValueNotSQL(t *testing.T) {
	for _, hostile := range []string{
		`2026-01' OR '1'='1`,
		`2026-01' OR team<>'`,
		`2026-01' UNION SELECT team, billed_cents FROM charges WHERE '1'='1`,
		`2026-01'; DROP TABLE charges; --`,
		`'`,
		"2026-01\x00",
	} {
		t.Run(strings.ReplaceAll(hostile, "\x00", `\0`), func(t *testing.T) {
			db := periodStore(t)
			rows, total, err := periodBreakdown(db, "team", "alpha", hostile, "service")
			if err != nil {
				t.Fatalf("a hostile period made the statement fail, so it was read as SQL: %v", err)
			}
			if len(rows) != 0 || total != 0 {
				t.Errorf("a hostile period returned %d rows totalling %d: the period was read as SQL, "+
					"not compared as a value (rows %+v)", len(rows), total, rows)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&n); err != nil || n != 3 {
				t.Errorf("after a hostile period the charges table holds %d rows (err %v), want 3", n, err)
			}
		})
	}
}

// The two column names are the one part of the statement that is still text,
// and they come from the code, never from a request. A name outside the four
// the pages use is refused rather than written into SQL.
func TestABreakdownRefusesAColumnItWasNotWrittenFor(t *testing.T) {
	db := periodStore(t)
	for _, c := range [][2]string{
		{"team=team OR 1", "service"},
		{"team", "service, (SELECT 1)"},
		{"billed_cents", "service"},
	} {
		if _, _, err := periodBreakdown(db, c[0], "alpha", "2026-01", c[1]); err == nil {
			t.Errorf("periodBreakdown(%q, %q) was answered; want a refusal naming the column", c[0], c[1])
		}
	}
}
