package sso_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/sso"
	"github.com/TAIPANBOX/costcrew/internal/sso/ssotest"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

const redirect = "http://127.0.0.1:8321" + sso.CallbackPath

func inputs(idp *ssotest.Provider) sso.Inputs {
	return sso.Inputs{
		Issuer: idp.Issuer(), ClientID: ssotest.ClientID, SecretEnv: ssotest.ClientSecret,
		RedirectURL: redirect,
		Roles:       "finops-viewers=viewer;finops-operators=operator;finops-admins=admin",
	}
}

func newProvider(t *testing.T, idp *ssotest.Provider, mut func(*sso.Inputs)) (*sso.Provider, *sql.DB) {
	t.Helper()
	in := inputs(idp)
	if mut != nil {
		mut(&in)
	}
	cfg, err := sso.Load(in)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := sso.New(st.DB(), *cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, st.DB()
}

// signIn runs one whole flow: Begin, the person at the provider, Finish.
func signIn(t *testing.T, p *sso.Provider, idp *ssotest.Provider, g ssotest.Grant) (*sso.Identity, error) {
	t.Helper()
	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	code, back := idp.Authorize(t, authURL, g)
	if back != state {
		t.Fatalf("the provider was sent state %q, the browser holds %q", back, state)
	}
	return p.Finish(context.Background(), url.Values{"code": {code}, "state": {back}}, state)
}

// refused requires a Failure, and returns its detail for the reason check.
func refused(t *testing.T, id *sso.Identity, err error, want string) {
	t.Helper()
	if id != nil {
		t.Fatalf("the sign-in completed (%+v); want it refused for %q", *id, want)
	}
	var f *sso.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a *sso.Failure", err)
	}
	if !strings.Contains(f.Detail, want) {
		t.Errorf("refused for %q, want the reason to say %q", f.Detail, want)
	}
	if f.Public != sso.MsgStartAgain && f.Public != sso.MsgUnreachable {
		t.Errorf("the person is shown %q, which is not one of the two fixed sentences", f.Public)
	}
}

// ------------------------------------------------------------ configuration

func TestLoadIsOffWhenNothingIsConfigured(t *testing.T) {
	cfg, err := sso.Load(sso.Inputs{})
	if cfg != nil || err != nil {
		t.Fatalf("Load of nothing = %v, %v; want nil, nil (sign-in stays as it was)", cfg, err)
	}
	if got := cfg.Describe(); !strings.Contains(got, "off") {
		t.Errorf("Describe of no configuration = %q, want it to say the feature is off", got)
	}
}

func TestLoadRefusesWhatIsHalfConfiguredOrUnsafe(t *testing.T) {
	idp := ssotest.New(t)
	cases := map[string]struct {
		mut  func(*sso.Inputs)
		want string
	}{
		"settings with no issuer":   {func(in *sso.Inputs) { in.Issuer = "" }, "-oidc-issuer is not set"},
		"-oidc-only with no issuer": {func(in *sso.Inputs) { *in = sso.Inputs{Only: true} }, "-oidc-issuer is not set"},
		"no client id":              {func(in *sso.Inputs) { in.ClientID = "" }, "-oidc-client-id"},
		"no secret":                 {func(in *sso.Inputs) { in.SecretEnv = "" }, "client secret is required"},
		"no redirect":               {func(in *sso.Inputs) { in.RedirectURL = "" }, "-oidc-redirect-url"},
		"plain http issuer off loopback": {func(in *sso.Inputs) { in.Issuer = "http://idp.example.test" },
			"must be https"},
		"plain http redirect off loopback": {func(in *sso.Inputs) {
			in.RedirectURL = "http://costcrew.example.test" + sso.CallbackPath
		}, "must be https"},
		"an issuer with a query": {func(in *sso.Inputs) { in.Issuer = "https://idp.example.test/?x=1" }, "query"},
		"a relative issuer":      {func(in *sso.Inputs) { in.Issuer = "/realms/x" }, "not an absolute"},
		"a redirect off the callback path": {func(in *sso.Inputs) {
			in.RedirectURL = "https://costcrew.example.test/callback"
		}, "must end in " + sso.CallbackPath},
		"no role mapping":       {func(in *sso.Inputs) { in.Roles = "" }, "-oidc-roles is required"},
		"scopes without openid": {func(in *sso.Inputs) { in.Scopes = "email profile" }, "must include openid"},
		"a roles claim that is not a name": {func(in *sso.Inputs) { in.RolesClaim = "gro ups" },
			"is not a claim name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := inputs(idp)
			c.mut(&in)
			cfg, err := sso.Load(in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load = %v, %v; want a refusal saying %q", cfg, err, c.want)
			}
		})
	}
	// And a loopback provider over plain http is fine: that is a provider
	// running beside the console, and what every test here uses.
	if _, err := sso.Load(inputs(idp)); err != nil {
		t.Fatalf("a loopback http issuer was refused: %v", err)
	}
}

