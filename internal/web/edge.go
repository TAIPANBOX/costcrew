package web

// The edge of the HTTP surface: what every response carries, how much of a
// request this console will read, and how long the server waits on a peer.
// Invariant 59. All three used to be absent: a page could be framed, sniffed
// or scripted by whatever an imported file put into it, a POST could be any
// size, and the server set one timeout (for the request line) and nothing else.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// contentSecurityPolicy is the strictest policy the pages as they stand
// satisfy, and the reason for each directive is the reason it is not wider.
//
// No page here runs a script: there is no <script>, no event-handler
// attribute and no javascript: URL anywhere in templates/ (the one control
// that needed a script, Sign out, is a form button now), so default-src 'none'
// leaves script-src unset and falls to none. TestNoPageReliesOnWhatThePolicy-
// Forbids reads every template and every served page for exactly those.
//
// style-src allows 'unsafe-inline' and is the only directive that does. The
// templates carry style= attributes (about sixty) and the login page and the
// downloadable report carry inline styling, so a stricter style-src would
// break the layout. That is a narrow concession: inline style cannot run code
// in a current browser, and it is named here rather than hidden by a wildcard.
//
// form-action 'self' stops an injected form posting a session's CSRF token
// elsewhere; base-uri 'none' stops an injected <base> redirecting every
// relative link; frame-ancestors 'none' is X-Frame-Options DENY for the
// browsers that read CSP first.
const contentSecurityPolicy = "default-src 'none'; style-src 'self' 'unsafe-inline'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// hsts is sent only where TLS is known to be in front (see securityHeaders).
// Six months, and no includeSubDomains or preload: this console says nothing
// about the rest of its host's name, and a longer or wider promise is one an
// operator cannot take back from a browser that has already heard it.
const hsts = "max-age=15552000"

// securityHeaders is set on every response before any handler runs, including
// /login, /healthz, /static/, the exports, the 404 and the 413. A handler that
// forgets cannot omit it, because no handler is asked.
//
// Strict-Transport-Security follows the cookie posture exactly (setCookie): on
// when -behind-tls says a TLS proxy is in front, or when this process
// terminated TLS itself. Sent over plain HTTP it is ignored by browsers, and
// sent where TLS is not really in front it would lock a host out of HTTP.
func (s *Server) securityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	if s.behindTLS || r.TLS != nil {
		h.Set("Strict-Transport-Security", hsts)
	}
}

// noStore marks every response but the stylesheet as one no cache may keep.
// Invariant 79.
//
// Every page and every download here is the estate's money, a person's
// decisions or a CSRF token, and none of them said anything about caching, so
// a browser on a shared machine kept them on disk and the back button showed
// them after sign-out, and a proxy in front was free to store them. no-store
// is the one directive that covers all three; private or no-cache still let a
// copy be written down. It is set before routing, so a handler that forgets
// cannot leave it out, and that includes the redirect a stranger is turned
// away with and the 404.
//
// /static/ is the exception and the only one: it is the stylesheet, shared by
// every page and holding nothing about the estate, so caching it costs nothing
// and refusing to would fetch it again on every page.
func noStore(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/static/") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
}

// ---------------------------------------------------------------------------
// Request bodies.

const (
	// maxBody is the cap on any request body without a reason to be larger.
	// The biggest ordinary form on this console is a hire or a re-brief, a few
	// kilobytes of text; a megabyte is two orders over that.
	maxBody = 1 << 20

	// multipartOverhead is what a multipart envelope adds around a file: the
	// boundary lines, the part headers, the csrf field.
	multipartOverhead = 64 << 10
)

// bodyLimit is the most this console reads of one request, by path.
//
// /intake/check keeps its own cap: the file may be up to maxIntake (intake.go)
// and the multipart envelope around it comes on top. /intake/apply is the
// round trip of that same file, carried back inside a form field, and a
// browser URL-encodes a newline as %0D%0A, six bytes for one, so a file of
// exactly maxIntake can honestly arrive as six times that. The cap there is the
// bound of what the check page can produce, not a guess; anything over it did
// not come from that page.
func bodyLimit(path string) int64 {
	switch path {
	case "/intake/check":
		return maxIntake + multipartOverhead
	case "/intake/apply":
		return 6*maxIntake + multipartOverhead
	}
	return maxBody
}

// limitBody refuses a request whose body is over its cap, before any handler
// sees it, and reports whether the request may go on.
//
// Why before, and not http.MaxBytesReader in each handler: there are forty
// POST handlers and each reads its form in its own way, several ignoring the
// parse error ("reload the page and try again" is the least useful sentence
// for a body that was too big). A request refused here changes nothing by
// construction, because no handler ran. The same reasoning put the intake
// upload's cap in the wrong place: ParseMultipartForm keeps maxMemory in RAM
// and spools the rest of a file to a temp file with no bound at all.
//
// A declared Content-Length over the cap is refused without reading a byte, so
// a client that sends "Expect: 100-continue" is told no before it sends the
// body. net/http guarantees a body never yields more than it declared, so a
// declared length at or under the cap is safe to stream, and it is wrapped in
// http.MaxBytesReader anyway. A body with no declared length (chunked) is read
// up to the cap plus one byte and refused if it is longer, which is the only
// way to answer 413 for it rather than hand a handler a truncated form.
func limitBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	max := bodyLimit(r.URL.Path)
	if r.ContentLength > max {
		tooLarge(w, max)
		return false
	}
	if r.ContentLength >= 0 {
		r.Body = http.MaxBytesReader(w, r.Body, max)
		return true
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		http.Error(w, "the request could not be read", http.StatusBadRequest)
		return false
	}
	if int64(len(buf)) > max {
		tooLarge(w, max)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	return true
}

func tooLarge(w http.ResponseWriter, max int64) {
	// The unread rest of the body is not worth keeping a connection open for.
	w.Header().Set("Connection", "close")
	http.Error(w, fmt.Sprintf("request too large: this page accepts at most %d bytes",
		max), http.StatusRequestEntityTooLarge)
}

// ---------------------------------------------------------------------------
// The server.

const (
	// ReadHeaderTimeout was the only timeout set. A client that has sent its
	// request line has ten seconds to finish the headers.
	ReadHeaderTimeout = 10 * time.Second

	// ReadTimeout bounds the whole request, body included. The largest body
	// this console reads is a budgets file round trip (about 12 MiB, only from
	// a pathological file), and a minute allows that at under 2 Mbit/s; every
	// ordinary form is a few kilobytes.
	ReadTimeout = 60 * time.Second

	// WriteTimeout runs from the end of the request headers to the end of the
	// response, so it must outlast the slowest legitimate handler. That is the
	// supervisor's plan-ask (planning.go), which waits on a model for up to
	// planAskTimeout and then renders; twice that leaves room for the render
	// and for a slow reader of the page. Every other page, measured, answers in
	// well under a second (see the pull request), so this is a bound for a
	// peer that stops reading, not a budget any page comes near.
	WriteTimeout = 2 * planAskTimeout

	// IdleTimeout is how long a keep-alive connection waits for its next
	// request. Without it a connection no browser will reuse is held open
	// until the peer decides otherwise.
	IdleTimeout = 120 * time.Second
)

// NewHTTPServer is the one place the console's http.Server is built, so the
// timeouts above are not a thing cmd/costcrew can leave out.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}
}
