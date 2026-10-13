package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// A drop that fails between its data file and its manifest must not leave
// the epoch folder behind: a sync would carry a folder Cloudability reads as
// a drop, and the next run at the same second would be refused by it.
func TestAWriteThatFailsPartWayLeavesNoFolder(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key"})
	real := createFile
	t.Cleanup(func() { createFile = real })
	createFile = func(p string, data []byte) error {
		if strings.HasSuffix(p, ".partial") { // the manifest, written after the data
			return errors.New("no space left on device")
		}
		return real(p, data)
	}
	r := runTool(t, at, baseFlags(in, out)...)
	if r.code != 1 || !strings.Contains(r.stderr, "no space left on device") {
		t.Fatalf("a failed write was not reported (exit %d): %s", r.code, r.stderr)
	}
	if got := filesUnder(t, out); len(got) != 0 {
		t.Errorf("a drop that failed part way left %v behind", got)
	}
	epochDir := filepath.Join(out, "TokenFuse", "20261001-20261101", fmt.Sprint(at.Unix()))
	if _, err := os.Stat(epochDir); !os.IsNotExist(err) {
		t.Errorf("the epoch folder of a failed drop was left behind: %v", err)
	}
	createFile = real
	dropAt(t, in, out, at) // nothing was left to refuse the same second again
}

// Two refusals a gateway export cannot reach through the reader, held where
// the drop is built: an instant SQLite cannot put on a day, and a month whose
// groups each fit in 64 bits but whose total does not.
func TestBuildRefusesAnInstantItCannotDateAndATotalThatOverflows(t *testing.T) {
	open := func(t *testing.T) *sql.DB {
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := connectors.EnsureFocusSchema(st.DB()); err != nil {
			t.Fatal(err)
		}
		return st.DB()
	}
	insert := func(t *testing.T, db *sql.DB, row int, ts, agent string, micros int64) {
		if _, err := db.Exec(`INSERT INTO ai_calls(file_sha256, row_no, ts, day, agent, provider, model,
				tokens_in, tokens_out, billed_microusd, blocked, basis)
			VALUES ('f', ?, ?, '2026-10-01', ?, 'Anthropic', 'claude-haiku-4-5', 1, 1, ?, 0, 'settled')`,
			row, ts, agent, micros); err != nil {
			t.Fatal(err)
		}
	}
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	db := open(t)
	insert(t, db, 1, "2026-10-01T09:00:00Z", reconciler, 1000)
	insert(t, db, 2, "the first of October", reconciler, 1000)
	if _, err := build(db, month, "acct", ""); err == nil || !strings.Contains(err.Error(), "cannot place") {
		t.Errorf("a call SQLite cannot date was not refused: %v", err)
	}

	db = open(t)
	insert(t, db, 1, "2026-10-01T09:00:00Z", reconciler, 5_000_000_000_000_000_000)
	insert(t, db, 2, "2026-10-01T09:00:00Z", scorer, 5_000_000_000_000_000_000)
	if _, err := build(db, month, "acct", ""); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Errorf("a month whose total overflows was not refused: %v", err)
	}
}
