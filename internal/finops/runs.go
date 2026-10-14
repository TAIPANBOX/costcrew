package finops

// The run grain (invariant 101). Every row the TokenFuse FOCUS reader keeps
// names the run it belongs to (x_run_id), and a run can call more than one
// model: an agent that starts on a large model and finishes on a small one is
// one run with two models in it. The Cloud's runs view, the console that reads
// it and anything that names a run by its LAST model call such a run by the
// model that did the least of its work (measured on forge 2026-10-14: an Opus
// then Haiku run with 96% of its spend on Opus read as a Haiku run). Here a run
// lists every model it called, in the order it first called each, with each
// one's share of the run's spend.

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// RunModel is one model a run called.
type RunModel struct {
	Model string
	Calls int
	Cost  money.Micros
	// First is when the run first called this model, which is what orders a
	// run's models: the order of a switch, not the size of each share.
	First string
	// share is Cost against the run's own spend, in tenths of a percent;
	// -1 when the run spent nothing.
	share int64
}

// Share is the model's part of the run's spend as a person reads it: "96%"
// (rounded down), "4.0%" below ten, "<0.1%" for a sliver, and "no spend" when
// the run spent nothing (every call blocked).
func (m RunModel) Share() string {
	switch {
	case m.share < 0:
		return "no spend"
	case m.share == 0 && m.Cost > 0:
		return "<0.1%"
	case m.share < 100:
		return fmt.Sprintf("%d.%d%%", m.share/10, m.share%10)
	default:
		// Rounded down, so only a model that did all of a run's spending
		// reads 100% beside another model's sliver.
		return fmt.Sprintf("%d%%", m.share/10)
	}
}

// RunRow is one run's month: what it spent, on which models, and how it
// ended as far as the gateway's own rows show.
type RunRow struct {
	// RunID is the run's id; "" for calls that named no run.
	RunID  string
	Agents []string
	Teams  []string
	Calls  int
	Tokens int64
	Cost   money.Micros
	// First and Last are the run's first and last call, as the gateway
	// settled them.
	First, Last string
	// Models in the order the run first called each.
	Models []RunModel
	// LastModel is the model of the run's last call: what a view that names a
	// run by one model shows.
	LastModel string
	// BlockedCalls and Blocked are the run's blocked calls, by reason.
	BlockedCalls int
	Blocked      []ReasonCount
	// Killed is true when the gateway refused one of the run's calls because
	// the run had been killed. A kill no call ran into is not in the export.
	Killed bool
	// LastBlocked is true when the run's last call was blocked, and
	// LastReason is why (as ReasonCount.Label reads it).
	LastBlocked bool
	LastReason  string
	// WarnedCalls and WarnedCost are the run's calls forwarded under identity
	// warn mode (invariant 100).
	WarnedCalls int
	WarnedCost  money.Micros
	// Doors is the gateway doors the run's calls were read through, main
	// first (invariant 102). Two of them is one run spending against two
	// ledgers: each door keeps its own budget for it.
	Doors []DoorSpend
}

// CrossedDoors is true when the run's calls came through more than one door.
func (r RunRow) CrossedDoors() bool { return len(r.Doors) > 1 }

// Switched is true when the run called more than one model.
func (r RunRow) Switched() bool { return len(r.Models) > 1 }

// State is how the run ended as the export shows it: killed, stopped by the
// gateway on its last call, refused some calls and went on, or ran.
func (r RunRow) State() string {
	switch {
	case r.Killed:
		return "killed"
	case r.LastBlocked:
		return "stopped by the gateway: " + r.LastReason
	case r.BlockedCalls > 0:
		return fmt.Sprintf("%d call%s refused, then went on", r.BlockedCalls, map[bool]string{true: "", false: "s"}[r.BlockedCalls == 1])
	default:
		return "ran"
	}
}

// BlockedText is the run's blocked calls by reason, "2 budget_exceeded, 1
// killed".
func (r RunRow) BlockedText() string {
	parts := make([]string, 0, len(r.Blocked))
	for _, b := range r.Blocked {
		parts = append(parts, fmt.Sprintf("%d %s", b.Calls, b.Label()))
	}
	return strings.Join(parts, ", ")
}

// KilledReason is the Breaker's wire string for a call refused because its
// run had been killed.
const KilledReason = "killed"

