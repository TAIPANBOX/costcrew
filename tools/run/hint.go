package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/typryx"
)

// hintTasks asks typryx for a typed hint on the anomaly behind each task
// about to be worked, before its packet is built, so the analyst reads the
// hint (invariant 76). Only from -live: a dry run makes no outbound call of
// any kind, and a hint asked of a hosted backend is metered by that backend.
//
// An anomaly already answered is not asked again. Every failure becomes "no
// hint" with a reason, and none of it stops the run: the return value is for
// the console line only.
func hintTasks(db *sql.DB, tx *typryx.Client, tasks []crew.Task, b bus) (typryx.Summary, error) {
	if tx == nil {
		return typryx.Summary{}, nil
	}
	if err := anomaly.EnsureHintColumns(db); err != nil {
		return typryx.Summary{}, fmt.Errorf("adding the hint columns: %w", err)
	}
	seen := map[string]bool{}
	var ids []string
	for _, t := range tasks {
		if t.Anomaly == "" || seen[t.Anomaly] {
			continue
		}
		seen[t.Anomaly] = true
		if h, ok, err := anomaly.HintOf(db, t.Anomaly); err == nil && ok && h.Answered() {
			continue
		}
		ids = append(ids, t.Anomaly)
	}
	var rec anomaly.Recorder = b.rec
	if b.em != nil && b.em.On() {
		rec = store.Tee(b.rec, b.em)
	}
	return typryx.HintAnomalies(context.Background(), db, tx, ids, rec), nil
}
