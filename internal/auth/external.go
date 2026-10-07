package auth

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Accounts that belong to the organisation's identity provider (invariant 74).
//
// An identity is (issuer, subject): the subject is the one claim a provider
// promises never to reassign, so that pair, not a name or an email, is what an
// account is linked to. The name is only what the account is created under the
// first time; an email that changes at the provider keeps its old account
// rather than becoming a new one.
//
// The provider decides access on every sign-in, never once:
//
//   - the first sign-in creates the account at the mapped role;
//   - a later one sets the role to whatever the mapping says now, up or down;
//   - one whose claim no longer maps to any role is refused AND ends every
//     session the account still holds, so a person removed from the group is
//     out at the next sign-in rather than at whenever a twelve-hour cookie
//     happens to lapse. A session that is never followed by another sign-in
//     still lasts until it expires; that limit is the invariant's to state.
//
// A local account is never adopted by an identity that happens to carry its
// name. Linking by name would let whoever controls a name at the provider
// sign in as the local account of the same name, admin included.

const identitiesSchema = `CREATE TABLE IF NOT EXISTS identities(
	issuer TEXT NOT NULL, subject TEXT NOT NULL, username TEXT NOT NULL UNIQUE,
	linked REAL, PRIMARY KEY(issuer, subject))`

// breakGlassSchema lists the accounts whose password was set from the command
// line. Under -oidc-only they are the only accounts a password signs in to.
const breakGlassSchema = `CREATE TABLE IF NOT EXISTS break_glass(
	username TEXT PRIMARY KEY, set_at REAL)`

// unusablePassword is what an account created by the provider holds in place
// of a hash: it has no scrypt prefix, so VerifyPassword refuses every password
// against it. The command line can still give such an account a password,
// which is the operator's act and makes it a break-glass account.
const unusablePassword = "external$$"

const (
	// ExternalNoAccess is what a person whose claim maps to no role is shown.
	ExternalNoAccess = "your organisation's sign-in does not give you access to this console"
	// ExternalNameTaken is what a first sign-in under a name a local account
	// already holds is shown.
	ExternalNameTaken = "an account with that name already exists here and is not linked to " +
		"your organisation's sign-in; ask an admin"
)

func ensureExternal(db *sql.DB) error {
	for _, q := range []string{identitiesSchema, breakGlassSchema} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// SignInExternal applies what the provider established about one person and
// returns the account to start a session for, or nil and the sentence to show.
// role is "" when the provider's claim mapped to no role.
func (a *Auth) SignInExternal(issuer, subject, username, role string) (*User, string, error) {
	username = strings.TrimSpace(username)
	if issuer == "" || subject == "" || username == "" {
		return nil, "", errors.New("an external sign-in needs an issuer, a subject and a name")
	}
	if role != "" && !validRole(role) {
		return nil, "", errors.New("an external sign-in mapped to a role that does not exist")
	}
	var linked string
	err := a.st.DB().QueryRow(`SELECT username FROM identities WHERE issuer=? AND subject=?`,
		issuer, subject).Scan(&linked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}

	if role == "" {
		ended := int64(0)
		if linked != "" {
			res, err := a.st.DB().Exec(`DELETE FROM sessions WHERE username=?`, linked)
			if err != nil {
				return nil, "", err
			}
			ended, _ = res.RowsAffected()
		}
		_, err := a.st.Journal("external_access_refused", 0, map[string]any{
			"issuer": issuer, "subject": subject, "username": firstNonEmpty(linked, username),
			"linked": linked != "", "sessions_ended": ended,
			"reason": "no value of the roles claim maps to a role",
		})
		return nil, ExternalNoAccess, err
	}

	now := float64(time.Now().UnixNano()) / 1e9
	if linked == "" {
		taken, err := a.External(username)
		if err != nil {
			return nil, "", err
		}
		if u, err := a.Get(username); err != nil {
			return nil, "", err
		} else if u != nil || taken {
			_, err := a.st.Journal("external_access_refused", 0, map[string]any{
				"issuer": issuer, "subject": subject, "username": username, "linked": false,
				"reason": "another account already holds this name",
			})
			return nil, ExternalNameTaken, err
		}
		if _, err := a.st.DB().Exec(`INSERT INTO identities(issuer, subject, username, linked) VALUES (?,?,?,?)`,
			issuer, subject, username, now); err != nil {
			return nil, "", err
		}
		linked = username
	}

	u, err := a.Get(linked)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		// The first sign-in, or an account linked once and removed since by
		// an admin: the provider grants access, so the account is created at
		// the role it maps to.
		if _, err := a.st.DB().Exec(
			`INSERT INTO users(username, pw_hash, role, created, last_login) VALUES (?,?,?,?,?)`,
			linked, unusablePassword, role, now, now); err != nil {
			return nil, "", err
		}
		if _, err := a.st.Journal("user_created", 0, map[string]any{
			"username": linked, "role": role, "by": "identity provider", "issuer": issuer,
		}); err != nil {
			return nil, "", err
		}
	} else {
		if u.Role != role {
			if _, err := a.st.DB().Exec(`UPDATE users SET role=? WHERE username=?`, role, linked); err != nil {
				return nil, "", err
			}
			if _, err := a.st.Journal("user_role_changed", 0, map[string]any{
				"username": linked, "role": role, "was": u.Role, "by": "identity provider",
			}); err != nil {
				return nil, "", err
			}
		}
		if _, err := a.st.DB().Exec(`UPDATE users SET last_login=? WHERE username=?`, now, linked); err != nil {
			return nil, "", err
		}
	}
	u, err = a.Get(linked)
	if err != nil || u == nil {
		return nil, "", errors.Join(err, errors.New("the account vanished while signing in"))
	}
	return u, "", nil
}

// External says whether an account is linked to an identity provider.
func (a *Auth) External(username string) (bool, error) {
	var n int
	err := a.st.DB().QueryRow(`SELECT COUNT(*) FROM identities WHERE username=?`, username).Scan(&n)
	return n > 0, err
}

// AuthenticateBreakGlass is Authenticate for -oidc-only: a password signs in
// only to an account whose password was set from the command line. Every
// other account answers exactly as a wrong password does (invariant 62), after
// the same hashing work, so the form under -oidc-only cannot be asked which
// accounts are break-glass ones either.
func (a *Auth) AuthenticateBreakGlass(username, password string) (*User, string, error) {
	var n int
	if err := a.st.DB().QueryRow(`SELECT COUNT(*) FROM break_glass WHERE username=?`,
		strings.TrimSpace(username)).Scan(&n); err != nil {
		return nil, "", err
	}
	if n == 0 {
		burn(password)
		return nil, LoginRefused, nil
	}
	return a.Authenticate(username, password)
}

func (a *Auth) markBreakGlass(username string) error {
	_, err := a.st.DB().Exec(`INSERT OR REPLACE INTO break_glass(username, set_at) VALUES (?,?)`,
		username, float64(time.Now().UnixNano())/1e9)
	return err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