// AIRuns reads the month's runs from ai_calls, largest spend first, ties on
// the latest activity and then the run id, so the page renders the same every
// time (invariant 7).
func AIRuns(db *sql.DB, month string) ([]RunRow, error) {
	rows, err := db.Query(`SELECT COALESCE(run_id,''), ts, agent, COALESCE(team,''), model,
			tokens_in+tokens_out, billed_microusd, blocked,
			block_reason IS NOT NULL, COALESCE(block_reason,''),
			CASE WHEN `+connectors.IdentityWarnedSQL+` THEN 1 ELSE 0 END,
			COALESCE(door,'`+connectors.DoorMain+`')
		FROM ai_calls WHERE day LIKE ?
		ORDER BY 1, ts, rowid`, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		row     RunRow
		agents  map[string]bool
		teams   map[string]bool
		models  map[string]int // index into row.Models
		reasons map[ReasonCount]int
		doors   map[string]*DoorSpend
	}
	byRun := map[string]*acc{}
	var order []string
	for rows.Next() {
		var run, ts, agent, team, model, reason, door string
		var tokens, micros int64
		var blocked, known, warned bool
		if err := rows.Scan(&run, &ts, &agent, &team, &model, &tokens, &micros,
			&blocked, &known, &reason, &warned, &door); err != nil {
			return nil, err
		}
		a := byRun[run]
		if a == nil {
			a = &acc{row: RunRow{RunID: run, First: ts}, agents: map[string]bool{},
				teams: map[string]bool{}, models: map[string]int{}, reasons: map[ReasonCount]int{},
				doors: map[string]*DoorSpend{}}
			byRun[run] = a
			order = append(order, run)
		}
		r := &a.row
		r.Calls++
		r.Tokens += tokens
		r.Cost += money.Micros(micros)
		a.agents[agent] = true
		if team != "" {
			a.teams[team] = true
		}
		i, ok := a.models[model]
		if !ok {
			i = len(r.Models)
			a.models[model] = i
			r.Models = append(r.Models, RunModel{Model: model, First: ts})
		}
		r.Models[i].Calls++
		r.Models[i].Cost += money.Micros(micros)
		// Rows arrive in time order within a run, so the last one read is the
		// run's last call; a tie on the instant keeps the file's own order.
		r.Last, r.LastModel = ts, model
		r.LastBlocked = blocked
		rc := ReasonCount{Reason: reason, Known: known}
		r.LastReason = rc.Label()
		if blocked {
			r.BlockedCalls++
			a.reasons[rc]++
			if known && reason == KilledReason {
				r.Killed = true
			}
		}
		if warned {
			r.WarnedCalls++
			r.WarnedCost += money.Micros(micros)
		}
		d := a.doors[door]
		if d == nil {
			d = &DoorSpend{Door: door}
			a.doors[door] = d
		}
		d.Calls++
		d.Cost += money.Micros(micros)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]RunRow, 0, len(order))
	for _, run := range order {
		a := byRun[run]
		r := a.row
		for ag := range a.agents {
			r.Agents = append(r.Agents, ag)
		}
		sort.Strings(r.Agents)
		for t := range a.teams {
			r.Teams = append(r.Teams, t)
		}
		sort.Strings(r.Teams)
		for i := range r.Models {
			if r.Cost == 0 {
				r.Models[i].share = -1
			} else {
				r.Models[i].share = int64(r.Models[i].Cost) * 1000 / int64(r.Cost)
			}
		}
		for _, d := range a.doors {
			r.Doors = append(r.Doors, *d)
		}
		sortDoors(r.Doors)
		for rc, n := range a.reasons {
			rc.Calls = n
			r.Blocked = append(r.Blocked, rc)
		}
		sort.Slice(r.Blocked, func(i, j int) bool {
			if r.Blocked[i].Calls != r.Blocked[j].Calls {
				return r.Blocked[i].Calls > r.Blocked[j].Calls
			}
			return r.Blocked[i].Label() < r.Blocked[j].Label()
		})
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		if out[i].Last != out[j].Last {
			return out[i].Last > out[j].Last
		}
		return out[i].RunID < out[j].RunID
	})
	return out, nil
}

// RunsSummary is the month's run grain in four numbers: how many runs, how
// many called more than one model, how many were killed, and how many the
// gateway stopped on their last call.
type RunsSummary struct {
	Runs, Switched, Killed, Stopped int
	// Crossed is how many runs came through more than one gateway door.
	Crossed int
}

// SummarizeRuns counts rows the way the runs page's header states them.
func SummarizeRuns(rows []RunRow) RunsSummary {
	s := RunsSummary{Runs: len(rows), Crossed: crossedDoors(rows)}
	for _, r := range rows {
		if r.Switched() {
			s.Switched++
		}
		if r.Killed {
			s.Killed++
		} else if r.LastBlocked {
			s.Stopped++
		}
	}
	return s
}

// DoorSpend is the calls and spend read through one gateway door.
type DoorSpend struct {
	Door  string
	Calls int
	Cost  money.Micros
}

// Label is the door as a person reads it.
func (d DoorSpend) Label() string { return DoorLabel(d.Door) }

// DoorLabel names a door: the connector's main folder is the main gateway,
// its second folder the OpenAI-wire door, and anything else is printed as
// stored.
func DoorLabel(door string) string {
	switch door {
	case connectors.DoorMain:
		return "main gateway"
	case connectors.DoorOpenAI:
		return "OpenAI-wire door"
	}
	return door
}

// sortDoors puts the main gateway first and the rest by name, so a page lists
// doors the same way every time (invariant 7).
func sortDoors(ds []DoorSpend) {
	sort.Slice(ds, func(i, j int) bool {
		mi, mj := ds[i].Door == connectors.DoorMain, ds[j].Door == connectors.DoorMain
		if mi != mj {
			return mi
		}
		return ds[i].Door < ds[j].Door
	})
}

// AIDoors is the month's calls and spend by the door they were read through
// (invariant 102). One entry means the month's spend is read from one source:
// the export does not say which door a call came through, and only one folder
// supplied it.
func AIDoors(db *sql.DB, month string) ([]DoorSpend, error) {
	rows, err := db.Query(`SELECT COALESCE(door,'`+connectors.DoorMain+`'), COUNT(*),
			COALESCE(SUM(billed_microusd),0)
		FROM ai_calls WHERE day LIKE ? GROUP BY 1`, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DoorSpend
	for rows.Next() {
		var d DoorSpend
		var micros int64
		if err := rows.Scan(&d.Door, &d.Calls, &micros); err != nil {
			return nil, err
		}
		d.Cost = money.Micros(micros)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortDoors(out)
	return out, nil
}

// RunsSummary's door count: how many runs crossed doors.
func crossedDoors(rows []RunRow) int {
	n := 0
	for _, r := range rows {
		if r.CrossedDoors() {
			n++
		}
	}
	return n
}
