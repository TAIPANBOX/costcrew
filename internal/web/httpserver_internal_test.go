package web

import (
	"net/http"
	"testing"
	"time"
)

// A server with only ReadHeaderTimeout lets a peer hold a connection, and a
// goroutine and a buffer with it, by sending a body a byte a minute or by
// never reading its response. All four must be set, and WriteTimeout must be
// longer than the longest thing a handler is allowed to wait for, or the
// protection is a cut-off of the one request that was paid for.
func TestTheServerSetsEveryTimeoutAndOutlastsTheLongestHandler(t *testing.T) {
	h := http.NewServeMux()
	srv := NewHTTPServer("127.0.0.1:0", h)
	if srv.Addr != "127.0.0.1:0" || srv.Handler != h {
		t.Fatalf("NewHTTPServer dropped its arguments: addr %q handler %v", srv.Addr, srv.Handler)
	}
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s is %v: unset, so a peer can hold the connection as long as it likes", name, d)
		}
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want the 10s it has always been", srv.ReadHeaderTimeout)
	}
	// The plan-ask waits on a model for planAskTimeout and then renders a page.
	if srv.WriteTimeout < 2*planAskTimeout {
		t.Errorf("WriteTimeout %v is under twice the plan-ask's %v wait on a model: the server "+
			"would cut off a response whose call was already paid for", srv.WriteTimeout, planAskTimeout)
	}
	if srv.ReadTimeout >= srv.WriteTimeout {
		t.Errorf("ReadTimeout %v is not shorter than WriteTimeout %v", srv.ReadTimeout, srv.WriteTimeout)
	}
}
