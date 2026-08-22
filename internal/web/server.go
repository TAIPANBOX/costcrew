// Package web serves the console.
//
// The port is held to the Python original's observable HTTP surface by the
// parity gate in tools/parity, so every handler here has a golden capture it
// must reproduce byte for byte. Routes that are not built yet are deliberately
// absent rather than stubbed: a stub returns 200 with the wrong body, which the
// gate reports as WRONG, while an absent route returns 404, which it reports as
// NOT YET. Those need opposite responses from a person, so they must not look
// alike.
package web

import (
	"database/sql"
	_ "embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

type Server struct {
	st         *store.Store
	au         *auth.Auth
	db         *sql.DB
	rec        anomaly.Recorder
	host       string
	eventsPath string
	mux        *http.ServeMux
}

// New builds the console. A nil recorder means the governance stack is
// switched off, which is the default and a perfectly good answer.
func New(st *store.Store, au *auth.Auth, rec anomaly.Recorder, host, eventsPath string) *Server {
	if host == "" {
		host = "costcrew.local"
	}
	s := &Server{st: st, au: au, db: st.DB(), rec: rec, host: host,
		eventsPath: eventsPath, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.healthz)

	s.mux.HandleFunc("GET /signup", s.signupPage)
	s.mux.HandleFunc("POST /signup", s.signupSubmit)
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.loginSubmit)
	s.mux.HandleFunc("POST /logout", s.logout)

	// Aliases the original keeps for links that predate a rename. They carry
	// no content of their own, which is why the gate records where a redirect
	// POINTS as well as that it happened.
	s.mux.HandleFunc("GET /calendar", s.redirect("/board?view=month"))
	s.mux.HandleFunc("GET /stats", s.redirect("/staff"))

	s.mux.HandleFunc("GET /intake/template/{name}", s.intakeTemplate)
	s.mux.HandleFunc("GET /export/budget.csv", s.exportBudget)

	s.mux.HandleFunc("GET /{$}", s.overview)
	s.mux.HandleFunc("GET /anomalies", s.anomalies)
	s.mux.HandleFunc("GET /anomalies/{id}", s.anomalyPage)
	s.mux.HandleFunc("POST /anomalies/{id}/assign", s.anomalyAction("assign"))
	s.mux.HandleFunc("POST /anomalies/{id}/explain", s.anomalyAction("explain"))
	s.mux.HandleFunc("POST /anomalies/{id}/dismiss", s.anomalyAction("dismiss"))
	s.mux.HandleFunc("GET /budgets", s.budgets)
	s.mux.HandleFunc("GET /board", s.board)
	s.mux.HandleFunc("GET /sprints", s.sprints)
	s.mux.HandleFunc("GET /sprint/{id}", s.sprintPage)
	s.mux.HandleFunc("GET /task/{id}", s.taskPage)
	s.mux.HandleFunc("POST /task/{id}/assign", s.taskAction("assign"))
	s.mux.HandleFunc("POST /task/{id}/block", s.taskAction("block"))
	s.mux.HandleFunc("POST /task/{id}/comment", s.taskAction("comment"))
	s.mux.HandleFunc("POST /artifact/{id}/post", s.artifactAction("post"))
	s.mux.HandleFunc("POST /artifact/{id}/return", s.artifactAction("return"))
	s.mux.HandleFunc("GET /staff", s.staff)
	s.mux.HandleFunc("GET /staff/{name}", s.analyst)

	s.mux.HandleFunc("GET /allocation", s.allocation)
	s.mux.HandleFunc("POST /allocation/rule/{id}", s.setRule)
	s.mux.HandleFunc("GET /chargeback", s.chargeback)
	s.mux.HandleFunc("POST /chargeback/close", s.closePeriod)
	s.mux.HandleFunc("POST /chargeback/reopen", s.reopenPeriod)
	s.mux.HandleFunc("GET /results", s.results)

	s.mux.HandleFunc("GET /export/allocation.csv", s.exportAllocation)
	s.mux.HandleFunc("GET /export/gl.csv", s.exportGL)
	s.mux.HandleFunc("GET /export/showback.csv", s.exportShowback)
	s.mux.HandleFunc("GET /export/results.csv", s.exportResultsCSV)
	s.mux.HandleFunc("GET /export/results.md", s.exportResultsMD)
	s.mux.HandleFunc("GET /export/crew.csv", s.exportCrewCSV)
	s.mux.HandleFunc("GET /export/exec-packet.md", s.exportExecPacket)

	s.mux.HandleFunc("GET /connectors", s.connectors)
	s.mux.HandleFunc("GET /connectors/{id}", s.connectorPage)
	s.mux.HandleFunc("POST /connectors/{id}/save", s.connectorAction("save"))
	s.mux.HandleFunc("POST /connectors/{id}/test", s.connectorAction("test"))
	s.mux.HandleFunc("POST /connectors/{id}/import", s.connectorAction("import"))
	s.mux.HandleFunc("GET /engines", s.engines)
	s.mux.HandleFunc("GET /accounts", s.accounts)
	s.mux.HandleFunc("POST /accounts/create", s.accountAction("create"))
	s.mux.HandleFunc("POST /accounts/role", s.accountAction("role"))
	s.mux.HandleFunc("GET /audit", s.audit)
	s.mux.HandleFunc("GET /static/app.css", s.styleCSS)
}

