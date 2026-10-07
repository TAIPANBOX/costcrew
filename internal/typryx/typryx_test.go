package typryx

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The options typryx's own examples/templates/triage.anomaly_class.json names.
var exampleOptions = []string{"expected_growth", "misconfiguration", "price_change", "runaway_agent", "unknown"}

// fake is a typryx: GET /v1/templates and POST /v1/ask, the two routes this
// package reads, shaped exactly as typryx's internal/api serves them.
type fake struct {
	mu      sync.Mutex
	fields  []string
	options []string
	answer  func(state map[string]any) (status int, body string)
	asks    []map[string]any // the state of every ask, as received
	raw     []string         // every ask's raw body
	keys    []string         // the X-Typryx-Key of every request
	srv     *httptest.Server
}

func newFake(t *testing.T, fields []string, answer func(map[string]any) (int, string)) *fake {
	t.Helper()
	f := &fake{fields: fields, options: exampleOptions, answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.keys = append(f.keys, r.Header.Get("X-Typryx-Key"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/templates":
			_ = json.NewEncoder(w).Encode(map[string]any{"templates": []map[string]any{
				{"id": "eval.outcome_met", "version": "x", "type": "noul", "fields": []string{"task"}},
				{"id": Template, "version": "6b4497ae", "type": "choice", "fields": f.fields, "options": f.options},
			}})
		case "/v1/ask":
			b, _ := io.ReadAll(r.Body)
			var req struct {
				Template string         `json:"template"`
				State    map[string]any `json:"state"`
			}
			_ = json.Unmarshal(b, &req)
			f.mu.Lock()
			f.asks = append(f.asks, req.State)
			f.raw = append(f.raw, string(b))
			f.mu.Unlock()
			status, body := f.answer(req.State)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) client() *Client { return New(f.srv.URL, "k1", 2*time.Second) }

func (f *fake) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asks)
}

// answerWith is a valid choice answer from backend, typryx's own wire shape.
func answerWith(backend, model, class string, p float64) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) {
		probs := map[string]float64{}
		rest := (1 - p) / float64(len(exampleOptions)-1)
		for _, o := range exampleOptions {
			probs[o] = rest
		}
		probs[class] = p
		b, _ := json.Marshal(map[string]any{
			"answer_id": "ans-0001", "template": Template, "template_version": "6b4497ae",
			"type": "choice", "answer": class, "probabilities": probs,
			"backend": backend, "model": model, "latency_ms": 12, "held_back_fields": 0,
		})
		return 200, string(b)
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, s := range []string{estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// secretTeam and secretAgent are on the anomaly and must never leave.
const secretTeam = "team-that-must-not-leave"
const secretAgent = "agent://customer.example/agent-that-must-not-leave"

func plant(t *testing.T, db *sql.DB, id string) anomaly.Anomaly {
	t.Helper()
	a := anomaly.Anomaly{
		ID: id, Source: "aws", Team: secretTeam, Service: "Amazon EC2", Day: "2026-07-14",
		Direction: "up", Amount: money.Cents(1_000_00), Baseline: money.Cents(300_00),
		Excess: money.Cents(700_00), Z: 6.2, RuleVer: anomaly.RuleVersion,
		CausedBy: secretAgent, CausedByKind: "agent", State: anomaly.Open,
		DetectedAt: "2026-07-15T00:00:00Z",
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		 handled_by, state, reason, detected_at, closed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,'',?,NULL,?,?,NULL,?,NULL,?,NULL)`,
		a.ID, a.Source, a.Team, a.Service, a.Day, a.Direction, int64(a.Amount), int64(a.Baseline),
		int64(a.Excess), a.Z, a.RuleVer, a.CausedBy, a.CausedByKind, string(a.State), a.DetectedAt); err != nil {
		t.Fatal(err)
	}
	if err := estate.InsertDriver(db, world.Driver{Start: "2026-07-10", End: "2026-07-10", Scope: "Amazon EC2",
		Label: "batch fleet resized to 40 nodes", Kind: "one-time", Source: "aws"}); err != nil {
		t.Fatal(err)
	}
	return a
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------- only the template

func TestOnlyTheTemplatesFieldsLeave(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-fields")
	f := newFake(t, []string{"anomaly", "recent_changes"}, answerWith("jev", "jev-1.13.0", "runaway_agent", 0.81))

	h, eg := f.client().Ask(context.Background(), State(db, a))
	if !h.Answered() {
		t.Fatalf("no hint: %s", h.Reason)
	}
	if f.askCount() != 1 {
		t.Fatalf("%d asks reached typryx, want 1", f.askCount())
	}
	if got := keysOf(f.asks[0]); strings.Join(got, ",") != "anomaly,recent_changes" {
		t.Errorf("the state that left carries %v, want exactly the template's fields [anomaly recent_changes]", got)
	}
	if strings.Join(eg.Fields, ",") != "anomaly,recent_changes" {
		t.Errorf("Egress names %v", eg.Fields)
	}
	for _, never := range []string{secretTeam, secretAgent, `"team"`, `"caused_by"`, `"owner"`} {
		if strings.Contains(f.raw[0], never) {
			t.Errorf("the ask's body carries %q, which must never leave:\n%s", never, f.raw[0])
		}
	}
	if !strings.Contains(f.asks[0]["recent_changes"].(string), "batch fleet resized to 40 nodes") {
		t.Errorf("recent_changes does not carry the registered change: %q", f.asks[0]["recent_changes"])
	}
}

// The template decides: a template naming one field gets that one field.
func TestTheTemplateDecidesWhichFieldsLeave(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-one-field")
	f := newFake(t, []string{"service"}, answerWith("stub", "stub-0", "unknown", 0.4))
	if h, _ := f.client().Ask(context.Background(), State(db, a)); !h.Answered() {
		t.Fatalf("no hint: %s", h.Reason)
	}
	if got := keysOf(f.asks[0]); strings.Join(got, ",") != "service" {
		t.Errorf("the state that left carries %v, want [service]", got)
	}
	if strings.Contains(f.raw[0], "batch fleet") || strings.Contains(f.raw[0], "robust deviations") {
		t.Errorf("fields the template does not name left anyway:\n%s", f.raw[0])
	}
}

// A template that names a field outside the offered vocabulary (a team, an
// owner) gets no hint, and nothing is asked at all.
func TestATemplateNamingAFieldThisConsoleDoesNotSendGetsNoHint(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-team-field")
	for _, field := range []string{"team", "caused_by", "owner"} {
		f := newFake(t, []string{"anomaly", field}, answerWith("jev", "", "unknown", 0.5))
		h, _ := f.client().Ask(context.Background(), State(db, a))
		if h.Answered() {
			t.Errorf("%s: a hint came back for a template asking for %q", field, field)
		}
		if !strings.Contains(h.Reason, field) {
			t.Errorf("%s: the reason does not name the field: %q", field, h.Reason)
		}
		if f.askCount() != 0 {
			t.Errorf("%s: an ask was sent anyway", field)
		}
	}
}

// --------------------------------------------------------- the data modes

func TestEachBackendIsRecordedWithItsDataMode(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-modes")
	for backend, want := range map[string]string{
		"jev": anomaly.BackendJev, "openai-logprobs": anomaly.BackendOwnModel, "stub": anomaly.BackendOff,
	} {
		f := newFake(t, []string{"anomaly", "recent_changes"}, answerWith(backend, "m-1", "price_change", 0.6))
		h, _ := f.client().Ask(context.Background(), State(db, a))
		if !h.Answered() || h.Backend != want || h.Class != "price_change" || h.Probability != 0.6 || h.Model != "m-1" {
			t.Errorf("%s: got %+v, want backend %s, price_change at 0.6", backend, h, want)
		}
	}
	f := newFake(t, []string{"anomaly"}, answerWith("some-new-backend", "", "unknown", 0.5))
	if h, _ := f.client().Ask(context.Background(), State(db, a)); h.Answered() || h.Backend != "" {
		t.Errorf("an unknown backend was recorded as a hint: %+v", h)
	}
}

// ---------------------------------------------------------- hostile bytes

func TestHostileAnswersAreNoHintNeverAGuess(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-hostile")
	const leak = "SECRET-BODY-TEXT"
	ok := func(mut func(m map[string]any)) func(map[string]any) (int, string) {
		return func(map[string]any) (int, string) {
			_, body := answerWith("jev", "jev-1.13.0", "runaway_agent", 0.8)(nil)
			var m map[string]any
			_ = json.Unmarshal([]byte(body), &m)
			mut(m)
			b, _ := json.Marshal(m)
			return 200, string(b)
		}
	}
	probs := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	cases := map[string]func(map[string]any) (int, string){
		"unanswered with a reason": func(map[string]any) (int, string) {
			return 200, `{"answer_id":"a","unanswered":true,"reason":"timeout"}`
		},
		"unanswered with junk reason":   func(map[string]any) (int, string) { return 200, `{"unanswered":true,"reason":"` + leak + `<script>"}` },
		"a class outside the options":   ok(func(m map[string]any) { m["answer"] = "aliens" }),
		"an answer that is a number":    ok(func(m map[string]any) { m["answer"] = 3 }),
		"not the most probable class":   ok(func(m map[string]any) { m["answer"] = "unknown" }),
		"probabilities not summing":     ok(func(m map[string]any) { m["probabilities"].(map[string]any)["unknown"] = 0.9 }),
		"a negative probability":        ok(func(m map[string]any) { m["probabilities"].(map[string]any)["unknown"] = -0.2 }),
		"a probability above one":       ok(func(m map[string]any) { m["probabilities"].(map[string]any)["runaway_agent"] = 1.5 }),
		"an extra probability key":      ok(func(m map[string]any) { m["probabilities"].(map[string]any)["aliens"] = 0.0 }),
		"a missing probability key":     ok(func(m map[string]any) { delete(m["probabilities"].(map[string]any), "unknown") }),
		"a probability that is text":    ok(func(m map[string]any) { m["probabilities"] = probs("runaway_agent", "0.8") }),
		"no answer id":                  ok(func(m map[string]any) { delete(m, "answer_id") }),
		"an answer id with a newline":   ok(func(m map[string]any) { m["answer_id"] = "a\nb" }),
		"another template":              ok(func(m map[string]any) { m["template"] = "eval.outcome_met" }),
		"not a choice":                  ok(func(m map[string]any) { m["type"] = "noul" }),
		"not JSON":                      func(map[string]any) (int, string) { return 200, leak + " {" },
		"a megabyte of JSON":            func(map[string]any) (int, string) { return 200, `{"x":"` + strings.Repeat(leak, 70_000) + `"}` },
		"HTTP 500 with a body":          func(map[string]any) (int, string) { return 500, leak },
		"HTTP 401, typryx's own code":   func(map[string]any) (int, string) { return 401, `{"error":"unauthorized"}` },
		"HTTP 429 with a hostile code":  func(map[string]any) (int, string) { return 429, `{"error":"` + leak + `"}` },
		"an empty body":                 func(map[string]any) (int, string) { return 200, `` },
		"a backend name with a newline": ok(func(m map[string]any) { m["backend"] = "jev\nx" }),
	}
	for name, ans := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, []string{"anomaly", "recent_changes"}, ans)
			h, _ := f.client().Ask(context.Background(), State(db, a))
			if h.Answered() {
				t.Fatalf("a hint was recorded: %+v", h)
			}
			if h.Reason == "" {
				t.Fatal("no hint and no reason")
			}
			if strings.Contains(h.Reason, leak) || strings.Contains(h.Reason, "<script>") {
				t.Errorf("the reason echoes the response: %q", h.Reason)
			}
			if h.Probability != 0 {
				t.Errorf("a refused answer kept a probability: %v", h.Probability)
			}
		})
	}
}

// A model name that is not a plain identifier is dropped, the hint kept.
func TestAHostileModelNameIsDroppedNotShown(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-model")
	f := newFake(t, []string{"anomaly"}, answerWith("jev", "<script>alert(1)</script>", "runaway_agent", 0.7))
	h, _ := f.client().Ask(context.Background(), State(db, a))
	if !h.Answered() || h.Model != "" {
		t.Errorf("got %+v, want the hint with no model", h)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a redirect was followed to %s, carrying key %q", r.URL.Path, r.Header.Get("X-Typryx-Key"))
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	db := testDB(t)
	a := plant(t, db, "A-redirect")
	h, _ := New(srv.URL, "k1", time.Second).Ask(context.Background(), State(db, a))
	if h.Answered() || !strings.Contains(h.Reason, "HTTP 307") {
		t.Errorf("got %+v, want no hint naming the redirect status", h)
	}
}

// ------------------------------------------------- slow, down, refused

func TestATimeoutIsNoHintWithAReason(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	db := testDB(t)
	a := plant(t, db, "A-slow")
	start := time.Now()
	h, _ := New(srv.URL, "", 150*time.Millisecond).Ask(context.Background(), State(db, a))
	if h.Answered() || !strings.Contains(h.Reason, "did not answer within 150ms") {
		t.Errorf("got %+v, want no hint, timed out", h)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a 150ms timeout took %s", took)
	}
}

func TestUnreachableIsNoHintWithAReason(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	db := testDB(t)
	a := plant(t, db, "A-down")
	h, _ := New(base, "", time.Second).Ask(context.Background(), State(db, a))
	if h.Answered() || h.Reason != "typryx could not be reached" {
		t.Errorf("got %+v", h)
	}
}

func TestATypryxWithoutTheTemplateIsNoHint(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-no-template")
	f := newFake(t, nil, answerWith("jev", "", "unknown", 0.5))
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"templates":[{"id":"eval.outcome_met","type":"noul","fields":["task"]}]}`)
	})
	h, _ := f.client().Ask(context.Background(), State(db, a))
	if h.Answered() || !strings.Contains(h.Reason, "does not serve the triage.anomaly_class template") {
		t.Errorf("got %+v", h)
	}
}