func TestTheRoleMappingIsStrictAndHasNoDefault(t *testing.T) {
	for spec, want := range map[string]string{
		"finops":                        "has no '='",
		"finops=superuser":              "is not viewer, operator or admin",
		"=admin":                        "names no claim value",
		"a=viewer;a=admin":              "maps \"a\" to both",
		" ; ;":                          "-oidc-roles is required",
		"fin\x00ops=admin":              "names no claim value",
		"finops=Admin":                  "is not viewer, operator or admin",
		"finops=admin;finops-x=viewer=": "is not viewer, operator or admin",
	} {
		if _, err := sso.ParseRoles(spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseRoles(%q) = %v, want an error saying %q", spec, err, want)
		}
	}
	// An LDAP distinguished name carries ',' and '='; the last '=' splits.
	m, err := sso.ParseRoles("cn=finops,ou=groups,dc=example=admin; viewers = viewer ;a=viewer;a=viewer")
	if err != nil {
		t.Fatal(err)
	}
	if m["cn=finops,ou=groups,dc=example"] != "admin" || m["viewers"] != "viewer" || len(m) != 3 {
		t.Fatalf("ParseRoles = %v", m)
	}
	cfg := &sso.Config{Roles: map[string]string{"v": "viewer", "o": "operator", "a": "admin"}}
	for values, want := range map[string]string{
		"":        "",
		"x,y":     "",
		"v":       "viewer",
		"v,o":     "operator",
		"o,v,a,x": "admin",
		"V,ADMIN": "",
		"a ,o":    "operator",
	} {
		var in []string
		if values != "" {
			in = strings.Split(values, ",")
		}
		if got := cfg.RoleFor(in); got != want {
			t.Errorf("RoleFor(%q) = %q, want %q", values, got, want)
		}
	}
}

