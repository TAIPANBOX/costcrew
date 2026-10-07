package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

const testKey = "sekret-test-key"

// request is one thing that reached the stand-in control plane.
type request struct {
	Method, Path, Auth, Body string
}

// plane stands in for TokenFuse's control plane. It is the ONLY host any test
// here is given, it holds unit budgets in micros, and it records every request
// that reaches it, so a test can say what left this process and not only what
// a function returned. Nothing in this file can reach a real host: the only
// address handed to run is this server's.
type plane struct {
	mu       sync.Mutex
	budgets  map[string]int64
	reqs     []request
	failPost map[string]int // unit -> status to answer a POST with
	srv      *httptest.Server
}

func newPlane(t *testing.T, start map[string]int64) *plane {
	t.Helper()
	p := &plane{budgets: map[string]int64{}, failPost: map[string]int{}}
	for k, v := range start {
		p.budgets[k] = v
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.reqs = append(p.reqs, request{r.Method, r.URL.Path, r.Header.Get("Authorization"), body.String()})
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("bad key"))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/unit-budgets":
			_ = json.NewEncoder(w).Encode(p.budgets)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/units/") &&
			strings.HasSuffix(r.URL.Path, "/budget"):
			unit := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/units/"), "/budget")
			if st := p.failPost[unit]; st != 0 {
				w.WriteHeader(st)
				_, _ = w.Write([]byte("refused " + unit))
				return
			}
			var in struct {
				BudgetUSD float64 `json:"budget_usd"`
			}
			_ = json.Unmarshal(body.Bytes(), &in)
			p.budgets[unit] = int64(math.Round(in.BudgetUSD * 1e6))
			_ = json.NewEncoder(w).Encode(map[string]any{"unit": unit})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *plane) requests() []request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]request(nil), p.reqs...)
}

func (p *plane) posts() []request {
	var out []request
	for _, r := range p.requests() {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

func (p *plane) set(unit string, micros int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.budgets[unit] = micros
}

func (p *plane) held(unit string) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.budgets[unit]
	return v, ok
}

// consoleWithBudgets builds a data directory whose budgets table holds exactly
// the given rows (source, team, month, cents), so the expected push is written
// down by hand rather than derived from the code under test.
func consoleWithBudgets(t *testing.T, rows ...[]any) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exec(t, st.DB(), estate.SeedSchema)
	exec(t, st.DB(), estate.BudgetSchema)
	for _, r := range rows {
		exec(t, st.DB(), `INSERT INTO budgets(source, team, month, budget_cents) VALUES (?,?,?,?)`, r...)
	}
	return dir
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func envWith(key string) func(string) string {
	return func(k string) string {
		if k == "TOKENFUSE_KEY" {
			return key
		}
		return ""
	}
}

func invoke(t *testing.T, key string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	code = run("enforce-test", args, envWith(key), &so, &se)
	if strings.Contains(so.String()+se.String(), testKey) {
		t.Errorf("the key was written to the terminal:\nstdout: %s\nstderr: %s", so.String(), se.String())
	}
	return code, so.String(), se.String()
}

var fpRe = regexp.MustCompile(`-apply ([0-9a-f]{12})\b`)

func fingerprintIn(t *testing.T, out string) string {
	t.Helper()
	m := fpRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no -apply <fingerprint> in the output:\n%s", out)
	}
	return m[1]
}

// consoleBudgets: growth is 1000.00 on aws and 500.00 on gcp (1500.00 in all),
// ml-platform 2000.00 on aws, in July; June holds different numbers that must
// not be pushed when July is asked for; a team at 0 is not a budget.
func standardConsole(t *testing.T) string {
	return consoleWithBudgets(t,
		[]any{"aws", "growth", "2026-07", 100000},
		[]any{"gcp", "growth", "2026-07", 50000},
		[]any{"aws", "ml-platform", "2026-07", 200000},
		[]any{"aws", "idle-team", "2026-07", 0},
		[]any{"aws", "growth", "2026-06", 99900},
	)
}

