package web

// Direct tests of the package's own helpers: the small pure functions every
// page leans on, and the few SQL helpers whose error and truncation branches
// no page request reaches. Each assertion names a value the code promises, so
// a change to that value fails here before it fails a page.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// covServer is the console's own Server over an empty store, for the helpers
// that read the database or the server's own fields.
func covServer(t *testing.T, eventsPath string) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, au, Stack{EventsPath: eventsPath}), st
}

// covCharges is an empty charges table with the columns the SQL helpers in
// pages.go and drill.go read. The production table has more columns and NOT
// NULL on billed_cents, which is why a NULL sum cannot reach these helpers.
func covCharges(t *testing.T) *Server {
	t.Helper()
	s, st := covServer(t, "")
	if _, err := st.DB().Exec(`CREATE TABLE charges(
		source TEXT NOT NULL, day TEXT NOT NULL, service TEXT NOT NULL,
		billed_cents INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCovRoleWeightRanksEachRoleAndRefusesTheUnknown(t *testing.T) {
	for role, want := range map[string]int{"admin": 3, "operator": 2, "viewer": 1, "": 0, "root": 0} {
		if got := roleWeight(role); got != want {
			t.Errorf("roleWeight(%q) = %d, want %d", role, got, want)
		}
	}
}

func TestCovHTMLEscapeNeutralisesEveryMarkupCharacter(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<a href="x">&`, `&lt;a href=&quot;x&quot;&gt;&amp;`},
		{"plain text", "plain text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := htmlEscape(c.in); got != c.want {
			t.Errorf("htmlEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCovComparatorsReturnTheSignOfTheDifference(t *testing.T) {
	if cmpInt(1, 2) != -1 || cmpInt(2, 1) != 1 || cmpInt(4, 4) != 0 {
		t.Errorf("cmpInt: got %d %d %d, want -1 1 0", cmpInt(1, 2), cmpInt(2, 1), cmpInt(4, 4))
	}
	if cmpFloat(0.5, 1.5) != -1 || cmpFloat(1.5, 0.5) != 1 || cmpFloat(2, 2) != 0 {
		t.Errorf("cmpFloat: got %d %d %d, want -1 1 0",
			cmpFloat(0.5, 1.5), cmpFloat(1.5, 0.5), cmpFloat(2, 2))
	}
	if absf(-12.5) != 12.5 || absf(12.5) != 12.5 || absf(0) != 0 {
		t.Errorf("absf: a -12.5 missed by the same size as +12.5 must read as equal, got %v %v %v",
			absf(-12.5), absf(12.5), absf(0))
	}
}

func TestCovSortSpecNamesItsParameters(t *testing.T) {
	if k := (sortSpec{}).key(); k != "sort" {
		t.Errorf("default key = %q, want sort", k)
	}
	if k := (sortSpec{param: "hsort"}).key(); k != "hsort" {
		t.Errorf("key with param hsort = %q", k)
	}
	if d := (sortSpec{}).dirKey(); d != "dir" {
		t.Errorf("default dirKey = %q, want dir", d)
	}
	if d := (sortSpec{param: "hsort"}).dirKey(); d != "hsortdir" {
		t.Errorf("dirKey with param hsort = %q, want hsortdir", d)
	}
}

// An unknown column must fall back to the default column, or to no sort at
// all when the default is unknown too. Leaving the rows in whatever order the
// database returned is the bug the comment in applySort names.
func TestCovApplySortFallsBackToTheDefaultColumn(t *testing.T) {
	byNum := map[string]func(a, b int) int{"num": cmpInt}

	rows := []int{3, 1, 2}
	applySort(rows, sortSpec{Col: "bogus"}, byNum, "num")
	if !slices.Equal(rows, []int{1, 2, 3}) {
		t.Errorf("unknown column fell back to the default and got %v, want [1 2 3]", rows)
	}

	rows = []int{3, 1, 2}
	applySort(rows, sortSpec{Col: "bogus"}, byNum, "nowhere")
	if !slices.Equal(rows, []int{3, 1, 2}) {
		t.Errorf("unknown column and unknown default reordered the rows to %v", rows)
	}

	rows = []int{3, 1, 2}
	applySort(rows, sortSpec{Col: "num", Desc: true}, byNum, "num")
	if !slices.Equal(rows, []int{3, 2, 1}) {
		t.Errorf("descending sort got %v, want [3 2 1]", rows)
	}
}

func TestCovHeadingNeedsOneLineAndASpaceAfterTheHashes(t *testing.T) {
	cases := []struct {
		in, text string
		level    int
	}{
		{"# Title", "Title", 1},
		{"### Deep  ", "Deep", 3},
		{"#Title", "", 0},
		{"####### seven", "", 0},
		{"#", "", 0},
		{"# Title\nbody", "", 0},
	}
	for _, c := range cases {
		text, level := heading(c.in)
		if text != c.text || level != c.level {
			t.Errorf("heading(%q) = (%q, %d), want (%q, %d)", c.in, text, level, c.text, c.level)
		}
	}
}

func TestCovBulletsTakeAListOnlyWhenEveryLineIsAnItem(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"- one\n* two", []string{"one", "two"}},
		{"1. one\n2) two", []string{"one", "two"}},
		{"- one\n\n- two", []string{"one", "two"}},
		{"- one\nprose in the middle", nil},
		{"just prose", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := bullets(c.in); !slices.Equal(got, c.want) {
			t.Errorf("bullets(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCovDriverWindowNeedsADriverClassAndBothDates(t *testing.T) {
	good := json.RawMessage(`{"start":"2026-09-01","end":"2026-09-30"}`)
	cases := []struct {
		name string
		o    crew.Option
		want string
	}{
		{"a recurring driver with its window", crew.Option{Class: "driver.recurring", Target: good}, "2026-09-01 to 2026-09-30"},
		{"a one-time driver with its window", crew.Option{Class: "driver.one-time", Target: good}, "2026-09-01 to 2026-09-30"},
		{"a class that is not a driver", crew.Option{Class: "forecast.project", Target: good}, ""},
		{"a driver with no target", crew.Option{Class: "driver.recurring"}, ""},
		{"a target that is not JSON", crew.Option{Class: "driver.recurring", Target: json.RawMessage(`{"start":`)}, ""},
		{"a window with no end", crew.Option{Class: "driver.recurring",
			Target: json.RawMessage(`{"start":"2026-09-01"}`)}, ""},
	}
	for _, c := range cases {
		if got := driverWindow(c.o); got != c.want {
			t.Errorf("%s: driverWindow = %q, want %q", c.name, got, c.want)
		}
	}
}

// The refusal sentence names the reason the call did not go out, which is the
// thing the planner needs to show. Four reasons, four sentences.
func TestCovUnpricedPlanAskNamesWhyTheCallWasRefused(t *testing.T) {
	cases := []struct {
		engine, model, wants string
	}{
		{"local", "m", "costcrew-run"},
		{"no-such-engine", "m", "is not an engine this console knows"},
		{"claude-cli", "m", "is not billed per token"},
		{"anthropic", "no-such-model", `has no price recorded for model "no-such-model"`},
	}
	for _, c := range cases {
		got := unpricedPlanAsk(c.engine, c.model)
		if !strings.Contains(got, c.wants) || !strings.Contains(got, "refuse") {
			t.Errorf("unpricedPlanAsk(%q, %q) = %q, want it to contain %q and say the call is refused",
				c.engine, c.model, got, c.wants)
		}
	}
}

// With no sprint dated, the week planned is the first Monday after the
// fixture's last day, and it runs seven days.
func TestCovNextWeekIsTheMondayAfterTheFixtureWhenNoSprintIsDated(t *testing.T) {
	s, _ := covServer(t, "")
	label, start, end := nextWeek(s)
	last, _ := time.Parse("2006-01-02", world.LastDay)
	first, err := time.Parse("2006-01-02", start)
	if err != nil {
		t.Fatalf("nextWeek start %q does not parse: %v", start, err)
	}
	if first.Weekday() != time.Monday {
		t.Errorf("the planned week starts on a %s, want a Monday", first.Weekday())
	}
	if gap := first.Sub(last); gap < 24*time.Hour || gap > 7*24*time.Hour {
		t.Errorf("the planned week starts %v after the last day %s, want between one and seven days",
			gap, world.LastDay)
	}
	stop, err := time.Parse("2006-01-02", end)
	if err != nil || stop.Sub(first) != 6*24*time.Hour {
		t.Errorf("the planned week runs %s to %s, want six days after the Monday", start, end)
	}
	if !strings.HasPrefix(label, "2026-W") {
		t.Errorf("label = %q, want an ISO week of 2026", label)
	}
}

func TestCovPadAndWeekLabelsAreTwoDigits(t *testing.T) {
	if pad2(3) != "03" || pad2(10) != "10" || pad2(42) != "42" {
		t.Errorf("pad2: got %q %q %q, want 03 10 42", pad2(3), pad2(10), pad2(42))
	}
	if got := strconvWeek(2026, 7); got != "2026-W07" {
		t.Errorf("strconvWeek(2026, 7) = %q, want 2026-W07", got)
	}
}

func TestCovEventWhenTrimsTheStreamTimestampToTheMinute(t *testing.T) {
	if got := (eventRow{when: "2026-10-14T09:31:22.123Z"}).When(); got != "2026-10-14 09:31" {
		t.Errorf("When = %q, want 2026-10-14 09:31", got)
	}
	if got := (eventRow{when: "2026-10-14"}).When(); got != "2026-10-14" {
		t.Errorf("a short timestamp must be shown whole, got %q", got)
	}
}

func TestCovClassRefsKeepOnlyTheClassesThatExist(t *testing.T) {
	got := classRefs([]string{"no.such.class", "anomaly.explain"})
	if len(got) != 1 || got[0].ID != "anomaly.explain" {
		t.Errorf("classRefs kept %+v, want only anomaly.explain", got)
	}
}

func TestCovWordInMatchesWholeItemsOnly(t *testing.T) {
	cases := []struct {
		s, name string
		want    bool
	}{
		{"q", "q", true},
		{"a, b", "b", true},
		{"x/y;z", "y", true},
		{"ab", "a", false},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := wordIn(c.s, c.name); got != c.want {
			t.Errorf("wordIn(%q, %q) = %v, want %v", c.s, c.name, got, c.want)
		}
	}
}

// Only the owner's own stamp, or an admin's, may answer for an option. Nobody
// else, and nobody signed out.
func TestCovMayAnswerForIsTheOwnersStampOrAnAdmins(t *testing.T) {
	admin := &auth.User{Username: "root", Role: "admin"}
	owner := &auth.User{Username: "ana", Role: "operator"}
	viewer := &auth.User{Username: "ana", Role: "viewer"}
	cases := []struct {
		name string
		u    *auth.User
		want bool
	}{
		{"nobody signed in", nil, false},
		{"an admin, for any owner", admin, true},
		{"the owner, as operator", owner, true},
		{"another operator", &auth.User{Username: "bo", Role: "operator"}, false},
		{"the owner, but only a viewer", viewer, false},
	}
	for _, c := range cases {
		if got := mayAnswerFor(c.u, "ana"); got != c.want {
			t.Errorf("mayAnswerFor, %s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCovMayManageIsTheOwnerOrAnAdmin(t *testing.T) {
	a := crew.Analyst{Name: "triage-aws", Owner: "ana"}
	cases := []struct {
		name string
		u    *auth.User
		want bool
	}{
		{"nobody signed in", nil, false},
		{"an admin", &auth.User{Username: "root", Role: "admin"}, true},
		{"the owner, as operator", &auth.User{Username: "ana", Role: "operator"}, true},
		{"another operator", &auth.User{Username: "bo", Role: "operator"}, false},
		{"the owner, as viewer", &auth.User{Username: "ana", Role: "viewer"}, false},
	}
	for _, c := range cases {
		if got := mayManage(c.u, a); got != c.want {
			t.Errorf("mayManage, %s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// An artifact id that names nothing is ErrNotFound, not an empty deliverable.
func TestCovGetArtifactRefusesAnIdThatNamesNothing(t *testing.T) {
	s, st := covServer(t, "")
	if _, err := st.DB().Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := getArtifact(s.db, 999999); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("getArtifact of a missing id: err = %v, want crew.ErrNotFound", err)
	}
}

func TestCovSummariseSortsTheKeysAndShortensLongText(t *testing.T) {
	if got := summarise(nil); got != "" {
		t.Errorf("summarise(nil) = %q, want empty", got)
	}
	if got := summarise(map[string]any{"b": 1, "a": "x"}); got != "a x · b 1" {
		t.Errorf("summarise = %q, want keys in order: %q", got, "a x · b 1")
	}
	long := strings.Repeat("y", 80)
	want := "k " + strings.Repeat("y", 57) + "…"
	if got := summarise(map[string]any{"k": long}); got != want {
		t.Errorf("a value over 60 bytes must be cut to 57 and an ellipsis, got %q", got)
	}
}

// The stream count is the lines that parse, blank and broken lines left out,
// and zero when there is no stream to read.
func TestCovEmittedCountsOnlyLinesThatParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.ndjson")
	body := `{"type":"a"}` + "\n\n" + "not json\n" + `{"type":"b"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := covServer(t, p)
	if n := s.emitted(); n != 2 {
		t.Errorf("emitted = %d, want 2 (the two lines that parse)", n)
	}
	none, _ := covServer(t, "")
	if n := none.emitted(); n != 0 {
		t.Errorf("with no stream configured, emitted = %d, want 0", n)
	}
	gone, _ := covServer(t, filepath.Join(filepath.Dir(p), "missing.ndjson"))
	if n := gone.emitted(); n != 0 {
		t.Errorf("with the stream file missing, emitted = %d, want 0", n)
	}
}

func TestCovDaysTextSaysWhenADayCountCannotBeTrusted(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cases := []struct {
		name string
		a    anomaly.Anomaly
		want string
	}{
		{"open for three days", anomaly.Anomaly{DetectedAt: rfc(-72 * time.Hour)}, "open for 3 days"},
		{"closed after two days", anomaly.Anomaly{DetectedAt: rfc(-48 * time.Hour), ClosedAt: rfc(0)},
			"closed after 2 days"},
		{"open with a date that does not parse", anomaly.Anomaly{DetectedAt: "yesterday"},
			"open, but detected_at does not parse, so no day count can be trusted"},
		{"closed with a date that does not parse", anomaly.Anomaly{DetectedAt: "yesterday", ClosedAt: rfc(0)},
			"closed, but detected_at does not parse, so no day count can be trusted"},
	}
	for _, c := range cases {
		if got := daysText(c.a, now); got != c.want {
			t.Errorf("%s: daysText = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCovParseCadenceWhyReadsTheWordAndTheLastPost(t *testing.T) {
	if w, last := parseCadenceWhy("nothing to read here"); w != "" || last != "" {
		t.Errorf("a sentence without the cadence marker gave (%q, %q), want empty", w, last)
	}
	w, last := parseCadenceWhy("weekly cadence, last posted 2026-10-01; rest of it")
	if w != "weekly" || last != "2026-10-01" {
		t.Errorf("parseCadenceWhy = (%q, %q), want (weekly, 2026-10-01)", w, last)
	}
}

func TestCovInt64FieldReadsEveryNumberShapeAndNothingElse(t *testing.T) {
	data := map[string]any{"f": float64(3), "i": 4, "j": int64(5), "s": "6"}
	for key, want := range map[string]int64{"f": 3, "i": 4, "j": 5, "s": 0, "missing": 0} {
		if got := int64Field(data, key); got != want {
			t.Errorf("int64Field(%q) = %d, want %d", key, got, want)
		}
	}
}

// A malformed disposition type comes back bare rather than as an empty header,
// so the download is still named by the server and never left unguarded.
func TestCovSetDispositionFallsBackToTheBareValue(t *testing.T) {
	rec := httptest.NewRecorder()
	setDisposition(rec, "attachment", "budgets.csv")
	if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename=budgets.csv" {
		t.Errorf("Content-Disposition = %q", got)
	}
	bad := httptest.NewRecorder()
	setDisposition(bad, "bad type", "x.csv")
	if got := bad.Header().Get("Content-Disposition"); got != "bad type" {
		t.Errorf("a malformed type must fall back to the bare value, got %q", got)
	}
}

func TestCovReadUploadTakesThePastedRowsAndRefusesTheRest(t *testing.T) {
	form := func(v url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/intake/check", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	body, err := readUpload(form(url.Values{"pasted": {"  team,aws,2026-09,100\n"}}))
	if err != nil || string(body) != "team,aws,2026-09,100" {
		t.Errorf("pasted rows: got %q, %v; want the trimmed rows", body, err)
	}
	if _, err := readUpload(form(url.Values{"pasted": {strings.Repeat("x", maxIntake+1)}})); err != errTooBig {
		t.Errorf("pasted rows over the cap: err = %v, want errTooBig", err)
	}
	if _, err := readUpload(form(url.Values{})); err != errNoFile {
		t.Errorf("no file and nothing pasted: err = %v, want errNoFile", err)
	}
}

func TestCovBodyLimitIsSetPerRoute(t *testing.T) {
	if got := bodyLimit("/intake/check"); got != maxIntake+multipartOverhead {
		t.Errorf("/intake/check cap = %d", got)
	}
	if got := bodyLimit("/intake/apply"); got != 6*maxIntake+multipartOverhead {
		t.Errorf("/intake/apply cap = %d", got)
	}
	if got := bodyLimit("/anywhere/else"); got != maxBody {
		t.Errorf("the default cap = %d, want %d", got, maxBody)
	}
}

// A body whose length is not declared (chunked) is read up to its cap. One
// under the cap reaches the handler whole; one over it is 413 and never read
// past the cap.
func TestCovLimitBodyCountsAChunkedBodyAgainstItsCap(t *testing.T) {
	small := httptest.NewRequest(http.MethodPost, "/x", nil)
	small.Body = io.NopCloser(strings.NewReader("hello"))
	small.ContentLength = -1
	if !limitBody(httptest.NewRecorder(), small) {
		t.Fatal("a small chunked body was refused")
	}
	got, _ := io.ReadAll(small.Body)
	if string(got) != "hello" || small.ContentLength != 5 {
		t.Errorf("the handler reads %q with length %d, want hello with length 5", got, small.ContentLength)
	}

	big := httptest.NewRequest(http.MethodPost, "/x", nil)
	big.Body = io.NopCloser(strings.NewReader(strings.Repeat("a", maxBody+1)))
	big.ContentLength = -1
	rec := httptest.NewRecorder()
	if limitBody(rec, big) {
		t.Fatal("a chunked body over the cap was let through")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

type covFailingBody struct{}

func (covFailingBody) Read([]byte) (int, error) { return 0, errors.New("the client hung up") }
func (covFailingBody) Close() error             { return nil }

// A body that cannot be read is a 400 that says so, and no handler runs.
func TestCovLimitBodyAnswersAReadFailureWithBadRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Body = covFailingBody{}
	r.ContentLength = -1
	rec := httptest.NewRecorder()
	if limitBody(rec, r) {
		t.Fatal("a body that failed to read was let through")
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "could not be read") {
		t.Errorf("status %d, body %q: want 400 saying the request could not be read", rec.Code, rec.Body.String())
	}
}

// Who has an open decision on a sprint, each owner once, in name order. A
// sprint with none, and a store with no decisions table, both have nobody.
func TestCovDecisionOwnersListsEachOwnerOnceAndNobodyWhenUnreadable(t *testing.T) {
	s, st := covServer(t, "")
	if _, err := st.DB().Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if got := decisionOwners(s, 7); len(got) != 0 {
		t.Errorf("a sprint with no decision requests has owners %v", got)
	}
	for _, row := range []struct {
		artifact int
		owner    string
	}{{1, "bo"}, {2, "ana"}, {3, "bo"}} {
		if _, err := st.DB().Exec(
			`INSERT INTO decision_requests(artifact, sprint, owner) VALUES (?, 7, ?)`,
			row.artifact, row.owner); err != nil {
			t.Fatal(err)
		}
	}
	if got := decisionOwners(s, 7); !slices.Equal(got, []string{"ana", "bo"}) {
		t.Errorf("owners = %v, want [ana bo]: each once, in name order", got)
	}
	if _, err := st.DB().Exec(`DROP TABLE decision_requests`); err != nil {
		t.Fatal(err)
	}
	if got := decisionOwners(s, 7); got != nil {
		t.Errorf("an unreadable decisions table names owners %v, want none", got)
	}
}

// A filter that does not parse is an error, not a month with some rows missing.
func TestCovMonthlyReportsAFilterItCannotRun(t *testing.T) {
	s := covCharges(t)
	if _, err := monthly(s.db, "1 = 1 OR", nil); err == nil {
		t.Error("a broken filter returned rows instead of an error")
	}
}

// The movers list keeps the eight largest moves by size, whichever way they
// went, and drops the rest.
func TestCovMoversKeepsTheEightLargestMoves(t *testing.T) {
	s := covCharges(t)
	for i := 1; i <= 10; i++ {
		if _, err := s.db.Exec(`INSERT INTO charges VALUES ('aws', '2026-10-05', ?, ?)`,
			fmt.Sprintf("svc-%02d", i), i*100); err != nil {
			t.Fatal(err)
		}
	}
	out, err := movers(s.db, "2026-10", "2026-09", "31")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 8 {
		t.Fatalf("movers kept %d rows, want 8", len(out))
	}
	if out[0].Service != "svc-10" || out[7].Service != "svc-03" {
		t.Errorf("movers run from %s to %s, want svc-10 down to svc-03", out[0].Service, out[7].Service)
	}
}

func TestCovTotalsThroughCutsEachMonthAtTheDay(t *testing.T) {
	s := covCharges(t)
	for _, r := range []struct {
		day string
		amt int
	}{{"2026-10-03", 100}, {"2026-10-20", 50}} {
		if _, err := s.db.Exec(`INSERT INTO charges VALUES ('aws', ?, 'svc', ?)`, r.day, r.amt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := totalsThrough(s.db, "2026-10", "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["aws"] != money.Cents(100) {
		t.Errorf("totals through the tenth = %v, want only aws at 100 (the twentieth is after the cut)", got)
	}
	if _, err := s.db.Exec(`DROP TABLE charges`); err != nil {
		t.Fatal(err)
	}
	if _, err := totalsThrough(s.db, "2026-10", "10"); err == nil {
		t.Error("an unreadable charges table gave totals with no error")
	}
}

func TestCovPrevMonthStepsBackOneCalendarMonth(t *testing.T) {
	if got := prevMonth("2026-01"); got != "2025-12" {
		t.Errorf("prevMonth(2026-01) = %q, want 2025-12", got)
	}
	if got := prevMonth("not a month"); got != "not a month" {
		t.Errorf("prevMonth of a string that is not a month = %q, want it returned as it came", got)
	}
}

// The sparkline is the last hundred days, marks the flagged day when it is in
// that window, and draws a flat line without dividing by zero.
func TestCovSparkDrawsTheLastHundredDaysAndTheFlaggedDay(t *testing.T) {
	if got := spark(nil, nil, "", 0, "up"); got != "" {
		t.Errorf("a series of nothing drew %q", got)
	}
	var days []string
	var vals []money.Cents
	for i := 0; i < 120; i++ {
		days = append(days, fmt.Sprintf("day-%03d", i))
		vals = append(vals, money.Cents(i*10))
	}
	down := string(spark(days, vals, days[119], 50, "down"))
	if !strings.Contains(down, `class="mark down"`) || !strings.Contains(down, "<circle") {
		t.Errorf("a flagged day inside the window must be marked down, got %q", down)
	}
	up := string(spark(days, vals, days[119], 50, "up"))
	if !strings.Contains(up, `class="mark"`) || strings.Contains(up, "mark down") {
		t.Errorf("a flagged day marked up must not carry the down class, got %q", up)
	}
	outside := string(spark(days, vals, days[5], 50, "up"))
	if strings.Contains(outside, "<circle") {
		t.Errorf("a day cut off by the hundred-day window must not be marked")
	}
	flat := make([]money.Cents, 3)
	if got := string(spark([]string{"a", "b", "c"}, flat, "b", 0, "up")); strings.Contains(got, "NaN") {
		t.Errorf("a flat series drew NaN: %q", got)
	}
}
