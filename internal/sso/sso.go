// Package sso signs a person in through the organisation's identity provider
// with OpenID Connect, so that multi-factor authentication and offboarding
// live where the organisation already keeps them (invariant 74).
//
// It is off unless an issuer is configured, and it speaks the protocol and
// nothing else: discovery, the authorization code flow with PKCE, state and
// nonce, and an ID token whose signature, issuer, audience, expiry, issue time
// and nonce are all checked before anything in it is believed. What an
// identity is ALLOWED to do here is not decided in this package: it maps a
// claim to a role and hands the answer to internal/auth, which owns accounts.
//
// The protocol libraries are github.com/coreos/go-oidc/v3 (discovery, the
// JWKS and the token signature) and golang.org/x/oauth2 (the code exchange and
// the PKCE verifier). Both are maintained by people who maintain identity code
// for a living, and the parts they leave to the caller are done here, each
// named where it is done: the nonce, the issue time, the authorized party, a
// bound on every response body, and no redirect followed.
package sso

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	// StartPath is where a person begins; it answers a redirect to the
	// identity provider. A plain link, never a form: the console's
	// Content-Security-Policy says form-action 'self', and a browser applies
	// form-action to the redirect that follows a form submission, so a
	// button posting here would be blocked on its way to the provider.
	StartPath = "/login/oidc"
	// CallbackPath is the one redirect URL this console accepts. The
	// configured -oidc-redirect-url must end in it, so a mistyped URL is
	// refused at start rather than found by the first person who signs in.
	CallbackPath = "/login/oidc/callback"

	// MaxClockSkew bounds how far this machine's clock and the provider's may
	// disagree about an ID token's issue time.
	MaxClockSkew = 2 * time.Minute
	// PendingLifetime is how long a started sign-in may take. A state older
	// than this is refused even if it was never used.
	PendingLifetime = 10 * time.Minute
	// maxResponseBytes caps every body read from the provider. go-oidc reads
	// discovery, the JWKS and the token response with io.ReadAll, so without
	// this a provider (or whatever answers in its place) can make the console
	// hold as much memory as it cares to send.
	maxResponseBytes = 1 << 20

	defaultRolesClaim    = "groups"
	defaultUsernameClaim = "email"
	defaultScopes        = "openid email profile"
)

// Secret is the client secret. It prints as [redacted] under every verb, so
// a configuration logged whole, an error that wraps one, or a %#v in a debug
// line cannot carry it. The value leaves this package only on the wire to the
// token endpoint.
type Secret string

// Format redacts the secret whatever the verb.
func (Secret) Format(f fmt.State, _ rune) { fmt.Fprint(f, "[redacted]") }

func (s Secret) reveal() string { return string(s) }

// Config is the validated OIDC configuration.
type Config struct {
	Issuer        string
	ClientID      string
	ClientSecret  Secret
	RedirectURL   string
	Scopes        []string
	RolesClaim    string
	UsernameClaim string
	// Roles maps a value of RolesClaim to one of viewer, operator, admin. A
	// person whose claim holds no mapped value has no access: there is no
	// default role.
	Roles map[string]string
	// Only switches password sign-in off for every account except one whose
	// password was set from the command line (-set-password), the way back
	// in when the provider itself is the thing that is down.
	Only bool
}

// Inputs is what the command line and the environment hand Load, unparsed.
type Inputs struct {
	Issuer, ClientID, SecretEnv, SecretFile, RedirectURL string
	Roles, RolesClaim, UsernameClaim, Scopes             string
	Only                                                 bool
}

// Env names the environment twin of each flag. The client secret has no flag
// at all: a flag is visible to anybody who can list processes.
var Env = struct {
	Issuer, ClientID, Secret, SecretFile, RedirectURL string
	Roles, RolesClaim, UsernameClaim, Scopes, Only    string
}{
	Issuer: "COSTCREW_OIDC_ISSUER", ClientID: "COSTCREW_OIDC_CLIENT_ID",
	Secret: "COSTCREW_OIDC_CLIENT_SECRET", SecretFile: "COSTCREW_OIDC_CLIENT_SECRET_FILE",
	RedirectURL: "COSTCREW_OIDC_REDIRECT_URL", Roles: "COSTCREW_OIDC_ROLES",
	RolesClaim: "COSTCREW_OIDC_ROLES_CLAIM", UsernameClaim: "COSTCREW_OIDC_USERNAME_CLAIM",
	Scopes: "COSTCREW_OIDC_SCOPES", Only: "COSTCREW_OIDC_ONLY",
}

