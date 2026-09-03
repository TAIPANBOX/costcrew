package main

// Stamp scoring: imported data, where nothing carries a driver label, so
// there is no known cause to hide or check against. B7-SPEC.md section 2's
// closing paragraph: "a case is an anomaly whose task has a posted or
// returned deliverable; the score is posted (accepted first pass) versus
// returned, per skill and per engine, from the rows, without any model
// call."

import (
	"database/sql"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// stampOutcome is what became of one task's deliverable.
type stampOutcome string

const (
	outcomePosted   stampOutcome = "posted"
	outcomeReturned stampOutcome = "returned"
)

// stampCase is one task whose most recent artifact settled, one way or the
// other, and the anomaly it came from.
type stampCase struct {
	Task    crew.Task
	Outcome stampOutcome
}

// selectStampCases reads the board rather than the packet: a task counts
// when its assignee matches -skill's role prefix and (when engine is a real
// engine name rather than one of the bench's own mocks, which no real
// analyst is ever hired with) -engine's hired route, it came from an
// anomaly, and its most recent deliverable is posted or returned.
//
// cases is capped at n, exactly like selectKnownCases; total is every
// matching row before that cap, so the caller can say when -n asked for
// more than the store actually holds.
func selectStampCases(db *sql.DB, skill, engine string, n int) (cases []stampCase, total int, err error) {
	prefix, err := rolePrefixForSkill(skill)
	if err != nil {
		return nil, 0, err
	}
	roster, err := crew.Roster(db)
	if err != nil {
		return nil, 0, err
	}
	byName := map[string]crew.Analyst{}
	for _, a := range roster {
		byName[a.Name] = a
	}

	all, err := crew.Tasks(db, crew.TaskFilter{})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	for _, t := range all {
		if t.Anomaly == "" {
			continue
		}
		a, ok := byName[t.Assignee]
		if !ok {
			continue
		}
		wantPrefix := prefix + "-"
		if len(a.Name) < len(wantPrefix) || a.Name[:len(wantPrefix)] != wantPrefix {
			continue
		}
		if !isMockEngine(engine) && engine != "" && a.Engine != engine {
			continue
		}
		outcome, ok := lastOutcome(db, t.ID)
		if !ok {
			continue
		}
		total++
		if len(cases) < n {
			cases = append(cases, stampCase{Task: t, Outcome: outcome})
		}
	}
	return cases, total, nil
}

// lastOutcome is the state of a task's most recent artifact, when that
// state is one this bench scores at all: drafts in progress say nothing
// about acceptance either way.
func lastOutcome(db *sql.DB, taskID int) (stampOutcome, bool) {
	as, err := crew.Artifacts(db, taskID)
	if err != nil || len(as) == 0 {
		return "", false
	}
	switch as[len(as)-1].State {
	case crew.PostedDraft:
		return outcomePosted, true
	case crew.ReturnedDraft:
		return outcomeReturned, true
	}
	return "", false
}