func TestTheClientSecretComesFromOnePlaceAndNeverPrints(t *testing.T) {
	idp := ssotest.New(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte(ssotest.ClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := inputs(idp)
	in.SecretFile = file
	if _, err := sso.Load(in); err == nil || !strings.Contains(err.Error(), "set one") {
		t.Errorf("a secret in both the environment and a file was accepted (%v)", err)
	}
	in.SecretEnv = ""
	cfg, err := sso.Load(in)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range map[string][]byte{"empty": []byte("\n"), "huge": make([]byte, 5000)} {
		f := filepath.Join(dir, "bad")
		if err := os.WriteFile(f, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		in.SecretFile = f
		if _, err := sso.Load(in); err == nil {
			t.Errorf("a secret file of %d bytes was accepted", len(bad))
		}
	}
	in.SecretFile = filepath.Join(dir, "missing")
	if _, err := sso.Load(in); err == nil {
		t.Error("a missing secret file was accepted")
	}

	printed := []string{
		fmt.Sprint(cfg), fmt.Sprint(*cfg), fmt.Sprintf("%v %+v %#v %s %q %x", *cfg, *cfg, *cfg,
			cfg.ClientSecret, cfg.ClientSecret, cfg.ClientSecret), cfg.Describe(),
		fmt.Errorf("wrapped: %v", cfg.ClientSecret).Error(),
	}
	for _, s := range printed {
		if strings.Contains(s, ssotest.ClientSecret) || strings.Contains(s, fmt.Sprintf("%x", ssotest.ClientSecret)) {
			t.Errorf("the client secret is printed: %s", s)
		}
	}
	if !strings.Contains(cfg.Describe(), idp.Issuer()) || !strings.Contains(cfg.Describe(), ssotest.ClientID) {
		t.Errorf("the startup line %q does not name the issuer and the client", cfg.Describe())
	}
}

// ---------------------------------------------------------------- the flow

func TestAGoodSignInEstablishesWhoAndWhichRole(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	id, err := signIn(t, p, idp, ssotest.Grant{Claims: map[string]any{
		"groups": []string{"unrelated", "finops-operators"},
	}})
	if err != nil {
		t.Fatalf("a good sign-in was refused: %v", err)
	}
	want := sso.Identity{Issuer: idp.Issuer(), Subject: "subject-alice", Username: "alice@example.test", Role: "operator"}
	if *id != want {
		t.Fatalf("identity = %+v, want %+v", *id, want)
	}
	// PKCE: the token request carries the verifier, and the fake provider
	// only issued the token because S256(verifier) matched the challenge.
	reqs := idp.TokenRequests()
	if len(reqs) != 1 || reqs[0].Get("code_verifier") == "" || reqs[0].Get("client_secret") != "" &&
		reqs[0].Get("client_secret") != ssotest.ClientSecret {
		t.Fatalf("token requests = %v, want one carrying a code_verifier", reqs)
	}
}

func TestTheIDTokenIsCheckedClaimByClaim(t *testing.T) {
	idp := ssotest.New(t)
	now := time.Now()
	cases := map[string]struct {
		g    ssotest.Grant
		want string
	}{
		"wrong audience":    {ssotest.Grant{Claims: map[string]any{"aud": "another-client"}}, "expected audience"},
		"expired":           {ssotest.Grant{Claims: map[string]any{"exp": now.Add(-time.Minute).Unix()}}, "expired"},
		"bad signature":     {ssotest.Grant{BadSignature: true}, "signature"},
		"wrong issuer":      {ssotest.Grant{Claims: map[string]any{"iss": "https://elsewhere.example.test"}}, "different provider"},
		"missing nonce":     {ssotest.Grant{Drop: []string{"nonce"}}, "no nonce"},
		"wrong nonce":       {ssotest.Grant{Claims: map[string]any{"nonce": "not-the-one-sent"}}, "not the one this sign-in sent"},
		"missing iat":       {ssotest.Grant{Drop: []string{"iat"}}, "no iat"},
		"iat in the future": {ssotest.Grant{Claims: map[string]any{"iat": now.Add(sso.MaxClockSkew + time.Minute).Unix()}}, "in the future"},
		"iat before the sign-in began": {ssotest.Grant{Claims: map[string]any{
			"iat": now.Add(-sso.MaxClockSkew - time.Minute).Unix()}}, "before this sign-in began"},
		"missing subject": {ssotest.Grant{Drop: []string{"sub"}}, "no subject"},
		"two audiences and no azp": {ssotest.Grant{Claims: map[string]any{
			"aud": []string{ssotest.ClientID, "another-client"}}}, "issued to"},
		"azp naming another client": {ssotest.Grant{Claims: map[string]any{"azp": "another-client"}}, "issued to"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			id, err := signIn(t, p, idp, c.g)
			refused(t, id, err, c.want)
		})
	}
	// The boundaries on the other side: within the skew, and two audiences
	// with this client as the authorized party, both complete.
	for name, g := range map[string]ssotest.Grant{
		"iat just inside the skew": {Claims: map[string]any{"iat": now.Add(sso.MaxClockSkew - 30*time.Second).Unix()}},
		"two audiences, azp is this client": {Claims: map[string]any{
			"aud": []string{ssotest.ClientID, "another-client"}, "azp": ssotest.ClientID}},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			if _, err := signIn(t, p, idp, g); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

func TestAStateIsSpentByItsFirstUse(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := idp.Authorize(t, authURL, ssotest.Grant{})
	q := url.Values{"code": {code}, "state": {state}}
	if _, err := p.Finish(context.Background(), q, state); err != nil {
		t.Fatalf("the first use was refused: %v", err)
	}
	id, err := p.Finish(context.Background(), q, state)
	refused(t, id, err, "unknown or was already used")
	if n := len(idp.TokenRequests()); n != 1 {
		t.Errorf("the replay reached the token endpoint (%d token requests, want 1)", n)
	}
}

func TestAStateFromAnotherBrowserIsRefusedAndBurned(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := idp.Authorize(t, authURL, ssotest.Grant{})
	q := url.Values{"code": {code}, "state": {state}}
	for _, browser := range []string{"", "some-other-state"} {
		id, err := p.Finish(context.Background(), q, browser)
		refused(t, id, err, "")
	}
	// The right browser coming after is refused too: the state was burned by
	// the first presentation, so a victim cannot be tricked into finishing a
	// sign-in an attacker started.
	id, err := p.Finish(context.Background(), q, state)
	refused(t, id, err, "unknown or was already used")
}

func TestAStateOlderThanItsLifetimeIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	p, db := newProvider(t, idp, nil)
	authURL, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := idp.Authorize(t, authURL, ssotest.Grant{})
	if _, err := db.Exec(`UPDATE oidc_pending SET expires = expires - ?`, sso.PendingLifetime.Seconds()+1); err != nil {
		t.Fatal(err)
	}
	id, err := p.Finish(context.Background(), url.Values{"code": {code}, "state": {state}}, state)
	refused(t, id, err, "more than")
}

func TestThePendingRowHoldsNoState(t *testing.T) {
	idp := ssotest.New(t)
	p, db := newProvider(t, idp, nil)
	_, state, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := db.QueryRow(`SELECT state_hash FROM oidc_pending`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key == state || strings.Contains(key, state) {
		t.Fatal("the table holds the state itself; a copy of app.db could finish a sign-in in progress")
	}
}

func TestAnErrorOrNoCodeFromTheProviderIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	for name, q := range map[string]url.Values{
		"error":   {"error": {"access_denied"}, "error_description": {"<script>"}},
		"no code": {},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			_, state, err := p.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			q.Set("state", state)
			id, err := p.Finish(context.Background(), q, state)
			refused(t, id, err, "")
		})
	}
	p, _ := newProvider(t, idp, nil)
	id, err := p.Finish(context.Background(), url.Values{"code": {"x"}}, "x")
	refused(t, id, err, "no state")
}

func TestTheClaimsThatNameAndMapAreReadStrictly(t *testing.T) {
	idp := ssotest.New(t)
	refusedName := map[string]any{
		"missing":         nil,
		"a number":        42,
		"empty":           "  ",
		"too long":        strings.Repeat("a", 129),
		"a control":       "alice\x07@example.test",
		"a bidi override": "alice\u202e@example.test",
	}
	for name, v := range refusedName {
		t.Run("username "+name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			g := ssotest.Grant{Claims: map[string]any{"email": v}}
			if v == nil {
				g = ssotest.Grant{Drop: []string{"email"}}
			}
			id, err := signIn(t, p, idp, g)
			refused(t, id, err, "email claim")
		})
	}
	noRole := map[string]any{
		"missing":         nil,
		"a number":        7,
		"an object":       map[string]any{"finops-admins": true},
		"unmapped values": []string{"everyone", "FINOPS-ADMINS"},
		"an empty array":  []string{},
	}
	for name, v := range noRole {
		t.Run("groups "+name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			g := ssotest.Grant{Claims: map[string]any{"groups": v}}
			if v == nil {
				g = ssotest.Grant{Drop: []string{"groups"}}
			}
			id, err := signIn(t, p, idp, g)
			if err != nil {
				t.Fatalf("refused outright: %v; want a completed sign-in with no role", err)
			}
			if id.Role != "" {
				t.Fatalf("role = %q from groups %v; there is no default role", id.Role, v)
			}
		})
	}
	// A single string, and an array mixing strings with other things: the
	// strings count.
	for name, v := range map[string]any{
		"a single string": "finops-admins",
		"a mixed array":   []any{1, map[string]any{}, "finops-admins", nil},
	} {
		t.Run("groups "+name, func(t *testing.T) {
			p, _ := newProvider(t, idp, nil)
			id, err := signIn(t, p, idp, ssotest.Grant{Claims: map[string]any{"groups": v}})
			if err != nil || id.Role != "admin" {
				t.Fatalf("groups %v gave %+v, %v; want admin", v, id, err)
			}
		})
	}
}

