package promptfixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// A write the fixture makes is checked. Build reads each one's error and
// stops at the first refusal, so a refused write must come back to the caller
// as an error and never as a fixture that is quietly missing a table.
//
// SQLite lets a BEFORE trigger refuse a write with RAISE(ABORT). The tests
// below plant one such trigger at a time, on one write, and then run the real
// fill against it. A trigger on a table the fixture first touches late cannot
// be planted before fill runs, so the schema the complete fixture ends with is
// replayed into an empty store first. The seeders are meant to run again over
// an existing schema on every start (invariant 11), and the replayed store
// runs them through every step the fixture makes.

// referenceSchema is every table and index a complete fixture ends with.
func referenceSchema(t *testing.T) []schemaObject {
	t.Helper()
	st := fixtureCopy(t)
	defer st.Close()
	rows, err := st.DB().Query(`SELECT type, name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.typ, &o.name, &o.sql); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("the complete fixture holds no schema to replay")
	}
	return out
}

type schemaObject struct{ typ, name, sql string }

// storeWithSchema opens an empty store and creates each object of schema that
// it does not already hold (users and sessions are created by store.Open).
func storeWithSchema(t *testing.T, schema []schemaObject) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// Throwaway stores: no fsync per commit. Durability is not what these tests look at.
	if _, err := st.DB().Exec(`PRAGMA synchronous = OFF`); err != nil {
		st.Close()
		t.Fatal(err)
	}
	for _, o := range schema {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, o.name).Scan(&n); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if n > 0 {
			continue
		}
		if _, err := st.DB().Exec(o.sql); err != nil {
			st.Close()
			t.Fatalf("replaying %s %s: %v", o.typ, o.name, err)
		}
	}
	return st
}

// refusedWrites holds one refusal per place where fill can return an error.
// A table that several steps write has a row for each step whose failure
// reaches a different return, and the WHEN clause picks out that step's
// statement, so each planted refusal stops fill where its row says it does.
// Rows that would reach a return another row already reaches are not repeated.
var refusedWrites = []struct{ name, event string }{
	{"users insert", "INSERT ON users"},
	{"sessions insert", "INSERT ON sessions"},
	{"drivers insert, estate", "INSERT ON drivers"},
	{"allocation rules insert, seeds", "INSERT ON allocation_rules"},
	{"anomalies insert", "INSERT ON anomalies"},
	{"anomalies hint update, first", "UPDATE OF hint_class ON anomalies"},
	{"anomalies hint update, second", "UPDATE OF hint_reason ON anomalies WHEN NEW.hint_reason LIKE 'typryx refused%'"},
	{"sprints insert, crew seed", "INSERT ON sprints"},
	{"explainer insert, history", "INSERT ON explainers"},
	{"teams insert", "INSERT ON teams"},
	{"tasks owner update, alice", "UPDATE OF owner ON tasks WHEN NEW.owner = 'alice'"},
	{"tasks owner update, bob", "UPDATE OF owner ON tasks WHEN NEW.owner = 'bob'"},
	{"sprints goal update", "UPDATE OF goal ON sprints"},
	{"charges invoice update, AWS", "UPDATE OF invoice_id ON charges WHEN NEW.source = 'aws'"},
	{"charges invoice update, GCP", "UPDATE OF invoice_id ON charges WHEN NEW.source = 'gcp'"},
	{"ai calls insert", "INSERT ON ai_calls"},
	{"ai call charge insert", "INSERT ON charges WHEN NEW.provenance = 'planted-fixture'"},
	{"commitments insert", "INSERT ON commitments"},
	{"connection record insert, first save", "INSERT ON connections"},
	{"connection record update, import read", "UPDATE ON connections"},
	{"connection record insert, provider", "INSERT ON connections WHEN NEW.id = 'anthropic-usage'"},
	{"provider usage insert", "INSERT ON provider_usage"},
	{"customer unit charge insert", "INSERT ON charges WHEN NEW.team = 'acme-retail-unit'"},
	{"chargeback insert", "INSERT ON chargeback"},
	{"forecast freeze update", "UPDATE OF frozen_by ON forecasts"},
	{"plan ask insert", "INSERT ON plan_asks"},
	{"desk halt insert", "INSERT ON desk_halts"},
	{"tasks desk update", "UPDATE OF desk ON tasks"},
	{"tasks insert, first explanation", "INSERT ON tasks WHEN NEW.title LIKE 'Explain the % move'"},
	{"tasks insert, follow-up", "INSERT ON tasks WHEN NEW.title LIKE '%in July'"},
	{"artifacts insert, first explanation", "INSERT ON artifacts WHEN NEW.body LIKE 'ZEBRA-QUOKKA-BODY: %ask bob.'"},
	{"artifacts insert, analyst history", "INSERT ON artifacts WHEN NEW.body LIKE '%number 0'"},
	{"artifact options insert, applied", "INSERT ON artifact_options WHEN NEW.state = 'applied'"},
	{"decision request insert", "INSERT ON decision_requests"},
	{"drivers insert, planted", "INSERT ON drivers WHEN NEW.label LIKE 'ZEBRA-QUOKKA-DRIVER%'"},
	{"unit rule insert", "INSERT ON unit_rules"},
}

// underRace is the part of refusedWrites that runs under -race. A refused
// fill is a whole fixture build, about fourteen seconds of CPU under the race
// detector, and the full table takes longer than the default test timeout.
// One row per stage stays, so the refusal path is still run under the race
// detector; the other rows run in the ordinary suite.
var underRace = map[string]bool{
	"users insert":                         true,
	"drivers insert, estate":               true,
	"allocation rules insert, seeds":       true,
	"sprints insert, crew seed":            true,
	"ai calls insert":                      true,
	"connection record insert, first save": true,
	"tasks desk update":                    true,
	"artifact options insert, applied":     true,
}

// TestAFixtureWriteThatIsRefusedStopsTheBuild plants one refusal per write and
// requires fill to return that refusal. Each row fails if the code under it
// swallows the error and goes on, because then fill returns nil.
func TestAFixtureWriteThatIsRefusedStopsTheBuild(t *testing.T) {
	schema := referenceSchema(t)
	for _, w := range refusedWrites {
		t.Run(w.name, func(t *testing.T) {
			if raceEnabled && !underRace[w.name] {
				t.Skip("not run under -race: see underRace")
			}
			t.Parallel()
			st := storeWithSchema(t, schema)
			defer st.Close()
			trigger := "CREATE TRIGGER planted_refusal BEFORE " + w.event +
				" BEGIN SELECT RAISE(ABORT, 'planted refusal'); END"
			if _, err := st.DB().Exec(trigger); err != nil {
				t.Fatalf("planting the refusal: %v", err)
			}
			err := fill(st)
			if err == nil {
				t.Fatalf("fill returned nil with the %q write refused: a refused write was swallowed", w.name)
			}
			if !strings.Contains(err.Error(), "planted refusal") {
				t.Fatalf("fill returned %v; want the planted refusal to be the reason", err)
			}
		})
	}
}

// TestAnEmptyAnomalyDeskStopsTheDeliverables covers the fixture's read of the
// anomaly the deliverables hang off. With every anomaly dropped on insert there
// is none to read, and Build must say so rather than write deliverables that
// point at nothing.
func TestAnEmptyAnomalyDeskStopsTheDeliverables(t *testing.T) {
	st := storeWithSchema(t, referenceSchema(t))
	defer st.Close()
	if _, err := st.DB().Exec(`CREATE TRIGGER planted_drop BEFORE INSERT ON anomalies
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatalf("planting the drop: %v", err)
	}
	err := fill(st)
	if err == nil {
		t.Fatal("fill returned nil with no anomaly to hang the deliverables on")
	}
	if !strings.Contains(err.Error(), "no rows") {
		t.Fatalf("fill returned %v; want the empty read to be the reason", err)
	}
}

