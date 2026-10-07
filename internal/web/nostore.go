package web

import (
	"net/http"
	"strings"
)

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