// ------------------------------------------------------ the network, bounded

func TestTheProviderIsNotContactedUntilASignInStarts(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	if got := idp.Requests(); len(got) != 0 {
		t.Fatalf("preparing the flow contacted the provider: %v", got)
	}
	if _, _, err := p.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := idp.Requests(); len(got) != 1 || got[0] != "GET /.well-known/openid-configuration" {
		t.Fatalf("two sign-ins started made %v; want discovery once, kept", got)
	}
}

func TestAProviderThatIsDownIsUnreachableNotACrash(t *testing.T) {
	idp := ssotest.New(t)
	p, _ := newProvider(t, idp, nil)
	idp.Down()
	_, _, err := p.Begin(context.Background())
	var f *sso.Failure
	if !errors.As(err, &f) || f.Public != sso.MsgUnreachable {
		t.Fatalf("Begin with the provider down = %v; want the unreachable sentence", err)
	}

	// Down after discovery, at the token endpoint.
	idp2 := ssotest.New(t)
	p2, _ := newProvider(t, idp2, nil)
	authURL, state, err := p2.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := idp2.Authorize(t, authURL, ssotest.Grant{})
	idp2.Down()
	id, err := p2.Finish(context.Background(), url.Values{"code": {code}, "state": {state}}, state)
	refused(t, id, err, "could not be reached")
	if !errors.As(err, &f) || f.Public != sso.MsgUnreachable {
		t.Fatalf("the person is shown %q, want the unreachable sentence", f.Public)
	}

	// A provider answering 503 is the same sentence, and the failure is not
	// kept: once it answers again, the next sign-in discovers it.
	idp3 := ssotest.New(t)
	p3, _ := newProvider(t, idp3, nil)
	idp3.Unavailable.Store(true)
	if _, _, err := p3.Begin(context.Background()); !errors.As(err, &f) || f.Public != sso.MsgUnreachable {
		t.Fatalf("Begin against a 503 = %v; want the unreachable sentence", err)
	}
	idp3.Unavailable.Store(false)
	if _, _, err := p3.Begin(context.Background()); err != nil {
		t.Fatalf("the provider is back and Begin still fails: %v", err)
	}
}

