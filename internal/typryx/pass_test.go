package typryx

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

type captured struct {
	kind, severity string
	data           map[string]any
}

type capture struct{ events []captured }

func (c *capture) Emit(kind, actor, severity string, data map[string]any, _ []string) error {
	c.events = append(c.events, captured{kind, severity, data})
	return nil
}

func hintedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	if err := anomaly.EnsureHintColumns(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// snapshot is every row of every table, with the hint columns left out of
// anomalies: what a hint pass is allowed to change is exactly those columns.
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		q := `SELECT * FROM "` + tb + `" ORDER BY 1`
		if tb == "anomalies" {
			q = `SELECT id, source, team, service, day, direction, amount_cents, baseline_cents,
				excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
				handled_by, state, reason, detected_at, closed_at FROM anomalies ORDER BY id`
		}
		r, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			fmt.Fprintf(&b, "%s %v\n", tb, vals)
		}
		r.Close()
	}
	return b.String()
}

// A hint decides nothing: after a pass that answered, every table but the
// hint columns is exactly what it was. The anomaly's state, owner and reason
// do not move; no option is applied; no task changes.
func TestAHintDecidesNothing(t *testing.T) {
	db := hintedDB(t)
	plant(t, db, "A-decides")
	plant2 := func(id, state string) {
		if _, err := db.Exec(`INSERT INTO anomalies (id, source, service, day, direction, amount_cents,
			baseline_cents, excess_cents, z, rule_version, state, detected_at)
			VALUES (?, 'gcp', 'GKE', '2026-07-01', 'up', 900, 100, 800, 5, 'v1', ?, '2026-07-02T00:00:00Z')`, id, state); err != nil {
			t.Fatal(err)
		}
	}
	plant2("A-triaged", "triaged")
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureOptionTarget(db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('explain', 'why', 'triage-aws', 'aws', 'active', 100, 0, 'A-decides', '2026-07-15', '2026-07-15')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'triage-aws', 'why', 'a body', 'posted', '2026-07-15')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := ares.LastInsertId()
	if _, err := db.Exec(`INSERT INTO artifact_options (artifact, ordinal, class, summary, figure_cents, risk, needs, state)
		VALUES (?, 1, 'anomaly.explain', 'runaway_agent: the batch loop', 70000, 'low', '', 'open')`, artID); err != nil {
		t.Fatal(err)
	}

	before := snapshot(t, db)
	f := newFake(t, []string{"anomaly", "recent_changes"}, answerWith("jev", "jev-1.13.0", "runaway_agent", 0.97))
	rec := &capture{}
	sum := HintAnomalies(context.Background(), db, f.client(), []string{"A-decides", "A-triaged"}, rec)
	if sum.Hinted != 2 {
		t.Fatalf("the pass hinted %d, want 2: %+v", sum.Hinted, sum)
	}
	if after := snapshot(t, db); after != before {
		t.Errorf("a hint changed something beyond its own columns:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, e := range rec.events {
		if e.kind != "anomaly_hinted" {
			t.Errorf("the pass emitted %q: a hint is reported and nothing else happens", e.kind)
		}
	}
	h, ok, err := anomaly.HintOf(db, "A-decides")
	if err != nil || !ok || h.Class != "runaway_agent" || h.Probability != 0.97 || h.Backend != "jev" {
		t.Errorf("HintOf = %+v, %v, %v", h, ok, err)
	}
}

// The bus says which backend answered, the class and its probability, and
// the NAMES of the fields that left; never a field's value.
func TestTheBusRecordsTheBackendNeverTheFields(t *testing.T) {
	db := hintedDB(t)
	a := plant(t, db, "A-bus")
	state := State(db, a)
	events := filepath.Join(t.TempDir(), "costcrew.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: events, Host: "costcrew.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	f := newFake(t, []string{"anomaly", "recent_changes"}, answerWith("openai-logprobs", "qwen2.5:7b", "misconfiguration", 0.66))
	HintAnomalies(context.Background(), db, f.client(), []string{a.ID}, em)
	em.Close()

	b, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if strings.Count(line, "\n") != 0 || line == "" {
		t.Fatalf("want exactly one event line, got:\n%s", b)
	}
	var ev struct {
		Type     string         `json:"type"`
		Severity string         `json:"severity"`
		Data     map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "anomaly_hinted" || ev.Severity != "info" {
		t.Errorf("type %q severity %q", ev.Type, ev.Severity)
	}
	if ev.Data["backend"] != "own-model" || ev.Data["model"] != "qwen2.5:7b" ||
		ev.Data["class"] != "misconfiguration" || ev.Data["outcome"] != "hinted" ||
		ev.Data["fields_sent"] != "anomaly,recent_changes" {
		t.Errorf("the event does not say which backend answered what: %v", ev.Data)
	}
	for name, v := range state {
		if len(v) > 6 && strings.Contains(line, v) {
			t.Errorf("the bus carries the value of field %q: %q", name, v)
		}
	}
	for _, never := range []string{secretTeam, secretAgent, "batch fleet resized", "robust deviations"} {
		if strings.Contains(line, never) {
			t.Errorf("the bus carries %q", never)
		}
	}
}

// A failure is on the bus too, with its reason and no class.
func TestANoHintIsReportedWithItsReason(t *testing.T) {
	db := hintedDB(t)
	a := plant(t, db, "A-refused")
	f := newFake(t, []string{"anomaly"}, func(map[string]any) (int, string) { return 429, `{"error":"over_hourly_cap"}` })
	rec := &capture{}
	sum := HintAnomalies(context.Background(), db, f.client(), []string{a.ID}, rec)
	if sum.NoHint != 1 || len(rec.events) != 1 {
		t.Fatalf("summary %+v, %d events", sum, len(rec.events))
	}
	d := rec.events[0].data
	if d["outcome"] != "no_hint" || !strings.Contains(fmt.Sprint(d["reason"]), "over_hourly_cap") || d["class"] != nil {
		t.Errorf("event data %v", d)
	}
	h, ok, _ := anomaly.HintOf(db, a.ID)
	if !ok || h.Answered() || !strings.Contains(h.Reason, "HTTP 429, over_hourly_cap") {
		t.Errorf("stored %+v", h)
	}
}

// A typryx that is down costs a pass three asks, never one per anomaly, and
// leaves the rest for the next pass.
func TestAPassStopsAfterThreeAsksTypryxNeverAnswered(t *testing.T) {
	db := hintedDB(t)
	var ids []string
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("A-down-%d", i)
		plant(t, db, id)
		ids = append(ids, id)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	sum := HintAnomalies(context.Background(), db, New(base, "", time.Second), ids, nil)
	if sum.Asked != 3 || !sum.Stopped {
		t.Errorf("summary %+v, want 3 asked and stopped", sum)
	}
	left, _ := anomaly.NeedingHint(db, 100)
	if len(left) != 10 {
		t.Errorf("%d anomalies still need a hint, want all 10 (a failure is asked again)", len(left))
	}
}

// A typryx that hangs costs three timeouts, bounded, and the pass returns.
func TestATypryxThatHangsCostsBoundedTime(t *testing.T) {
	db := hintedDB(t)
	var ids []string
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("A-hang-%d", i)
		plant(t, db, id)
		ids = append(ids, id)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	sum := HintAnomalies(context.Background(), db, New(srv.URL, "", 100*time.Millisecond), ids, nil)
	if took := time.Since(start); took > 3*time.Second || sum.Asked != 3 || !sum.Stopped {
		t.Errorf("took %s, summary %+v", took, sum)
	}
}

// One answered ask per anomaly: an answer is never asked again, and a later
// failure never overwrites it.
func TestAnAnswerIsKeptAndNeverAskedAgain(t *testing.T) {
	db := hintedDB(t)
	a := plant(t, db, "A-once")
	f := newFake(t, []string{"anomaly"}, answerWith("jev", "", "expected_growth", 0.9))
	HintAnomalies(context.Background(), db, f.client(), []string{a.ID}, nil)
	left, _ := anomaly.NeedingHint(db, 10)
	if len(left) != 0 {
		t.Errorf("an answered anomaly still needs a hint: %v", left)
	}
	if err := anomaly.SaveHint(db, a.ID, anomaly.Hint{Reason: "typryx could not be reached"}); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := anomaly.HintOf(db, a.ID); h.Class != "expected_growth" || h.Reason != "" {
		t.Errorf("a failure overwrote an answer: %+v", h)
	}
}

func TestACancelledPassStopsAtOnce(t *testing.T) {
	db := hintedDB(t)
	a := plant(t, db, "A-cancel")
	f := newFake(t, []string{"anomaly"}, answerWith("jev", "", "unknown", 0.5))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sum := HintAnomalies(ctx, db, f.client(), []string{a.ID}, nil); sum.Asked != 0 || !sum.Stopped {
		t.Errorf("summary %+v", sum)
	}
}
