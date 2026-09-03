package web_test

// C8-LEADERSHIP-SPEC.md's own gate list, section 4. Every test here is red
// first against origin/main: /leadership does not exist there, so each of
// these 404s (proven in the PR body, not asserted here -- a 404 IS the
// route missing, and asserting it explicitly would only prove the route is
// missing, which is not what any of these tests is FOR once the route
// exists).

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// startSingleMonth is the console over a store with exactly one month of
// charges: the same shape internal/finops/executive_test.go's own
// singleMonthDB/estateSchemaOnlyDB build (four schemas plus the live-spend
// ledger migration, which is everything KPIs() reads from without a full
// estate.Seed), wired to a real HTTP server the way startFull does. Needed
// because start(t)'s own harness always seeds a multi-month estate
// (estate.Seed never produces fewer), and the boundary this test proves
// ("no previous period") only exists on the estate's very first period.
func startSingleMonth(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, schema := range []string{estate.SeedSchema, finops.Schema, crew.Schema, anomaly.Schema} {
		if _, err := st.DB().Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureLiveSpendLedger(st.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents)
		VALUES ('aws', '2026-01-15', 'Amazon EC2', 'ml-platform', 'Compute', 10000)`); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(web.New(st, au, web.Stack{
		Host: "costcrew.test", Recorder: st.AsRecorder(),
	}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &harness{srv: srv, au: au, st: st, c: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// plantRealAIAttribution gives agent-attribution a real value for the
// EXISTING executive period, without adding a new month to the estate (a
// row inside a month Months() already lists does not change which period
// Executive() picks -- Months() is DISTINCT substr(day,1,7), so a second
// row in the same month is not a second entry). source='ai',
// provenance NOT NULL is what AttributionCoverage counts at all
// (internal/finops/ai.go); the matching attribution row is what makes the
// charge count as ATTRIBUTED rather than merely present.
func plantRealAIAttribution(t *testing.T, h *harness, period string) {
	t.Helper()
	day := period + "-10"
	if _, err := h.st.DB().Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, provenance)
		VALUES ('ai', ?, 'Claude API', 'ml-platform', 'LLM inference', 4200, 'test-import')`,
		day); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB().Exec(`INSERT INTO attribution
		(source, team, service, day_start, day_end, agent, confidence)
		VALUES ('ai', 'ml-platform', 'Claude API', ?, ?, 'agent://costcrew.test/investigator-aws', 'high')`,
		period+"-01", period+"-28"); err != nil {
		t.Fatal(err)
	}
}

// figureTile scopes an assertion to one tile's own markup, so a "0.0"
// check (or any other) cannot pass by accident on a DIFFERENT tile or on
// the page's static prose. Bounded by whichever comes first: the NEXT
// tile's own id, or the reconciliation paragraph right after the last one
// -- a fixed "to" marker would silently swallow every tile after the one
// asked for, since they all share that same single downstream marker.
func figureTile(t *testing.T, body, id string) string {
	t.Helper()
	from := `id="figure-` + id
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("no tile with %s in the page:\n%s", from, body)
	}
	rest := body[i+len(from):]
	end := strings.Index(rest, `<p class="note">These tiles`)
	if j := strings.Index(rest, `id="figure-`); j >= 0 && (end < 0 || j < end) {
		end = j
	}
	if end < 0 {
		t.Fatalf("could not find the end of the %s tile in the page:\n%s", id, body)
	}
	return rest[:end]
}

