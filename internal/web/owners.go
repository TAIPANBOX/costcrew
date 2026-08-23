package web

import (
	"net/http"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var tplOwners = page("owners.html")

// ownerRow is one person and everything they answer for.
type ownerRow struct {
	Name      string
	Agents    int
	Active    int
	Spent     money.Cents // since the board opened
	ThisMonth money.Cents
	Guard     money.Cents // monthly allowance across their agents
	OverGuard int
	Open      int
	Findings  int
	Unbound   int // agents carrying no attestation
}

// owners lists who answers for what.
//
// The owner view existed and had no way in: it was reachable from an agent
// card and from an alert link, which meant a person could only get to it if
// they already knew whose agent they were looking at. That is backwards for
// the question it answers, which is asked BEFORE a name is known: who is
// running things here, and is any of it out of hand.
//
// One owner today. The page is still a table rather than a redirect to that
// one, because transferring an agent to another owner is a thing this console
// does, and a page that silently means "yurii" would be wrong the first time
// somebody uses it.
func (s *Server) owners(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	roster, err := crew.Roster(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	scores, _ := crew.Scoreboards(s.db)
	month := world.LastDay[:7]

	// Spend comes from the owner recorded on each charge, not from who owns
	// the agent today. An agent that changes hands used to rewrite history:
	// the new owner's total jumped by an amount they had never authorised and
	// the previous owner's dropped by the same, so "what has this person
	// spent" had no stable answer.
	spentEver, _ := crew.SpendByOwner(s.db, "")
	spentMonth, _ := crew.SpendByOwner(s.db, month)
	inMonth, _ := crew.SpendInMonth(s.db, month)

	// One query for every finding, then grouped, rather than one query per
	// agent. The owner page can afford the per-agent call because it has
	// filtered to one person first; this page has not.
	byAgent := map[string]int{}
	if all, err := anomaly.List(s.db, anomaly.Filter{}); err == nil {
		for _, a := range all {
			// The grain is recorded rather than implied, so use it: a finding
			// attributed to a team is not one of its agents' findings, and
			// adding it to an owner's count would charge a person for
			// something no agent of theirs did.
			if a.CausedByKind == "agent" {
				byAgent[a.CausedBy]++
			}
		}
	}

	idx := map[string]*ownerRow{}
	var order []string
	for _, a := range roster {
		who := a.Owner
		if who == "" {
			// Not folded into a bucket with a friendly name. An agent nobody
			// owns is a real state and the page has to be able to say so.
			who = "(unowned)"
		}
		row, ok := idx[who]
		if !ok {
			row = &ownerRow{Name: who}
			idx[who] = row
			order = append(order, who)
		}
		sc := scores[a.Name]
		row.Agents++
		if a.State == string(world.Active) {
			row.Active++
		}
		row.Open += sc.Open
		row.Guard += a.Monthly
		row.Findings += byAgent[a.Name]
		// The guard is about the agents somebody holds NOW, so it is measured
		// against what those agents are spending now. That is a different
		// question from what the person answers for historically, and the two
		// are kept apart rather than summed into one column that means neither.
		if v := inMonth[a.Name]; a.Monthly > 0 && v > a.Monthly {
			row.OverGuard++
		}
		// The same predicate the crew page counts with, called rather than
		// re-derived. ValidAttestation is a different question and answering
		// this one with it reported zero unbound agents on an estate where
		// every one of them was bound to nothing.
		if crew.CountsForAttestation(a) && crew.IsUnattested(a) {
			row.Unbound++
		}
	}
	// Somebody who has handed every agent over still answers for what they
	// spent, so the page is the union of who holds agents now and who is
	// recorded on a charge. Dropping the second set would make a transfer
	// look like the money had never been spent.
	for who := range spentEver {
		if who == "" {
			continue
		}
		if _, ok := idx[who]; !ok {
			idx[who] = &ownerRow{Name: who}
			order = append(order, who)
		}
	}
	sort.Strings(order)

	rows := make([]ownerRow, 0, len(order))
	for _, k := range order {
		r := *idx[k]
		r.Spent = spentEver[k]
		r.ThisMonth = spentMonth[k]
		rows = append(rows, r)
	}

	srt := readSort(r, "month", true)
	applySort(rows, srt, map[string]func(a, b ownerRow) int{
		"owner":    func(a, b ownerRow) int { return cmpString(a.Name, b.Name) },
		"agents":   func(a, b ownerRow) int { return cmpInt(a.Agents, b.Agents) },
		"month":    func(a, b ownerRow) int { return cmpInt64(int64(a.ThisMonth), int64(b.ThisMonth)) },
		"spent":    func(a, b ownerRow) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
		"guard":    func(a, b ownerRow) int { return cmpInt64(int64(a.Guard), int64(b.Guard)) },
		"over":     func(a, b ownerRow) int { return cmpInt(a.OverGuard, b.OverGuard) },
		"open":     func(a, b ownerRow) int { return cmpInt(a.Open, b.Open) },
		"findings": func(a, b ownerRow) int { return cmpInt(a.Findings, b.Findings) },
		"unbound":  func(a, b ownerRow) int { return cmpInt(a.Unbound, b.Unbound) },
	}, "month")

	// Work charged to no agent at all belongs to nobody, and it is named
	// rather than dropped: a page that silently omits it reports a total that
	// does not match the ledger and gives no hint why.
	unowned := spentEver[""]

	var totalMonth, totalGuard money.Cents
	var totalAgents, totalUnbound int
	for _, o := range rows {
		totalMonth += o.ThisMonth
		totalGuard += o.Guard
		totalAgents += o.Agents
		totalUnbound += o.Unbound
	}

	s.render(w, tplOwners, struct {
		shell
		Rows        []ownerRow
		Month       string
		TotalMonth  money.Cents
		TotalGuard  money.Cents
		TotalAgents int
		Unbound     int
		Unowned     money.Cents
		Sort        sortSpec
	}{s.shellFor(r, "Owners", "owners"), rows, month,
		totalMonth, totalGuard, totalAgents, totalUnbound, unowned, srt})
}
