// Package ssotest is an OpenID Connect identity provider that runs inside a
// test process: discovery, a JWKS, an authorization step the test drives by
// hand, and a token endpoint that checks the client's secret, the redirect URL,
// the one-use code and the PKCE verifier. It has its own RSA key, and a second
// one the JWKS never names, for a token whose signature must not verify.
//
// It exists so the sign-in can be tested end to end with no vendor on the
// network: every endpoint is an httptest server on loopback.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const (
	ClientID     = "costcrew-console"
	ClientSecret = "s3cret-client-value-that-must-never-be-logged"
	kid          = "test-key-1"
)

var (
	keysOnce       sync.Once
	signKey, rogue *rsa.PrivateKey
)

func keys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	keysOnce.Do(func() {
		var err error
		if signKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if rogue, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return signKey, rogue
}

// Grant is what the provider puts in the ID token for one authorization.
type Grant struct {
	// Claims are merged over the defaults (iss, aud, sub, iat, exp, nonce,
	// email, groups). A claim given here replaces the default.
	Claims map[string]any
	// Drop leaves default claims out of the token.
	Drop []string
	// BadSignature signs the token with a key the JWKS does not hold, under
	// the JWKS's own key id.
	BadSignature bool
}

type code struct {
	nonce, challenge, redirect string
	grant                      Grant
}

// Provider is the fake identity provider.
type Provider struct {
	Server *httptest.Server

	// DiscoveryIssuer, when set, is the issuer the discovery document claims,
	// so a test can make it disagree with the configured one.
	DiscoveryIssuer string
	// JWKSPadding adds this many bytes of padding to the JWKS response.
	JWKSPadding int
	// TokenRedirect makes the token endpoint answer a redirect to this URL.
	TokenRedirect string
	// Unavailable makes every endpoint answer 503 while it is true.
	Unavailable atomic.Bool
	// UnavailableBody is the body a 503 carries, when set.
	UnavailableBody string
	// AuthorizeEndpoint, when set, is the authorization endpoint discovery
	// names in place of this provider's own.
	AuthorizeEndpoint string

	mu       sync.Mutex
	codes    map[string]code
	requests []string
	tokenReq []url.Values
}

// New starts the provider and stops it when the test ends.
func New(t testing.TB) *Provider {
	t.Helper()
	keys(t)
	p := &Provider{codes: map[string]code{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		if p.Unavailable.Load() {
			body := "unavailable"
			if p.UnavailableBody != "" {
				body = p.UnavailableBody
			}
			http.Error(w, body, http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Server.Close)
	return p
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string { return p.Server.URL }

// Down stops the provider: every request to it from now on fails to connect.
func (p *Provider) Down() { p.Server.Close() }

// Requests is every request the provider has answered, "METHOD /path".
func (p *Provider) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// TokenRequests is the form of every request to the token endpoint.
func (p *Provider) TokenRequests() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.tokenReq...)
}

func (p *Provider) discovery(w http.ResponseWriter, r *http.Request) {
	iss := p.Issuer()
	if p.DiscoveryIssuer != "" {
		iss = p.DiscoveryIssuer
	}
	authz := p.Issuer() + "/authorize"
	if p.AuthorizeEndpoint != "" {
		authz = p.AuthorizeEndpoint
	}
	writeJSON(w, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                authz,
		"token_endpoint":                        p.Issuer() + "/token",
		"jwks_uri":                              p.Issuer() + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, r *http.Request) {
	sk, _ := keys(nil)
	set := map[string]any{"keys": []jose.JSONWebKey{{Key: &sk.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}}
	if p.JWKSPadding > 0 {
		set["padding"] = strings.Repeat("x", p.JWKSPadding)
	}
	writeJSON(w, set)
}

// Authorize plays the person signing in at the provider: it reads the
// console's authorization URL, checks what a provider would check, and
// returns the code and state the provider would redirect back with.
func (p *Provider) Authorize(t testing.TB, authURL string, g Grant) (codeValue, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("the authorization URL does not parse: %v", err)
	}
	if !strings.HasPrefix(authURL, p.Issuer()+"/authorize?") {
		t.Fatalf("the console sent the browser to %q, not to this provider's authorization endpoint", authURL)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": ClientID, "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Fatalf("authorization request %s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge", "redirect_uri"} {
		if q.Get(k) == "" {
			t.Fatalf("authorization request carries no %s", k)
		}
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		t.Fatalf("authorization request scope %q has no openid", q.Get("scope"))
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	codeValue = base64.RawURLEncoding.EncodeToString(b)
	p.mu.Lock()
	p.codes[codeValue] = code{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		redirect: q.Get("redirect_uri"), grant: g}
	p.mu.Unlock()
	return codeValue, q.Get("state")
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if p.TokenRedirect != "" {
		http.Redirect(w, r, p.TokenRedirect, http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request")
		return
	}
	p.mu.Lock()
	p.tokenReq = append(p.tokenReq, r.PostForm)
	p.mu.Unlock()
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != ClientID || secret != ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	c, found := p.codes[r.PostFormValue("code")]
	delete(p.codes, r.PostFormValue("code"))
	p.mu.Unlock()
	if !found || r.PostFormValue("grant_type") != "authorization_code" ||
		r.PostFormValue("redirect_uri") != c.redirect {
		oauthError(w, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		oauthError(w, "invalid_grant")
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.Issuer(), "aud": ClientID, "sub": "subject-alice",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": c.nonce,
		"email": "alice@example.test", "groups": []string{"finops-viewers"},
	}
	for _, k := range c.grant.Drop {
		delete(claims, k)
	}
	for k, v := range c.grant.Claims {
		claims[k] = v
	}
	sk, rk := keys(nil)
	key := sk
	if c.grant.BadSignature {
		key = rk
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	payload, _ := json.Marshal(claims)
	jws, err := signer.Sign(payload)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	raw, _ := jws.CompactSerialize()
	writeJSON(w, map[string]any{
		"access_token": "opaque-access-token", "token_type": "Bearer",
		"expires_in": 300, "id_token": raw,
	})
}

func oauthError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
