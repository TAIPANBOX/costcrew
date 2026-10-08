package finops_test

// Invariant 90: /ai and the AI desk read a credential as a credential and say
// why the gateway blocked a call.
//
// The two fixtures are the same four calls exported twice, hand-written to the
// shape TokenFuse 1.7.0's release and its focusexport tests describe (its
// invariant 81): two honest calls and one budget refusal by flint, and one
// call an imposter made claiming to be the victim. The export written before
// 1.7.0 files the imposter's refusal under the victim and carries no key and
// no reason; the 1.7.0 export files it under key:imposter.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

const (
	export170   = "tokenfuse-focus-1.7.0-2026-10-07.csv"
	exportOlder = "tokenfuse-focus-before-1.7.0-2026-10-07.csv"
)

// importExports copies the named fixtures into one folder and imports it.
func importExports(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := connectors.Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "tokenfuse-focus", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestACredentialRowIsMarkedAndItsBlocksCarryTheirReason(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export170)
	rows, err := finops.AIByAgent(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]finops.AgentAIRow{}
	for _, r := range rows {
		by[r.Agent] = r
	}
	key, ok := by["key:imposter"]
	if !ok {
		t.Fatalf("no row for key:imposter in %+v", rows)
	}
	if !key.Credential {
		t.Error("key:imposter is not marked as a credential")
	}
	if !key.OnlyIdentityRefusals() || key.BlockedText() != "1 identity_mismatch" {
		t.Errorf("key:imposter's blocks: %q, only identity refusals %v", key.BlockedText(), key.OnlyIdentityRefusals())
	}
	flint := by["agent://acme.example/finops/flint"]
	if flint.Credential {
		t.Error("an agent:// id is marked as a credential")
	}
	if flint.BlockedText() != "1 budget_exceeded" || flint.OnlyIdentityRefusals() {
		t.Errorf("flint's blocks: %q, only identity refusals %v", flint.BlockedText(), flint.OnlyIdentityRefusals())
	}
}

func TestBlockedByReasonCountsEveryReasonAndNamesAnOlderExport(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export170)
	got, err := finops.BlockedByReason(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, r := range got {
		labels = append(labels, r.Label())
	}
	if strings.Join(labels, ",") != "budget_exceeded,identity_mismatch" {
		t.Errorf("reasons from a 1.7.0 export: %v", labels)
	}
	for _, r := range got {
		if r.Reason == finops.IdentityMismatch && !strings.Contains(r.Meaning(), "filed under the credential") {
			t.Errorf("identity_mismatch's meaning does not say where the call is filed: %q", r.Meaning())
		}
	}

	older := bareDB(t)
	importExports(t, older, exportOlder)
	got, err = finops.BlockedByReason(older, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Known || got[0].Calls != 2 || got[0].Label() != "not in this export" ||
		!strings.Contains(got[0].Meaning(), "before TokenFuse 1.7.0") {
		t.Errorf("an export from before 1.7.0 carries no reason, and says so: %+v", got)
	}
}

func TestExportMixNoteOnlyWhenAMonthHoldsBoth(t *testing.T) {
	if n := finops.ExportMixNote(0, 4); n != "" {
		t.Errorf("only 1.7.0 rows: %q", n)
	}
	if n := finops.ExportMixNote(4, 0); n != "" {
		t.Errorf("only older rows: %q", n)
	}
	n := finops.ExportMixNote(1, 3)
	for _, want := range []string{"1 call from an export written before TokenFuse 1.7.0", "3 from 1.7.0 or later",
		"reasoning as output", "keeps its older settlement"} {
		if !strings.Contains(n, want) {
			t.Errorf("the note %q does not say %q", n, want)
		}
	}

	db := bareDB(t)
	importExports(t, db, export170)
	if before, from, err := finops.ExportMix(db, "2026-10"); err != nil || before != 0 || from != 4 {
		t.Errorf("one 1.7.0 export: %d before, %d from 1.7.0, %v", before, from, err)
	}
}
