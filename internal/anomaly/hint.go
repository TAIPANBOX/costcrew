package anomaly

// A typed hint (invariant 76): what typryx's triage.anomaly_class template
// said about an anomaly, kept on the anomaly's own row beside the backend
// that said it.
//
// A hint is a suggestion and nothing else. It is written by SaveHint alone,
// and SaveHint touches the hint columns and nothing else on the row: not the
// state, not the owner, not the reason. Nothing in this repository reads a
// hint to decide anything; the page and the packet print it, labelled, and
// that is the whole of what it does.
//
// The columns exist only on a store whose console or runner was started with
// -typryx-url (EnsureHintColumns). Every reader here treats a store without
// them as a store with no hint, so an installation that never configured
// typryx carries no new column and renders exactly what it rendered before
// (invariant 77).

import (
	"database/sql"
	"errors"
	"strings"
)

// The three data modes a hint can come from, named the way the stack's
// launchers name typryx's modes. typryx itself reports its backend as stub,
// openai-logprobs or jev; internal/typryx maps those onto these, and refuses
// any other name rather than storing a word nobody defined.
const (
	BackendJev      = "jev"       // TypeSafe AI's hosted model: the template's fields leave for a processor
	BackendOwnModel = "own-model" // typryx's openai-logprobs backend, pointed at a model the operator runs
	BackendOff      = "off"       // typryx's stub backend: deterministic, free, not a model's judgement
)

// Hint is one answer, or one recorded failure to get one.
type Hint struct {
	Class       string  // the class typryx answered; empty when there is no hint
	Probability float64 // the probability typryx gave that class
	Backend     string  // BackendJev, BackendOwnModel or BackendOff
	Model       string  // the model the backend named, when it named one
	AnswerID    string  // typryx's own answer id, for its ledger
	Reason      string  // why there is no hint, when there is none
	LatencyMS   int64   // how long the ask took, as this console measured it
	At          string  // RFC 3339, when the ask finished
	// FieldsSent names the fields that left for typryx, comma separated, ""
	// when none did (typryx was unreachable, its template was refused, the
	// state lacked a field). HeldBack is how many typryx itself reported
	// holding back. FieldsKnown is false on a row recorded before this
	// console kept them (invariant 92): then nobody can say what was sent.
	FieldsSent  string
	HeldBack    int64
	FieldsKnown bool
}

// Fields is FieldsSent as a list.
func (h Hint) Fields() []string {
	if h.FieldsSent == "" {
		return nil
	}
	return strings.Split(h.FieldsSent, ",")
}

// Answered says whether this is a hint rather than a recorded failure.
func (h Hint) Answered() bool { return h.Class != "" }

// hintColumns are added in this order; a column already present is skipped,
// so the migration runs on every start and changes nothing the second time
// (invariant 11).
var hintColumns = []struct{ name, decl string }{
	{"hint_class", "TEXT"},
	{"hint_probability", "REAL"},
	{"hint_backend", "TEXT"},
	{"hint_model", "TEXT"},
	{"hint_answer_id", "TEXT"},
	{"hint_reason", "TEXT"},
	{"hint_latency_ms", "INTEGER"},
	{"hint_at", "TEXT"},
	// Invariant 92: what left for typryx. NULL on a row recorded before.
	{"hint_fields_sent", "TEXT"},
	{"hint_held_back", "INTEGER"},
}

