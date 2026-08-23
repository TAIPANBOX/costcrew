package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var tplOwner = page("owner.html")

// The links an alert carries have to land somewhere.
//
// heraldyx watches the agent-event stream this console writes and mails a
// person when something needs them tonight. Every message it sends ends with
// three links into "the operator's own console": the event, the agent, and
// everything that agent's owner runs. They are addressed by what the STACK
// knows, which is an agent:// URI and an owner name, not by this console's own
// row ids.
//
// Until these existed, all three answered 404. An alert that arrives at
// two in the morning and cannot be opened is worse than no alert: somebody is
// awake, worried, and has to go and find the thing by hand.
//
// Go's ServeMux collapses the // in agent:// before a handler sees it, so
// these accept both forms rather than requiring the sender to escape a URI it
// is right to send unescaped.

// nameFromURI takes the last segment of an agent:// URI.
//
// The trust domain is deliberately not checked against this installation's
// host. An operator who renamed the host, or who is reading an alert raised
// before a rename, should still land on the agent; getting the wrong agent is
// not a risk here because the name is unique within the roster and the page
// says which agent it opened.
func nameFromURI(s string) string {
	s = strings.TrimPrefix(s, "agent://")
	s = strings.TrimPrefix(s, "agent:/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

func (s *Server) byAgentURI(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	name := nameFromURI(r.PathValue("uri"))
	if name == "" {
		http.Error(w, "that link carries no agent", http.StatusNotFound)
		return
	}
	s.toAgentCard(w, r, name)
}

// toAgentCard sends a reader to an agent's card, or explains its absence.
//
// Shared by both links an alert carries, because they had drifted: /a/ said
// plainly that the agent was gone and /i/ redirected to a card that does not
// exist, which is a bare 404 arriving from a link this console itself put in
// an email. An alert is read minutes or days after it was raised, so the agent
// named in it is exactly the one most likely to have been removed since.
func (s *Server) toAgentCard(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := crew.GetAnalyst(s.db, name); err != nil {
		// Named, and gone. Said plainly, because "404" on an alert's own link
		// reads as a broken console rather than as an agent that was removed.
		redirectMsg(w, r, "/staff", "no agent called "+name+" is on the roster now. "+
			"It may have been taken off it since that alert was raised; "+
			"what it did is still on the board and in the journal.")
		return
	}
	http.Redirect(w, r, "/staff/"+name, http.StatusSeeOther)
}

// byIncident opens whatever the event was about.
//
// heraldyx addresses an incident as "<type>:<agent-uri>", which is what it can
// build from an event alone. This console can usually do better: if the agent
// has an open finding, that is what somebody was woken up about, so it opens
// the finding. Otherwise it opens the agent, which is always right and never
// precise.
func (s *Server) byIncident(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	raw := r.PathValue("ref")
	kind, uri, _ := strings.Cut(raw, ":")
	name := nameFromURI(uri)
	if name == "" {
		name = nameFromURI(raw)
	}
	if strings.HasPrefix(kind, "anomaly") && name != "" {
		for _, f := range []anomaly.Filter{{HandledBy: name}, {CausedBy: name}} {
			if list, err := anomaly.List(s.db, f); err == nil && len(list) > 0 {
				http.Redirect(w, r, "/anomalies/"+list[0].ID, http.StatusSeeOther)
				return
			}
		}
	}
	if name != "" {
		s.toAgentCard(w, r, name)
		return
	}
	http.Redirect(w, r, "/audit", http.StatusSeeOther)
}

// ------------------------------------------------------------------- owner

type ownerAgent struct {
	crew.Analyst
	Open      int
	Spent     money.Cents
	Anomalies int
}

// owner is everything one person answers for.
//
// The third link in every alert, and a view the console did not have. It is
// the question somebody asks the moment an agent misbehaves: what ELSE is this
// person running, and is any of it doing the same thing.
func (s *Server) owner(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	who := r.PathValue("name")
	roster, err := crew.Roster(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	scores, _ := crew.Scoreboards(s.db)

	var mine []ownerAgent
	var totalSpent, totalGuard money.Cents
	var open, findings int
	byState := map[string]int{}
	byDesk := map[string]int{}
	for _, a := range roster {
		if a.Owner != who {
			continue
		}
		sc := scores[a.Name]
		n := 0
		if list, err := anomaly.List(s.db, anomaly.Filter{CausedBy: a.Name}); err == nil {
			n = len(list)
		}
		mine = append(mine, ownerAgent{a, sc.Open, sc.Spent, n})
		totalSpent += sc.Spent
		totalGuard += a.Monthly
		open += sc.Open
		findings += n
		byState[a.State]++
		byDesk[a.Desk]++
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Spent > mine[j].Spent })

	// The month's spend as well as the lifetime one, because the guard beside
	// it is monthly. Setting a total since the board opened against a monthly
	// allowance is the same category error the crew page had, and repeating it
	// on a new page is how it comes back.
	month := world.LastDay[:7]
	inMonth, _ := crew.SpendInMonth(s.db, month)
	var thisMonth money.Cents
	var overGuard int
	for _, a := range mine {
		v := inMonth[a.Name]
		thisMonth += v
		if a.Monthly > 0 && v > a.Monthly {
			overGuard++
		}
	}

	srt := readSort(r, "spent", true)
	applySort(mine, srt, map[string]func(a, b ownerAgent) int{
		"agent":     func(a, b ownerAgent) int { return cmpString(a.Name, b.Name) },
		"role":      func(a, b ownerAgent) int { return cmpString(a.Role, b.Role) },
		"desk":      func(a, b ownerAgent) int { return cmpString(a.Desk, b.Desk) },
		"open":      func(a, b ownerAgent) int { return cmpInt(a.Open, b.Open) },
		"spent":     func(a, b ownerAgent) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
		"guard":     func(a, b ownerAgent) int { return cmpInt64(int64(a.Monthly), int64(b.Monthly)) },
		"anomalies": func(a, b ownerAgent) int { return cmpInt(a.Anomalies, b.Anomalies) },
		"state":     func(a, b ownerAgent) int { return cmpString(a.State, b.State) },
	}, "spent")

	// Whether this is an account at all, said plainly. An owner name in an
	// alert can outlive the account it named.
	account, _ := s.au.Get(who)

	desks := make([]string, 0, len(byDesk))
	for d := range byDesk {
		desks = append(desks, d)
	}
	sort.Strings(desks)

	s.render(w, tplOwner, struct {
		shell
		Who          string
		HasAccount   bool
		Role         string
		Agents       []ownerAgent
		Spent, Guard money.Cents
		Open         int
		Findings     int
		States       map[string]int
		Desks        []string
		Month        string
		ThisMonth    money.Cents
		OverGuard    int
		Sort         sortSpec
	}{s.shellFor(r, who, "staff"), who, account != nil,
		roleOf(account), mine, totalSpent, totalGuard, open, findings, byState, desks,
		month, thisMonth, overGuard, srt})
}

func roleOf(u *auth.User) string {
	if u == nil {
		return ""
	}
	return u.Role
}
