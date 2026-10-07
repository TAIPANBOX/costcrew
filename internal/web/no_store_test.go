package web_test

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every page and every download this console serves to a signed-in person
// is the estate's money, and a shared machine's browser cache, a proxy or a
// back button after sign-out keeps whatever is not marked otherwise. The
// stylesheet is the one thing worth caching and carries no data. Invariant 79.
func TestEveryGuardedResponseIsMarkedNoStore(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	seen := 0
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		method, pattern := m[1], m[2]
		if method != "GET" || strings.HasPrefix(pattern, "/static/") {
			continue
		}
		seen++
		path := concrete(pattern)
		resp, err := h.c.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s (%d) Cache-Control %q, want no-store", path, resp.StatusCode, got)
		}
	}
	if seen < 50 {
		t.Fatalf("only %d GET routes found in server.go; the scan is broken, not the routes", seen)
	}

	// A stranger turned away, and a 404, are answers about this console too.
	stranger := startWith(t, false)
	for _, path := range []string{"/board", "/export/allocation.csv", "/no-such-page"} {
		resp, err := stranger.c.Get(stranger.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s without a session (%d): Cache-Control %q, want no-store",
				path, resp.StatusCode, got)
		}
	}
	t.Logf("checked %d GET routes", seen)
}

// The stylesheet is shared by every page and holds nothing about the estate,
// so marking it no-store would only make every page load fetch it again.
func TestTheStylesheetIsNotMarkedNoStore(t *testing.T) {
	h := start(t)
	resp, err := h.c.Get(h.srv.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/app.css: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); strings.Contains(got, "no-store") {
		t.Errorf("GET /static/app.css carries Cache-Control %q; the stylesheet holds no "+
			"estate data and is the one response worth caching", got)
	}
}

var teamLink = regexp.MustCompile(`href="(/team/[^"?]+)[^"]*"`)

// A TokenFuse unit is the team a FOCUS row named in x_unit. The chargeback
// and allocation pages list it like any team and link it to /team/<unit>,
// and that page answered 404 for anything not on the roster, so every unit
// link led nowhere.
func TestEveryTeamLinkOnTheMoneyPagesLeadsToAPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	p := latestPeriod(t, h)
	if _, err := h.st.DB().Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, provenance) VALUES
		('ai', ?, 'claude', 'acme-corp', 'Usage', 4200, 'tokenfuse-focus')`, p+"-03"); err != nil {
		t.Fatal(err)
	}
	links := map[string]bool{}
	for _, page := range []string{"/chargeback?period=" + p, "/allocation?period=" + p} {
		code, body, _ := h.get(t, page)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d", page, code)
		}
		for _, m := range teamLink.FindAllStringSubmatch(body, -1) {
			links[m[1]] = true
		}
	}
	if !links["/team/acme-corp"] {
		t.Fatalf("the unit is not linked from /chargeback or /allocation; links: %v", links)
	}
	for l := range links {
		code, body, _ := h.get(t, l+"?period="+p)
		if code != http.StatusOK {
			t.Errorf("GET %s answered %d: a link on a money page leads nowhere", l, code)
			continue
		}
		if l == "/team/acme-corp" {
			for _, want := range []string{"acme-corp", "42.00", "not on the roster"} {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s does not say %q", l, want)
				}
			}
		}
	}
}

// A name that is neither a roster team nor a unit any charge carries is
// still nothing, and says so.
func TestATeamNobodyChargedIsStillNotFound(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	for _, name := range []string{"no-such-team", "acme%27%20OR%20%271"} {
		code, _, _ := h.get(t, "/team/"+name)
		if code != http.StatusNotFound {
			t.Errorf("GET /team/%s: %d, want 404", name, code)
		}
	}
}