// ------------------------------------------------------------ the key

func TestTheKeyTravelsInItsHeaderAndNowhereElse(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-key")
	f := newFake(t, []string{"anomaly"}, answerWith("jev", "", "unknown", 0.5))
	c := New(f.srv.URL, "the-key-value", time.Second)
	c.Ask(context.Background(), State(db, a))
	for _, k := range f.keys {
		if k != "the-key-value" {
			t.Errorf("a request carried key %q", k)
		}
	}
	if strings.Contains(f.raw[0], "the-key-value") {
		t.Error("the key is in the ask's body")
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "  ": "", "http://127.0.0.1:4320": "http://127.0.0.1:4320",
		"https://typryx.internal/": "https://typryx.internal",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"127.0.0.1:4320", "ftp://x", "http://", "http://user:pw@host", "http://h/?k=v", "http://h/#f", "http://h?",
	} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) was accepted", bad)
		}
	}
}

func TestANilClientAsksNothing(t *testing.T) {
	if New("", "k", time.Second) != nil {
		t.Fatal("an empty URL built a client")
	}
	var c *Client
	h, eg := c.Ask(context.Background(), map[string]string{"anomaly": "x"})
	if h.Answered() || len(eg.Fields) != 0 {
		t.Errorf("a nil client answered: %+v %+v", h, eg)
	}
	if s := HintAnomalies(context.Background(), nil, nil, []string{"A-1"}, nil); s != (Summary{}) {
		t.Errorf("a nil client's pass did something: %+v", s)
	}
}

