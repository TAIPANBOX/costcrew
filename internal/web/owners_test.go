package web_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

var bigVal = regexp.MustCompile(`class="v big[^"]*">([^<]+)<`)

// bigTiles pulls the headline figure out of each summary tile, in order.
func bigTiles(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, m := range bigVal.FindAllStringSubmatch(body, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// The owners page and the crew page must agree about how many agents are
// bound to nothing.
//
// They are one number cut two ways: the crew page counts by analyst, the
// owners page counts by the person who answers for them. Two pages that give
// two answers to one question make both useless, and this one is worse than
// most, because the owners page is where somebody looks to find out who to
// ask about an unbound identity.
func TestOwnersAndCrewAgreeOnUnboundAgents(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")

	_, owners, _ := h.get(t, "/owners")
	_, staff, _ := h.get(t, "/staff")

	got := tileAfter(t, owners, "Bound to nothing")
	want := tileAfter(t, staff, "Bound to nothing")
	if got != want {
		t.Errorf("the owners page says %q agents are bound to nothing and the "+
			"crew page says %q; they are the same agents counted twice", got, want)
	}
	// And it must not be the reassuring answer by accident: this fixture has
	// agents carrying no attestation, so a zero here is the bug, not the news.
	if got == "0" {
		t.Errorf("both pages report zero unbound agents, but the seeded roster " +
			"records no attestation for any of them: the count is measuring " +
			"something other than what its label says")
	}
}

// tileAfter reads the headline figure of the tile with the given label.
func tileAfter(t *testing.T, body, label string) string {
	t.Helper()
	i := strings.Index(body, label)
	if i < 0 {
		t.Fatalf("no tile labelled %q on the page", label)
	}
	m := bigVal.FindStringSubmatch(body[i:])
	if m == nil {
		t.Fatalf("tile %q has no figure", label)
	}
	return strings.TrimSpace(m[1])
}

// The owners page must account for every agent on the roster.
//
// An owner column that quietly dropped agents with no owner would make the
// estate look smaller than it is, and "nobody owns this" is the state most
// worth seeing on a page about who answers for what.
func TestOwnersPageCoversTheWholeRoster(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")

	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/owners")
	want := strconv.Itoa(len(roster)) + " agents"
	if !strings.Contains(body, want) {
		t.Errorf("the owners page does not say it covers %q; the roster has %d "+
			"analysts and an owner page that covers fewer is hiding some",
			want, len(roster))
	}
}

// A page rendered twice from an unchanged store must be byte-identical.
//
// Nondeterminism here does not look like a bug. It looks like a table whose
// rows moved, which a reader takes for new data, and it makes every diff-based
// check downstream useless: the parity gate reported eight differing surfaces
// between two installs of the same binary, seven of them legitimate chain
// hashes and one of them this.
//
// Ranging a Go map is the usual cause and it is invisible at the call site,
// which is why this is checked at the page rather than in the one function
// that had it.
func TestPagesRenderTheSameTwice(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")

	// The read surface where an unstable order would actually show: every one
	// of these builds rows by grouping something.
	paths := []string{
		"/ai", "/ai?sort=model&dir=desc", "/ai?sort=team&dir=asc",
		"/owners", "/staff", "/teams", "/desks", "/services",
		"/budgets", "/allocation", "/kpis", "/utilisation",
		"/chargeback", "/engines", "/accounts", "/connectors",
	}
	for _, p := range paths {
		code, first, _ := h.get(t, p)
		if code != 200 {
			t.Errorf("GET %s answered %d", p, code)
			continue
		}
		for i := 0; i < repeatsFor(first); i++ {
			_, again, _ := h.get(t, p)
			if again != first {
				t.Errorf("GET %s rendered differently on request %d from an "+
					"unchanged store: %s", p, i+2, firstDiff(first, again))
				break
			}
		}
	}
}

// repeatsFor decides how many times a page must agree with itself.
//
// Comparing two renders of a SMALL table is close to a coin toss. Ranging a Go
// map does not produce a uniformly random permutation: it starts at a random
// bucket and offset and then walks in order, so a handful of rows take only a
// handful of distinct orders and two renders often agree by luck.
//
// Measured on 2026-08-23 against the AIUnits map-order fault, on the /ai page,
// which renders four rows: three repeats caught it 17 times in 20, and the
// gates-have-teeth run reported TOOTHLESS twice in ten for exactly that
// reason. Thirty repeats caught it 30 in 30 and took the package from 1.7s to
// 10s, which is why this is not simply thirty everywhere.
//
// A first attempt to reason about it from 1/n! predicted a miss rate of one in
// ten thousand for four rows and was wrong by three orders of magnitude, so
// the number below comes from the measurement and not from the arithmetic.
func repeatsFor(body string) int {
	rows := strings.Count(body, "<tr>")
	if rows > 24 {
		// Enough distinct orders that a repeat by luck is not the failure mode.
		return 3
	}
	return 30
}

// firstDiff names the line where two renders part company, because a report
// that only says "they differ" leaves somebody diffing two 200KB pages by hand.
func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) && i < len(lb); i++ {
		if la[i] != lb[i] {
			return "line " + strconv.Itoa(i+1) + "\n  first: " +
				strings.TrimSpace(la[i]) + "\n  again: " + strings.TrimSpace(lb[i])
		}
	}
	return "same lines, different length: " + strconv.Itoa(len(la)) +
		" then " + strconv.Itoa(len(lb))
}
