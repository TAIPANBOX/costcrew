// Package typryx asks TAIPANBOX/typryx for a typed hint about an anomaly
// before an analyst works it: the class typryx's triage.anomaly_class
// template picks, with its probability and the backend that answered
// (invariant 76).
//
// This is the console's third door to the network (invariant 77), and it is
// narrow on purpose. It is reached only when an operator passed -typryx-url,
// only from the console's start (cmd/costcrew, after detection) and from the
// runner's -live path, never from a page handler. What leaves is decided by
// the template: this package asks typryx which fields the template names and
// sends exactly those, from a closed vocabulary that never includes a team,
// an owner, an agent or a person. typryx holds back anything else on its own
// side as well; this side does not rely on it.
//
// A hint decides nothing. Every failure (unreachable, a timeout, a refusal, an
// answer that does not validate) becomes "no hint" with a reason, stored and
// shown as such, and never stops detection or a run.
package typryx

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// Template is the one typryx template this console asks.
const Template = "triage.anomaly_class"

// The environment twins. The key is read from the environment and from
// nowhere else: never a flag (a flag is visible in a process listing), never
// written, never logged.
const (
	URLEnv = "COSTCREW_TYPRYX_URL"
	KeyEnv = "COSTCREW_TYPRYX_KEY"
)

// keyHeader is typryx's own credential header (internal/door.KeyHeader).
const keyHeader = "X-Typryx-Key"

// DefaultTimeout bounds one ask, end to end. A local 7B model answers in
// about two seconds on a CPU (typryx-evalset, 2026-09-30); a hint that takes
// longer than this is not worth holding a start for.
const DefaultTimeout = 5 * time.Second

// maxBody caps what is read back from typryx. Its own answers are a few
// hundred bytes; anything near this is not an answer.
const maxBody = 64 << 10

// URLEnvDefault is -typryx-url's default, the same shape -gateway's is.
func URLEnvDefault() string { return os.Getenv(URLEnv) }

// KeyFromEnv is the credential sent as X-Typryx-Key, or empty for a typryx
// with no keys configured (loopback only, by typryx's own rule).
func KeyFromEnv() string { return strings.TrimSpace(os.Getenv(KeyEnv)) }

// NormalizeURL validates -typryx-url: empty is off; anything else must be an
// absolute http or https URL with a host and no credentials, query or
// fragment, the same rule typryx applies to its own upstream URLs. A trailing
// slash is dropped.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("-typryx-url must be an absolute http or https URL, e.g. http://127.0.0.1:4320")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("-typryx-url must carry no credentials, query or fragment; the key goes in %s", KeyEnv)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Client asks one typryx. A nil *Client is "typryx is not configured", and
// every method on it is a no-op, so a caller never needs a second branch.
type Client struct {
	base    string
	key     string
	timeout time.Duration
	hc      *http.Client

	tmpl *templateView // fetched once, on the first ask that succeeds in reading it
}