func TestADiscoveryDocumentNamingAnotherIssuerIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	idp.DiscoveryIssuer = "https://impostor.example.test"
	p, _ := newProvider(t, idp, nil)
	_, _, err := p.Begin(context.Background())
	var f *sso.Failure
	if !errors.As(err, &f) || !strings.Contains(f.Detail, "impostor") {
		t.Fatalf("Begin = %v; want a refusal naming the issuer the document claims", err)
	}
}

// go-oidc puts the body of a failed discovery response into its error, and
// that error is what the journal records. A provider (or whatever answers in
// its place) must not be able to write a page of its own text, control
// characters included, into the hash chain.
func TestWhatTheProviderSaysReachesTheJournalBoundedAndPlain(t *testing.T) {
	idp := ssotest.New(t)
	idp.UnavailableBody = strings.Repeat("A", 100_000) + "\x1b[2J\r\nforged line"
	idp.Unavailable.Store(true)
	p, _ := newProvider(t, idp, nil)
	_, _, err := p.Begin(context.Background())
	var f *sso.Failure
	if !errors.As(err, &f) {
		t.Fatalf("Begin = %v, want a Failure", err)
	}
	if len(f.Detail) > 512 {
		t.Errorf("the journal detail is %d bytes, want at most 512", len(f.Detail))
	}
	if strings.ContainsAny(f.Detail, "\x1b\r\n") {
		t.Errorf("the journal detail carries control characters: %q", f.Detail[max(0, len(f.Detail)-80):])
	}
}

// The authorization endpoint is where the browser is sent, so it is held to
// the same rule as the issuer: https, or http only to this machine.
func TestADiscoveredAuthorizationEndpointOverPlainHTTPIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	idp.AuthorizeEndpoint = "http://login.example.test/authorize"
	p, _ := newProvider(t, idp, nil)
	_, _, err := p.Begin(context.Background())
	var f *sso.Failure
	if !errors.As(err, &f) || !strings.Contains(f.Detail, "authorization endpoint") {
		t.Fatalf("Begin = %v; want a refusal naming the authorization endpoint", err)
	}
}

func TestAResponseOverTheCapIsRefusedNotRead(t *testing.T) {
	idp := ssotest.New(t)
	idp.JWKSPadding = 2 << 20
	p, _ := newProvider(t, idp, nil)
	id, err := signIn(t, p, idp, ssotest.Grant{})
	refused(t, id, err, "larger than 1 MiB")
}

func TestNoRedirectFromTheProviderIsFollowed(t *testing.T) {
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	t.Cleanup(other.Close)
	idp := ssotest.New(t)
	idp.TokenRedirect = other.URL + "/token"
	p, _ := newProvider(t, idp, nil)
	id, err := signIn(t, p, idp, ssotest.Grant{})
	refused(t, id, err, "")
	if elsewhere != 0 {
		t.Fatalf("the token request followed the provider's redirect to another server (%d requests there)", elsewhere)
	}
}
