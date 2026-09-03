package web_test

// C1-SPEC.md: the owner is told the moment the explanation is posted, the
// queue says how long it takes, and nothing closes without a person. Red
// first against main: posting a deliverable emits nothing about the
// anomaly it answers, the queue page carries no owner or told column, and
// the anomaly page says nothing about how long it has been open.

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

func nullableTeamWeb(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// plantAnomalyForTelling writes one open anomaly detected daysAgo days
// before now, so a test can assert an EXACT day count rather than merely
// that some number appeared.
func plantAnomalyForTelling(t *testing.T, h *harness, id, team string, daysAgo int) {
	t.Helper()
	detectedAt := time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.RFC3339)
	if _, err := h.st.DB().Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?, 'aws', ?, 'Amazon EC2', '2026-07-14', 'up', 10000, 5000, 5000, 4.1,
		        'v1', 'open', ?)`, id, nullableTeamWeb(team), detectedAt); err != nil {
		t.Fatal(err)
	}
}

// plantDraftOnAnomalyTask writes a task tied to anomalyID, assigned to
// analyst on desk, and a DRAFT artifact on it carrying one option: the
// shape crew.Post itself expects, one state earlier than finops_test.go's
// own plantOption (which starts already posted), so this test can exercise
// the real POST route rather than a fixture that skips it.
func plantDraftOnAnomalyTask(t *testing.T, h *harness, anomalyID, analyst, desk, optionClass, optionSummary string) (artifactID int) {
	t.Helper()
	tres, err := h.st.DB().Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('investigate', 'find the cause', ?, ?, 'active', 0, 0, ?, datetime('now'), datetime('now'))`,
		analyst, desk, anomalyID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := h.st.DB().Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, ?, 'a deliverable', 'the cause was X', 'draft', datetime('now'))`,
		taskID, analyst)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB().Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, ?, ?, 50000, 0, 'low', 'nothing', '[]', 'open')`,
		artID, optionClass, optionSummary); err != nil {
		t.Fatal(err)
	}
	return int(artID)
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// anomalyExplainedFor finds the newest anomaly_explained journal entry
// naming anomalyID, or nil.
func anomalyExplainedFor(t *testing.T, h *harness, anomalyID string) map[string]any {
	t.Helper()
	tail, err := h.st.JournalTail(200)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range tail {
		if rec.Event != "anomaly_explained" {
			continue
		}
		if id, _ := rec.Data["anomaly"].(string); id == anomalyID {
			return rec.Data
		}
	}
	return nil
}

func countAnomalyExplainedFor(t *testing.T, h *harness, anomalyID string) int {
	t.Helper()
	tail, err := h.st.JournalTail(200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rec := range tail {
		if rec.Event != "anomaly_explained" {
			continue
		}
		if id, _ := rec.Data["anomaly"].(string); id == anomalyID {
			n++
		}
	}
	return n
}

// The scenario section 4 names first: the owner is told, with the named
// cause and the option classes offered, the moment the deliverable is
// posted.
func TestPostingAnAnomalyDeliverableTellsTheOwner(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	plantAnomalyForTelling(t, h, "A-tell-owner", "ml-platform", 2)
	if _, err := h.st.DB().Exec(`INSERT INTO teams(name, owner) VALUES('ml-platform', 'y.mercer')`); err != nil {
		t.Fatal(err)
	}
	artID := plantDraftOnAnomalyTask(t, h, "A-tell-owner", "investigator-aws", "aws",
		"anomaly.explain", "a scheduled batch job ran long")

	if got := anomalyExplainedFor(t, h, "A-tell-owner"); got != nil {
		t.Fatalf("anomaly_explained was already journalled before the deliverable was posted: %v", got)
	}

	path := "/artifact/" + strconv.Itoa(artID) + "/post"
	code, loc := h.post(t, path, url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("posting the deliverable was refused: %d %s", code, loc)
	}

	got := anomalyExplainedFor(t, h, "A-tell-owner")
	if got == nil {
		t.Fatal("no anomaly_explained event was journalled when the deliverable was posted")
	}
	if owner, _ := got["owner"].(string); owner != "y.mercer" {
		t.Errorf("owner = %q, want y.mercer (the team's own named owner)", owner)
	}
	if cause, _ := got["cause"].(string); cause != "a scheduled batch job ran long" {
		t.Errorf("cause = %q, want the anomaly.explain option's own summary", cause)
	}
	classes, _ := got["option_classes"].([]any)
	if len(classes) != 1 || classes[0] != "anomaly.explain" {
		t.Errorf("option_classes = %v, want [anomaly.explain]", classes)
	}
	if int64Of(got["artifact"]) != int64(artID) {
		t.Errorf("artifact = %v, want %d", got["artifact"], artID)
	}
}

// Mutant: emit before the post instead of after. A post that is refused
// (here: an artifact already posted) must tell nobody anything, because it
// did not actually happen. A recorder that ran before crew.Post would have
// told the owner regardless.
func TestARefusedSecondPostTellsNobodyTwice(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	plantAnomalyForTelling(t, h, "A-refused-post", "", 1)
	artID := plantDraftOnAnomalyTask(t, h, "A-refused-post", "investigator-aws", "aws",
		"anomaly.explain", "one cause")

	path := "/artifact/" + strconv.Itoa(artID) + "/post"
	if code, loc := h.post(t, path, url.Values{"csrf": {h.csrf(t, "/board")}}); code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("the first post was refused: %d %s", code, loc)
	}
	if n := countAnomalyExplainedFor(t, h, "A-refused-post"); n != 1 {
		t.Fatalf("after one post: %d anomaly_explained events, want 1", n)
	}

	// Post again. crew.Post refuses ("already posted"): a stamp is not
	// taken back.
	code, loc := h.post(t, path, url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 || !strings.Contains(loc, "msg=") {
		t.Fatalf("re-posting an already-posted artifact was accepted: %d %s", code, loc)
	}
	if n := countAnomalyExplainedFor(t, h, "A-refused-post"); n != 1 {
		t.Errorf("after a REFUSED second post: %d anomaly_explained events, want still 1", n)
	}
}

// Telling the owner is a side effect of reading and emitting, never of
// closing: the anomaly's own state must be exactly what it was before its
// deliverable was posted. Nothing closes without a person.
func TestTellingTheOwnerNeverChangesTheAnomalysOwnState(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	plantAnomalyForTelling(t, h, "A-state-untouched", "", 1)
	artID := plantDraftOnAnomalyTask(t, h, "A-state-untouched", "investigator-aws", "aws",
		"anomaly.explain", "a cause")

	before, err := anomaly.Get(h.st.DB(), "A-state-untouched")
	if err != nil {
		t.Fatal(err)
	}

	path := "/artifact/" + strconv.Itoa(artID) + "/post"
	if code, loc := h.post(t, path, url.Values{"csrf": {h.csrf(t, "/board")}}); code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("posting the deliverable was refused: %d %s", code, loc)
	}

	after, err := anomaly.Get(h.st.DB(), "A-state-untouched")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State {
		t.Errorf("state moved from %q to %q merely because a deliverable was posted", before.State, after.State)
	}
	if after.ClosedAt != "" {
		t.Errorf("closed_at is %q; posting a deliverable must never close an anomaly by itself", after.ClosedAt)
	}
	if after.Reason != "" {
		t.Errorf("reason is %q; nothing was applied, so nothing should have written a reason", after.Reason)
	}
}

