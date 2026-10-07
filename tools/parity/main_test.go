package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func invoke(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	code = run(args, &so, &se)
	return code, so.String(), se.String()
}

// ------------------------------------------------------------- normalise

// Each scrub removes exactly the bytes that legitimately move between two runs
// of the same code, and nothing a person would call content.
func TestNormaliseRemovesOnlyWhatMovesBetweenRuns(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"csrf", `<input name="csrf" value="a1b2c3d4">`, `<input name="csrf" value="<CSRF>">`},
		{"iso datetime", "at 2026-10-07T12:30:45 and 2026-10-07 12:30", "at <TS> and <TS>"},
		{"relative age", "seen 5 minutes ago, 1 day ago, 30 seconds ago", "seen <AGO>, <AGO>, <AGO>"},
		{"http date", "Date: Wed, 07 Oct 2026 12:30:45 GMT\r\nLast-Modified: x\r\n", "Date: <HTTPDATE>\r\nLast-Modified: <HTTPDATE>\r\n"},
		{"feed clock", "<td>07.10 12:30</td>", "<td><WHEN></td>"},
		{"journal count", "chain: verified, 41 events", "chain: verified, <N> events"},
		{"journal count 2", "across <b>7</b> events", "across <b><N></b> events"},
		{"journal hash", `<td class="qid">0123abcd0123abcd</td>`, `<td class="qid"><HASH></td>`},
	}
	for _, tc := range cases {
		if got := string(normalise([]byte(tc.in))); got != tc.want {
			t.Errorf("%s: normalise(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// The things that ARE the product survive untouched: money, copy, a date
	// with no time of day, a plain count.
	keep := `<td>$1,650.00</td> Newest first. due 2026-10-07, 12 events, 3 days left`
	if got := string(normalise([]byte(keep))); got != keep {
		t.Errorf("content was scrubbed:\n got %q\nwant %q", got, keep)
	}
}

func TestEveryScrubSaysWhyItExists(t *testing.T) {
	if len(scrubs) == 0 {
		t.Fatal("no scrubs")
	}
	for _, s := range scrubs {
		if strings.TrimSpace(s.why) == "" || s.name == "" {
			t.Errorf("scrub %q has no justification; a rule that cannot say why erases differences for nothing", s.name)
		}
	}
}

// ------------------------------------------------- family, safeName, diff

func TestFamilyCollapsesIdsAndKeepsQueryKeys(t *testing.T) {
	for in, want := range map[string]string{
		"/task/17":                   "/task/{id}",
		"/task/93":                   "/task/{id}",
		"/board":                     "/board",
		"/board?view=month":          "/board?view",
		"/board?view=month&d=2026":   "/board?d&view",
		"/board?d=2026-01-02&view=x": "/board?d&view",
		"/a/1/b/22":                  "/a/{id}/b/{id}",
	} {
		if got := family(in); got != want {
			t.Errorf("family(%q) = %q, want %q", in, got, want)
		}
	}
	if family("/task/17") != family("/task/93") || family("/board?view=a") == family("/board?d=1&view=a") {
		t.Errorf("same rendering path must share a family and different query keys must not")
	}
}

func TestSafeNameIsAFileNameAndStaysDistinct(t *testing.T) {
	for in, want := range map[string]string{
		"/":                 "index",
		"/kpis":             "kpis",
		"/board?view=month": "board~view-month",
		"/a/b?x=1&y=2":      "a__b~x-1_y-2",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	long1 := "/x?" + strings.Repeat("a", 200) + "=1"
	long2 := "/x?" + strings.Repeat("a", 200) + "=2"
	a, b := safeName(long1), safeName(long2)
	if len(a) > 120 || len(b) > 120 {
		t.Errorf("a long path made a name of %d and %d bytes", len(a), len(b))
	}
	if a == b {
		t.Errorf("two long paths that differ only at the end collided on %q", a)
	}
	if a != safeName(long1) {
		t.Errorf("the name of a long path is not deterministic")
	}
}

func TestFirstDiffNamesTheLineAndBothSides(t *testing.T) {
	got := firstDiff([]byte("a\nb\nc"), []byte("a\nB\nc"))
	if !strings.Contains(got, "line 2") || !strings.Contains(got, "golden: b") || !strings.Contains(got, "actual: B") {
		t.Errorf("firstDiff = %q", got)
	}
	if got := firstDiff([]byte("a\nb"), []byte("a\nb\nc")); !strings.Contains(got, "identical for 2 lines, then lengths differ (2 vs 3 lines)") {
		t.Errorf("length-only difference = %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := firstDiff([]byte(long), []byte("y")); strings.Count(got, "x") > 170 || !strings.Contains(got, "…") {
		t.Errorf("a 300-byte line was not clipped: %d x's", strings.Count(got, "x"))
	}
}

// --------------------------------------------------------- hand-made captures

type page struct {
	status   int
	location string
	body     string
}

// makeCapture writes a capture directory exactly as capture() lays one out, by
// the manifest's own rules, so compare() can be judged on bytes written down
// here and not on anything the capturing half produced.
func makeCapture(t *testing.T, pages map[string]page) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bodies"), 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range pages {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	m := manifest{Base: "http://example.test"}
	for _, p := range paths {
		pg := pages[p]
		sum := sha256.Sum256([]byte(pg.body))
		name := safeName(p)
		if err := os.WriteFile(filepath.Join(dir, "bodies", name), []byte(pg.body), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Entries = append(m.Entries, entry{
			Path: p, Status: pg.status, Type: "text/html", Location: pg.location,
			Bytes: len(pg.body), SHA256: hex.EncodeToString(sum[:]), File: name,
		})
	}
	if err := save(dir, &m); err != nil {
		t.Fatal(err)
	}
	return dir
}

func goldenPages() map[string]page {
	return map[string]page{
		"/":      {200, "", "<h1>Home</h1>\n"},
		"/kpis":  {200, "", "<p>Budget $1,650.00</p>\n<p>Newest first.</p>\n"},
		"/audit": {200, "", "<p>chain ok</p>\n"},
		"/teams": {200, "", "<p>teams</p>\n"},
		"/board": {303, "/login", ""},
	}
}

func with(base map[string]page, edits func(map[string]page)) map[string]page {
	out := map[string]page{}
	for k, v := range base {
		out[k] = v
	}
	edits(out)
	return out
}

func TestACaptureComparedWithItselfIsParity(t *testing.T) {
	g := makeCapture(t, goldenPages())
	code, out, errOut := invoke(t, "compare", "-a", g, "-b", g)
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "PARITY: all 5 surfaces identical") || !strings.Contains(out, "digest ") {
		t.Errorf("stdout:\n%s", out)
	}
}

// The committed golden record, the only surviving record of the Python
// console's surface, compares clean with itself and has the shape the parity
// gate script relies on.
func TestTheCommittedGoldenComparesCleanWithItself(t *testing.T) {
	golden := filepath.Join("..", "..", "parity", "captures", "golden")
	code, out, errOut := invoke(t, "compare", "-a", golden, "-b", golden)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m, err := load(golden)
	if err != nil || m.Count != len(m.Entries) || m.Count < 100 {
		t.Fatalf("golden manifest: %v, count %d, entries %d", err, m.Count, len(m.Entries))
	}
	if !strings.Contains(out, fmt.Sprintf("PARITY: all %d surfaces identical", m.Count)) {
		t.Errorf("stdout:\n%s", out)
	}
	if rollDigest(m) != m.Digest {
		t.Errorf("the golden manifest's digest does not describe its own entries")
	}
}

// Each kind of difference is reported under its own name, with the thing a
// person needs to act on, and exits non-zero.
func TestCompareNamesEachKindOfDifference(t *testing.T) {
	golden := makeCapture(t, goldenPages())
	cases := []struct {
		name  string
		edit  func(map[string]page)
		wants []string
	}{
		{"a changed figure",
			func(p map[string]page) { p["/kpis"] = page{200, "", "<p>Budget $1,651.00</p>\n<p>Newest first.</p>\n"} },
			[]string{"CONTENT  /kpis", "line 1", "golden: <p>Budget $1,650.00</p>", "actual: <p>Budget $1,651.00</p>", "0 gone, 0 extra, 1 differing"},
		},
		{"a changed status",
			func(p map[string]page) { p["/audit"] = page{500, "", "boom\n"} },
			[]string{"STATUS   /audit: golden 200, actual 500"},
		},
		{"a redirect to the wrong page",
			func(p map[string]page) { p["/board"] = page{303, "/elsewhere", ""} },
			[]string{`REDIRECT /board: golden -> "/login", actual -> "/elsewhere"`},
		},
		{"a route that went away",
			func(p map[string]page) { delete(p, "/teams") },
			[]string{"GONE     /teams (in golden, absent here)", "1 gone, 0 extra, 0 differing"},
		},
		{"a route that appeared",
			func(p map[string]page) { p["/new"] = page{200, "", "new\n"} },
			[]string{"EXTRA    /new (here, not in golden)", "0 gone, 1 extra, 0 differing"},
		},
	}
	for _, tc := range cases {
		actual := makeCapture(t, with(goldenPages(), tc.edit))
		code, out, errOut := invoke(t, "compare", "-a", golden, "-b", actual)
		if code != 1 {
			t.Errorf("%s: exit %d, want 1", tc.name, code)
		}
		if !strings.Contains(errOut, "NO PARITY:") {
			t.Errorf("%s: stderr %q", tc.name, errOut)
		}
		for _, want := range tc.wants {
			if !strings.Contains(out+errOut, want) {
				t.Errorf("%s: lacks %q in:\n%s%s", tc.name, want, out, errOut)
			}
		}
		if strings.Contains(out, "PARITY: all") {
			t.Errorf("%s: reads as parity:\n%s", tc.name, out)
		}
	}
}

// A comparison over nothing must not read as success: it is the case that turns
// a broken harness into a green light.
func TestACaptureOfNothingIsRefusedNotPassed(t *testing.T) {
	golden := makeCapture(t, goldenPages())
	empty := makeCapture(t, map[string]page{})
	for _, args := range [][]string{
		{"compare", "-a", golden, "-b", empty},
		{"compare", "-a", empty, "-b", golden},
		{"compare", "-a", empty, "-b", empty},
	} {
		code, out, errOut := invoke(t, args...)
		if code != 1 || !strings.Contains(errOut, "measured nothing") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args[1:], code, out, errOut)
		}
		if strings.Contains(out, "PARITY") {
			t.Errorf("%v: reads as parity: %q", args[1:], out)
		}
	}
	// A directory that is not a capture at all is an error too, not zero.
	code, _, errOut := invoke(t, "compare", "-a", golden, "-b", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "NO PARITY:") {
		t.Errorf("not a capture: exit %d, stderr %q", code, errOut)
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "manifest.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := invoke(t, "compare", "-a", golden, "-b", bad); code != 1 {
		t.Errorf("a corrupt manifest exited %d", code)
	}
}

// -partial counts a surface the actual server has not built yet apart from one
// it built wrong: "not yet" is a to-do list, "differs" is a bug.
func TestPartialSeparatesNotBuiltFromWrong(t *testing.T) {
	golden := makeCapture(t, goldenPages())

	notYet := makeCapture(t, with(goldenPages(), func(p map[string]page) {
		p["/teams"] = page{404, "", "nope\n"}
		p["/audit"] = page{405, "", ""}
	}))
	code, out, errOut := invoke(t, "compare", "-partial", "-a", golden, "-b", notYet)
	if code != 0 {
		t.Fatalf("unbuilt surfaces are progress, not failure: exit %d, %s", code, errOut)
	}
	if !strings.Contains(out, "PROGRESS: 3 of 5 surfaces identical (60.0%)") ||
		!strings.Contains(out, "2 not built yet, 0 differing") {
		t.Errorf("stdout:\n%s", out)
	}

	wrong := makeCapture(t, with(goldenPages(), func(p map[string]page) {
		p["/teams"] = page{500, "", "boom\n"}               // not built
		p["/kpis"] = page{200, "", "<p>Budget $9.00</p>\n"} // built, wrong
		p["/board"] = page{303, "/elsewhere", ""}           // built, wrong place
		p["/audit"] = page{302, "", ""}                     // built, wrong status
	}))
	code, out, errOut = invoke(t, "compare", "-partial", "-a", golden, "-b", wrong)
	if code != 1 || !strings.Contains(errOut, "3 surfaces are built but wrong") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"PROGRESS: 1 of 5 surfaces identical (20.0%)",
		"1 not built yet, 3 differing",
		"CONTENT  /kpis", "golden: <p>Budget $1,650.00</p>",
		`REDIRECT /board: golden -> "/login", actual -> "/elsewhere"`,
		"STATUS   /audit: golden 200, actual 302",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/teams: golden") || strings.Contains(out, "CONTENT  /teams") {
		t.Errorf("an unbuilt surface was reported as wrong:\n%s", out)
	}

	// Nothing identical at all is a harness failure, never a 0% success.
	allDown := makeCapture(t, map[string]page{
		"/": {404, "", ""}, "/kpis": {404, "", ""}, "/audit": {404, "", ""},
		"/teams": {404, "", ""}, "/board": {404, "", ""},
	})
	code, _, errOut = invoke(t, "compare", "-partial", "-a", golden, "-b", allDown)
	if code != 1 || !strings.Contains(errOut, "measured nothing: not one surface matched") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// ---------------------------------------------------------------- fault tools

func entryFor(t *testing.T, dir, path string) entry {
	t.Helper()
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entry for %s", path)
	return entry{}
}

// mutate plants ONE changed fact and keeps everything else about the capture
// consistent, so what compare then sees is a real difference and not a corrupt
// directory: the entry's sha256 and bytes describe the new body, and count and
// digest describe the entries.
func TestMutatePlantsOneFaultAndKeepsTheCaptureConsistent(t *testing.T) {
	golden := makeCapture(t, goldenPages())
	faulty := makeCapture(t, goldenPages())

	code, _, errOut := invoke(t, "mutate", "-dir", faulty, "-path", "/kpis", "-old", "$1,650.00", "-new", "$1,651.00")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	e := entryFor(t, faulty, "/kpis")
	body, err := os.ReadFile(filepath.Join(faulty, "bodies", e.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "$1,651.00") || strings.Contains(string(body), "$1,650.00") {
		t.Errorf("body = %q", body)
	}
	sum := sha256.Sum256(body)
	if e.SHA256 != hex.EncodeToString(sum[:]) || e.Bytes != len(body) {
		t.Errorf("entry does not describe the mutated body: %+v", e)
	}
	m, _ := load(faulty)
	if m.Count != 5 || m.Digest != rollDigest(m) {
		t.Errorf("count %d, digest consistent %v", m.Count, m.Digest == rollDigest(m))
	}
	// And compare names it, and only it.
	code, out, _ := invoke(t, "compare", "-a", golden, "-b", faulty)
	if code != 1 || !strings.Contains(out, "CONTENT  /kpis") || strings.Count(out, "CONTENT") != 1 {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// A fault that changed nothing, or changed more than was asked, would make the
// gate pass or fail for the wrong reason, so both are refused.
func TestMutateRefusesZeroOrManyOccurrencesAndUnknownPaths(t *testing.T) {
	dir := makeCapture(t, with(goldenPages(), func(p map[string]page) {
		p["/twice"] = page{200, "", "same same\n"}
	}))
	before := entryFor(t, dir, "/twice")
	for name, args := range map[string][]string{
		"absent":  {"mutate", "-dir", dir, "-path", "/kpis", "-old", "not there", "-new", "x"},
		"twice":   {"mutate", "-dir", dir, "-path", "/twice", "-old", "same", "-new", "x"},
		"no path": {"mutate", "-dir", dir, "-path", "/nowhere", "-old", "x", "-new", "y"},
	} {
		code, _, errOut := invoke(t, args...)
		if code != 1 || !strings.Contains(errOut, "mutate failed:") {
			t.Errorf("%s: exit %d, stderr %q", name, code, errOut)
		}
	}
	if !strings.Contains(func() string {
		_, _, e := invoke(t, "mutate", "-dir", dir, "-path", "/twice", "-old", "same", "-new", "x")
		return e
	}(), `"same" occurs 2 times`) {
		t.Errorf("the refusal does not say how many times")
	}
	if after := entryFor(t, dir, "/twice"); after != before {
		t.Errorf("a refused mutation changed the entry: %+v -> %+v", before, after)
	}
}

func TestDropRemovesTheEntryAndItsBodyAndCompareSaysGone(t *testing.T) {
	golden := makeCapture(t, goldenPages())
	faulty := makeCapture(t, goldenPages())
	e := entryFor(t, faulty, "/teams")

	if code, _, errOut := invoke(t, "drop", "-dir", faulty, "-path", "/teams"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(faulty, "bodies", e.File)); err == nil {
		t.Errorf("the body file survived")
	}
	m, _ := load(faulty)
	if m.Count != 4 || len(m.Entries) != 4 || m.Digest != rollDigest(m) {
		t.Errorf("manifest after drop: count %d, entries %d", m.Count, len(m.Entries))
	}
	code, out, _ := invoke(t, "compare", "-a", golden, "-b", faulty)
	if code != 1 || !strings.Contains(out, "GONE     /teams") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if code, _, errOut := invoke(t, "drop", "-dir", faulty, "-path", "/teams"); code != 1 || !strings.Contains(errOut, "drop failed:") {
		t.Errorf("dropping twice: exit %d, stderr %q", code, errOut)
	}
}

// The three faults parity/gate-has-teeth.sh plants on a copy of the golden
// record, planted here the same way: each must be caught and named.
func TestTheGateScriptsThreeFaultsAreCaughtOnTheRealGolden(t *testing.T) {
	golden := filepath.Join("..", "..", "parity", "captures", "golden")
	copyDir := func() string {
		dst := filepath.Join(t.TempDir(), "copy")
		if err := os.MkdirAll(filepath.Join(dst, "bodies"), 0o755); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Join(golden, "bodies"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range append(entries, nil...) {
			raw, _ := os.ReadFile(filepath.Join(golden, "bodies", f.Name()))
			_ = os.WriteFile(filepath.Join(dst, "bodies", f.Name()), raw, 0o644)
		}
		raw, _ := os.ReadFile(filepath.Join(golden, "manifest.json"))
		_ = os.WriteFile(filepath.Join(dst, "manifest.json"), raw, 0o644)
		return dst
	}
	cases := []struct {
		name  string
		plant []string
		want  string
	}{
		{"a budget off by one", []string{"mutate", "-path", "/kpis", "-old", "$1,650.00", "-new", "$1,651.00"}, "CONTENT  /kpis"},
		{"a one-character copy change", []string{"mutate", "-path", "/audit", "-old", "Newest first.", "-new", "Newest first!"}, "CONTENT  /audit"},
		{"a route that moved", []string{"drop", "-path", "/teams"}, "GONE     /teams"},
	}
	for _, tc := range cases {
		faulty := copyDir()
		args := append([]string{tc.plant[0], "-dir", faulty}, tc.plant[1:]...)
		if code, _, errOut := invoke(t, args...); code != 0 {
			t.Fatalf("%s: planting failed: exit %d: %s", tc.name, code, errOut)
		}
		code, out, _ := invoke(t, "compare", "-a", golden, "-b", faulty)
		if code != 1 || !strings.Contains(out, tc.want) {
			t.Errorf("%s: exit %d, want %q in:\n%s", tc.name, code, tc.want, out)
		}
	}
}

// ---------------------------------------------------------------- capture

// site is a stand-in console. It records every GET so a test can say what the
// crawl walked, mints a different CSRF token and clock reading on every
// request (the bytes that legitimately move), and closes signup once claimed.
type site struct {
	mu        sync.Mutex
	gets      []string
	claimed   bool
	refuseAll bool // sign-in and signup both refuse
	hits      int
	srv       *httptest.Server
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *site) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.gets...)
}

func (s *site) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	now := time.Now().Format("2006-01-02 15:04:05")
	form := fmt.Sprintf(`<form><input name="csrf" value="tok%d"></form>`, s.hits)
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/signup":
			if s.claimed || s.refuseAll {
				w.Header().Set("Location", "/signup?msg=closed")
			} else {
				s.claimed = true
				w.Header().Set("Location", "/")
			}
		case "/login":
			if s.refuseAll {
				w.Header().Set("Location", "/login?msg=bad")
			} else {
				w.Header().Set("Location", "/")
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	s.gets = append(s.gets, r.URL.RequestURI())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Date", time.Now().Format(http.TimeFormat))
	switch r.URL.Path {
	case "/signup", "/login":
		fmt.Fprint(w, form)
	case "/":
		var b strings.Builder
		b.WriteString("<h1>Home</h1>" + form + "\n")
		for _, p := range []string{"/logout", "/signup", "/login", "/static-no", "#frag"} {
			fmt.Fprintf(&b, `<a href="%s">x</a>`+"\n", p)
		}
		for i := 1; i <= 40; i++ {
			fmt.Fprintf(&b, `<a href="/task/%d">t</a>`+"\n", i)
		}
		for _, d := range []string{"2026-01-01", "2026-01-02", "2026-01-03", "2026-01-04"} {
			fmt.Fprintf(&b, `<a href="/board?d=%s&view=month">d</a>`+"\n", d)
		}
		fmt.Fprintf(&b, `<p>rendered %s, seen 4 minutes ago</p>`+"\n", now)
		fmt.Fprint(w, b.String())
	case "/board":
		fmt.Fprintf(w, "<h1>Board %s</h1>%s\n<a href=\"/task/41\">t</a>", r.URL.RawQuery, form)
	case "/staff":
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusSeeOther)
	case "/connectors":
		fmt.Fprintf(w, "<p>connectors at %s</p><a href=\"/connectors/1\">c</a>", now)
	case "/connectors/1":
		fmt.Fprint(w, "<p>one</p>")
	default:
		if strings.HasPrefix(r.URL.Path, "/task/") {
			fmt.Fprintf(w, "<p>%s</p>", r.URL.Path)
			return
		}
		http.NotFound(w, r)
	}
}

// The thing the whole gate stands on: capturing the SAME console twice gives
// the same bytes. The pages here carry a fresh CSRF token, clock reading,
// relative age and Date header every time, and the captures still agree.
func TestCapturingTheSameConsoleTwiceGivesTheSameBytes(t *testing.T) {
	s := newSite(t)
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, out := range []string{a, b} {
		code, stdout, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out, "-per-family", "3")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		if !strings.Contains(stdout, "captured ") || !strings.Contains(stdout, "digest   ") {
			t.Errorf("stdout:\n%s", stdout)
		}
	}
	ma, _ := load(a)
	mb, _ := load(b)
	if ma.Digest != mb.Digest || ma.Count != mb.Count || ma.Count < 6 {
		t.Fatalf("two captures of one console disagree: %d/%s vs %d/%s", ma.Count, ma.Digest, mb.Count, mb.Digest)
	}
	if code, out, _ := invoke(t, "compare", "-a", a, "-b", b); code != 0 || !strings.Contains(out, "PARITY") {
		t.Errorf("compare exit %d:\n%s", code, out)
	}
	// The scrubbed bytes are what is on disk, not the raw response.
	home := entryFor(t, a, "/")
	body, _ := os.ReadFile(filepath.Join(a, "bodies", home.File))
	for _, want := range []string{`name="csrf" value="<CSRF>"`, "<TS>", "<AGO>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the stored body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "tok") {
		t.Errorf("a raw CSRF token reached disk")
	}
}

// The crawl is bounded per rendering family (the first members a page links
// to, so two captures of a page that renders its links in a stable order
// agree), never walks the routes that would end the session, and follows only
// href. The crawl's own comment says members are "taken in sorted order"; the
// code takes them in discovery order and sorts only the result, which this
// test records as it is.
func TestTheCrawlIsBoundedPerFamilyAndAvoidsForbiddenRoutes(t *testing.T) {
	s := newSite(t)
	out := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out, "-per-family", "3"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m, _ := load(out)
	var tasks, boards, all []string
	for _, e := range m.Entries {
		all = append(all, e.Path)
		switch {
		case strings.HasPrefix(e.Path, "/task/"):
			tasks = append(tasks, e.Path)
		case strings.HasPrefix(e.Path, "/board?"):
			boards = append(boards, e.Path)
		}
	}
	if strings.Join(tasks, ",") != "/task/1,/task/2,/task/3" {
		t.Errorf("tasks = %v: want the first three of the family the page links to", tasks)
	}
	if len(boards) != 3 {
		t.Errorf("board queries = %v, want 3 of one family", boards)
	}
	for _, p := range append(all, s.paths()...) {
		if p == "/logout" {
			t.Errorf("the crawl walked /logout")
		}
	}
	for _, e := range m.Entries {
		if e.Path == "/signup" || e.Path == "/login" || strings.Contains(e.Path, "#") {
			t.Errorf("captured a surface the crawl must skip: %s", e.Path)
		}
	}
	// The sorted order of the manifest is what makes two captures comparable.
	if !sort.StringsAreSorted(all) {
		t.Errorf("entries are not sorted: %v", all)
	}
}

// Redirects are the route's own answer: recorded with their Location rather
// than followed, and a 404 is recorded as a 404.
func TestCaptureRecordsRedirectsAndMissesAsTheyAre(t *testing.T) {
	s := newSite(t)
	out := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	staff := entryFor(t, out, "/staff")
	if staff.Status != 303 || staff.Location != "/login" {
		t.Errorf("/staff = %+v: a redirect must be recorded, not followed", staff)
	}
	if res := entryFor(t, out, "/results"); res.Status != 404 {
		t.Errorf("/results = %+v", res)
	}
	if home := entryFor(t, out, "/"); home.Type != "text/html" || home.Status != 200 || home.Bytes == 0 {
		t.Errorf("/ = %+v: the content type loses its charset suffix", home)
	}
}

// A second capture of an installation that is already claimed signs in instead
// of signing up; both paths are normal.
func TestCaptureClaimsOnTheFirstRunAndSignsInOnTheSecond(t *testing.T) {
	s := newSite(t)
	for i := 1; i <= 2; i++ {
		out := filepath.Join(t.TempDir(), fmt.Sprintf("c%d", i))
		if code, _, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out); code != 0 {
			t.Fatalf("capture %d: exit %d: %s", i, code, errOut)
		}
	}
	if !s.claimed {
		t.Errorf("the first capture did not sign up")
	}
	// If neither works the capture says so rather than recording a wall of
	// redirects as if it were the product.
	s.refuseAll = true
	code, _, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", filepath.Join(t.TempDir(), "x"))
	if code != 1 || !strings.Contains(errOut, "capture failed: claiming the installation: neither signup nor sign-in worked") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// -from takes the golden's own path list instead of crawling, so the question
// asked is "of the surfaces the reference serves, how many does this one serve
// identically".
func TestCaptureFromAGoldenFetchesExactlyItsPaths(t *testing.T) {
	s := newSite(t)
	golden := makeCapture(t, map[string]page{
		"/":             {200, "", "x"},
		"/task/7":       {200, "", "x"},
		"/connectors/1": {200, "", "x"},
		"/gone":         {200, "", "x"},
	})
	out := filepath.Join(t.TempDir(), "c")
	code, stdout, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out, "-from", golden)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(stdout, "captured 4 surfaces from "+s.srv.URL) {
		t.Errorf("stdout:\n%s", stdout)
	}
	m, _ := load(out)
	var got []string
	for _, e := range m.Entries {
		got = append(got, fmt.Sprintf("%s=%d", e.Path, e.Status))
	}
	if strings.Join(got, " ") != "/=200 /connectors/1=200 /gone=404 /task/7=200" {
		t.Errorf("entries = %v", got)
	}
	for _, p := range s.paths() {
		if strings.HasPrefix(p, "/board") || strings.HasPrefix(p, "/staff") {
			t.Errorf("crawled %s although a path list was given", p)
		}
	}
	code, _, errOut = invoke(t, "capture", "-base", s.srv.URL, "-out", out, "-from", filepath.Join(t.TempDir(), "missing"))
	if code != 1 || !strings.Contains(errOut, "reading the golden manifest to take its paths") {
		t.Errorf("missing golden: exit %d, stderr %q", code, errOut)
	}
}

