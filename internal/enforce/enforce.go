// Package enforce pushes this console's budgets to the thing that can stop a
// call: TokenFuse's control plane.
//
// # Why this package is not like the other three
//
// heraldyx, trailryx and genaryx all PULL. This console writes a file, they
// read it on their own schedule, and the worst a bad line can do is be
// refused. Nothing here can take one of them down.
//
// This is the opposite. It makes an HTTP call to another service and CHANGES
// something there, and the thing it changes is the number a gateway uses to
// decide whether to refuse a model call. A budget pushed too low stops real
// traffic; pushed too high, the guard does not bite; pushed at all, it may
// overwrite a figure somebody set by hand for a reason this console does not
// know.
//
// So the package is built to be hard to fire by accident:
//
//   - it is OFF unless an address and a key are configured;
//   - MakePlan sends nothing. It reads what is set now, compares, and returns
//     exactly what would change and in which direction;
//   - Apply takes the plan that was shown and refuses if the remote has moved
//     since, because a diff somebody approved is not that diff once the
//     numbers under it have changed;
//   - a budget that would go DOWN is counted separately, because that is the
//     direction that stops work;
//   - nothing is ever deleted. This console can raise, lower or add a budget;
//     removing one is a decision about somebody else's system.
//
// # What it does NOT do
//
// It does not push per-agent budgets, though this console has them. TokenFuse
// binds an agent to a unit through its identity map, and which credential may
// speak as which agent is a decision made there, inside the perimeter the
// operator runs. Writing that map from here would be this console asserting an
// identity binding it has no way to verify.
package enforce

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Config is where the control plane is and how to speak to it. A zero Config
// means this is switched off, which is the default.
type Config struct {
	BaseURL string // e.g. http://127.0.0.1:8788
	Key     string // the bearer token; never rendered, never journalled
}

func (c Config) On() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Key) != ""
}

// Change is one budget this console would set, and what is there now.
type Change struct {
	Unit    string
	Now     money.Cents // what the control plane has; zero when it has none
	HasNow  bool
	Want    money.Cents // what this console's budgets say
	Lowered bool        // the direction that stops work
	New     bool
}

// Plan is what would happen, and it is the only thing a caller sees before
// anything is sent.
type Plan struct {
	Changes   []Change
	Unchanged int
	Lowered   int
	Added     int
	// Snapshot is what the remote said when this plan was made. Apply refuses
	// if it no longer matches.
	Snapshot map[string]money.Cents
}

func (p Plan) Empty() bool { return len(p.Changes) == 0 }

