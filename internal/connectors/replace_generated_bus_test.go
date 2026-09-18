package connectors

// The replacement of the generated estate, journaled through the recorder the
// console actually wires once -stack-events is given.
//
// TestGeneratedEstateIsNotMixed hands the reader st.AsRecorder(), the hash
// chain alone, and the chain writes whatever severity it is given, an empty
// one included. cmd/costcrew/main.go tees that chain with the stack emitter,
// and the emitter refuses a severity outside the envelope's closed enum. The
// reader emitted generated_estate_replaced with "" from the day it was
// written, so on an installation wired to the bus the emit failed, the reader
// returned "journaling the generated estate's replacement: severity \"\" is
// not one of info, low, medium, high, critical", and the deferred rollback
// undid the whole import. Measured on the appliance proving run of 2026-09-17
// (costcrew#66): the reader's own /test had read 277 rows and 4 agents, and
// the console kept its 19550 generated charges. A fixture too convenient for
// the defect to exist in is why no test here saw it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	asgevent "github.com/TAIPANBOX/agent-stack-go/event"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/stack"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func TestReplacingTheGeneratedEstateIsJournaledWithASeverityTheBusAccepts(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	var generated int
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&generated)
	if generated == 0 {
		t.Fatal("sanity: estate.Seed wrote no generated charges")
	}
	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)

	events := filepath.Join(t.TempDir(), "costcrew.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: events, Host: "costcrew.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	// The production shape, not the convenient one: the chain AND the bus.
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{
		ReplaceGenerated: true, Actor: "boss", Rec: store.Tee(st.AsRecorder(), em),
	})
	if err != nil {
		t.Fatalf("Import with -replace-generated, on the bus: %v (%s)", err, msg)
	}

	// The transaction committed: the generated rows are gone and the real
	// ones are there. Checked because the failure this guards against is a
	// ROLLBACK, and a redirect alone cannot tell a landed import from an
	// undone one.
	var stillGenerated, real int
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&stillGenerated)
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance='tokenfuse-focus'`).Scan(&real)
	if stillGenerated != 0 || real == 0 {
		t.Errorf("after the import: %d generated charges left, %d real ones; the "+
			"replacement did not land", stillGenerated, real)
	}

	// The bus carries the replacement with a severity every consumer accepts,
	// read back through the contract's own decoder rather than by string.
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		ev, err := asgevent.Unmarshal([]byte(line))
		if err != nil {
			t.Fatalf("line %d: the contract refused it: %v", i+1, err)
		}
		if ev.Type != "generated_estate_replaced" {
			continue
		}
		found++
		if ev.Severity != asgevent.SeverityInfo {
			t.Errorf("generated_estate_replaced went out with severity %q, want %q: a "+
				"fixture being retired is information, not a warning", ev.Severity, asgevent.SeverityInfo)
		}
		if ev.AgentID != "agent://costcrew.test/boss" {
			t.Errorf("the replacement is credited to %q, not to the operator who asked for it", ev.AgentID)
		}
		if ev.Data["connector"] != "tokenfuse-focus" {
			t.Errorf("the event does not name the connector that replaced the estate: %v", ev.Data)
		}
	}
	if found != 1 {
		t.Errorf("the bus carries %d generated_estate_replaced events, want exactly 1", found)
	}

	// And the chain has it too: the tee writes both, and the audit page reads
	// the chain, not the bus.
	tail, err := st.JournalTail(20)
	if err != nil {
		t.Fatal(err)
	}
	inChain := false
	for _, rec := range tail {
		if rec.Event == "generated_estate_replaced" {
			inChain = true
		}
	}
	if !inChain {
		t.Error("no generated_estate_replaced entry in the hash chain")
	}
}
