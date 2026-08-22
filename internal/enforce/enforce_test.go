package enforce_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/enforce"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// plane is a stand-in for TokenFuse's control plane: it holds unit budgets in
// micros and records what was sent, so a test can say what left this process
// rather than what a function returned.
type plane struct {
	mu      sync.Mutex
	budgets map[string]int64
	posts   []string
	srv     *httptest.Server
}

func newPlane(t *testing.T, start map[string]int64) *plane {
	t.Helper()
	p := &plane{budgets: map[string]int64{}}
	for k, v := range start {
		p.budgets[k] = v
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer devkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/unit-budgets":
			_ = json.NewEncoder(w).Encode(p.budgets)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/budget"):
			unit := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/units/"), "/budget")
			var body struct {
				BudgetUSD float64 `json:"budget_usd"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			p.budgets[unit] = int64(body.BudgetUSD * 1e6)
			p.posts = append(p.posts, unit)
			_ = json.NewEncoder(w).Encode(map[string]any{"unit": unit})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *plane) cfg() enforce.Config {
	return enforce.Config{BaseURL: p.srv.URL, Key: "devkey"}
}

func (p *plane) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.posts...)
}

// Planning sends nothing. This is the whole safety property of the two-step,
// and it is worth a test that watches the far end rather than the return
// value: this is the one integration in the estate that CHANGES something in
// another service, and the thing it changes decides whether a real model call
// is refused.
func TestPlanningSendsNothing(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 5_000_000})
	_, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{
		"growth":      money.Cents(100_00),
		"ml-platform": money.Cents(2_000_00),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.sent(); len(got) != 0 {
		t.Errorf("planning sent %v to the control plane", got)
	}
}

// A budget going DOWN is counted on its own, because that is the direction
// that stops work.
func TestALoweredBudgetIsCalledOut(t *testing.T) {
	p := newPlane(t, map[string]int64{
		"growth":      5_000_000, // 5.00
		"ml-platform": 1_000_000, // 1.00
		"research":    2_000_000, // 2.00, and unchanged below
	})
	plan, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{
		"growth":      money.Cents(1_00), // down from 5.00
		"ml-platform": money.Cents(9_00), // up from 1.00
		"research":    money.Cents(2_00), // the same
		"support":     money.Cents(4_00), // new
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Lowered != 1 {
		t.Errorf("%d lowered, wanted 1", plan.Lowered)
	}
	if plan.Added != 1 {
		t.Errorf("%d new, wanted 1", plan.Added)
	}
	if plan.Unchanged != 1 {
		t.Errorf("%d unchanged, wanted 1", plan.Unchanged)
	}
	if len(plan.Changes) != 3 {
		t.Errorf("%d changes, wanted 3", len(plan.Changes))
	}
}

// The approval binds to the diff it was given for.
//
// A two-step that re-plans on the second step is not a two-step: a person
// reads a diff, thinks, runs the apply, and sends something else. Nothing
// anywhere would say so, which is why the fingerprint exists.
func TestApplyRefusesAPlanThatIsNotTheOneApproved(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 5_000_000})
	want := map[string]money.Cents{"growth": money.Cents(100_00)}

	plan, err := enforce.MakePlan(context.Background(), p.cfg(), want)
	if err != nil {
		t.Fatal(err)
	}
	fp := plan.Fingerprint()

	// Somebody moves it by hand, behind our back.
	p.mu.Lock()
	p.budgets["growth"] = 1_000_000
	p.mu.Unlock()

	// The same plan object, applied with the fingerprint it was shown as.
	if _, err := enforce.Apply(context.Background(), p.cfg(), plan, fp); err == nil {
		t.Error("it applied a plan whose ground had moved")
	} else if !strings.Contains(err.Error(), "changed since") {
		t.Errorf("it refused for the wrong reason: %v", err)
	}
	if got := p.sent(); len(got) != 0 {
		t.Errorf("it sent %v after refusing", got)
	}

	// And a fingerprint from a different plan is refused too.
	fresh, _ := enforce.MakePlan(context.Background(), p.cfg(), want)
	if _, err := enforce.Apply(context.Background(), p.cfg(), fresh, fp); err == nil {
		t.Error("it applied a plan under somebody else's fingerprint")
	}
	if got := p.sent(); len(got) != 0 {
		t.Errorf("it sent %v after refusing", got)
	}
}

// Applying the approved plan sets exactly it, and the far end holds the right
// numbers afterwards.
func TestApplySetsExactlyWhatWasApproved(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 5_000_000})
	plan, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{
		"growth":      money.Cents(100_00),
		"ml-platform": money.Cents(2_000_00),
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := enforce.Apply(context.Background(), p.cfg(), plan, plan.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("set %d, wanted 2", n)
	}
	got, err := enforce.Current(context.Background(), p.cfg())
	if err != nil {
		t.Fatal(err)
	}
	// Read BACK from the far end, in cents, so the micro conversion is checked
	// in both directions. A factor of ten thousand is the easiest thing in
	// this package to get wrong and the hardest to notice.
	for unit, want := range map[string]money.Cents{
		"growth": money.Cents(100_00), "ml-platform": money.Cents(2_000_00),
	} {
		if got[unit] != want {
			t.Errorf("%s reads back as %s, was set to %s", unit, got[unit], want)
		}
	}
}

// Switched off is switched off.
func TestWithNoAddressOrKeyNothingHappens(t *testing.T) {
	for _, cfg := range []enforce.Config{
		{}, {BaseURL: "http://127.0.0.1:1"}, {Key: "devkey"},
	} {
		if cfg.On() {
			t.Errorf("%+v reports itself on", cfg)
		}
		if _, err := enforce.MakePlan(context.Background(), cfg, nil); err == nil {
			t.Errorf("%+v planned with nothing configured", cfg)
		}
	}
}
