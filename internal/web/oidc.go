package web

import (
	"errors"
	"net/http"

	"github.com/TAIPANBOX/costcrew/internal/sso"
)

// oidcStateCookie binds a started sign-in to the browser that started it. It
// holds the state (the table holds only its hash), lives no longer than a
// started sign-in may take, and is scoped to the two OIDC paths. SameSite=Lax
// is what lets it ride along on the provider's redirect back, which is a
// top-level GET navigation from another site.
const oidcStateCookie = "costcrew_oidc_state"

// oidcStart sends the browser to the identity provider. It is reached by a
// plain link on the sign-in page, never by a form: see sso.StartPath.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	authURL, state, err := s.oidc.Begin(r.Context())
	if err != nil {
		s.oidcRefused(w, r, err)
		return
	}
	s.setCookie(w, r, &http.Cookie{
		Name: oidcStateCookie, Value: state, Path: sso.StartPath,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sso.PendingLifetime.Seconds()),
	})
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// oidcCallback is where the provider sends the browser back. Everything the
// provider says is checked in sso.Provider.Finish; what the identity may do
// here is decided in auth.SignInExternal; this only joins the two and starts
// the session.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	browserState := ""
	if c, err := r.Cookie(oidcStateCookie); err == nil {
		browserState = c.Value
	}
	// Spent whatever the outcome, like the state it carries.
	s.setCookie(w, r, &http.Cookie{Name: oidcStateCookie, Value: "", Path: sso.StartPath, MaxAge: -1})

	id, err := s.oidc.Finish(r.Context(), r.URL.Query(), browserState)
	if err != nil {
		s.oidcRefused(w, r, err)
		return
	}
	u, why, err := s.au.SignInExternal(id.Issuer, id.Subject, id.Username, id.Role)
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

// oidcRefused journals why a sign-in did not complete and shows the person
// the one sentence meant for them.
func (s *Server) oidcRefused(w http.ResponseWriter, r *http.Request, err error) {
	var f *sso.Failure
	if !errors.As(err, &f) {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	_, _ = s.st.Journal("external_sign_in_failed", 0, map[string]any{"detail": f.Detail})
	http.Redirect(w, r, "/login?msg="+urlQuery(f.Public), http.StatusSeeOther)
}

// oidcLink is the sign-in page's way to the provider, or nothing when no
// provider is configured.
func (s *Server) oidcLink() string {
	if s.oidc == nil {
		return ""
	}
	out := `<p class="row"><a class="button" href="` + sso.StartPath +
		`">Sign in with your organisation</a></p>`
	if s.oidc.Config().Only {
		out += `<p style="color:var(--ink-2)">Password sign-in is off. The form below ` +
			`takes only an account whose password was set from the command line, ` +
			`the way back in when the organisation's sign-in is down.</p>`
	}
	return out
}
