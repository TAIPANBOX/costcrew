package web

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// agentStop is one time this agent could not finish something, and why.
type agentStop struct {
	When   string
	Kind   string // returned, or blocked
	Task   string
	Reason string
	Spent  money.Cents // what the task had cost by the time it stopped
}

// stopsFor reads an agent's stops from the BOARD, not from the event stream.
//
// The stream holds what this console has done since the governance plane was
// switched on, which on most installations is a handful of clicks. The board
// holds what the agent actually did: three hundred tasks, two hundred and
// seventy-nine artifacts, every return with the reason somebody typed. Reading
// the second is the difference between a panel that says "nothing yet" on
// thirty-nine cards and one that answers the question people open a card to
// ask, which is what goes wrong with this agent.
//
// Nothing is written anywhere to make this work. Deriving these into the
// agent-event stream was the other option and was rejected: that stream is
// read by trailryx, heraldyx and idryx, several hundred reconstructed lines
// carrying dates months old would arrive at the end of an append-only record
// as though they were new, and trailryx would refuse every one of them because
// none is in the shared vocabulary. Noise in somebody else's log, to fill a
// panel in this one.
func stopsFor(db *sql.DB, name string) ([]agentStop, error) {
	// Two questions in one list, ordered by when they happened. A returned
	// artifact and a blocked task are both "this stopped", and a reader
	// scanning for a pattern should not have to merge two tables by eye.
	rows, err := db.Query(`
		SELECT a.created, 'returned', t.title, COALESCE(a.reason,''),
		       COALESCE(t.spent_cents,0)
		  FROM artifacts a JOIN tasks t ON t.id = a.task
		 WHERE a.author = ? AND a.state = 'returned'
		UNION ALL
		SELECT COALESCE(t.updated, t.created), 'blocked', t.title,
		       COALESCE(t.reason,''), COALESCE(t.spent_cents,0)
		  FROM tasks t
		 WHERE t.assignee = ? AND t.state = 'blocked'
		 ORDER BY 1 DESC`, name, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []agentStop
	for rows.Next() {
		var s agentStop
		var cents int64
		if err := rows.Scan(&s.When, &s.Kind, &s.Task, &s.Reason, &cents); err != nil {
			return nil, err
		}
		s.Spent = money.Cents(cents)
		out = append(out, s)
	}
	return out, rows.Err()
}

// stopSummary is the sentence above the table.
type stopSummary struct {
	Returned int
	Blocked  int
	Spent    money.Cents // what the stopped work had cost by the time it stopped
	Latest   string
}

func summariseStops(stops []agentStop) stopSummary {
	var s stopSummary
	for _, x := range stops {
		switch x.Kind {
		case "returned":
			s.Returned++
		case "blocked":
			s.Blocked++
		}
		// Counted because it is money already spent on work that produced
		// nothing yet, which is the figure a guard does not show: a guard is
		// about what an agent may spend, not about what it spent and had sent
		// back.
		s.Spent += x.Spent
		if x.When > s.Latest {
			s.Latest = x.When
		}
	}
	return s
}