// Capturing replaces the directory it was given; a stale body from an earlier
// capture must not survive into this one.
func TestCaptureReplacesWhatWasInTheOutputDirectory(t *testing.T) {
	s := newSite(t)
	out := filepath.Join(t.TempDir(), "c")
	if err := os.MkdirAll(filepath.Join(out, "bodies"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(out, "bodies", "stale")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := invoke(t, "capture", "-base", s.srv.URL, "-out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("a stale body survived the capture")
	}
	m, _ := load(out)
	for _, e := range m.Entries {
		if _, err := os.Stat(filepath.Join(out, "bodies", e.File)); err != nil {
			t.Errorf("entry %s has no body file", e.Path)
		}
	}
	var raw map[string]any
	buf, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err := json.Unmarshal(buf, &raw); err != nil || raw["base"] != s.srv.URL {
		t.Errorf("manifest base = %v (%v)", raw["base"], err)
	}
	if !bytes.HasSuffix(buf, []byte("}\n")) {
		t.Errorf("manifest should end in a newline")
	}
}

func TestCaptureOfAnUnreachableConsoleFailsNamed(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	code, _, errOut := invoke(t, "capture", "-base", url, "-out", filepath.Join(t.TempDir(), "c"))
	if code != 1 || !strings.Contains(errOut, "capture failed:") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// -------------------------------------------------------------------- usage

func TestUsageAndFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"frobnicate"},
		{"compare"},
		{"compare", "-a", "x"},
		{"mutate", "-dir", "x"},
		{"drop", "-dir", "x"},
	} {
		code, out, errOut := invoke(t, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "parity - hold one CostCrew implementation against another") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	if code, _, errOut := invoke(t, "compare", "-nope"); code != 2 || !strings.Contains(errOut, "nope") {
		t.Errorf("bad flag: exit %d, stderr %q", code, errOut)
	}
	for _, verb := range []string{"capture", "compare", "mutate", "drop"} {
		if code, _, _ := invoke(t, verb, "-h"); code != 0 {
			t.Errorf("%s -h exited %d", verb, code)
		}
	}
}
