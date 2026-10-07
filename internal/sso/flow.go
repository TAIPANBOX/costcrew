package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// What a person is shown. Deliberately few, and none of them carries what the
// provider said: an error string from the outside is the journal's business,
// not the page's.
const (
	MsgUnreachable = "the organisation's sign-in could not be reached; try again in a few minutes"
	MsgStartAgain  = "the sign-in could not be completed; start it again"
)

// Failure is a sign-in that did not complete. Public is what the person sees;
// Detail is what the journal records, and never holds a token, a code, a
// nonce, a verifier or the client secret.
type Failure struct {
	Public string
	Detail string
}

func (f *Failure) Error() string { return f.Detail }

// maxDetailBytes bounds what one refusal writes into the journal. go-oidc puts
// the body of a failed discovery response into its error, so without a bound
// whatever answers at the issuer's address writes as much of its own text into
// the hash chain as it likes.
const maxDetailBytes = 512

func fail(public, format string, args ...any) *Failure {
	return &Failure{Public: public, Detail: bound(fmt.Sprintf(format, args...), maxDetailBytes)}
}

// Identity is what a completed sign-in established. Role is "" when no value
// of the roles claim is mapped: the person proved who they are and is still
// not let in.
type Identity struct {
	Issuer   string
	Subject  string
	Username string
	Role     string
}

// Provider runs the flow against one configured issuer.
type Provider struct {
	cfg    Config
	db     *sql.DB
	client providerClient
	now    func() time.Time

	mu     sync.Mutex
	oauth  *oauth2.Config
	verify *oidc.IDTokenVerifier
}

const pendingSchema = `CREATE TABLE IF NOT EXISTS oidc_pending(
	state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, verifier TEXT NOT NULL,
	created REAL NOT NULL, expires REAL NOT NULL)`

// New prepares the flow. It reaches nothing: discovery waits for the first
// person who starts a sign-in, so a provider that is down when the console
// starts costs that sign-in and not the console (password sign-in, and every
// page for a person already signed in, go on working).
func New(db *sql.DB, cfg Config) (*Provider, error) {
	if _, err := db.Exec(pendingSchema); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, db: db, client: newClient(), now: time.Now}, nil
}

// Config is the configuration this provider runs under.
func (p *Provider) Config() Config { return p.cfg }