// The queue's own reading of what happened: the owner column, and a "told"
// mark that only appears once the event has actually gone out.
func TestTheQueuePageShowsTheOwnerAndWhetherItToldThem(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	plantAnomalyForTelling(t, h, "A-queue-owner", "ml-platform", 1)
	if _, err := h.st.DB().Exec(`INSERT INTO teams(name, owner) VALUES('ml-platform', 't.langley')`); err != nil {
		t.Fatal(err)
	}
	artID := plantDraftOnAnomalyTask(t, h, "A-queue-owner", "investigator-aws", "aws",
		"anomaly.explain", "a cause")

	_, before, _ := h.get(t, "/anomalies")
	if !strings.Contains(before, "t.langley") {
		t.Error("the queue page does not show the anomaly's own owner")
	}
	beforeRow := fragment(before, "A-queue-owner", 800)
	if strings.Contains(beforeRow, "told") {
		t.Error(`the queue page already says "told" before the deliverable was posted`)
	}

	path := "/artifact/" + strconv.Itoa(artID) + "/post"
	if code, loc := h.post(t, path, url.Values{"csrf": {h.csrf(t, "/board")}}); code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("posting the deliverable was refused: %d %s", code, loc)
	}

	_, after, _ := h.get(t, "/anomalies")
	afterRow := fragment(after, "A-queue-owner", 800)
	if !strings.Contains(afterRow, "told") {
		t.Error(`the queue page does not say "told" once the event has gone out`)
	}
}

// The anomaly page's own closure figure: open for N days while open.
func TestTheAnomalyPageSaysHowLongItHasBeenOpen(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	plantAnomalyForTelling(t, h, "A-open-days", "", 3)

	_, body, _ := h.get(t, "/anomalies/A-open-days")
	if !strings.Contains(body, "open for 3 day") {
		t.Errorf("the anomaly page does not say it has been open for 3 days:\n%s",
			fragment(body, "Detected", 300))
	}
}

// And once closed, closed after N days -- the same basis, the other side of
// it.
func TestTheAnomalyPageSaysHowLongItTookToClose(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	plantAnomalyForTelling(t, h, "A-closed-days", "", 5)

	path := "/anomalies/A-closed-days"
	if _, loc := h.post(t, path+"/dismiss", url.Values{
		"reason": {"Known and already fixed"}, "csrf": {h.csrf(t, path)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("dismiss was refused: %s", loc)
	}

	_, body, _ := h.get(t, path)
	if !strings.Contains(body, "closed after 5 day") {
		t.Errorf("the anomaly page does not say it closed after 5 days:\n%s",
			fragment(body, "Closed", 300))
	}
}