// New returns a client for base, or nil when base is empty. base must already
// have been through NormalizeURL.
func New(base, key string, timeout time.Duration) *Client {
	if base == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		base: base, key: key, timeout: timeout,
		hc: &http.Client{
			Timeout: timeout,
			// A redirect would send the key and the fields somewhere the
			// operator did not name.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// ---------------------------------------------------------------- the state

// Offered is the closed vocabulary of fields this console is willing to send
// at all. The template picks from it; a template that names a field outside
// it gets no hint, never a guess at what the field should hold. There is no
// team, owner, agent, analyst or person here, and none may be added without
// changing invariant 76.
var Offered = []string{"anomaly", "recent_changes", "desk", "service", "day", "direction", "excess"}

// State builds every offered field for one anomaly. Only the ones the
// template names ever leave (Ask filters).
//
// typryx may hand these to a hosted model, so the installation's
// -prompt-data setting governs them exactly as it governs a packet
// (invariant 70): under masked every name the store holds is replaced by its
// token and a driver's label is withheld; under aggregates the service is not
// offered at all (a template naming it gets no hint), the anomaly line
// carries no service, and no registered change is sent.
func State(db *sql.DB, a anomaly.Anomaly) map[string]string {
	pol := deliver.ActivePolicy()
	line := fmt.Sprintf("desk %s, service %s, day %s: daily spend moved %s, %s against a baseline of %s "+
		"(excess %s, %.1f robust deviations)",
		a.Source, a.Service, a.Day, a.Direction, a.Amount, a.Baseline, a.Excess, a.Z)
	if pol.Aggregates() {
		line = fmt.Sprintf("desk %s, day %s: daily spend moved %s, %s against a baseline of %s "+
			"(excess %s, %.1f robust deviations)",
			a.Source, a.Day, a.Direction, a.Amount, a.Baseline, a.Excess, a.Z)
	}
	st := map[string]string{
		"anomaly":        line,
		"recent_changes": recentChanges(db, a),
		"desk":           a.Source,
		"service":        a.Service,
		"day":            a.Day,
		"direction":      a.Direction,
		"excess":         a.Excess.String(),
	}
	if pol.Full() {
		return st
	}
	if pol.Aggregates() {
		delete(st, "service")
		st["recent_changes"] = "registered changes are not sent under this installation's -prompt-data setting"
	}
	for k, v := range st {
		st[k] = pol.MaskText(v)
	}
	return st
}

// recentChanges lists the registered drivers on the anomaly's desk that apply
// to its service and were in force during the 30 days up to its day: the
// changes a person triaging it would look at first. Labels and kinds only.
func recentChanges(db *sql.DB, a anomaly.Anomaly) string {
	ds, err := estate.Drivers(db)
	if err != nil {
		return "the change registry could not be read"
	}
	from := a.Day
	if t, err := time.Parse("2006-01-02", a.Day); err == nil {
		from = t.AddDate(0, 0, -30).Format("2006-01-02")
	}
	var lines []string
	for _, d := range ds {
		if d.Source != a.Source || (d.Scope != "*" && d.Scope != a.Service) {
			continue
		}
		if d.End < from || d.Start > a.Day {
			continue
		}
		label := d.Label
		if !deliver.ActivePolicy().Full() {
			// Text somebody typed is withheld, never scrubbed (invariant 70).
			label = deliver.WithheldLabel
		}
		lines = append(lines, fmt.Sprintf("%s to %s, %s: %s", d.Start, d.End, d.Kind, label))
	}
	if len(lines) == 0 {
		return "no change registered on this desk and service in the 30 days up to the anomaly"
	}
	sort.Strings(lines)
	if len(lines) > 10 {
		lines = append(lines[:10], fmt.Sprintf("and %d more", len(lines)-10))
	}
	return strings.Join(lines, "\n")
}

// -------------------------------------------------------------- the template

type templateView struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Type    string   `json:"type"`
	Fields  []string `json:"fields"`
	Options []string `json:"options"`
}

func (c *Client) template(ctx context.Context) (*templateView, string) {
	if c.tmpl != nil {
		return c.tmpl, ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/templates", nil)
	if err != nil {
		return nil, "the typryx URL could not be used"
	}
	body, status, reason := c.do(req)
	if reason != "" {
		return nil, reason
	}
	if status != http.StatusOK {
		return nil, refusalReason(status, body)
	}
	var all struct {
		Templates []templateView `json:"templates"`
	}
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, "typryx's template list did not parse"
	}
	for _, t := range all.Templates {
		if t.ID != Template {
			continue
		}
		if t.Type != "choice" || len(t.Options) == 0 || len(t.Fields) == 0 {
			return nil, "typryx's " + Template + " is not a choice template with options and fields"
		}
		tt := t
		c.tmpl = &tt
		return c.tmpl, ""
	}
	return nil, "typryx does not serve the " + Template + " template"
}

// Egress is what an ask actually sent: the field NAMES, never their values.
type Egress struct {
	Fields   []string
	HeldBack int // fields typryx itself reported holding back; 0 when this side sent only the template's
}

// pick returns exactly the template's fields from state, or a reason.
func pick(fields []string, state map[string]string) (map[string]string, []string, string) {
	offered := map[string]bool{}
	for _, f := range Offered {
		offered[f] = true
	}
	out := map[string]string{}
	var names []string
	for _, f := range fields {
		if !offered[f] {
			return nil, nil, fmt.Sprintf("the template asks for a field this console does not send (%q)", clip(f, 40))
		}
		v, ok := state[f]
		if !ok {
			return nil, nil, fmt.Sprintf("the template asks for a field this console does not send (%q)", clip(f, 40))
		}
		out[f] = v
		names = append(names, f)
	}
	sort.Strings(names)
	return out, names, ""
}

// ------------------------------------------------------------------ the ask

type askResult struct {
	AnswerID        string             `json:"answer_id"`
	Template        string             `json:"template"`
	TemplateVersion string             `json:"template_version"`
	Type            string             `json:"type"`
	Answer          json.RawMessage    `json:"answer"`
	Probabilities   map[string]float64 `json:"probabilities"`
	Backend         string             `json:"backend"`
	Model           string             `json:"model"`
	HeldBackFields  int                `json:"held_back_fields"`
	Unanswered      bool               `json:"unanswered"`
	Reason          string             `json:"reason"`
}

var (
	tokenRe    = regexp.MustCompile(`^[a-z0-9_]{1,48}$`)
	answerIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)
	modelRe    = regexp.MustCompile(`^[A-Za-z0-9_.:/@+-]{1,80}$`)
)