// Without an address AND a key the tool is off: it says so, exits 2, opens no
// store and reaches no host.
func TestItIsOffWithoutAnAddressAndAKey(t *testing.T) {
	p := newPlane(t, nil)
	dir := t.TempDir()

	code, out, errOut := invoke(t, testKey, "-data", dir)
	if code != 2 || out != "" || !strings.Contains(errOut, "enforcement is off: pass -cloud URL and set TOKENFUSE_KEY") {
		t.Errorf("no -cloud: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, errOut = invoke(t, "", "-data", dir, "-cloud", p.srv.URL)
	if code != 2 || out != "" || !strings.Contains(errOut, "enforcement is off") {
		t.Errorf("no key: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, _, _ = invoke(t, "   ", "-data", dir, "-cloud", p.srv.URL)
	if code != 2 {
		t.Errorf("a blank key is not a key: exit %d", code)
	}
	if n := len(p.requests()); n != 0 {
		t.Errorf("%d requests reached the control plane while the tool was off", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.db")); err == nil {
		t.Errorf("a store was opened although the tool was off")
	}
}

// The default run prints the diff and sends NOTHING: the only thing that
// reaches the control plane is the read of what is set now.
func TestTheDefaultRunPrintsTheDiffAndSendsNothing(t *testing.T) {
	p := newPlane(t, map[string]int64{
		"growth": 2_000_000_000, // 2000.00 set now, 1500.00 wanted: a LOWER
		"legacy": 7_000_000,     // not this console's to touch
	})
	dir := standardConsole(t)

	code, out, errOut := invoke(t, testKey, "-data", dir, "-cloud", p.srv.URL, "-period", "2026-07")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"3 team budgets from 2026-07", // growth, ml-platform, idle-team
		"UNIT",
		"SET NOW",
		"WOULD BE",
		"2 to change, 1 of them lower, 1 new, 0 already right.",
		"Nothing was sent. To send exactly this and nothing else:",
		"If anything moves in between, that command refuses",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	growth := lineOf(t, out, "growth")
	for _, want := range []string{"2000.00", "1500.00", "<-- LOWER, the direction that stops work"} {
		if !strings.Contains(growth, want) {
			t.Errorf("growth line lacks %q: %s", want, growth)
		}
	}
	ml := lineOf(t, out, "ml-platform")
	for _, want := range []string{"(none)", "2000.00", "new"} {
		if !strings.Contains(ml, want) {
			t.Errorf("ml-platform line lacks %q: %s", want, ml)
		}
	}
	if strings.Contains(out, "idle-team") {
		t.Errorf("a budget of nothing is a stop, not a budget, and must not be proposed:\n%s", out)
	}
	if !strings.Contains(out, "enforce-test -apply "+fingerprintIn(t, out)) {
		t.Errorf("the apply hint does not name the program:\n%s", out)
	}

	reqs := p.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/unit-budgets" {
		t.Errorf("a dry run must only read the current budgets, got %+v", reqs)
	}
	if reqs[0].Auth != "Bearer "+testKey {
		t.Errorf("the read did not carry the bearer key: %q", reqs[0].Auth)
	}
	if v, _ := p.held("growth"); v != 2_000_000_000 {
		t.Errorf("growth changed to %d by a dry run", v)
	}
}

func lineOf(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no line starting %q in:\n%s", prefix, out)
	return ""
}

// Spend is summed across desks (a team's budget is what it may spend in total,
// and TokenFuse's unit is the team), and only the asked-for month is pushed.
func TestATeamsBudgetIsTheSumAcrossDesksForTheMonthAsked(t *testing.T) {
	p := newPlane(t, nil)
	dir := standardConsole(t)
	_, out, _ := invoke(t, testKey, "-data", dir, "-cloud", p.srv.URL, "-period", "2026-07")
	if g := lineOf(t, out, "growth"); !strings.Contains(g, "1500.00") {
		t.Errorf("growth should be 1000.00 + 500.00 = 1500.00: %s", g)
	}
	_, out, _ = invoke(t, testKey, "-data", dir, "-cloud", p.srv.URL, "-period", "2026-06")
	if g := lineOf(t, out, "growth"); !strings.Contains(g, "999.00") {
		t.Errorf("June's growth is 999.00: %s", g)
	}
	if strings.Contains(out, "ml-platform") {
		t.Errorf("June has no ml-platform budget:\n%s", out)
	}
}

// With no -period the last CLOSED month is used, which for this fixture's
// calendar is July 2026; a budget in the newer open month is not pushed.
func TestTheDefaultPeriodIsTheLastClosedMonth(t *testing.T) {
	p := newPlane(t, nil)
	dir := consoleWithBudgets(t,
		[]any{"aws", "growth", "2026-07", 100000},
		[]any{"aws", "growth", "2026-08", 777700}, // the open month
	)
	code, out, errOut := invoke(t, testKey, "-data", dir, "-cloud", p.srv.URL)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "1 team budgets from 2026-07") {
		t.Errorf("default period is not July:\n%s", out)
	}
	if g := lineOf(t, out, "growth"); !strings.Contains(g, "1000.00") || strings.Contains(g, "7777.00") {
		t.Errorf("wrong month pushed: %s", g)
	}
}

// The two-step: the fingerprint printed by the dry run is what -apply takes,
// and then exactly the printed changes are POSTed, each to its own unit, with
// the key, and the far end ends up holding the new numbers. Units the console
// has no budget for are never touched and nothing is ever deleted.
func TestApplyingThePrintedPlanSendsExactlyThatPlan(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 2_000_000_000, "legacy": 7_000_000})
	dir := standardConsole(t)
	args := []string{"-data", dir, "-cloud", p.srv.URL, "-period", "2026-07"}

	_, dry, _ := invoke(t, testKey, args...)
	fp := fingerprintIn(t, dry)

	code, out, errOut := invoke(t, testKey, append(args, "-apply", fp)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "Set 2 unit budget(s). Gateways poll this every three seconds.") {
		t.Errorf("no confirmation:\n%s", out)
	}
	posts := p.posts()
	if len(posts) != 2 {
		t.Fatalf("%d POSTs, want 2: %+v", len(posts), posts)
	}
	got := map[string]string{}
	for _, r := range posts {
		got[r.Path] = r.Body
		if r.Auth != "Bearer "+testKey {
			t.Errorf("POST %s without the key: %q", r.Path, r.Auth)
		}
	}
	if got["/v1/units/growth/budget"] != `{"budget_usd":1500}` {
		t.Errorf("growth body = %q", got["/v1/units/growth/budget"])
	}
	if got["/v1/units/ml-platform/budget"] != `{"budget_usd":2000}` {
		t.Errorf("ml-platform body = %q", got["/v1/units/ml-platform/budget"])
	}
	if v, _ := p.held("growth"); v != 1_500_000_000 {
		t.Errorf("growth at the far end = %d micros", v)
	}
	if v, _ := p.held("ml-platform"); v != 2_000_000_000 {
		t.Errorf("ml-platform at the far end = %d micros", v)
	}
	if v, ok := p.held("legacy"); !ok || v != 7_000_000 {
		t.Errorf("a unit this console has no budget for was touched: %d, %v", v, ok)
	}
	for _, r := range p.requests() {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s: this tool never deletes or replaces", r.Method, r.Path)
		}
		if strings.Contains(r.Path, "legacy") || strings.Contains(r.Path, "idle-team") {
			t.Errorf("request for a unit that is not this console's to set: %s", r.Path)
		}
	}
}