// TestBuildReturnsAStoreItCannotOpen covers the first refusal Build can hit:
// a data directory whose parent is a file cannot be created, and Build returns
// that error with no store.
func TestBuildReturnsAStoreItCannotOpen(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Build(filepath.Join(parent, "data"))
	if st != nil {
		st.Close()
	}
	if err == nil {
		t.Fatal("Build inside a path whose parent is a file returned no error")
	}
	if st != nil {
		t.Fatal("Build returned a store alongside its error")
	}
}

// TestBuildReturnsTheRefusalOfAWriteAndNoStore covers the path where fill
// fails inside Build: the store is closed and the refusal is what comes back.
func TestBuildReturnsTheRefusalOfAWriteAndNoStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER planted_refusal BEFORE INSERT ON users
		BEGIN SELECT RAISE(ABORT, 'planted refusal'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	built, err := Build(dir)
	if err == nil || !strings.Contains(err.Error(), "planted refusal") {
		if built != nil {
			built.Close()
		}
		t.Fatalf("Build = (%v, %v); want the planted refusal and no store", built, err)
	}
	if built != nil {
		t.Fatal("Build returned a store alongside a refusal")
	}
}

// TestTheCSVImportStopsWhenItsTempDirCannotBeMade covers the CSV import's own
// refusal to start: with no usable temp directory, Build says so instead of
// importing from nothing. Not parallel: it sets TMPDIR for the whole process.
func TestTheCSVImportStopsWhenItsTempDirCannotBeMade(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(dir, "no-such-dir"))
	st, err := Build(t.TempDir())
	if err == nil {
		st.Close()
		t.Fatal("Build with an unusable TMPDIR returned no error")
	}
	if !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("Build error = %v; want the temp directory's own refusal", err)
	}
}