// Red first, against main: /leadership does not exist, so GET answers 404
// and neither of the three computable KPIs' values nor either period name
// is anywhere on that page. C8-LEADERSHIP-SPEC.md section 2's third
// bullet.
func TestTheLeadershipPageShowsTheFourFiguresForTheLatestPeriod(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")

	_, period, previous, err := finops.Executive(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if period == "" {
		t.Fatal("the seeded estate reports no period at all; this test needs a real one")
	}
	plantRealAIAttribution(t, h, period)

	figs, period2, previous2, err := finops.Executive(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if period2 != period || previous2 != previous {
		t.Fatalf("adding a charge inside an existing month moved the period: was (%q,%q), now (%q,%q)",
			period, previous, period2, previous2)
	}

	status, body, _ := h.get(t, "/leadership")
	if status != 200 {
		t.Fatalf("GET /leadership = %d, want 200", status)
	}
	if !strings.Contains(body, period) {
		t.Errorf("the leadership page does not name its own period %q:\n%s", period, body)
	}
	if previous != "" && !strings.Contains(body, previous) {
		t.Errorf("the leadership page does not name the previous period %q:\n%s", previous, body)
	}

	for _, id := range []string{"allocation-coverage", "unallocated-share", "agent-attribution"} {
		var f finops.ExecutiveFigure
		found := false
		for _, cand := range figs {
			if cand.ID == id {
				f, found = cand, true
			}
		}
		if !found {
			t.Fatalf("Executive() named no figure %q", id)
		}
		if !f.HasVal {
			t.Fatalf("%s has no value on this fixture; this test cannot prove the page shows "+
				"a real number for it (Blocked: %q)", id, f.Blocked)
		}
		want := fmt.Sprintf("%.1f", f.Numeric)
		tile := figureTile(t, body, id)
		if !strings.Contains(tile, want) {
			t.Errorf("the %s tile does not carry its own value %q:\n%s", id, want, tile)
		}
	}

	// invariant 13, figures reconcile: the page names the two as different
	// questions, in one sentence under the tiles (C8-LEADERSHIP-SPEC.md
	// section 2, the paragraph after the tile list).
	if !strings.Contains(body, "the numbers now") || !strings.Contains(body, "what the analyst said then") {
		t.Errorf("the leadership page does not distinguish the tiles from the packs in its own "+
			"words (\"the numbers now\" / \"what the analyst said then\"):\n%s", body)
	}
}

// Red first, against main: /leadership 404s. cost-per-outcome refuses on
// any estate with no AI import (internal/finops/kpi.go's own
// executiveKPIIDs comment), which the seeded estate alone is, so this
// needs no special fixture -- C8-LEADERSHIP-SPEC.md section 4's own words.
func TestTheLeadershipPageShowsARefusedKPIAsRefusedNeverZero(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")

	figs, _, _, err := finops.Executive(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	var cpo finops.ExecutiveFigure
	for _, f := range figs {
		if f.ID == "cost-per-outcome" {
			cpo = f
		}
	}
	if cpo.Blocked == "" {
		t.Fatal("cost-per-outcome is not refused on the seeded estate before any AI import; " +
			"this test cannot prove anything about a refusal that is not there")
	}

	status, body, _ := h.get(t, "/leadership")
	if status != 200 {
		t.Fatalf("GET /leadership = %d, want 200", status)
	}
	tile := figureTile(t, body, "cost-per-outcome")
	if !strings.Contains(tile, cpo.Blocked) {
		t.Errorf("the cost-per-outcome tile does not carry its own refusal %q:\n%s", cpo.Blocked, tile)
	}
	if strings.Contains(tile, "0.0") {
		t.Errorf("the cost-per-outcome tile's own HTML contains \"0.0\": a refused KPI must "+
			"never read as a real reading of zero:\n%s", tile)
	}
}

// Red first, against main: /leadership 404s. C8-LEADERSHIP-SPEC.md section
// 2's second bullet, the boundary internal/finops's own
// TestExecutiveSaysNoPreviousPeriodForTheFirstPeriod already builds at the
// data layer -- this proves the same boundary reaches the rendered page.
func TestTheLeadershipPageSaysNoPreviousPeriodForTheFirstPeriod(t *testing.T) {
	h := startSingleMonth(t)
	h.signUp(t, "owner1", "owner-password-2026")

	status, body, _ := h.get(t, "/leadership")
	if status != 200 {
		t.Fatalf("GET /leadership = %d, want 200", status)
	}
	if !strings.Contains(body, "Period 2026-01; no previous period.") {
		t.Errorf("the leadership page does not say \"no previous period\" on the estate's very "+
			"first period:\n%s", body)
	}
	// The OTHER branch of the period line (", beside <month>.") must not
	// ALSO have fired: scoped to "Period 2026-01" specifically, not to the
	// word "beside" alone, which the page's own static intro sentence uses
	// in an unrelated description that has nothing to do with this estate's
	// actual previous period.
	if strings.Contains(body, "Period 2026-01, beside") {
		t.Errorf("the leadership page names a previous period on an estate that has none:\n%s", body)
	}
}

// Red first, against main: /leadership 404s. C8-LEADERSHIP-SPEC.md section
// 2's fourth bullet: published leadership packs only, newest first. The
// two published leadership packs are given explicit, DISAGREEING published
// and id order (the earlier-created one is stamped LATER) so a sort that
// silently used id or insertion order instead of Published would fail
// this the same way a sort that used Published correctly would pass a
// same-second pair by luck.
func TestTheLeadershipPageListsOnlyPublishedLeadershipPacks(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	db := h.st.DB()

	draftID, err := crew.Commission(db, "ml-platform", "A draft leadership pack", "leadership",
		"exec-reporter", 12300)
	if err != nil {
		t.Fatal(err)
	}
	_ = draftID // never published: stays a draft

	teamID, err := crew.Commission(db, "ml-platform", "An ordinary team explainer", "the team",
		"reporter-aws", 45600)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.Publish(db, teamID, "owner1"); err != nil {
		t.Fatal(err)
	}

	olderID, err := crew.Commission(db, "ml-platform", "First leadership pack", "leadership",
		"exec-reporter", 78900)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.Publish(db, olderID, "owner1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE explainers SET published=? WHERE id=?`,
		"2026-09-03T23:00:00Z", olderID); err != nil {
		t.Fatal(err)
	}

	newerID, err := crew.Commission(db, "ml-platform", "Second leadership pack", "leadership",
		"exec-reporter", 11100)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.Publish(db, newerID, "owner1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE explainers SET published=? WHERE id=?`,
		"2026-09-01T08:00:00Z", newerID); err != nil {
		t.Fatal(err)
	}

	status, body, _ := h.get(t, "/leadership")
	if status != 200 {
		t.Fatalf("GET /leadership = %d, want 200", status)
	}
	if strings.Contains(body, "A draft leadership pack") {
		t.Errorf("the leadership page shows a DRAFT leadership pack:\n%s", body)
	}
	if strings.Contains(body, "An ordinary team explainer") {
		t.Errorf("the leadership page shows a published TEAM explainer (audience \"the team\"):\n%s", body)
	}
	if !strings.Contains(body, "First leadership pack") {
		t.Errorf("the leadership page is missing the first published leadership pack:\n%s", body)
	}
	if !strings.Contains(body, "Second leadership pack") {
		t.Errorf("the leadership page is missing the second published leadership pack:\n%s", body)
	}
	// Newest PUBLISHED first: "First leadership pack" carries the later
	// published timestamp (2026-09-03) even though it has the lower id and
	// was created first, so it must render before "Second leadership pack"
	// (published 2026-09-01).
	iFirst := strings.Index(body, "First leadership pack")
	iSecond := strings.Index(body, "Second leadership pack")
	if iFirst < 0 || iSecond < 0 || iFirst > iSecond {
		t.Errorf("the leadership page does not list packs newest published first: "+
			"\"First leadership pack\" at %d, \"Second leadership pack\" at %d", iFirst, iSecond)
	}
}

// Red first, against main: /leadership 404s. C8-LEADERSHIP-SPEC.md section
// 2's fourth bullet, last sentence: "This page has no form, no CSRF field,
// no button: a leader reads; a viewer sees exactly what an operator
// sees." Scoped to the page's own content, not the whole document: every
// page's shared layout carries a hidden logout form in <nav>, which is
// chrome, not a control this page offers.
//
// A published leadership pack is planted FIRST, not an empty page: a
// control hidden inside the per-pack loop (the shape /explainers' own
// publish/return forms take, one per row) would render only once a pack
// exists, and a check against an empty page cannot catch a control that
// only ever appears alongside a row. Found by hand while planting this
// invariant's own gates-have-teeth.sh mutant: a form added INSIDE the
// {{range .Packs}} block passed this test outright the first time, since
// the fixture it ran against had no packs for the loop to ever render.
func TestTheLeadershipPageHasNoControlsEvenForAnOperator(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	id, err := crew.Commission(h.st.DB(), "ml-platform", "A published leadership pack",
		"leadership", "exec-reporter", 9900)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.Publish(h.st.DB(), id, "owner1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := h.au.Create("hand", "hand-password-2026", "operator"); err != nil || !ok {
		t.Fatalf("creating an operator: %v %v", ok, err)
	}
	op := h.as(t, "hand", "hand-password-2026")

	status, body, _ := op.get(t, "/leadership")
	if status != 200 {
		t.Fatalf("GET /leadership = %d, want 200", status)
	}
	content := between(t, body, `id="content"`, "</main>")
	for _, bad := range []string{"<form", "csrf", "<button"} {
		if strings.Contains(content, bad) {
			t.Errorf("the leadership page's own content carries %q, for an operator who can "+
				"act everywhere else in this console:\n%s", bad, content)
		}
	}
}

// Red first, against main: /leadership 404s. C8-LEADERSHIP-SPEC.md section
// 2's fourth bullet: body via renderBody, "the same renderer /explainers
// uses, so a <script> in a topic or body is text" -- this is the TOPIC
// half; renderBody's own escaping of the body is already
// internal/web/renderbody_test.go's job, not this page's to re-prove.
func TestAScriptTagInAPacksTopicRendersAsTextOnTheLeadershipPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")
	hostile := `<script>alert(1)</script>`
	id, err := crew.Commission(h.st.DB(), "ml-platform", hostile, "leadership", "exec-reporter", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.Publish(h.st.DB(), id, "owner1"); err != nil {
		t.Fatal(err)
	}
	// Proves the setup actually created the hostile-titled, published,
	// leadership-audience pack, so an absence of "<script>" below cannot
	// pass merely because nothing was ever rendered.
	list, err := crew.Explainers(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range list {
		if e.Topic == hostile && e.Audience == "leadership" && e.State == "published" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no published leadership explainer with the hostile topic exists; this test "+
			"proves nothing about escaping: %+v", list)
	}

	_, body, _ := h.get(t, "/leadership")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("a script tag in a pack's topic rendered as markup, unescaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the pack's topic does not appear escaped as text either:\n%s", body)
	}
}

// Red first, against main: neither page names /leadership at all, since
// the route and its links do not exist yet. C8-LEADERSHIP-SPEC.md section
// 2's entry-point bullets, both sentences copied verbatim.
func TestTheKPIsAndExplainersPagesLinkToTheLeadershipPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner1", "owner-password-2026")

	_, kpisBody, _ := h.get(t, "/kpis")
	if !strings.Contains(kpisBody, `href="/leadership"`) {
		t.Errorf("the KPIs page does not link to /leadership:\n%s", kpisBody)
	}
	if !strings.Contains(kpisBody, "The four figures a leader is owed each period, beside the "+
		"pack written about them:") {
		t.Errorf("the KPIs page's own link sentence is missing or reworded:\n%s", kpisBody)
	}

	// Always shown, not only when a pack exists, so a fresh installation
	// with nothing published yet can still find the page.
	_, explainersBody, _ := h.get(t, "/explainers")
	if !strings.Contains(explainersBody, `href="/leadership"`) {
		t.Errorf("the Explainers page does not link to /leadership:\n%s", explainersBody)
	}
	if !strings.Contains(explainersBody, "Packs written for leadership are on their own page:") {
		t.Errorf("the Explainers page's own link sentence is missing or reworded:\n%s", explainersBody)
	}
}
