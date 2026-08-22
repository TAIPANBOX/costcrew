package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var tplSearch = page("search.html")

// Finding one thing should not require knowing which page it is on.
//
// The console holds forty-eight services, thirty-nine agents, ten teams, six
// desks, three hundred tasks and every finding the detector has raised. Until
// this existed, looking up "BigQuery" meant guessing whether it was a service
// page, a desk page or a row in the anomaly list, and being wrong twice.
//
// It searches what the console can OPEN, not the free text inside it: the
// answer to "where is BigQuery" is a page, not a list of sentences mentioning
// it. Matching is on the name, case-insensitively, with an exact match first
// and a prefix before a substring, because a person typing three letters is
// usually most of the way to one thing.

type hit struct {
	Kind   string // service, team, desk, agent, anomaly, task, connector
	Name   string
	Detail string
	URL    string
	Amount money.Cents
	rank   int
}

func rankOf(name, q string) (int, bool) {
	n, l := strings.ToLower(name), strings.ToLower(q)
	switch {
	case n == l:
		return 0, true
	case strings.HasPrefix(n, l):
		return 1, true
	case strings.Contains(n, l):
		return 2, true
	}
	return 0, false
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var hits []hit
	add := func(kind, name, detail, url string, amount money.Cents) {
		if rank, ok := rankOf(name, q); ok {
			hits = append(hits, hit{kind, name, detail, url, amount, rank})
		}
	}

	if q != "" {
		month := world.LastDay[:7]

		for _, t := range world.Teams {
			add("team", t.Name, t.Unit+" unit, hears from FinOps "+t.Cadence,
				"/team/"+t.Name, 0)
		}
		for _, d := range world.Desks {
			add("desk", d.Name, d.Kind, "/desk/"+d.Name, 0)
		}
		if rows, _, err := services(s.db, month, prevMonth(month)); err == nil {
			for _, x := range rows {
				add("service", x.Name, "on the "+x.Source+" desk", "/service/"+x.Name, x.Amount)
			}
		}
		if roster, err := crew.Roster(s.db); err == nil {
			for _, a := range roster {
				add("agent", a.Name, a.Role+", "+a.State, "/staff/"+a.Name, a.Monthly)
			}
		}
		for _, c := range connectors.Catalogue {
			add("connector", c.Name, string(c.Status)+", fills "+c.Feeds, "/connectors/"+c.ID, 0)
		}
		// Anomalies match on their id AND on what they are about, because
		// nobody remembers an id and everybody remembers the service.
		if list, err := anomaly.List(s.db, anomaly.Filter{}); err == nil {
			for _, a := range list {
				name := a.ID
				detail := a.Service + " on " + a.Day + ", " + string(a.State)
				if _, ok := rankOf(a.ID, q); !ok {
					if _, ok := rankOf(a.Service, q); ok {
						name = a.Service + " on " + a.Day
					} else {
						continue
					}
				}
				hits = append(hits, hit{"anomaly", name, detail, "/anomalies/" + a.ID, a.Excess.Abs(), 2})
			}
		}
		if tasks, err := crew.Tasks(s.db, crew.TaskFilter{}); err == nil {
			for _, t := range tasks {
				if _, ok := rankOf(t.Title, q); !ok {
					continue
				}
				who := t.Assignee
				if who == "" {
					who = "unassigned"
				}
				hits = append(hits, hit{"task", t.Title, who + ", " + string(t.State),
					"/task/" + strconv.Itoa(t.ID), t.Spent, 2})
			}
		}
	}

	// Exact first, then prefix, then substring; inside a rank the biggest
	// money first, because that is the one somebody is usually looking for.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].Amount > hits[j].Amount
	})
	shown := hits
	if len(shown) > 60 {
		shown = shown[:60]
	}

	s.render(w, tplSearch, struct {
		shell
		Q     string
		Hits  []hit
		Total int
	}{s.shellFor(r, "Search", "search"), q, shown, len(hits)})
}
