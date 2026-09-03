package web

// C1-SPEC.md section 2: the owner column and the "told" mark on the
// anomalies list, the event that puts something in the journal for the
// mark to read, and the closure figure the anomaly page shows.

import (
	"fmt"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// anomalyRow is one queue row: the anomaly itself, plus who to tell and
// whether they already have been. Embedding anomaly.Anomaly rather than
// naming its fields again keeps every existing template reference
// (.ID, .Excess, .State, ...) working unchanged.
type anomalyRow struct {
	anomaly.Anomaly
	Owner string
	Told  bool
}

// toldAnomalies is every anomaly id THIS STEP'S OWN event has already told
// an owner about, read from the journal the way /cadence reads crew_ran
// (internal/web/cadence.go): a generous tail, filtered by kind. This
// console is the only writer of the event this page reads back, so a tail
// generous enough to outrun a day's postings is enough to outrun a day's
// worth of "told" marks too.
//
// Event name alone is not enough: internal/anomaly's own pre-existing
// state-transition emit ("anomaly_"+state, anomaly.go, unchanged by this
// step) fires the SAME event name, "anomaly_explained", on every
// Explain/Dismiss/Accept -- including the pre-existing direct
// POST /anomalies/{id}/explain route, which needs no task, team,
// deliverable or owner lookup at all. Only tellOwnerAnomalyExplained's own
// emission carries a non-empty "owner" field, so that is what distinguishes
// "an owner was actually told" from "an anomaly happened to change state".
// Found in review: a direct-explain on an anomaly with no team and no task
// rendered Owner="unclaimed" and Told="told" on the same row at once.
func (s *Server) toldAnomalies() map[string]bool {
	told := map[string]bool{}
	tail, err := s.st.JournalTail(2000)
	if err != nil {
		return told
	}
	for _, rec := range tail {
		if rec.Event != "anomaly_explained" {
			continue
		}
		if stringField(rec.Data, "owner") == "" {
			continue
		}
		if id := stringField(rec.Data, "anomaly"); id != "" {
			told[id] = true
		}
	}
	return told
}

// tellOwnerAnomalyExplained is C1-SPEC.md section 2's event: the moment a
// deliverable on an anomaly task is posted, the owner is told, with the
// named cause, the option classes offered, and the artifact id.
// anomaly_explained already exists on the wire (internal/stack/types.go);
// this call site extends its data rather than adding a type. It is
// deliberately a second call site from internal/anomaly's own
// "anomaly_"+state emit: that one fires when the anomaly's OWN state moves
// to Explained (an option later APPLIED, internal/finops.Apply); this one
// fires the moment a deliverable is POSTED, before anybody has decided
// anything about it, because a person still has to be told there is
// something to decide.
//
// Called only after crew.Post has already succeeded (internal/web/work.go):
// emitting first, and finding out afterwards that the post was refused (an
// artifact already posted, a task with no artifact at all), would tell an
// owner about something that never actually happened.
func (s *Server) tellOwnerAnomalyExplained(artifactID int) {
	if s.rec == nil {
		return
	}
	taskID, err := crew.TaskOfArtifact(s.db, artifactID)
	if err != nil {
		return
	}
	t, err := crew.GetTask(s.db, taskID)
	if err != nil || t.Anomaly == "" {
		return // not an anomaly task: nothing here to tell an owner about
	}
	owner, reason := crew.OwnerOfAnomaly(s.db, t.Anomaly)

	opts, _ := crew.Options(s.db, artifactID)
	classes := make([]string, 0, len(opts))
	cause := ""
	for _, o := range opts {
		classes = append(classes, o.Class)
		if o.Class == "anomaly.explain" && cause == "" {
			cause = o.Summary
		}
	}

	_ = s.rec.Emit("anomaly_explained", t.Assignee, "info", map[string]any{
		"anomaly":        t.Anomaly,
		"artifact":       artifactID,
		"task":           taskID,
		"owner":          owner,
		"owner_reason":   reason,
		"cause":          cause,
		"option_classes": classes,
	}, nil)
}

// daysText is the anomaly page's own closure figure: open for N days while
// open, closed after N days once it is not. anomaly.DaysBetween is the one
// basis this and the closure KPI (internal/finops.AnomalyClosureDays) both
// read, so the two can never silently disagree about what a day means. A
// detected_at that will not parse says so honestly rather than printing an
// invented count.
func daysText(a anomaly.Anomaly, now time.Time) string {
	if a.ClosedAt != "" {
		if d, ok := anomaly.DaysBetween(a.DetectedAt, a.ClosedAt); ok {
			return fmt.Sprintf("closed after %d %s", d, plural(d, "day", "days"))
		}
		return "closed, but detected_at does not parse, so no day count can be trusted"
	}
	if d, ok := anomaly.DaysBetween(a.DetectedAt, now.UTC().Format(time.RFC3339)); ok {
		return fmt.Sprintf("open for %d %s", d, plural(d, "day", "days"))
	}
	return "open, but detected_at does not parse, so no day count can be trusted"
}