// EnsureHintColumns adds the hint columns to anomalies when they are missing.
func EnsureHintColumns(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	have, err := columnsOf(db)
	if err != nil {
		return err
	}
	for _, c := range hintColumns {
		if have[c.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE anomalies ADD COLUMN ` + c.name + ` ` + c.decl); err != nil {
			return err
		}
	}
	return nil
}

func columnsOf(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('anomalies')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// ErrNoHintColumns is SaveHint's answer on a store EnsureHintColumns never ran
// on: writing a hint is never a reason to migrate a store behind its
// operator's back.
var ErrNoHintColumns = errors.New("this store has no hint columns: EnsureHintColumns has not run")

// SaveHint writes a hint, or a recorded failure, onto the anomaly's row.
//
// The statement names the hint columns and no other: a hint never moves an
// anomaly's state, owner or reason. A failure never overwrites an answer
// already stored, because a timeout today says nothing against the answer
// typryx gave yesterday.
func SaveHint(db *sql.DB, id string, h Hint) error {
	q := `UPDATE anomalies SET hint_class=?, hint_probability=?, hint_backend=?,
		hint_model=?, hint_answer_id=?, hint_reason=?, hint_latency_ms=?, hint_at=?
		WHERE id=?`
	if !h.Answered() {
		q += ` AND hint_class IS NULL`
	}
	var class, prob any
	if h.Answered() {
		class, prob = h.Class, h.Probability
	}
	res, err := db.Exec(q, class, prob, nullIf(h.Backend), nullIf(h.Model),
		nullIf(h.AnswerID), nullIf(h.Reason), h.LatencyMS, nullIf(h.At), id)
	if err != nil {
		if strings.Contains(err.Error(), "no such column") {
			return ErrNoHintColumns
		}
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// The same row, the same guard: only when the hint above was written.
		if _, err := db.Exec(`UPDATE anomalies SET hint_fields_sent=?, hint_held_back=? WHERE id=?`,
			h.FieldsSent, h.HeldBack, id); err != nil {
			if strings.Contains(err.Error(), "no such column") {
				return ErrNoHintColumns
			}
			return err
		}
	}
	if n, _ := res.RowsAffected(); n == 0 && h.Answered() {
		if _, err := Get(db, id); errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
	}
	return nil
}

// HintOf reads an anomaly's hint. ok is false when nothing was ever recorded
// for it, and on a store that has no hint columns at all.
func HintOf(db *sql.DB, id string) (h Hint, ok bool, err error) {
	var class, backend, model, answer, reason, at sql.NullString
	var prob sql.NullFloat64
	var lat sql.NullInt64
	err = db.QueryRow(`SELECT hint_class, hint_probability, hint_backend, hint_model,
		hint_answer_id, hint_reason, hint_latency_ms, hint_at FROM anomalies WHERE id=?`, id).
		Scan(&class, &prob, &backend, &model, &answer, &reason, &lat, &at)
	switch {
	case err == sql.ErrNoRows:
		return Hint{}, false, nil
	case err != nil && strings.Contains(err.Error(), "no such column"):
		return Hint{}, false, nil
	case err != nil:
		return Hint{}, false, err
	}
	if !class.Valid && !reason.Valid {
		return Hint{}, false, nil
	}
	h = Hint{
		Class: class.String, Probability: prob.Float64, Backend: backend.String,
		Model: model.String, AnswerID: answer.String, Reason: reason.String,
		LatencyMS: lat.Int64, At: at.String,
	}
	// Read apart from the rest, so a store whose hint columns predate these
	// two still shows the hint it has (with nothing known about the fields).
	var fields sql.NullString
	var held sql.NullInt64
	ferr := db.QueryRow(`SELECT hint_fields_sent, hint_held_back FROM anomalies WHERE id=?`, id).
		Scan(&fields, &held)
	if ferr != nil && !strings.Contains(ferr.Error(), "no such column") {
		return Hint{}, false, ferr
	}
	h.FieldsSent, h.HeldBack, h.FieldsKnown = fields.String, held.Int64, fields.Valid
	return h, true, nil
}

// NeedingHint lists, largest by money first, the anomalies a hint pass should
// ask about: still open or being worked, and with no answer stored yet. A
// recorded failure is asked about again; an answer never is, so one anomaly
// costs one answered ask at most.
func NeedingHint(db *sql.DB, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT id FROM anomalies
		WHERE state IN (?, ?) AND hint_class IS NULL
		ORDER BY ABS(excess_cents) DESC, day DESC, id LIMIT ?`,
		string(Open), string(Triaged), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Source names where a hint came from, in words a reader of the page or the
// packet can act on: whether a hosted processor saw the fields, whether it
// was the operator's own model, or whether no model was involved at all.
func (h Hint) Source() string {
	switch h.Backend {
	case BackendJev:
		return "Jev, hosted by TypeSafe AI (the template's fields left for that processor)"
	case BackendOwnModel:
		return "the operator's own model, through typryx's openai-logprobs backend"
	case BackendOff:
		return "typryx's stub backend: typed answers are off, and this is not a model's judgement"
	}
	return "typryx, with no backend answering"
}
