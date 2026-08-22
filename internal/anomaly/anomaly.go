// Package anomaly turns a detection into something with a life.
//
// A finding recomputed on every page load cannot be assigned, explained or
// dismissed, because there is nothing for a decision to attach to: dismiss it
// on Monday and it is back on Tuesday. Everything here follows from fixing
// that, and the identity is the part that carries the weight.
package anomaly

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Recorder is how an anomaly's life reaches the outside world.
//
// An interface and not a direct dependency on the stack package, so this one
// stays about anomalies: the store does not know what an agent-event is, and
// a nil recorder is a perfectly good answer to "governance is switched off".
type Recorder interface {
	Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error
}

// RuleVersion is part of the identity on purpose. Retune the detector and the
// same day becomes a NEW anomaly rather than quietly changing the numbers
// underneath a decision somebody already made about the old one.
const RuleVersion = "v1"

type State string

const (
	Open      State = "open"      // detected, nobody has looked
	Triaged   State = "triaged"   // an analyst owns it
	Explained State = "explained" // there is an answer, awaiting a person
	Accepted  State = "accepted"  // the answer stands
	Dismissed State = "dismissed" // not worth pursuing, and why
)

// closed states are the ones a re-detection must not reopen.
var closed = map[State]bool{Accepted: true, Dismissed: true}

type Anomaly struct {
	ID      string
	Source  string
	Team    string
	Service string
	Day     string

	Direction string
	Amount    money.Cents
	Baseline  money.Cents
	Excess    money.Cents
	Z         float64
	Rule      string
	RuleVer   string
	Driver    string

	// Two different questions, deliberately two different columns.
	CausedBy     string // whose spend produced it
	CausedByKind string // agent, team, or unknown: the grain, never implied
	HandledBy    string // which analyst took it

	State      State
	Reason     string
	DetectedAt string
	ClosedAt   string
}

const Schema = `
CREATE TABLE IF NOT EXISTS anomalies(
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL, team TEXT, service TEXT NOT NULL, day TEXT NOT NULL,
  direction TEXT NOT NULL,
  amount_cents INTEGER NOT NULL, baseline_cents INTEGER NOT NULL,
  excess_cents INTEGER NOT NULL, z REAL NOT NULL,
  rule TEXT, rule_version TEXT NOT NULL, driver TEXT,
  caused_by TEXT, caused_by_kind TEXT, handled_by TEXT,
  state TEXT NOT NULL, reason TEXT,
  detected_at TEXT NOT NULL, closed_at TEXT);
CREATE INDEX IF NOT EXISTS anomalies_state ON anomalies(state, excess_cents);
CREATE INDEX IF NOT EXISTS anomalies_series ON anomalies(source, team, service);
`

// ID is derived from what the anomaly IS, never assigned.
//
// An auto-increment key would mint a fresh row on every detection run, so
// every dismissal would come back the next morning and no state could
// survive. Deriving it means the same underlying event resolves to the same
// object forever, and a change to the rule deliberately produces a different
// one instead of mutating a decision's subject underneath it.
func ID(source, team, service, day, ruleVersion string) string {
	h := sha256.Sum256([]byte(strings.Join(
		[]string{source, team, service, day, ruleVersion}, "\x00")))
	return "A-" + hex.EncodeToString(h[:6])
}

