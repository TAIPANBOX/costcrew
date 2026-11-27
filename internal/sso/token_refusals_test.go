package sso_test

// The token endpoint's refusals: a code the provider never issued, a code
// already exchanged once, and a client secret that is not the registered
// one. Each is a refusal the person can start again from, and the journal's
// detail names the status and the OAuth error code, never the body.

import (
	"context"
	"net/url"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/sso"
	"github.com/TAIPANBOX/costcrew/internal/sso/ssotest"
)

func TestACodeTheProviderNeverIssuedIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, back := idp.Authorize(t, authURL, ssotest.Grant{})
	id, err := p.Finish(context.Background(), url.Values{"code": {"forged-code"}, "state": {back}}, state)
	refused(t, id, err, `the token endpoint refused the code (HTTP 400, "invalid_grant")`)
}

func TestACodeIsExchangedOnlyOnce(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)

	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, back := idp.Authorize(t, authURL, ssotest.Grant{})
	if id, err := p.Finish(context.Background(), url.Values{"code": {code}, "state": {back}}, state); err != nil || id == nil {
		t.Fatalf("the first exchange: %v", err)
	}

	// The same code under a fresh sign-in of this browser.
	_, state2, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Finish(context.Background(), url.Values{"code": {code}, "state": {state2}}, state2)
	refused(t, id, err, `(HTTP 400, "invalid_grant")`)
}

func TestAWrongClientSecretIsRefusedAtTheTokenEndpoint(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, func(in *sso.Inputs) { in.SecretEnv = "not-the-registered-secret" })
	id, err := signIn(t, p, idp, ssotest.Grant{})
	refused(t, id, err, "the token endpoint refused the code (HTTP 401")
}
