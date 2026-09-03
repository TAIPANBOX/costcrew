package web_test

// B4-STEP-TWO-SPEC.md: POST /sprint/plan/ask and POST /sprint/plan/approve-model
// do not exist yet, so this package does not compile against main --
// server.go already registers the routes and references s.askPlan and
// s.approveModelPlan, which do not exist until planning.go defines them.
//
// Every test here drives the real HTTP surface (a seeded console, a real
// operator session, a real CSRF token) against a fake gateway server, the
// same response shape tools/run/due_test.go's own fakeEngineServer already
// proves out for a real -live call: this is the FIRST place internal/web
// itself spends anything, so there is no existing web-level precedent to
// reuse, only the runner's.

import (
	"fmt"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/history"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// planAnswer is a fake gateway's response text, mutable so a test can seed
// the store, read off what crew.Propose actually produced, and only THEN
// decide what the model should have said -- the response body has to exist
// before startWithGateway wires the server's URL in, but its CONTENT is not
// known until after the store is seeded, since it has to reference a real
// ref from whatever the fixture happened to produce this run.
type planAnswer struct{ body string }

// fakePlanGateway answers every call with a.body, read fresh per request, in
// the same Anthropic-shaped JSON tools/run/due_test.go's own
// fakeEngineServer already proves out.
func fakePlanGateway(t *testing.T, a *planAnswer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		esc := strings.ReplaceAll(strings.ReplaceAll(a.body, `\`, `\\`), `"`, `\"`)
		esc = strings.ReplaceAll(esc, "\n", `\n`)
		fmt.Fprintf(w, `{"content":[{"type":"text","text":"%s"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":40,"output_tokens":20}}`, esc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startWithGateway is start(t) with the console's own TokenFuse gateway
// wired to gatewayURL from the moment the server exists -- duplicated from
// web_test.go's own startFull rather than given a new parameter there,
// because startFull's two existing callers (startWith, startStream) must
// never gain a gateway they did not ask for, and every session established
// against this harness has to be scoped to THIS server's own origin from
// the first request, which ruled out building a plain harness and swapping
// its server in afterwards (cookies are scoped per origin, and the plan-ask
// route is the first thing in this console that can ever spend).
func startWithGateway(t *testing.T, gatewayURL string) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := estate.Seed(st.DB()); err != nil {
		t.Fatal(err)
	}
	if err := connectors.EnsureFocusSchema(st.DB()); err != nil {
		t.Fatal(err)
	}
	if err := estate.SeedBudgets(st.DB()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := anomaly.Run(st.DB(), time.Now(), detect.Default(), nil); err != nil {
		t.Fatal(err)
	}
	var seeds []crew.AnomalySeed
	list, err := anomaly.List(st.DB(), anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		seeds = append(seeds, crew.AnomalySeed{
			ID: a.ID, Source: a.Source, Service: a.Service,
			Day: a.Day, Direction: a.Direction, Excess: a.Excess,
		})
	}
	if _, _, _, err := crew.Seed(st.DB(), seeds); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(st.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SeedRoster(st.DB(), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Seed(st.DB(), st.AsRecorder()); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(web.New(st, au, web.Stack{
		Host: "costcrew.test", Recorder: st.AsRecorder(), Gateway: gatewayURL,
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

var (
	labelRe = regexp.MustCompile(`name="label" value="([^"]*)"`)
	startRe = regexp.MustCompile(`name="start" value="([^"]*)"`)
	endRe   = regexp.MustCompile(`name="end" value="([^"]*)"`)
)

// planFormFields reads the hidden label/start/end fields off the plan page,
// the same fields the existing approve form already carries, so a test asks
// about EXACTLY the sprint the page is showing rather than guessing at
// nextWeek()'s own arithmetic.
func planFormFields(t *testing.T, body string) url.Values {
	t.Helper()
	get := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("could not find a hidden field matching %s in the plan page", re)
		}
		return m[1]
	}
	return url.Values{
		"label": {get(labelRe)}, "start": {get(startRe)},
		"end": {get(endRe)}, "goal": {""},
	}
}

func postBody(t *testing.T, h *harness, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := h.c.PostForm(h.srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<20)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ------------------------------------------------------------------ red first

// A fake model answer that re-routes an item to an active holder of its own
// skill is accepted and shown beside the deterministic plan.
func TestAskPlanAcceptsAValidRerouteAndShowsBothPlans(t *testing.T) {
	answer := &planAnswer{}
	srv := fakePlanGateway(t, answer)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	// Find a routable item (Skill != "") and a second active roster member
	// holding it, from the SEEDED estate itself -- no fixture invented here.
	det, err := crew.Propose(h.st.DB(), form.Get("label"), form.Get("start"), form.Get("end"), "")
	if err != nil {
		t.Fatal(err)
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	ref, altAssignee := -1, ""
	for i, it := range det.Items {
		if it.Skill == "" {
			continue
		}
		for _, a := range roster {
			if a.State != "active" || a.Name == it.Assignee || a.Desk != it.Desk {
				continue
			}
			for _, s := range a.Skills {
				if s == it.Skill {
					ref, altAssignee = i+1, a.Name
				}
			}
			if ref > 0 {
				break
			}
		}
		if ref > 0 {
			break
		}
	}
	if ref < 0 {
		t.Skip("the seeded estate produced no re-routable item with a second active holder of its skill this run")
	}

	answer.body = "Reasoned about the sprint.\n\n```plan\n" +
		fmt.Sprintf(`{"items": [{"ref": %d, "assignee": %q, "budget_cents": 100, "why": "%s has more headroom this week"}]}`,
			ref, altAssignee, altAssignee) + "\n```"

	month := form.Get("start")[:7]
	before, err := crew.SpendInMonth(h.st.DB(), month)
	if err != nil {
		t.Fatal(err)
	}

	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200:\n%s", code, trimTo(body, 2000))
	}
	if !strings.Contains(body, altAssignee) {
		t.Errorf("the model's plan does not show the re-routed assignee %s:\n%s", altAssignee, trimTo(body, 4000))
	}
	if !strings.Contains(body, det.Items[ref-1].Title) {
		t.Errorf("the model's plan does not show item #%d's own title:\n%s", ref, trimTo(body, 4000))
	}
	if !strings.Contains(body, det.Items[ref-1].Assignee) {
		t.Errorf("the deterministic plan's own original assignee is gone from the page:\n%s", trimTo(body, 4000))
	}

	after, err := crew.SpendInMonth(h.st.DB(), month)
	if err != nil {
		t.Fatal(err)
	}
	if after["supervisor"] <= before["supervisor"] {
		t.Errorf("SpendInMonth[supervisor] went from %s to %s, want an increase after a real ask",
			before["supervisor"], after["supervisor"])
	}
}

// An answer that invents an item with no ref is refused whole: the person
// sees what the model wrote and why, never a partially applied plan.
func TestAskPlanShowsARefusedAnswerWhole(t *testing.T) {
	answer := &planAnswer{body: "```plan\n" +
		`{"items": [{"assignee": "supervisor", "budget_cents": 100, "why": "invented"}]}` +
		"\n```"}
	srv := fakePlanGateway(t, answer)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200", code)
	}
	if !strings.Contains(body, "invented") {
		t.Errorf("the refused answer's own reason is not shown: %s", trimTo(body, 4000))
	}
	if !strings.Contains(body, "no valid ref") {
		t.Errorf("the page does not explain why the answer was refused: %s", trimTo(body, 4000))
	}
}

// A script tag in a why is rendered as text, never executable markup.
func TestAskPlanRendersAScriptTagAsText(t *testing.T) {
	answer := &planAnswer{body: "```plan\n" +
		`{"items": [{"ref": 1, "budget_cents": 0, "why": "<script>alert(1)</script>"}]}` +
		"\n```"}
	srv := fakePlanGateway(t, answer)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	det, err := crew.Propose(h.st.DB(), form.Get("label"), form.Get("start"), form.Get("end"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Items) == 0 {
		t.Skip("the seeded estate produced no deterministic item to reference by ref 1")
	}

	_, body := postBody(t, h, "/sprint/plan/ask", form)
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("a script tag from the model's why was rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the escaped script tag is not on the page at all: %s", trimTo(body, 4000))
	}
}

// No gateway configured: refused with one sentence, no call made, nothing
// settled.
func TestAskPlanRefusesWithNoGatewayConfigured(t *testing.T) {
	h := start(t) // start(t), not startWithGateway: no -gateway at all
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	month := form.Get("start")[:7]
	// The seeded estate's own history already attributes real task spend to
	// "supervisor" in most months (proposeReturned and CadenceDue's own
	// headroom-skip branches route to it, desk "management", among other
	// paths), so the baseline is read fresh rather than assumed to be zero:
	// what this test holds is that an ask with no gateway configured adds
	// NOTHING to it, not that nothing was ever there.
	before, err := crew.SpendInMonth(h.st.DB(), month)
	if err != nil {
		t.Fatal(err)
	}

	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask with no gateway = %d, want 200 (a shown refusal, not an error page)", code)
	}
	if !strings.Contains(body, "gateway") {
		t.Errorf("the page does not say a gateway is required: %s", trimTo(body, 4000))
	}
	after, err := crew.SpendInMonth(h.st.DB(), month)
	if err != nil {
		t.Fatal(err)
	}
	if after["supervisor"] != before["supervisor"] {
		t.Errorf("SpendInMonth[supervisor] moved from %s to %s: nothing was ever called",
			before["supervisor"], after["supervisor"])
	}
}

// The worst case above the supervisor's own PerTask refuses BEFORE any call:
// the fake server records whether it was ever hit at all.
func TestAskPlanRefusesBeforeAnyCallWhenWorstExceedsPerTask(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	// Lower the supervisor's own guard below any plausible worst case.
	if _, err := h.st.DB().Exec(`UPDATE analysts SET per_task_cents = 0 WHERE name = 'supervisor'`); err != nil {
		t.Fatal(err)
	}

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200", code)
	}
	if hit {
		t.Error("the gateway was called even though the worst case must have exceeded PerTask 0")
	}
	if !strings.Contains(body, "worst case") && !strings.Contains(body, "per-task") {
		t.Errorf("the page does not explain the pre-call refusal: %s", trimTo(body, 4000))
	}
}

// A viewer, and a request with no CSRF token, are refused the same way every
// other write route already is -- exercised directly here rather than only
// through the generic scans (TestAViewerCannotWrite, TestEveryWriteRouteChecksCSRF
// already cover both new routes by discovering them straight from
// server.go), so this step's own report can quote the exact wording.
func TestAskPlanRefusesAViewerAndAMissingCSRFToken(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.au.Create("looker", "looker-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	v := h.as(t, "looker", "looker-password-2026")

	code, loc := v.post(t, "/sprint/plan/ask", url.Values{"csrf": {v.csrf(t, "/")}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "may+read+and+export") {
		t.Errorf("a viewer asking the supervisor to plan = %d %s, want the role refusal", code, loc)
	}

	code2, loc2 := h.post(t, "/sprint/plan/ask", url.Values{"csrf": {"wrong-token"}})
	if code2 != http.StatusSeeOther || !strings.Contains(loc2, "reload+the+page") {
		t.Errorf("a bad CSRF token on the ask route = %d %s, want the CSRF refusal", code2, loc2)
	}
}

var answerFieldRe = regexp.MustCompile(`name="answer" value="([^"]*)"`)

// The person approves the model's plan through the SAME crew.Approve the
// deterministic one uses: the task that lands on the board carries the
// model's OWN why, not the deterministic item's, and the model's chosen
// (or unchanged) assignee.
func TestApproveModelPlanCreatesATaskWithTheModelsOwnBudget(t *testing.T) {
	answer := &planAnswer{}
	srv := fakePlanGateway(t, answer)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))

	det, err := crew.Propose(h.st.DB(), form.Get("label"), form.Get("start"), form.Get("end"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Items) == 0 || det.Items[0].Budget == 0 {
		t.Skip("the seeded estate produced no item #1 with a nonzero budget to lower")
	}
	answer.body = "```plan\n" +
		`{"items": [{"ref": 1, "budget_cents": 0, "why": "kept the same analyst, spending less"}]}` +
		"\n```"

	_, askBody := postBody(t, h, "/sprint/plan/ask", form)
	m := answerFieldRe.FindStringSubmatch(askBody)
	if m == nil {
		t.Fatalf("the ask response carried no hidden answer field to approve:\n%s", trimTo(askBody, 4000))
	}
	// html/template escapes the attribute (quotes among them, since the
	// answer is itself JSON, all quotes) on the way OUT; a real browser
	// decodes that on the way back IN when the form is submitted, which
	// html.UnescapeString reproduces here -- the regex above only strips the
	// HTML around the value, it does not decode entities inside it.
	answerValue := html.UnescapeString(m[1])

	approveForm := url.Values{
		"csrf": {h.csrf(t, "/sprint/plan")}, "label": {form.Get("label")},
		"start": {form.Get("start")}, "end": {form.Get("end")}, "goal": {form.Get("goal")},
		"answer": {answerValue},
	}
	code, loc := h.post(t, "/sprint/plan/approve-model", approveForm)
	if code != http.StatusSeeOther || loc != "/sprints" {
		t.Fatalf("POST /sprint/plan/approve-model = %d %s, want a redirect to /sprints", code, loc)
	}

	// crew.Approve stores PlanItem.Budget as tasks.budget_cents;
	// modelPlanFrom carries the DETERMINISTIC item's Title and Goal through
	// unchanged but takes Budget from the model's own validated answer --
	// this proves it is the model's 0, not the deterministic item's own
	// (nonzero, per the skip above) budget, that landed on the board.
	var gotBudget, taskCount int64
	if err := h.st.DB().QueryRow(
		`SELECT budget_cents FROM tasks t JOIN sprints s ON s.id = t.sprint WHERE s.label = ? AND t.title = ?`,
		form.Get("label"), det.Items[0].Title,
	).Scan(&gotBudget); err != nil {
		t.Fatalf("reading the approved task back: %v", err)
	}
	if gotBudget != 0 {
		t.Errorf("the approved task's budget_cents = %d, want 0 (the model's own answer, not the "+
			"deterministic item's %s)", gotBudget, det.Items[0].Budget)
	}
	if err := h.st.DB().QueryRow(
		`SELECT COUNT(*) FROM tasks t JOIN sprints s ON s.id = t.sprint WHERE s.label = ?`,
		form.Get("label")).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	// The model's own answer named only ref 1: every other deterministic
	// item was legitimately DROPPED (section 1: "it may ... drop"), so
	// exactly one task lands on the board, not one per deterministic item.
	if taskCount != 1 {
		t.Errorf("tasks created = %d, want 1: the model's answer named only ref 1, "+
			"dropping the other %d deterministic item(s)", taskCount, len(det.Items)-1)
	}

	// A second approve, of the OTHER plan (the deterministic one, now that
	// the label is on the board), must refuse: crew.Approve is unchanged,
	// and "already on the board" is its own existing check.
	code2, loc2 := h.post(t, "/sprint/plan", url.Values{
		"csrf": {h.csrf(t, "/sprint/plan")}, "label": {form.Get("label")},
		"start": {form.Get("start")}, "end": {form.Get("end")}, "goal": {form.Get("goal")},
	})
	if code2 != http.StatusSeeOther || !strings.Contains(loc2, "already") {
		t.Errorf("approving the deterministic plan after the model's own was already approved = %d %s, want an already-on-the-board refusal", code2, loc2)
	}
}