// Run detects across the whole estate and reconciles the result with what is
// already stored.
//
// Reconcile, not replace. A run must not disturb a decision: a closed anomaly
// stays closed, an owned one keeps its owner, and only genuinely new findings
// arrive as open. Returns how many were new.
func Run(db *sql.DB, now time.Time, cfg detect.Config, rec Recorder) (found, added int, err error) {
	if _, err := db.Exec(Schema); err != nil {
		return 0, 0, err
	}
	// This pass, named, so every finding it raises belongs to the same run.
	//
	// The stack's contract says a run is "one execution of an agent", and a
	// detection pass is exactly that: the detector woke, read the estate, and
	// raised what it found. Minting one run per FINDING instead would put nine
	// runs of one record into a store that shards and indexes by run, and a
	// query for the run would answer with one row where the answer is nine.
	pass := "detect-" + strconv.FormatInt(now.UTC().Unix(), 10)
	drivers, err := estate.Drivers(db)
	if err != nil {
		return 0, 0, err
	}
	var dd []detect.Driver
	for _, d := range drivers {
		dd = append(dd, detect.Driver{
			Start: d.Start, End: d.End, Scope: d.Scope, Label: d.Label, Kind: d.Kind,
		})
	}

	series, err := estate.AllSeries(db)
	if err != nil {
		return 0, 0, err
	}
	stamp := now.UTC().Format(time.RFC3339)

	for _, k := range series {
		days, vals, err := estate.SeriesDays(db, k)
		if err != nil {
			return found, added, err
		}
		pts := make([]detect.Point, len(days))
		for i := range days {
			pts[i] = detect.Point{Day: days[i], Amount: vals[i]}
		}
		for _, f := range detect.Find(pts, k.Service, dd, cfg) {
			found++
			a := Anomaly{
				ID:         ID(k.Source, k.Team, k.Service, f.Day, RuleVersion),
				Source:     k.Source,
				Team:       k.Team,
				Service:    k.Service,
				Day:        f.Day,
				Direction:  string(f.Direction),
				Amount:     f.Amount,
				Baseline:   f.Baseline,
				Excess:     f.Excess,
				Z:          f.Z,
				Rule:       f.Rule,
				RuleVer:    RuleVersion,
				Driver:     f.Driver,
				State:      Open,
				DetectedAt: stamp,
			}
			a.CausedBy, a.CausedByKind, err = attribute(db, k, f.Day)
			if err != nil {
				return found, added, err
			}
			isNew, err := upsert(db, a)
			if err != nil {
				return found, added, err
			}
			if isNew {
				added++
				emit(rec, "anomaly_detected", "", a, map[string]any{"pass": pass})
			}
		}
	}
	return found, added, nil
}

// attribute answers "whose spend was this" at the finest grain that can be
// PROVEN, and reports the grain alongside the answer.
//
// The grain is not decoration. Naming a team where an agent is meant overstates
// what is known, and a console that overstates once is not trusted again.
func attribute(db *sql.DB, k estate.SeriesKey, day string) (who, kind string, err error) {
	agent, _, err := estate.AgentFor(db, k, day)
	if err != nil {
		return "", "", err
	}
	if agent != "" {
		return agent, "agent", nil
	}
	if k.Team != "" {
		return k.Team, "team", nil
	}
	return "", "unknown", nil
}