// discover fetches the provider's metadata once and keeps it. A failure is
// not kept, so the next attempt tries again.
func (p *Provider) discover(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauth != nil {
		return p.oauth, p.verify, nil
	}
	cctx := oidc.ClientContext(ctx, p.client)
	prov, err := oidc.NewProvider(cctx, p.cfg.Issuer)
	if err != nil {
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			return nil, nil, fail(MsgUnreachable,
				"the discovery document names issuer %q, not the configured %q", mismatch.Discovered, mismatch.Provided)
		}
		return nil, nil, fail(MsgUnreachable, "discovery at %s: %v", p.cfg.Issuer, err)
	}
	// The browser is sent to the authorization endpoint, which the guarded
	// client never sees, so it is held to the issuer's rule here.
	if _, err := endpoint("the discovered authorization endpoint", prov.Endpoint().AuthURL); err != nil {
		return nil, nil, fail(MsgUnreachable, "%v", err)
	}
	// The key set is fetched through the same guarded client, with a context
	// that outlives this request: go-oidc keeps it for later refreshes.
	kctx := oidc.ClientContext(context.Background(), p.client)
	p.verify = prov.VerifierContext(kctx, &oidc.Config{ClientID: p.cfg.ClientID})
	p.oauth = &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret.reveal(),
		Endpoint: prov.Endpoint(), RedirectURL: p.cfg.RedirectURL, Scopes: p.cfg.Scopes,
	}
	return p.oauth, p.verify, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func stateKey(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Begin starts a sign-in: it returns the provider's authorization URL and the
// state the caller binds to the browser (a cookie). The state is stored only
// as its hash, beside the nonce and the PKCE verifier it travels with, and is
// good for one use within PendingLifetime.
func (p *Provider) Begin(ctx context.Context) (authURL, state string, err error) {
	oc, _, err := p.discover(ctx)
	if err != nil {
		return "", "", err
	}
	if state, err = randomToken(); err != nil {
		return "", "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	now := p.now()
	if _, err := p.db.Exec(`DELETE FROM oidc_pending WHERE expires < ?`, unix(now)); err != nil {
		return "", "", err
	}
	if _, err := p.db.Exec(
		`INSERT INTO oidc_pending(state_hash, nonce, verifier, created, expires) VALUES (?,?,?,?,?)`,
		stateKey(state), nonce, verifier, unix(now), unix(now.Add(PendingLifetime))); err != nil {
		return "", "", err
	}
	return oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

// Finish completes a sign-in from the provider's redirect. browserState is
// the state this browser was given by Begin (its cookie); the query's state
// must equal it, which is what stops somebody else's authorization code
// being completed in this browser (a login CSRF).
func (p *Provider) Finish(ctx context.Context, q url.Values, browserState string) (*Identity, error) {
	state := q.Get("state")
	if state == "" {
		return nil, fail(MsgStartAgain, "the provider's redirect carries no state")
	}
	// Taken out of the table whatever happens next: a state is spent by the
	// first redirect that names it, so the same redirect replayed finds
	// nothing, and a state presented by the wrong browser is burned rather
	// than left for the right one to be tricked into using.
	pend, err := p.consume(state)
	if err != nil {
		return nil, err
	}
	if browserState == "" || subtle.ConstantTimeCompare([]byte(state), []byte(browserState)) != 1 {
		return nil, fail(MsgStartAgain, "the state does not belong to the browser that presented it")
	}
	if pend == nil {
		return nil, fail(MsgStartAgain, "the state is unknown or was already used")
	}
	if p.now().After(pend.expires) {
		return nil, fail(MsgStartAgain, "the sign-in was started more than %s ago", PendingLifetime)
	}
	if e := q.Get("error"); e != "" {
		return nil, fail(MsgStartAgain, "the provider answered error %q", clip(e))
	}
	code := q.Get("code")
	if code == "" {
		return nil, fail(MsgStartAgain, "the provider's redirect carries no code")
	}
	oc, verifier, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := oc.Exchange(context.WithValue(ctx, oauth2.HTTPClient, p.client), code,
		oauth2.VerifierOption(pend.verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			// The body is not recorded: it is the provider's to word, and the
			// journal is not the place to keep whatever it chose to echo.
			return nil, fail(MsgStartAgain, "the token endpoint refused the code (HTTP %d, %q)",
				re.Response.StatusCode, clip(re.ErrorCode))
		}
		return nil, fail(MsgUnreachable, "the token endpoint could not be reached: %v", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, fail(MsgStartAgain, "the token response carries no id_token")
	}
	// Signature against the issuer's JWKS, iss, aud and exp: go-oidc.
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fail(MsgStartAgain, "the ID token was refused: %v", err)
	}
	// nonce: go-oidc leaves it to the caller.
	if idt.Nonce == "" {
		return nil, fail(MsgStartAgain, "the ID token carries no nonce")
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pend.nonce)) != 1 {
		return nil, fail(MsgStartAgain, "the ID token's nonce is not the one this sign-in sent")
	}
	// iat: go-oidc does not check it at all. Present, not from the future
	// beyond the skew, and not from before this sign-in began, so a token
	// minted for an earlier flow cannot complete this one.
	now := p.now()
	switch {
	case idt.IssuedAt.IsZero():
		return nil, fail(MsgStartAgain, "the ID token carries no iat")
	case idt.IssuedAt.After(now.Add(MaxClockSkew)):
		return nil, fail(MsgStartAgain, "the ID token was issued %s in the future, more than the %s skew allowed",
			idt.IssuedAt.Sub(now).Round(time.Second), MaxClockSkew)
	case idt.IssuedAt.Before(pend.created.Add(-MaxClockSkew)):
		return nil, fail(MsgStartAgain, "the ID token was issued before this sign-in began")
	}
	if idt.Subject == "" {
		return nil, fail(MsgStartAgain, "the ID token carries no subject")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, fail(MsgStartAgain, "the ID token's claims do not decode: %v", err)
	}
	// azp: OIDC Core 3.1.3.7. With more than one audience the token must name
	// this client as the party it was issued to; when it names one, it must
	// be this client whatever the audience.
	azp, _ := claims["azp"].(string)
	if (len(idt.Audience) > 1 && azp != p.cfg.ClientID) || (azp != "" && azp != p.cfg.ClientID) {
		return nil, fail(MsgStartAgain, "the ID token was issued to %q, not to this console", clip(azp))
	}
	name, err := username(claims[p.cfg.UsernameClaim])
	if err != nil {
		return nil, fail(MsgStartAgain, "the %s claim %v", p.cfg.UsernameClaim, err)
	}
	return &Identity{
		Issuer: p.cfg.Issuer, Subject: idt.Subject, Username: name,
		Role: p.cfg.RoleFor(claimValues(claims[p.cfg.RolesClaim])),
	}, nil
}

type pending struct {
	nonce, verifier  string
	created, expires time.Time
}

func (p *Provider) consume(state string) (*pending, error) {
	var nonce, verifier string
	var created, expires float64
	err := p.db.QueryRow(`DELETE FROM oidc_pending WHERE state_hash=? RETURNING nonce, verifier, created, expires`,
		stateKey(state)).Scan(&nonce, &verifier, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pending{nonce: nonce, verifier: verifier,
		created: fromUnix(created), expires: fromUnix(expires)}, nil
}

func fromUnix(f float64) time.Time {
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9))
}

// claimValues reads the roles claim: one string, or an array of which only
// the strings count. Anything else (a number, an object, a missing claim) is
// no values, so it maps to no role.
func claimValues(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// username is the account name the identity is created under the first time.
func username(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", errors.New("is missing or not a string")
	}
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("is empty")
	case len(s) > 128:
		return "", errors.New("is longer than 128 bytes")
	case !utf8.ValidString(s):
		return "", errors.New("is not valid UTF-8")
	case hasControl(s) || hasFormat(s):
		return "", errors.New("holds a control or text-direction character")
	}
	return s, nil
}

// clip keeps a string from outside short and printable before it reaches the
// journal.
func clip(s string) string { return bound(s, 64) }

// bound drops control characters and cuts s to at most n bytes of valid UTF-8.
func bound(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