// A fingerprint that is not the plan's is refused before anything is sent.
func TestAWrongFingerprintSendsNothing(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 2_000_000_000})
	dir := standardConsole(t)
	code, out, errOut := invoke(t, testKey, "-data", dir, "-cloud", p.srv.URL,
		"-period", "2026-07", "-apply", "000000000000")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "enforce: this is not the plan that was approved") {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Contains(out, "Set ") {
		t.Errorf("stdout claims a send: %q", out)
	}
	if n := len(p.posts()); n != 0 {
		t.Errorf("%d POSTs after a refused fingerprint", n)
	}
}

// What the person approved is the diff they saw: if the control plane moves
// between the dry run and the apply, the fingerprint no longer matches and
// nothing is sent.
func TestAPlanThatMovedBetweenTheTwoStepsIsRefused(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 2_000_000_000})
	dir := standardConsole(t)
	args := []string{"-data", dir, "-cloud", p.srv.URL, "-period", "2026-07"}
	_, dry, _ := invoke(t, testKey, args...)
	fp := fingerprintIn(t, dry)

	p.set("growth", 1_700_000_000) // somebody set it by hand in between

	code, _, errOut := invoke(t, testKey, append(args, "-apply", fp)...)
	if code != 1 || !strings.Contains(errOut, "not the plan that was approved") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if n := len(p.posts()); n != 0 {
		t.Errorf("%d POSTs although the remote moved", n)
	}
	if v, _ := p.held("growth"); v != 1_700_000_000 {
		t.Errorf("the hand-set figure was overwritten: %d", v)
	}
}