// ------------------------------------------------------------------ session

// current returns the signed-in user, or nil. An expired or forged cookie is
// simply nobody, never an error the caller has to branch on.
func (s *Server) current(r *http.Request) *auth.User {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return nil
	}
	u, err := s.au.SessionUser(c.Value)
	if err != nil {
		return nil
	}
	return u
}

func (s *Server) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   auth.SessionHours * 3600,
	})
}

// ----------------------------------------------------------------- handlers

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

// redirect writes the Location and nothing else.
//
// Not http.Redirect: for a GET it helpfully appends an HTML body
// ("<a href=...>Temporary Redirect</a>."), and the original's response has an
// empty body. The parity gate caught it on the first run against the golden
// master, which is the whole reason the gate exists.
func (s *Server) redirect(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", to)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}
}

func (s *Server) intakeTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body string
	switch name {
	case "budgets.csv":
		body = "platform,team,month,budget_usd\n" +
			"aws,sre-platform,2026-09,960\n" +
			"aws,ml,2026-09,820\n"
	case "requests.csv":
		body = "platform,team,title,kind,est_monthly_usd,status,target_month,note\n" +
			"gcp,ml,GKE burst pool,capacity,540,new,2026-10,fill me\n"
	default:
		http.Error(w, "unknown template", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	fmt.Fprint(w, body)
}

// The stylesheet is embedded so the binary stays self-contained: one file to
// copy to a server, and no chance of a page rendering unstyled because an
// asset directory did not travel with it.
//
//go:embed assets/app.css
var styleSheet string

func (s *Server) styleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	fmt.Fprint(w, styleSheet)
}

// -------------------------------------------------------------------- auth

// The sign-in and sign-up pages are the two surfaces the parity gate cannot
// capture, because the capture itself has to post to them to get a session.
// They are written for a person rather than for the golden master.
const authPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>CostCrew</title><link rel="stylesheet" href="/static/app.css"></head>
<body><main style="max-width:380px;margin:9vh auto;padding:0 20px">
<h1 style="font-size:27px;letter-spacing:-.02em;margin:0 0 8px">CostCrew</h1>
<p style="color:var(--ink-2);margin:0 0 22px">%s</p>
%s
<form class="action" method="post" action="%s">
<input type="hidden" name="csrf" value="%s">
<div><label for="u">Name</label><input id="u" type="text" name="username" autocomplete="username" autofocus></div>
<div><label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password"></div>
%s
<div class="row"><button type="submit">%s</button></div>
</form>
</main></body></html>
`

func (s *Server) authPage(w http.ResponseWriter, r *http.Request, signup bool) {
	msg := ""
	if m := r.URL.Query().Get("msg"); m != "" {
		msg = "<p class=\"msg\">" + htmlEscape(m) + "</p>"
	}
	token := s.au.CSRFToken(s.sessionToken(r))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if signup {
		code := ""
		if auth.SignupCode() != "" {
			code = `<div><label for="c">Joining code</label><input id="c" type="text" name="code"></div>`
		}
		fmt.Fprintf(w, authPage,
			"The first account created becomes the admin of this installation.",
			msg, "/signup", token, code, "Create account")
		return
	}
	fmt.Fprintf(w, authPage, "Sign in to the console.", msg, "/login", token, "", "Sign in")
}

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	open, err := s.au.SignupOpen()
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	if !open {
		http.Redirect(w, r, "/login?msg="+urlQuery("registration is closed"), http.StatusSeeOther)
		return
	}
	s.authPage(w, r, true)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	// Nobody has claimed this installation yet, so there is no account to sign
	// in with. Showing the form anyway is how a first run becomes a dead end.
	if n, err := s.au.Count(); err == nil && n == 0 {
		http.Redirect(w, r, "/signup", http.StatusSeeOther)
		return
	}
	s.authPage(w, r, false)
}

func (s *Server) signupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/signup?msg="+urlQuery("reload the page and try again"), http.StatusSeeOther)
		return
	}
	if !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {
		http.Redirect(w, r, "/signup?msg="+urlQuery("reload the page and try again"), http.StatusSeeOther)
		return
	}
	open, err := s.au.SignupOpen()
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	if !open {
		http.Redirect(w, r, "/login?msg="+urlQuery("registration is closed"), http.StatusSeeOther)
		return
	}
	user := r.PostFormValue("username")
	ok, why, err := s.au.Register(user, r.PostFormValue("password"), r.PostFormValue("code"))
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Redirect(w, r, "/signup?msg="+urlQuery(why), http.StatusSeeOther)
		return
	}
	token, err := s.au.StartSession(strings.TrimSpace(user))
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	s.setSession(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?msg="+urlQuery("reload the page and try again"), http.StatusSeeOther)
		return
	}
	if !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {
		http.Redirect(w, r, "/login?msg="+urlQuery("reload the page and try again"), http.StatusSeeOther)
		return
	}
	u, why, err := s.au.Authenticate(r.PostFormValue("username"), r.PostFormValue("password"))
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	if u == nil {
		http.Redirect(w, r, "/login?msg="+urlQuery(why), http.StatusSeeOther)
		return
	}
	token, err := s.au.StartSession(u.Username)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	s.setSession(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.au.EndSession(s.sessionToken(r))
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ------------------------------------------------------------------ helpers

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func urlQuery(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "%20"), ";", "%3B")
}
