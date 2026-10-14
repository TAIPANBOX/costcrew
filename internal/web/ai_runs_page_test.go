package web_test

// Invariant 101: /ai/runs lists every run the gateway's rows name, with every
// model it called and each model's share of its spend in the order it called
// them, how it ended, and that the door it came through is not in the export.

import (
	"strings"
	"testing"
)

// runRow is the /ai/runs row for one run id, so an assertion about it cannot
// be met by another row.
func runRow(t *testing.T, body, run string) string {
	t.Helper()
	i := strings.Index(body, "<code>"+run+"</code><span class=\"sub\">")
	if i < 0 {
		t.Fatalf("no /ai/runs row for %s", run)
	}
	j := strings.Index(body[i:], "</tr>")
	return body[i : i+j]
}

func TestTheRunsPageShowsEveryModelARunUsedWithItsShare(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-runs-2026-10-14.csv")

	code, body, _ := admin.get(t, "/ai/runs")
	if code != 200 {
		t.Fatalf("GET /ai/runs: %d", code)
	}
	a := runRow(t, body, "w2-b12-A")
	for _, want := range []string{"model switch",
		"<code>claude-opus-5-5</code> 96%", "<code>claude-haiku-5-5</code> 4.0%",
		"3 calls, 0.05", "2 calls, 0.0020", "last call on <code>claude-haiku-5-5</code>"} {
		if !strings.Contains(a, want) {
			t.Errorf("w2-b12-A's row does not show %q", want)
		}
	}
	if strings.Index(a, "claude-opus-5-5") > strings.Index(a, "claude-haiku-5-5") {
		t.Error("w2-b12-A's models are not in the order the run called them")
	}
	if one := runRow(t, body, "run-budget"); strings.Contains(one, "model switch") {
		t.Error("a run on one model is marked as a model switch")
	}
	for run, want := range map[string]string{
		"run-killed":       "<span class=\"chip open\">killed</span>",
		"run-budget":       "stopped by the gateway: budget_exceeded",
		"run-on":           "1 call refused, then went on",
		"run-blocked-only": "no spend",
	} {
		if !strings.Contains(runRow(t, body, run), want) {
			t.Errorf("run %s's row does not say %q", run, want)
		}
	}
	for _, want := range []string{"is not in the FOCUS export", "read from one source",
		"<div class=\"k\">Switched model</div><div class=\"v\">1</div>",
		"<div class=\"k\">Runs</div><div class=\"v\">7</div>",
		"no run id on the row"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai/runs does not contain %q", want)
		}
	}
	// Largest spend first.
	if strings.Index(body, "<code>w2-b12-A</code>") > strings.Index(body, "<code>run-killed</code>") {
		t.Error("the runs are not listed largest spend first")
	}

	_, ai, _ := admin.get(t, "/ai")
	if !strings.Contains(ai, `<a href="/ai/runs">Runs</a>`) {
		t.Error("/ai does not link to the runs page")
	}
}

func TestTheRunsPageSaysThereAreNoRunsBeforeAnImport(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	code, body, _ := admin.get(t, "/ai/runs")
	if code != 200 {
		t.Fatalf("GET /ai/runs: %d", code)
	}
	for _, want := range []string{"<h2>No runs yet</h2>", `href="/connectors/tokenfuse-focus"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai/runs before an import does not contain %q", want)
		}
	}
	if strings.Contains(body, "<h2>By run</h2>") {
		t.Error("/ai/runs lists runs for a generated estate that has none")
	}
}
