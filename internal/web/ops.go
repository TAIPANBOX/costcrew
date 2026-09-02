package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/world"
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
	spec := readSort(r, "name", false)
	applySort(rows, spec, map[string]func(x, y connectorRow) int{
		"name":     func(x, y connectorRow) int { return cmpString(x.Name, y.Name) },
		"feeds":    func(x, y connectorRow) int { return cmpString(x.Feeds, y.Feeds) },
		"kind":     func(x, y connectorRow) int { return cmpString(string(x.Kind), string(y.Kind)) },
		"provider": func(x, y connectorRow) int { return cmpString(x.Provider, y.Provider) },
		"status":   func(x, y connectorRow) int { return cmpString(string(x.Status), string(y.Status)) },
		"lasttest": func(x, y connectorRow) int { return cmpString(x.LastTest, y.LastTest) },
	}, "name")
	s.render(w, tplConnectors, struct {
		shell
		Rows                       []connectorRow
		Built, Documented, Metered int
		Sort                       sortSpec
	}{s.shellFor(r, "Connectors", "connectors"), rows, built, documented, metered, spec})
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
	// Whether this connector's provider is one of the estate's own desks, so
	// the crumb can open it. "kubernetes" is a provider and not a desk, and
	// linking it would offer a page that does not exist.
	isDesk := false
	for _, d := range world.Desks {
		if d.Name == c.Provider {
			isDesk = true
			break
		}
	}
	s.render(w, tplConnector, struct {
		shell
		C      connectors.Connector
		Conn   connectors.Connection
		Env    map[string]bool
		CanAct bool
		IsDesk bool
	}{s.shellFor(r, c.Name, "connectors"), c, conn, env, u.May("operator"), isDesk})
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
			// it, and the page prints the cost beside the box. replace-generated
			// is the same shape of separate act, for the one refusal that is
			// not about money: a reader that would mix a generated estate with
			// real rows needs the same explicit yes, checked fresh on every
			// submission rather than remembered from a saved setting.
			confirmed := r.PostFormValue("confirm") == "yes"
			replaceGenerated := r.PostFormValue("replace-generated") == "yes"
			_, err := connectors.Import(s.db, id, confirmed, connectors.ImportOptions{
				ReplaceGenerated: replaceGenerated,
				Actor:            u.Username,
				Rec:              s.rec,
			})
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

// roleWeight orders roles by what they can do, which is what the column means.
func roleWeight(role string) int {
	switch role {
	case "admin":
		return 3
	case "operator":
		return 2
	case "viewer":
		return 1
	}
	return 0
}

type accountRow struct {
	Username  string
	Role      string
	LastLogin string
}

// A viewer reads the account list and is served no controls.
//
// @yurii 2026-08-23, asked directly whether to close it: "лишай список
// акаунтів". So this is a decision, not an oversight, and it should not be
// "fixed" by a later reader who notices that a read-only account can see every
// username and role.
//
// The reasoning it was decided on: in a console where everyone with an account
// is a colleague, who to ask about an account is useful, and the list is not a
// secret. What a viewer must not get is the CONTROLS, which is held by
// TestWhatAViewerCanRead.
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
	srt := readSort(r, "account", false)
	applySort(out, srt, map[string]func(a, b accountRow) int{
		"account": func(a, b accountRow) int { return cmpString(a.Username, b.Username) },
		// By WEIGHT, not alphabetically. "admin, operator, viewer" happens to
		// be reverse alphabetical and that is a coincidence; sorting a role
		// column by its spelling would put them in a different order the
		// moment a role is renamed, and the order somebody wants is by how
		// much the role can do.
		"role": func(a, b accountRow) int { return cmpInt(roleWeight(a.Role), roleWeight(b.Role)) },
		// Never signed in sorts as the OLDEST rather than as the newest.
		// An empty string sorts before every date, which is right: an account
		// nobody has ever used is the far end of "least recently used", and
		// it is the end somebody scanning this column is looking for.
		"seen": func(a, b accountRow) int { return cmpString(a.LastLogin, b.LastLogin) },
	}, "account")
	s.render(w, tplAccounts, struct {
		shell
		Users   []accountRow
		Roles   []string
		Me      string
		IsAdmin bool
		Dummy   string
		Sort    sortSpec
	}{s.shellFor(r, "Accounts", "accounts"), out,
		[]string{"viewer", "operator", "admin"}, u.Username, u.May("admin"), "", srt})
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
		case "remove":
			// Deleting an account is irreversible and one click away from the
			// row above it, so it asks for the name to be typed. A confirm
			// dialog somebody clicks through without reading is not a check;
			// typing the name is, because it cannot be done by accident and it
			// cannot be done to the wrong row.
			if r.PostFormValue("confirm") != name {
				redirectMsg(w, r, back,
					"to remove an account, type its name in the box beside the button")
				return
			}
			// Not yourself. It is not that it cannot be undone, it is that
			// nobody is left holding the session that could undo it.
			if name == u.Username {
				redirectMsg(w, r, back,
					"you cannot remove the account you are signed in as: "+
						"ask another admin, or promote one first")
				return
			}
			s.done(w, r, back, s.au.Delete(name))
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
	// What is waiting on the wire, and how many of it another service can
	// actually read. The vocabulary is shared and this console's practice
	// events are not in it, so the honest number is a pair.
	mapped, own := 0, 0
	if s.eventsPath != "" {
		if f, err := os.Open(s.eventsPath); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for sc.Scan() {
				var e struct {
					Type  string `json:"type"`
					RunID string `json:"run_id"`
				}
				if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "" {
					continue
				}
				if e.RunID != "" {
					mapped++
				} else {
					own++
				}
			}
			f.Close()
		}
	}

	srt := readSort(r, "when", true)
	applySort(rows, srt, map[string]func(a, b auditRow) int{
		"when":  func(a, b auditRow) int { return cmpString(a.When, b.When) },
		"event": func(a, b auditRow) int { return cmpString(a.Kind, b.Kind) },
		"what":  func(a, b auditRow) int { return cmpString(a.Detail, b.Detail) },
	}, "when")
	s.render(w, tplAudit, struct {
		shell
		Events  []auditRow
		OK      bool
		N       int
		BreakAt string
		StackOn bool
		Emitted int
		Sort    sortSpec
		Stream  string
		Mapped  int
		OwnOnly int
	}{s.shellFor(r, "Audit", "audit"), rows, ok, n, breakAt,
		// Whether the agent-event STREAM is configured, not whether a recorder
		// exists. s.rec is the store's own journal recorder and is present on
		// every installation, so this tile appeared on all of them and printed
		// "On the stack: 0" beside "The chain verified: 36 events". A reader
		// comparing the two concludes the console is failing to emit, when the
		// truth is that nobody switched the governance plane on. The template
		// already guarded it; the guard was reading the wrong thing.
		s.eventsPath != "", s.emitted(), srt, s.eventsPath, mapped, own})
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