// Fingerprint identifies THIS plan: what it would set, and what was there when
// it was made.
//
// It exists because a two-step that re-plans on the second step is not a
// two-step. A person reads a diff, thinks about it, and runs the apply; if the
// apply builds its own plan, what they approved and what they sent are two
// different things, and nothing anywhere would say so. Passing the
// fingerprint back is what makes the approval bind to the diff it was given
// for.
func (p Plan) Fingerprint() string {
	h := sha256.New()
	units := make([]string, 0, len(p.Snapshot))
	for u := range p.Snapshot {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		fmt.Fprintf(h, "was\x00%s\x00%d\n", u, int64(p.Snapshot[u]))
	}
	for _, c := range p.Changes {
		fmt.Fprintf(h, "set\x00%s\x00%d\n", c.Unit, int64(c.Want))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

type client struct {
	cfg Config
	c   *http.Client
}

func newClient(cfg Config) *client {
	return &client{cfg: cfg, c: &http.Client{Timeout: 10 * time.Second}}
}

func (cl *client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader = bytes.NewReader(nil)
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(cl.cfg.BaseURL, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.cfg.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		// The control plane's own words about a refusal are more use than a
		// status code on its own.
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, msg)
	}
	return out, nil
}

// Current reads the unit budgets the control plane holds, in cents.
//
// The wire carries MICROS, a ten-thousandth of a cent. Converting on the way
// in and out rather than carrying two units through this package is
// deliberate: this console's money is integer cents everywhere else, and a
// second unit in one package is how a factor of ten thousand gets lost.
func (cl *client) Current(ctx context.Context) (map[string]money.Cents, error) {
	raw, err := cl.do(ctx, http.MethodGet, "/v1/unit-budgets", nil)
	if err != nil {
		return nil, err
	}
	var micros map[string]int64
	if err := json.Unmarshal(raw, &micros); err != nil {
		return nil, fmt.Errorf("the control plane answered something that is not a "+
			"unit-to-budget map: %w", err)
	}
	out := make(map[string]money.Cents, len(micros))
	for unit, m := range micros {
		out[unit] = money.Cents(m / 10_000)
	}
	return out, nil
}

// Current is the exported reader, for a page that wants to show what is set
// without proposing anything.
func Current(ctx context.Context, cfg Config) (map[string]money.Cents, error) {
	if !cfg.On() {
		return nil, nil
	}
	return newClient(cfg).Current(ctx)
}

// MakePlan compares what this console wants with what the control plane has.
func MakePlan(ctx context.Context, cfg Config, want map[string]money.Cents) (Plan, error) {
	if !cfg.On() {
		return Plan{}, fmt.Errorf("enforcement is switched off: no control plane " +
			"address or key is configured")
	}
	cl := newClient(cfg)
	now, err := cl.Current(ctx)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Snapshot: now}
	units := make([]string, 0, len(want))
	for u := range want {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		w := want[u]
		if w <= 0 {
			// A budget of nothing is not a budget, it is a stop. This console
			// does not express one, so it does not push one.
			continue
		}
		cur, has := now[u]
		if has && cur == w {
			p.Unchanged++
			continue
		}
		ch := Change{Unit: u, Now: cur, HasNow: has, Want: w, New: !has}
		ch.Lowered = has && w < cur
		if ch.Lowered {
			p.Lowered++
		}
		if ch.New {
			p.Added++
		}
		p.Changes = append(p.Changes, ch)
	}
	return p, nil
}

// Apply sends the plan, after checking the remote has not moved under it.
//
// It stops at the first refusal rather than carrying on: a half-applied budget
// set is a state nobody asked for, and knowing where it stopped is what makes
// it recoverable.
func Apply(ctx context.Context, cfg Config, p Plan, expect string) (applied int, err error) {
	if !cfg.On() {
		return 0, fmt.Errorf("enforcement is switched off")
	}
	// The plan somebody approved, or nothing.
	if got := p.Fingerprint(); expect != got {
		return 0, fmt.Errorf("this is not the plan that was approved: it was shown as %s "+
			"and is now %s. Something changed between reading it and sending it, which is "+
			"exactly what this check is for. Look at it again", expect, got)
	}
	if p.Empty() {
		return 0, nil
	}
	cl := newClient(cfg)
	now, err := cl.Current(ctx)
	if err != nil {
		return 0, err
	}
	if len(now) != len(p.Snapshot) {
		return 0, fmt.Errorf("the control plane holds %d unit budgets and held %d when "+
			"this was proposed. Nothing was sent: look again", len(now), len(p.Snapshot))
	}
	for unit, was := range p.Snapshot {
		cur, ok := now[unit]
		if !ok || cur != was {
			return 0, fmt.Errorf("the control plane has changed since this was proposed "+
				"(%s was %s, is now %s). Nothing was sent: look again", unit, was, cur)
		}
	}
	for _, ch := range p.Changes {
		body := map[string]any{"budget_usd": ch.Want.Float()}
		if _, err := cl.do(ctx, http.MethodPost, "/v1/units/"+ch.Unit+"/budget", body); err != nil {
			return applied, fmt.Errorf("setting %s: %w (%d of %d were set before this)",
				ch.Unit, err, applied, len(p.Changes))
		}
		applied++
	}
	return applied, nil
}
