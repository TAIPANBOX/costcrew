package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

var (
	tplConnectors = page("connectors.html")
	tplConnector  = page("connector.html")
	tplEngines    = page("engines.html")
	tplAccounts   = page("accounts.html")
	tplAudit      = page("audit.html")
)

// ------------------------------------------------------------- connectors

// connectorRow keeps the connection's fields NAMED rather than embedded.
// Embedding both put two ID fields at the same depth and the template's .ID
// silently became ambiguous, which Go reports at render time rather than at
// compile time.
type connectorRow struct {
	connectors.Connector
	LastTest   string
	LastResult string
	OK         bool
}

func (s *Server) connectors(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	built, documented, metered := connectors.Counts()
	rows := make([]connectorRow, 0, len(connectors.Catalogue))
	for _, c := range connectors.Catalogue {
		conn, _ := connectors.Load(s.db, c.ID)
		rows = append(rows, connectorRow{c, conn.LastTest, conn.LastResult, conn.OK})
	}
	s.render(w, tplConnectors, struct {
		shell
		Rows                       []connectorRow
		Built, Documented, Metered int
	}{s.shellFor(r, "Connectors", "connectors"), rows, built, documented, metered})
}

func (s *Server) connectorPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	c, ok := connectors.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such connector", http.StatusNotFound)
		return
	}
	conn, err := connectors.Load(s.db, c.ID)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// Whether a secret is present, never its value. The page says set or not
	// set, which is all anybody needs and all it is safe to render.
	env := map[string]bool{}
	for _, in := range c.Inputs {
		if in.Secret {
			env[in.EnvVar] = strings.TrimSpace(os.Getenv(in.EnvVar)) != ""
		}
	}
	s.render(w, tplConnector, struct {
		shell
		C      connectors.Connector
		Conn   connectors.Connection
		Env    map[string]bool
		CanAct bool
	}{s.shellFor(r, c.Name, "connectors"), c, conn, env, u.May("operator")})
}

func (s *Server) connectorAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		id := r.PathValue("id")
		back := "/connectors/" + id
		if !s.checked(w, r, back, u) {
			return
		}
		c, ok := connectors.Get(id)
		if !ok {
			http.Error(w, "no such connector", http.StatusNotFound)
			return
		}
		switch kind {
		case "save":
			cfg := map[string]string{}
			for _, in := range c.Inputs {
				if !in.Secret {
					cfg[in.Name] = r.PostFormValue(in.Name)
				}
			}
			s.done(w, r, back, connectors.Save(s.db, id, cfg))
		case "test":
			_, _, err := connectors.Test(s.db, id, os.Getenv)
			s.done(w, r, back, err)
		case "import":
			// The confirmation is a separate act from the click that started
			// it, and the page prints the cost beside the box.
			confirmed := r.PostFormValue("confirm") == "yes"
			_, err := connectors.Import(s.db, id, confirmed)
			s.done(w, r, back, err)
		}
	}
}

// ----------------------------------------------------------------- engines

type engineGroup struct {
	Title   string
	Note    string
	Engines []engines.Availability
}

func (s *Server) engines(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	av := engines.Check(nil, nil)
	var groups []engineGroup
	for _, f := range []engines.Family{engines.Subscription, engines.APIKey, engines.Existing} {
		groups = append(groups, engineGroup{
			Title:   engines.FamilyTitle(f),
			Note:    engines.FamilyNote(f),
			Engines: engines.ByFamily(av, f),
		})
	}
	s.render(w, tplEngines, struct {
		shell
		Groups []engineGroup
		Dry    bool
	}{s.shellFor(r, "Engines", "engines"), groups, engines.Dry(av)})
}

// ---------------------------------------------------------------- accounts

type accountRow struct {
	Username  string
	Role      string
	LastLogin string
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	rows, err := s.au.List()
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]accountRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, accountRow{x.Username, x.Role, x.LastLoginText()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	s.render(w, tplAccounts, struct {
		shell
		Users   []accountRow
		Roles   []string
		Me      string
		IsAdmin bool
		Dummy   string
	}{s.shellFor(r, "Accounts", "accounts"), out,
		[]string{"viewer", "operator", "admin"}, u.Username, u.May("admin"), ""})
}

func (s *Server) accountAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		back := "/accounts"
		if err := r.ParseForm(); err != nil {
			redirectMsg(w, r, back, "reload the page and try again")
			return
		}
		if !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {
			redirectMsg(w, r, back, "reload the page and try again")
			return
		}
		// Managing accounts is an admin's job, and it is a HIGHER bar than
		// acting: an operator can spend the budget and still not hand somebody
		// else the ability to.
		if !u.May("admin") {
			redirectMsg(w, r, back, "managing accounts is an admin's job")
			return
		}
		name := strings.TrimSpace(r.PostFormValue("username"))
		switch kind {
		case "create":
			ok, err := s.au.Create(name, r.PostFormValue("password"), r.PostFormValue("role"))
			if err != nil {
				redirectMsg(w, r, back, "that did not work: "+err.Error())
				return
			}
			if !ok {
				redirectMsg(w, r, back, "that name is taken, or the password is under ten characters")
				return
			}
			redirectMsg(w, r, back, "")
		case "role":
			// Nobody demotes themselves out of the last admin seat. An
			// installation with no admin cannot be managed by anybody, and the
			// only fix is the database.
			if name == u.Username && r.PostFormValue("role") != "admin" {
				admins, err := s.au.CountRole("admin")
				if err == nil && admins <= 1 {
					redirectMsg(w, r, back, "you are the only admin: promote somebody else first")
					return
				}
			}
			s.done(w, r, back, s.au.SetRole(name, r.PostFormValue("role")))
		}
	}
}

// ------------------------------------------------------------------- audit

type auditRow struct {
	When   string
	Kind   string
	Detail string
	Hash   string
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	ok, n, breakAt, err := s.st.VerifyChain()
	if err != nil {
		http.Error(w, "the journal could not be read", http.StatusInternalServerError)
		return
	}
	tail, err := s.st.JournalTail(120)
	if err != nil {
		http.Error(w, "the journal could not be read", http.StatusInternalServerError)
		return
	}
	rows := make([]auditRow, 0, len(tail))
	for _, rec := range tail {
		rows = append(rows, auditRow{
			When:   rec.When(),
			Kind:   rec.Event,
			Detail: summarise(rec.Data),
			Hash:   rec.Hash,
		})
	}
	s.render(w, tplAudit, struct {
		shell
		Events  []auditRow
		OK      bool
		N       int
		BreakAt string
		StackOn bool
		Emitted int
	}{s.shellFor(r, "Audit", "audit"), rows, ok, n, breakAt,
		s.rec != nil, s.emitted()})
}

// summarise turns an event's payload into one readable line, in a stable
// order so the column does not reshuffle between page loads.
func summarise(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := data[k]
		if s, ok := v.(string); ok && len(s) > 60 {
			s = s[:57] + "…"
			v = s
		}
		parts = append(parts, fmt.Sprintf("%s %v", k, v))
	}
	return strings.Join(parts, " · ")
}

// emitted counts what has gone onto the shared event stream, which is a
// different number from the console's own journal and should not be conflated
// with it.
func (s *Server) emitted() int {
	if s.eventsPath == "" {
		return 0
	}
	raw, err := os.ReadFile(s.eventsPath)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var probe map[string]any
		if json.Unmarshal([]byte(line), &probe) == nil {
			n++
		}
	}
	return n
}

var _ = auth.SessionCookie