// upsert writes a newly detected anomaly, or leaves an existing one alone.
func upsert(db *sql.DB, a Anomaly) (bool, error) {
	var state string
	err := db.QueryRow(`SELECT state FROM anomalies WHERE id=?`, a.ID).Scan(&state)
	switch {
	case err == sql.ErrNoRows:
		_, err = db.Exec(`INSERT INTO anomalies
			(id, source, team, service, day, direction, amount_cents, baseline_cents,
			 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
			 handled_by, state, reason, detected_at, closed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Source, nullIf(a.Team), a.Service, a.Day, a.Direction,
			int64(a.Amount), int64(a.Baseline), int64(a.Excess), a.Z,
			a.Rule, a.RuleVer, nullIf(a.Driver), nullIf(a.CausedBy), a.CausedByKind,
			nil, string(Open), nil, a.DetectedAt, nil)
		return err == nil, err
	case err != nil:
		return false, err
	}
	// Already known. The measurements are refreshed because a later run may
	// see a longer history, but the state and its owner are decisions and are
	// never touched by a detector.
	_, err = db.Exec(`UPDATE anomalies SET amount_cents=?, baseline_cents=?,
		excess_cents=?, z=?, driver=? WHERE id=?`,
		int64(a.Amount), int64(a.Baseline), int64(a.Excess), a.Z, nullIf(a.Driver), a.ID)
	return false, err
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// emit reports one moment in an anomaly's life, and never gets in the way.
//
// A failure here is swallowed on purpose. The console's job is to show the
// estate; if the event stream is unwritable the right outcome is a gap a
// verifier can see, not a page that will not render.
func emit(rec Recorder, kind, actor string, a Anomaly, extra map[string]any) {
	if rec == nil {
		return
	}
	data := map[string]any{
		"anomaly":        a.ID,
		"source":         a.Source,
		"service":        a.Service,
		"day":            a.Day,
		"direction":      a.Direction,
		"excess_cents":   int64(a.Excess),
		"excess":         a.Excess.String(),
		"rule_version":   a.RuleVer,
		"caused_by":      a.CausedBy,
		"caused_by_kind": a.CausedByKind,
	}
	if a.Team != "" {
		data["team"] = a.Team
	}
	if a.Driver != "" {
		data["driver"] = a.Driver
	}
	for k, v := range extra {
		data[k] = v
	}
	_ = rec.Emit(kind, actor, severityOf(a.Excess), data, nil)
}

// severityOf is duplicated deliberately rather than imported from the stack
// package: this one decides how loud an anomaly is, which is a product
// judgement, and the stack package's is about the envelope. Tying them
// together would make a change to one silently change the other.
func severityOf(excess money.Cents) string {
	switch a := excess.Abs(); {
	case a >= money.Cents(5_000_00):
		return "critical"
	case a >= money.Cents(1_000_00):
		return "high"
	case a >= money.Cents(200_00):
		return "medium"
	default:
		return "low"
	}
}

// ------------------------------------------------------------- transitions

var (
	ErrNotFound   = errors.New("no such anomaly")
	ErrNeedReason = errors.New("this decision needs a reason")
	ErrClosed     = errors.New("this anomaly is already closed")
)

// Assign gives an anomaly an owner. The only transition that needs no reason,
// because taking something on is not a decision anybody has to justify later.
func Assign(db *sql.DB, id, analyst string, rec Recorder) error {
	return transition(db, id, Triaged, "", rec, func(cur State) error {
		if closed[cur] {
			return ErrClosed
		}
		return nil
	}, analyst)
}

// Explain records an answer. The reason IS the deliverable here, so an empty
// one is refused rather than stored as an empty string that reads, later, as
// though nobody could think of anything.
func Explain(db *sql.DB, id, reason string, rec Recorder) error {
	return transition(db, id, Explained, reason, rec, func(cur State) error {
		if closed[cur] {
			return ErrClosed
		}
		return nil
	}, "")
}

// Accept closes an anomaly with its explanation standing.
func Accept(db *sql.DB, id, reason string, rec Recorder) error {
	return transition(db, id, Accepted, reason, rec, func(cur State) error {
		if cur != Explained && cur != Triaged {
			return fmt.Errorf("%w: nothing has been explained yet", ErrNeedReason)
		}
		return nil
	}, "")
}

// Dismiss closes an anomaly as not worth pursuing.
//
// The reason is mandatory and it is the whole point: a dismissal without one
// is indistinguishable from nobody having looked, and six months later that
// difference is the only thing anybody wants to know.
func Dismiss(db *sql.DB, id, reason string, rec Recorder) error {
	return transition(db, id, Dismissed, reason, rec, func(cur State) error {
		if closed[cur] {
			return ErrClosed
		}
		return nil
	}, "")
}

func transition(db *sql.DB, id string, to State, reason string, rec Recorder,
	allowed func(State) error, analyst string) error {

	if to != Triaged && strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	var cur string
	err := db.QueryRow(`SELECT state FROM anomalies WHERE id=?`, id).Scan(&cur)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := allowed(State(cur)); err != nil {
		return err
	}

	closedAt := any(nil)
	if closed[to] {
		closedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if analyst != "" {
		if _, err = db.Exec(`UPDATE anomalies SET state=?, handled_by=?, closed_at=? WHERE id=?`,
			string(to), analyst, closedAt, id); err != nil {
			return err
		}
	} else if _, err = db.Exec(`UPDATE anomalies SET state=?, reason=?, closed_at=? WHERE id=?`,
		string(to), reason, closedAt, id); err != nil {
		return err
	}

	// Read it back before reporting it: an event describing a state the store
	// does not hold is worse than no event, because it is believed.
	a, err := Get(db, id)
	if err != nil {
		return nil
	}
	actor := a.HandledBy
	extra := map[string]any{"to": string(to), "from": cur}
	if reason != "" {
		extra["reason"] = reason
	}
	if analyst != "" {
		extra["assigned_to"] = analyst
	}
	emit(rec, "anomaly_"+string(to), actor, a, extra)
	return nil
}

// ------------------------------------------------------------------ queries

type Filter struct {
	State     State
	Source    string
	Direction string
	CausedBy  string
	HandledBy string
}

// List returns anomalies ranked by money, which is the order a person can work
// through. Ranking by z puts a four-sigma move worth three dollars above a
// two-sigma one worth nine thousand.
func List(db *sql.DB, f Filter) ([]Anomaly, error) {
	q := `SELECT id, source, COALESCE(team,''), service, day, direction,
		amount_cents, baseline_cents, excess_cents, z, COALESCE(rule,''),
		rule_version, COALESCE(driver,''), COALESCE(caused_by,''),
		COALESCE(caused_by_kind,''), COALESCE(handled_by,''), state,
		COALESCE(reason,''), detected_at, COALESCE(closed_at,'')
		FROM anomalies WHERE 1=1`
	var args []any
	add := func(clause string, v string) {
		if v != "" {
			q += clause
			args = append(args, v)
		}
	}
	add(" AND state=?", string(f.State))
	add(" AND source=?", f.Source)
	add(" AND direction=?", f.Direction)
	add(" AND caused_by=?", f.CausedBy)
	add(" AND handled_by=?", f.HandledBy)
	q += " ORDER BY ABS(excess_cents) DESC, day DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Anomaly
	for rows.Next() {
		var a Anomaly
		var amt, base, exc int64
		var state string
		if err := rows.Scan(&a.ID, &a.Source, &a.Team, &a.Service, &a.Day,
			&a.Direction, &amt, &base, &exc, &a.Z, &a.Rule, &a.RuleVer,
			&a.Driver, &a.CausedBy, &a.CausedByKind, &a.HandledBy, &state,
			&a.Reason, &a.DetectedAt, &a.ClosedAt); err != nil {
			return nil, err
		}
		a.Amount, a.Baseline, a.Excess = money.Cents(amt), money.Cents(base), money.Cents(exc)
		a.State = State(state)
		out = append(out, a)
	}
	return out, rows.Err()
}

func Get(db *sql.DB, id string) (Anomaly, error) {
	all, err := List(db, Filter{})
	if err != nil {
		return Anomaly{}, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return Anomaly{}, ErrNotFound
}

// Counts is the summary a tab header shows, in a fixed order so the row does
// not reshuffle as work moves through it.
func Counts(db *sql.DB) ([]struct {
	State State
	N     int
}, error) {
	rows, err := db.Query(`SELECT state, COUNT(*) FROM anomalies GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[State]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		seen[State(s)] = n
	}
	order := []State{Open, Triaged, Explained, Accepted, Dismissed}
	var out []struct {
		State State
		N     int
	}
	for _, s := range order {
		out = append(out, struct {
			State State
			N     int
		}{s, seen[s]})
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out, rows.Err()
}
