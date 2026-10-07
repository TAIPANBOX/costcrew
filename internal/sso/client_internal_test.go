package sso

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type recorder struct{ calls int }

func (r *recorder) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

// The discovery document names the JWKS and token URLs, so a document that
// arrived intact can still point at plain http elsewhere. The client refuses
// that before a connection is attempted.
func TestTheSignInClientReachesOnlyHTTPSOrLoopback(t *testing.T) {
	rec := &recorder{}
	c := &http.Client{Transport: guarded{next: rec}}
	for _, u := range []string{
		"http://idp.example.test/jwks", "http://10.0.0.1/token", "ftp://idp.example.test/x",
		"http://localhost.example.test/x",
	} {
		if _, err := c.Get(u); err == nil || !strings.Contains(err.Error(), "https only") {
			t.Errorf("GET %s = %v; want refused before any connection", u, err)
		}
	}
	if rec.calls != 0 {
		t.Fatalf("%d refused URL(s) reached the transport", rec.calls)
	}
	for _, u := range []string{"https://idp.example.test/jwks", "http://127.0.0.1:9/x", "http://localhost:9/x", "http://[::1]:9/x"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Errorf("GET %s refused: %v", u, err)
			continue
		}
		resp.Body.Close()
	}
	if rec.calls != 4 {
		t.Fatalf("allowed URLs reached the transport %d times, want 4", rec.calls)
	}
}

func TestTheSignInClientIsTheGuardedOne(t *testing.T) {
	c := newClient()
	if _, ok := c.Transport.(guarded); !ok || c.CheckRedirect == nil || c.Timeout == 0 {
		t.Fatalf("newClient = %+v; want the guarded transport, no redirects and a timeout", c)
	}
}
