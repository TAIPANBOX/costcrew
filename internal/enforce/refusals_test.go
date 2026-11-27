package enforce_test

// What the control plane can answer besides yes: a refusal, something that is
// not a budget map, a budget added behind the plan's back, and a refusal half
// way through an apply. Each one is reported, and nothing more is sent.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/enforce"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func TestARefusalCarriesTheControlPlanesOwnWordsTrimmed(t *testing.T) {
	long := "budget service unavailable: " + strings.Repeat("x", 500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()

	_, err := enforce.MakePlan(context.Background(), enforce.Config{BaseURL: srv.URL + "/", Key: "devkey"},
		map[string]money.Cents{"growth": 100})
	if err == nil {
		t.Fatal("a 503 was read as a budget map")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GET /v1/unit-budgets: 503") || !strings.Contains(msg, "budget service unavailable") {
		t.Errorf("refusal %q does not carry the status and the control plane's words", msg)
	}
	if strings.Contains(msg, long[:301]) || !strings.Contains(msg, long[:300]) {
		t.Errorf("the control plane's words were not cut at 300 bytes: %d bytes", len(msg))
	}
}

func TestAWrongKeyIsRefused(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 5_000_000})
	cfg := p.cfg()
	cfg.Key = "not-the-key"
	if _, err := enforce.Current(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Errorf("a wrong key: %v, want the 401", err)
	}
}

func TestAnAnswerThatIsNotABudgetMapIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`["growth", 5000000]`))
	}))
	defer srv.Close()
	_, err := enforce.Current(context.Background(), enforce.Config{BaseURL: srv.URL, Key: "devkey"})
	if err == nil || !strings.Contains(err.Error(), "not a unit-to-budget map") {
		t.Errorf("a list where a map belongs: %v", err)
	}
}

func TestSwitchedOffCurrentReadsNothing(t *testing.T) {
	got, err := enforce.Current(context.Background(), enforce.Config{BaseURL: "http://127.0.0.1:1"})
	if got != nil || err != nil {
		t.Errorf("switched off: %v, %v; want nothing and no error", got, err)
	}
	if _, err := enforce.Apply(context.Background(), enforce.Config{}, enforce.Plan{}, ""); err == nil ||
		!strings.Contains(err.Error(), "switched off") {
		t.Errorf("Apply switched off: %v", err)
	}
}

// A budget of nothing is a stop, and this console does not push stops.
func TestAZeroBudgetIsNotPushed(t *testing.T) {
	p := newPlane(t, nil)
	plan, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{
		"growth": 0, "research": -100, "support": 4_00,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Unit != "support" {
		t.Errorf("changes %+v, want only support", plan.Changes)
	}
	// An empty plan applies as nothing, with nothing sent.
	empty, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{"growth": 0})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := enforce.Apply(context.Background(), p.cfg(), empty, empty.Fingerprint()); n != 0 || err != nil {
		t.Errorf("empty plan applied %d, %v", n, err)
	}
	if got := p.sent(); len(got) != 0 {
		t.Errorf("sent %v for an empty plan", got)
	}
}

func TestApplyRefusesWhenABudgetWasAddedBehindThePlan(t *testing.T) {
	p := newPlane(t, map[string]int64{"growth": 5_000_000})
	plan, err := enforce.MakePlan(context.Background(), p.cfg(), map[string]money.Cents{"growth": 1_00})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.budgets["research"] = 2_000_000
	p.mu.Unlock()

	_, err = enforce.Apply(context.Background(), p.cfg(), plan, plan.Fingerprint())
	if err == nil || !strings.Contains(err.Error(), "holds 2 unit budgets and held 1") {
		t.Errorf("a budget added behind the plan: %v", err)
	}
	if got := p.sent(); len(got) != 0 {
		t.Errorf("sent %v after refusing", got)
	}
}

// A refusal half way says how far it got, and nothing after it is sent.
func TestApplyStopsAtTheFirstRefusalAndSaysWhere(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/b-unit/"):
			posted = append(posted, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("unit is frozen"))
		default:
			posted = append(posted, r.URL.Path)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	cfg := enforce.Config{BaseURL: srv.URL, Key: "devkey"}
	plan, err := enforce.MakePlan(context.Background(), cfg, map[string]money.Cents{
		"a-unit": 1_00, "b-unit": 2_00, "c-unit": 3_00,
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := enforce.Apply(context.Background(), cfg, plan, plan.Fingerprint())
	if n != 1 {
		t.Errorf("applied %d, want 1 (a-unit) before the refusal", n)
	}
	if err == nil || !strings.Contains(err.Error(), "setting b-unit") ||
		!strings.Contains(err.Error(), "(1 of 3 were set before this)") ||
		!strings.Contains(err.Error(), "unit is frozen") {
		t.Errorf("refusal: %v", err)
	}
	if strings.Join(posted, ",") != "/v1/units/a-unit/budget,/v1/units/b-unit/budget" {
		t.Errorf("posted %v; nothing after the refusal may be sent", posted)
	}
}