// backendLabel maps typryx's own backend name onto this console's three data
// modes. Anything else is refused: a hint whose source cannot be named is
// not shown as though it had one.
func backendLabel(b string) (string, bool) {
	switch b {
	case "jev":
		return anomaly.BackendJev, true
	case "openai-logprobs":
		return anomaly.BackendOwnModel, true
	case "stub":
		return anomaly.BackendOff, true
	}
	return "", false
}

// Ask sends one anomaly's state, filtered to the template's fields, and
// returns the hint or the reason there is none. It never returns an error:
// every failure is a Hint with a Reason, because nothing a caller could do
// with an error is different from recording the reason.
func (c *Client) Ask(ctx context.Context, state map[string]string) (anomaly.Hint, Egress) {
	start := time.Now()
	h, eg := c.ask(ctx, state)
	h.LatencyMS = time.Since(start).Milliseconds()
	h.At = time.Now().UTC().Format(time.RFC3339)
	if !h.Answered() {
		h.Class, h.Probability = "", 0
	}
	return h, eg
}

func (c *Client) ask(ctx context.Context, state map[string]string) (anomaly.Hint, Egress) {
	if c == nil {
		return anomaly.Hint{Reason: "typryx is not configured"}, Egress{}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tmpl, reason := c.template(ctx)
	if reason != "" {
		return anomaly.Hint{Reason: reason}, Egress{}
	}
	sent, names, reason := pick(tmpl.Fields, state)
	if reason != "" {
		return anomaly.Hint{Reason: reason}, Egress{}
	}
	eg := Egress{Fields: names}
	payload, err := json.Marshal(map[string]any{"template": Template, "state": sent})
	if err != nil {
		return anomaly.Hint{Reason: "the state could not be encoded"}, eg
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/ask", bytes.NewReader(payload))
	if err != nil {
		return anomaly.Hint{Reason: "the typryx URL could not be used"}, eg
	}
	req.Header.Set("Content-Type", "application/json")
	body, status, reason := c.do(req)
	if reason != "" {
		return anomaly.Hint{Reason: reason}, eg
	}
	if status != http.StatusOK {
		return anomaly.Hint{Reason: refusalReason(status, body)}, eg
	}
	var r askResult
	if err := json.Unmarshal(body, &r); err != nil {
		return anomaly.Hint{Reason: "typryx's answer did not parse"}, eg
	}
	eg.HeldBack = r.HeldBackFields
	return validate(r, tmpl.Options), eg
}

// do sends one request and reads at most maxBody back. reason is set for a
// failure that never produced a status: unreachable, a timeout, a body too
// large. The transport's own error text is never returned, so nothing the
// network says reaches a page.
func (c *Client) do(req *http.Request) (body []byte, status int, reason string) {
	if c.key != "" {
		req.Header.Set(keyHeader, c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, 0, fmt.Sprintf("typryx did not answer within %s", c.timeout)
		}
		if errors.Is(err, context.Canceled) {
			return nil, 0, "the ask was cancelled"
		}
		return nil, 0, "typryx could not be reached"
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, 0, fmt.Sprintf("typryx did not answer within %s", c.timeout)
		}
		return nil, 0, "typryx's answer could not be read"
	}
	if len(body) > maxBody {
		return nil, 0, "typryx's answer was larger than any answer it gives"
	}
	return body, resp.StatusCode, ""
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// refusalReason names a non-200 by its status and typryx's own error code,
// and only when the code is one of typryx's short tokens; any other body is
// not echoed.
func refusalReason(status int, body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if tokenRe.MatchString(e.Error) {
		return fmt.Sprintf("typryx refused the ask: HTTP %d, %s", status, e.Error)
	}
	return fmt.Sprintf("typryx refused the ask: HTTP %d", status)
}

// validate turns typryx's answer into a hint, refusing anything that is not
// exactly the shape a choice answer has: one of the template's own options,
// with a probability distribution over exactly those options, the answer
// carrying the highest one. typryx already derives the answer from the
// distribution; this side checks it again rather than trusting the label.
func validate(r askResult, options []string) anomaly.Hint {
	if r.Unanswered {
		if tokenRe.MatchString(r.Reason) {
			return anomaly.Hint{Reason: "typryx answered unanswered: " + r.Reason}
		}
		return anomaly.Hint{Reason: "typryx answered unanswered"}
	}
	label, ok := backendLabel(r.Backend)
	if !ok {
		return anomaly.Hint{Reason: "typryx answered from a backend this console does not know"}
	}
	bad := func(what string) anomaly.Hint {
		return anomaly.Hint{Backend: label, Reason: "typryx's answer did not validate: " + what}
	}
	if r.Template != "" && r.Template != Template {
		return bad("it names another template")
	}
	if r.Type != "" && r.Type != "choice" {
		return bad("it is not a choice")
	}
	if !answerIDRe.MatchString(r.AnswerID) {
		return bad("its answer id is not one typryx mints")
	}
	var class string
	if err := json.Unmarshal(r.Answer, &class); err != nil {
		return bad("its answer is not a class name")
	}
	want := map[string]bool{}
	for _, o := range options {
		want[o] = true
	}
	if !want[class] {
		return bad("its answer is not one of the template's options")
	}
	if len(r.Probabilities) != len(options) {
		return bad("its probabilities do not cover exactly the template's options")
	}
	sum, top := 0.0, 0.0
	for k, p := range r.Probabilities {
		if !want[k] {
			return bad("its probabilities do not cover exactly the template's options")
		}
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return bad("a probability is outside 0 to 1")
		}
		sum += p
		if p > top {
			top = p
		}
	}
	if math.Abs(sum-1) > 0.01 {
		return bad("its probabilities do not sum to one")
	}
	if r.Probabilities[class] < top {
		return bad("its answer is not the most probable class")
	}
	model := ""
	if modelRe.MatchString(r.Model) {
		model = r.Model
	}
	return anomaly.Hint{
		Class: class, Probability: r.Probabilities[class], Backend: label,
		Model: model, AnswerID: r.AnswerID,
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