// When everything already matches the tool says so and exits 0 without a
// fingerprint to apply, and an -apply on it sends nothing.
func TestNothingToChangeSaysSoAndSendsNothing(t *testing.T) {
	p := newPlane(t, map[string]int64{
		"growth":      1_500_000_000,
		"ml-platform": 2_000_000_000,
	})
	dir := standardConsole(t)
	args := []string{"-data", dir, "-cloud", p.srv.URL, "-period", "2026-07"}
	code, out, _ := invoke(t, testKey, args...)
	if code != 0 || !strings.Contains(out, "Nothing to change: 2 already match.") {
		t.Errorf("exit %d, stdout:\n%s", code, out)
	}
	if strings.Contains(out, "-apply") {
		t.Errorf("an empty plan offers an apply:\n%s", out)
	}
	code, out, _ = invoke(t, testKey, append(args, "-apply", "anything")...)
	if code != 0 || !strings.Contains(out, "Nothing to change") {
		t.Errorf("-apply on an empty plan: exit %d, %s", code, out)
	}
	if n := len(p.posts()); n != 0 {
		t.Errorf("%d POSTs for an empty plan", n)
	}
}

// The control plane refusing one unit stops the run there, says where, and
// leaves the units after it unset.
func TestAControlPlaneRefusalStopsTheApplyAndSaysWhere(t *testing.T) {
	p := newPlane(t, nil)
	p.failPost["ml-platform"] = http.StatusForbidden // second unit alphabetically
	dir := consoleWithBudgets(t,
		[]any{"aws", "alpha", "2026-07", 10000},
		[]any{"aws", "ml-platform", "2026-07", 20000},
		[]any{"aws", "zeta", "2026-07", 30000},
	)
	args := []string{"-data", dir, "-cloud", p.srv.URL, "-period", "2026-07"}
	_, dry, _ := invoke(t, testKey, args...)
	code, _, errOut := invoke(t, testKey, append(args, "-apply", fingerprintIn(t, dry))...)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{"setting ml-platform", "403", "refused ml-platform", "(1 of 3 were set before this)"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %s", want, errOut)
		}
	}
	if _, ok := p.held("alpha"); !ok {
		t.Errorf("alpha was before the refusal and should be set")
	}
	if _, ok := p.held("zeta"); ok {
		t.Errorf("zeta came after the refusal and must not be set")
	}
}

// A wrong key is the control plane's refusal, reported with its status; the
// key itself never appears (invoke checks that on every call).
func TestAWrongKeyIsReportedAndNothingIsSet(t *testing.T) {
	p := newPlane(t, nil)
	dir := standardConsole(t)
	code, _, errOut := invoke(t, "wrong-key", "-data", dir, "-cloud", p.srv.URL, "-period", "2026-07")
	if code != 1 || !strings.Contains(errOut, "401") || !strings.HasPrefix(errOut, "enforce: ") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if n := len(p.posts()); n != 0 {
		t.Errorf("%d POSTs with a wrong key", n)
	}
}

// A control plane answering something that is not a unit-to-budget map, and
// one that is not there at all, are failures that name themselves.
func TestAControlPlaneThatAnswersNonsenseOrNotAtAllFails(t *testing.T) {
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `["not","a","map"]`)
	}))
	t.Cleanup(junk.Close)
	dir := standardConsole(t)
	code, _, errOut := invoke(t, testKey, "-data", dir, "-cloud", junk.URL, "-period", "2026-07")
	if code != 1 || !strings.Contains(errOut, "not a unit-to-budget map") {
		t.Errorf("junk: exit %d, stderr %q", code, errOut)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close() // nothing listens there any more
	code, _, errOut = invoke(t, testKey, "-data", dir, "-cloud", url, "-period", "2026-07")
	if code != 1 || !strings.HasPrefix(errOut, "enforce: ") {
		t.Errorf("unreachable: exit %d, stderr %q", code, errOut)
	}
}

// A store that cannot be opened, or one with no budgets table (a console that
// never seeded one), fails before the control plane is asked for anything.
func TestAStoreThatCannotBeReadFailsBeforeTheControlPlaneIsAsked(t *testing.T) {
	p := newPlane(t, nil)
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := invoke(t, testKey, "-data", file, "-cloud", p.srv.URL)
	if code != 1 || !strings.HasPrefix(errOut, "enforce: ") {
		t.Errorf("unopenable store: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = invoke(t, testKey, "-data", t.TempDir(), "-cloud", p.srv.URL)
	if code != 1 || !strings.HasPrefix(errOut, "enforce: ") {
		t.Errorf("no budgets table: exit %d, stderr %q", code, errOut)
	}
	if n := len(p.requests()); n != 0 {
		t.Errorf("%d requests reached the control plane before the store was read", n)
	}
}

func TestFlagErrorsAreUsageErrors(t *testing.T) {
	code, out, errOut := invoke(t, testKey, "-nope")
	if code != 2 || out != "" || !strings.Contains(errOut, "nope") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code, _, _ := invoke(t, testKey, "-h"); code != 0 {
		t.Errorf("-h exited %d", code)
	}
}
