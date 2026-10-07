package main

import (
	"net/http/httptest"
	"path/filepath"
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
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// The capture half is also held against the real console's own handler, in
// this process on a loopback listener, and not only against the stand-in in
// main_test.go: the real forms, the real CSRF field, the real first-account
// signup and the real redirects. A stand-in can agree with the capture code
// while both disagree with the product.
func realConsole(t *testing.T) string {
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
	if err := connectors.EnsureRecommendationsSchema(st.DB()); err != nil {
		t.Fatal(err)
	}
	if err := connectors.EnsureLicenceSchema(st.DB()); err != nil {
		t.Fatal(err)
	}
	if err := estate.SeedBudgets(st.DB()); err != nil {
		t.Fatal(err)
	}
	// The same seeding sequence the web package's own harness runs, which is
	// the production one: without the anomaly and crew planes most pages 500,
	// and a capture of a wall of 500s would compare equal and mean nothing.
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
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(web.New(st, au, web.Stack{Host: "costcrew.test", Recorder: st.AsRecorder()}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The documented use: each capture wants its OWN fresh install (the capture
// claims it by signing up, and signing in changes the accounts page and the
// journal), and two captures of two fresh installs of the same code give the
// same bytes. The path list is a short one taken through -from, which is also
// how a crawl of a half-built server is made comparable; a full crawl of the
// real console is about a thousand pages and a minute, and the crawl itself is
// held against the stand-in.
//
// /audit is deliberately NOT on the list, and that is a finding rather than a
// choice of convenience: measured 2026-10-07, two fresh installs of this build
// differ there, on `<td class="tight"><code>{16 hex}</code></td>` (the
// journal's chain hash, which moves with the event's timestamp). The
// `journal-hash` scrub matches `<td class="qid">`, which is the markup of the
// page that was ported from, not the markup the Go console renders, so the
// selfcheck property the tool's header promises does not hold for that page.
// The scrub list is the parity gate's to change, not this test's.
func TestTwoFreshInstallsOfTheRealConsoleCaptureAlike(t *testing.T) {
	list := makeCapture(t, map[string]page{
		"/": {200, "", "x"}, "/board": {200, "", "x"}, "/kpis": {200, "", "x"},
		"/staff": {200, "", "x"}, "/accounts": {200, "", "x"},
		"/allocation": {200, "", "x"}, "/no-such-page": {200, "", "x"},
	})
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, out := range []string{a, b} {
		code, stdout, errOut := invoke(t, "capture", "-base", realConsole(t), "-out", out, "-from", list)
		if code != 0 {
			t.Fatalf("capture into %s: exit %d: %s", out, code, errOut)
		}
		if !strings.Contains(stdout, "captured 7 surfaces from http://127.0.0.1:") {
			t.Fatalf("stdout:\n%s", stdout)
		}
	}
	code, out, errOut := invoke(t, "compare", "-a", a, "-b", b)
	if code != 0 {
		t.Fatalf("two fresh installs of one build differ: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "PARITY: all 7 surfaces identical") {
		t.Errorf("stdout:\n%s", out)
	}

	// Signed in as the claiming account: the pages are pages, not the login
	// redirect; and a page the console does not have is recorded as missing.
	for _, p := range []string{"/", "/board", "/kpis", "/staff"} {
		if e := entryFor(t, a, p); e.Status != 200 || e.Bytes < 500 {
			t.Errorf("%s = %+v: the capture was not signed in to a real page", p, e)
		}
	}
	if e := entryFor(t, a, "/no-such-page"); e.Status != 404 {
		t.Errorf("/no-such-page = %+v", e)
	}

	// One changed fact in one real page is named, which is the point of a
	// gate: plant it in b by the tool's own verb and compare again.
	home := entryFor(t, b, "/kpis")
	if code, _, errOut := invoke(t, "mutate", "-dir", b, "-path", "/kpis", "-old", "<title>", "-new", "<title>X "); code != 0 {
		t.Fatalf("planting on %s (%d bytes): %s", home.Path, home.Bytes, errOut)
	}
	code, out, _ = invoke(t, "compare", "-a", a, "-b", b)
	if code != 1 || !strings.Contains(out, "CONTENT  /kpis") || strings.Count(out, "CONTENT") != 1 {
		t.Errorf("a changed title was not caught and named alone: exit %d\n%s", code, out)
	}
}
