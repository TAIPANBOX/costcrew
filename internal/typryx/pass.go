package typryx

import (
	"context"
	"database/sql"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

// Recorder is anomaly.Recorder: the journal, and the bus when it is on.
type Recorder = anomaly.Recorder

// unreachableRun is how many asks in a row may fail without typryx having
// answered at all (unreachable, a timeout) before a pass stops asking. A
// typryx that is down would otherwise cost every anomaly a full timeout.
// Anomalies the pass did not reach are left without a record, and the next
// pass asks about them.
const unreachableRun = 3

// Summary is what one pass did, for a log line.
type Summary struct {
	Asked, Hinted, NoHint int
	Stopped               bool // the pass stopped early: typryx stopped answering
}

// HintAnomalies asks typryx about each anomaly in ids, in order, stores the
// hint or the reason there is none, and reports each outcome on the journal
// and the bus. It never returns an error to stop detection or a run with:
// a store failure is counted as no hint, and the pass goes on.
//
// The anomaly itself is never touched beyond its hint columns: no state
// moves, no owner is set, no option is applied (invariant 76).
func HintAnomalies(ctx context.Context, db *sql.DB, c *Client, ids []string, rec Recorder) Summary {
	var s Summary
	if c == nil {
		return s
	}
	misses := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			s.Stopped = true
			return s
		}
		a, err := anomaly.Get(db, id)
		if err != nil {
			continue
		}
		h, eg := c.Ask(ctx, State(db, a))
		s.Asked++
		if err := anomaly.SaveHint(db, id, h); err != nil {
			s.NoHint++
			continue
		}
		emitHinted(rec, a, h, eg)
		if h.Answered() {
			s.Hinted++
			misses = 0
			continue
		}
		s.NoHint++
		if strings.Contains(h.Reason, "could not be reached") || strings.Contains(h.Reason, "did not answer within") {
			misses++
			if misses >= unreachableRun {
				s.Stopped = true
				return s
			}
		} else {
			misses = 0
		}
	}
	return s
}

// emitHinted puts one hint outcome on the journal and the bus.
//
// Which backend answered, which model, the class and its probability, the
// NAMES of the fields that left and how many typryx held back. Never a field's
// value: the anomaly line and the change registry are what an operator chose
// to send to typryx, not what the bus's other readers asked to see.
func emitHinted(rec Recorder, a anomaly.Anomaly, h anomaly.Hint, eg Egress) {
	if rec == nil {
		return
	}
	data := map[string]any{
		"anomaly":          a.ID,
		"template":         Template,
		"fields_sent":      strings.Join(eg.Fields, ","),
		"held_back_fields": eg.HeldBack,
		"latency_ms":       h.LatencyMS,
	}
	if h.Backend != "" {
		data["backend"] = h.Backend
	}
	if h.Answered() {
		data["outcome"] = "hinted"
		data["class"] = h.Class
		data["probability"] = h.Probability
		data["answer_id"] = h.AnswerID
		if h.Model != "" {
			data["model"] = h.Model
		}
	} else {
		data["outcome"] = "no_hint"
		data["reason"] = h.Reason
	}
	_ = rec.Emit("anomaly_hinted", "", "info", data, nil)
}
