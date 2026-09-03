package web_test

// C8-SPEC.md section 2's fourth and fifth bullets: the pack reaches
// leadership only through a stamp, and the leadership page (the explainers
// page filtered to the leadership audience) then shows it. This file holds
// that end to end, through the console's own read routes; the write side
// (finops.Apply itself) is internal/finops/apply_test.go's own
// TestApplyExplainerPublishPublishesTheArtifactsBodyAsAnExplainer.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// plantExecReporterOption is an exec-reporter's posted pack, ending in one
// open explainer.publish option -- exactly the shape B3's ValidateAndSaveOptions
// already accepts for this role (roles.yaml's own executive-reporter
// hands_up carries explainer.publish), not yet applied.
func plantExecReporterOption(t *testing.T, h *harness, topic, body string) (artifact, ordinal int) {
	t.Helper()
	db := h.st.DB()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('the fortnightly pack', 'write it', 'exec-reporter', 'management', 'active', 0, 0,
		        datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'exec-reporter', 'The executive pack', ?, 'posted', datetime('now'))`,
		taskID, body)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'explainer.publish', ?, 0, 0, '', '', '[]', 'open')`,
		artID, topic); err != nil {
		t.Fatal(err)
	}
	return int(artID), 1
}

// Red first, against main: applySideEffect has no case for explainer.publish
// (finops/apply.go's own comment says so), so applying the option changes
// nothing an /explainers?audience=leadership read could ever see; the route
// itself does not exist either (planning.go's explainers() reads no
// "audience" query parameter). C8-SPEC.md section 4: "applying an
// explainer.publish option publishes the artifact's body as an explainer
// (today recorded only); the leadership page shows it".
func TestTheLeadershipPageShowsTheExecutivePackOnlyAfterAStamp(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	body := "## The fortnight in four numbers\n\nallocation-coverage: 92.3% (was 91.0%, +1.3)\n"
	artID, ordinal := plantExecReporterOption(t, h, "The fortnight in four numbers", body)

	// Before the stamp: nothing on the leadership page.
	_, before, _ := h.get(t, "/explainers?audience=leadership")
	if strings.Contains(before, "The fortnight in four numbers") {
		t.Fatalf("the leadership page shows the pack BEFORE anything applied the option:\n%s", before)
	}

	opt, err := crew.GetOption(h.st.DB(), artID, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	// The supervisor's own pass (finops.Supervise, roles.yaml's own
	// decides_alone for explainer.publish) is what would call this in
	// production; called directly here because the routing itself
	// (internal/finops/supervise_test.go) is not what this scenario is
	// about -- the STAMP is.
	if err := finops.Apply(h.st.DB(), opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}

	status, after, _ := h.get(t, "/explainers?audience=leadership")
	if status != 200 {
		t.Fatalf("GET /explainers?audience=leadership = %d, want 200", status)
	}
	if !strings.Contains(after, "The fortnight in four numbers") {
		t.Fatalf("the leadership page does not show the pack after a stamp applied it:\n%s", after)
	}
	if !strings.Contains(after, "allocation-coverage: 92.3%") {
		t.Errorf("the leadership page does not carry the pack's own four numbers:\n%s", after)
	}

	// And the leadership audience is a FILTER, not merely a query string
	// nothing reads: an ordinary team explainer must not appear on it.
	if _, err := crew.Commission(h.st.DB(), "ml-platform", "Why EC2 moved", "the team", "reporter-aws",
		184000); err != nil {
		t.Fatal(err)
	}
	_, filtered, _ := h.get(t, "/explainers?audience=leadership")
	if strings.Contains(filtered, "Why EC2 moved") {
		t.Errorf("an ordinary team explainer (audience \"the team\") appears on the "+
			"leadership-filtered page:\n%s", filtered)
	}
	// The unfiltered page still shows both.
	_, all, _ := h.get(t, "/explainers")
	for _, want := range []string{"The fortnight in four numbers", "Why EC2 moved"} {
		if !strings.Contains(all, want) {
			t.Errorf("the unfiltered /explainers page is missing %q:\n%s", want, all)
		}
	}
}

// The published pack's own Team is "leadership", not a roster team --
// world.Teams names ten real ones, and "leadership" is none of them.
// explainers.html must not link it to /team/leadership, which
// internal/web/drill.go's team() answers 404 for; a page that renders a
// dead link is a defect on its own, independent of anything C8 asks for.
func TestTheLeadershipPacksTeamIsNotALinkToANonexistentTeamPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	artID, ordinal := plantExecReporterOption(t, h, "The fortnight", "four numbers here")
	opt, err := crew.GetOption(h.st.DB(), artID, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(h.st.DB(), opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/explainers")
	if strings.Contains(body, `href="/team/leadership"`) {
		t.Errorf("the explainers page links \"leadership\" to /team/leadership, which does not exist:\n%s", body)
	}
	if !strings.Contains(body, "leadership") {
		t.Errorf("the explainers page does not name \"leadership\" at all:\n%s", body)
	}
}

// Hostile, C8-SPEC.md section 4: "a script tag in a title rendered as
// text". The title is the option's own summary, exactly the shape
// invariant 27 (CLAUDE.md) already holds for an option's summary field --
// html/template escapes a plain string field by default, and this proves it
// holds for the pack's own topic too, not merely for optionView's.
func TestAScriptTagInThePacksTitleRendersAsTextOnTheLeadershipPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	hostile := `<script>alert(1)</script>`
	artID, ordinal := plantExecReporterOption(t, h, hostile, "four numbers here")
	opt, err := crew.GetOption(h.st.DB(), artID, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(h.st.DB(), opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	// Proves this test's own setup worked BEFORE trusting an absence of
	// "<script>" to mean escaping succeeded: applying the option must have
	// actually created the hostile-titled explainer, or the assertions below
	// would pass just as well with explainer.publish still doing nothing.
	list, err := crew.Explainers(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range list {
		if e.Topic == hostile {
			found = true
		}
	}
	if !found {
		t.Fatalf("no explainer with the hostile topic was created; this test proves nothing "+
			"about escaping if explainer.publish did not actually publish anything: %+v", list)
	}
	_, body, _ := h.get(t, "/explainers")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("a script tag in the pack's title rendered as markup, unescaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the pack's title does not appear escaped as text either:\n%s", body)
	}
}