// EnvDefault is a flag's default read from its environment twin.
func EnvDefault(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// EnvBool is EnvDefault for a switch: on for 1, true or yes.
func EnvBool(name string) bool {
	switch strings.ToLower(EnvDefault(name)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

var claimNameRe = regexp.MustCompile(`^[A-Za-z0-9_:/.\-]{1,64}$`)

// Load validates the inputs. It returns nil and no error when nothing is
// configured, which is the default and leaves sign-in exactly as it was. A
// configuration that is partly there is an error, never a partial feature.
func Load(in Inputs) (*Config, error) {
	in.Issuer = strings.TrimSpace(in.Issuer)
	if in.Issuer == "" {
		if in.ClientID != "" || in.RedirectURL != "" || in.Roles != "" ||
			in.SecretEnv != "" || in.SecretFile != "" || in.Only {
			return nil, errors.New("-oidc-issuer is not set, but other OIDC settings are; " +
				"set the issuer or remove the rest")
		}
		return nil, nil
	}
	c := &Config{Only: in.Only}
	var err error
	if c.Issuer, err = endpoint("-oidc-issuer", in.Issuer); err != nil {
		return nil, err
	}
	c.ClientID = strings.TrimSpace(in.ClientID)
	if c.ClientID == "" || len(c.ClientID) > 256 || hasControl(c.ClientID) {
		return nil, errors.New("-oidc-client-id is required with -oidc-issuer, " +
			"at most 256 bytes and printable")
	}
	if c.ClientSecret, err = loadSecret(in.SecretEnv, in.SecretFile); err != nil {
		return nil, err
	}
	if c.RedirectURL, err = endpoint("-oidc-redirect-url", in.RedirectURL); err != nil {
		return nil, err
	}
	if u, _ := url.Parse(c.RedirectURL); u.Path != CallbackPath {
		return nil, fmt.Errorf("-oidc-redirect-url must end in %s, the one path this console "+
			"answers the provider on; got path %q", CallbackPath, u.Path)
	}
	if c.Roles, err = ParseRoles(in.Roles); err != nil {
		return nil, err
	}
	c.RolesClaim = orDefault(in.RolesClaim, defaultRolesClaim)
	c.UsernameClaim = orDefault(in.UsernameClaim, defaultUsernameClaim)
	for name, v := range map[string]string{"-oidc-roles-claim": c.RolesClaim, "-oidc-username-claim": c.UsernameClaim} {
		if !claimNameRe.MatchString(v) {
			return nil, fmt.Errorf("%s %q is not a claim name", name, v)
		}
	}
	c.Scopes = strings.Fields(orDefault(in.Scopes, defaultScopes))
	hasOpenID := false
	for _, s := range c.Scopes {
		if s == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		return nil, errors.New("-oidc-scopes must include openid, or the provider returns no ID token")
	}
	return c, nil
}

// ParseRoles reads "value=role;value=role". Entries are separated by ';' and
// each splits at its LAST '=', because a group named as an LDAP distinguished
// name carries both ',' and '=' and the role never does.
func ParseRoles(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		i := strings.LastIndex(entry, "=")
		if i < 0 {
			return nil, fmt.Errorf("-oidc-roles entry %q has no '=': want value=role", entry)
		}
		value, role := strings.TrimSpace(entry[:i]), strings.TrimSpace(entry[i+1:])
		if value == "" || hasControl(value) {
			return nil, fmt.Errorf("-oidc-roles entry %q names no claim value", entry)
		}
		if _, ok := roleRank[role]; !ok {
			return nil, fmt.Errorf("-oidc-roles entry %q: %q is not viewer, operator or admin", entry, role)
		}
		if prev, dup := out[value]; dup && prev != role {
			return nil, fmt.Errorf("-oidc-roles maps %q to both %s and %s", value, prev, role)
		}
		out[value] = role
	}
	if len(out) == 0 {
		return nil, errors.New("-oidc-roles is required with -oidc-issuer: without a mapping " +
			"nobody could be let in, and there is no default role")
	}
	return out, nil
}

// Describe is the startup line: what a person reading the log needs to know
// about how sign-in works on this installation. It names the issuer, the
// client and the mapping's size, and never the secret.
func (c *Config) Describe() string {
	if c == nil {
		return "sign-in through an identity provider is off; accounts are local"
	}
	pw := "password sign-in stays on for local accounts"
	if c.Only {
		pw = "password sign-in is off except for accounts set with -set-password"
	}
	return fmt.Sprintf("sign-in through OIDC issuer %s as client %s; %d claim value(s) of %q map to a role, "+
		"anything else is refused; registration is closed; %s", c.Issuer, c.ClientID, len(c.Roles),
		c.RolesClaim, pw)
}

// RoleFor is the highest role any of the values maps to, or "" for none.
func (c *Config) RoleFor(values []string) string {
	best := ""
	for _, v := range values {
		if r, ok := c.Roles[v]; ok && roleRank[r] > roleRank[best] {
			best = r
		}
	}
	return best
}

func loadSecret(env, file string) (Secret, error) {
	env, file = strings.TrimSpace(env), strings.TrimSpace(file)
	switch {
	case env != "" && file != "":
		return "", errors.New("the client secret is set both in " + Env.Secret +
			" and as a file; set one, so nobody has to guess which is in use")
	case env != "":
		return Secret(env), nil
	case file != "":
		fi, err := os.Stat(file)
		if err != nil {
			return "", fmt.Errorf("reading the client secret file: %w", err)
		}
		if fi.Size() > 4096 {
			return "", fmt.Errorf("the client secret file %s is %d bytes; a secret is not that long", file, fi.Size())
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the client secret file: %w", err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", fmt.Errorf("the client secret file %s is empty", file)
		}
		return Secret(s), nil
	}
	return "", errors.New("the client secret is required with -oidc-issuer: set " + Env.Secret +
		" or -oidc-client-secret-file (there is no flag for the value itself, " +
		"because a flag is visible in the process list)")
}

// endpoint accepts an absolute https URL, or http to a loopback host for a
// provider running beside the console. No query and no fragment: an issuer is
// compared byte for byte with the token's iss claim.
func endpoint(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%s %q is not an absolute http(s) URL", name, raw)
	}
	if u.Scheme == "http" && !Loopback(u.Hostname()) {
		return "", fmt.Errorf("%s %q must be https; plain http is accepted only for a loopback host", name, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%s %q carries a query, a fragment or credentials", name, raw)
	}
	return raw, nil
}

// Loopback is true for localhost and the loopback addresses.
func Loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func orDefault(v, d string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return d
}

// hasFormat catches the invisible formatting characters (Unicode category Cf:
// the bidirectional overrides, zero-width joiners) that make one account name
// render as another on a page.
func hasFormat(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
