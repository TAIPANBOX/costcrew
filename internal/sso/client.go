package sso

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// providerClient is the only name flow.go uses for it, so the egress walk
// (invariant 64) finds every outbound construction of this package in this
// one file and can require that it stays here.
type providerClient = *http.Client

// newClient is the one place the console's sign-in reaches the network, and
// the second of the two doors invariant 64 names (the first is
// internal/deliver.Call). Every request it makes goes to the configured
// provider: discovery at the issuer, then the JWKS and the token endpoint the
// issuer's own discovery document names. It is never built unless an issuer
// is configured, and it makes no request until somebody starts a sign-in.
//
// Three things the libraries above it do not do, done here:
//
//   - no redirect is followed. A provider's endpoints answer where discovery
//     says they are; a redirect from one is either a misconfiguration or
//     somebody else's server, and following it would send the client secret
//     and the authorization code wherever it points;
//   - a URL that is not https is refused unless its host is loopback, the
//     same rule Load applies to the issuer, because the discovery document
//     names the JWKS and token URLs and is itself only as trustworthy as the
//     connection it came over;
//   - every body is capped at maxResponseBytes, and a body over the cap is an
//     error rather than a silently truncated document.
func newClient() providerClient {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: guarded{next: base},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("the identity provider answered with a redirect to %s; not followed", req.URL.Redacted())
		},
	}
}

type guarded struct{ next http.RoundTripper }

func (g guarded) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && Loopback(req.URL.Hostname())) {
		return nil, fmt.Errorf("refusing %s: the identity provider is reached over https only, "+
			"or plain http to a loopback host", req.URL.Redacted())
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &capped{r: resp.Body, left: maxResponseBytes}
	return resp, nil
}

// errTooLarge is what a body over the cap reads as.
var errTooLarge = errors.New("the identity provider's response is larger than 1 MiB")

type capped struct {
	r    io.ReadCloser
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// One byte more than the cap is enough to know it was over.
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *capped) Close() error { return c.r.Close() }