func TestTheURLAndTheKeyComeFromTheirEnvironmentTwins(t *testing.T) {
	t.Setenv(URLEnv, "http://127.0.0.1:4320")
	t.Setenv(KeyEnv, "  k-from-env \n")
	if URLEnvDefault() != "http://127.0.0.1:4320" || KeyFromEnv() != "k-from-env" {
		t.Errorf("URLEnvDefault %q, KeyFromEnv %q", URLEnvDefault(), KeyFromEnv())
	}
	if c := New("http://127.0.0.1:4320", "", 0); c == nil || c.timeout != DefaultTimeout {
		t.Errorf("a zero timeout did not fall back to DefaultTimeout: %+v", c)
	}
}

// The registered changes are capped at ten, in date order, with a
// trailing count, so one desk's long registry cannot grow the ask.
func TestRecentChangesAreCappedAtTenAndScopedToTheDeskAndService(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-many")
	for i := 0; i < 14; i++ {
		day := fmt.Sprintf("2026-07-%02d", i+1)
		if err := estate.InsertDriver(db, world.Driver{Start: day, End: day, Scope: "*",
			Label: "change " + day, Kind: "one-time", Source: "aws"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []world.Driver{
		{Start: "2026-07-10", End: "2026-07-10", Scope: "*", Label: "other desk", Kind: "one-time", Source: "gcp"},
		{Start: "2026-07-10", End: "2026-07-10", Scope: "Amazon S3", Label: "other service", Kind: "one-time", Source: "aws"},
		{Start: "2026-05-01", End: "2026-05-02", Scope: "*", Label: "too old", Kind: "one-time", Source: "aws"},
	} {
		if err := estate.InsertDriver(db, d); err != nil {
			t.Fatal(err)
		}
	}
	got := State(db, a)["recent_changes"]
	if !strings.HasSuffix(got, "and 5 more") || strings.Count(got, "\n") != 10 {
		t.Errorf("want ten changes and 'and 5 more', got:\n%s", got)
	}
	for _, never := range []string{"other desk", "other service", "too old"} {
		if strings.Contains(got, never) {
			t.Errorf("recent_changes carries %q", never)
		}
	}
	empty := testDB(t)
	b := plant(t, empty, "A-none")
	if _, err := empty.Exec(`DELETE FROM drivers`); err != nil {
		t.Fatal(err)
	}
	if got := State(empty, b)["recent_changes"]; !strings.HasPrefix(got, "no change registered") {
		t.Errorf("an empty registry reads %q", got)
	}
}

func TestATemplateThatIsNotAChoiceIsNoHint(t *testing.T) {
	db := testDB(t)
	a := plant(t, db, "A-noul")
	f := newFake(t, nil, answerWith("jev", "", "unknown", 0.5))
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"templates":[{"id":"triage.anomaly_class","type":"noul","fields":["anomaly"]}]}`)
	})
	h, _ := f.client().Ask(context.Background(), State(db, a))
	if h.Answered() || !strings.Contains(h.Reason, "is not a choice template") {
		t.Errorf("got %+v", h)
	}
}
