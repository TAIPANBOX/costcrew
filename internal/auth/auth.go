// Package auth is accounts, sessions, roles and the demo guard.
//
// It reproduces the Python original's stored formats exactly, not just its
// behaviour: a database written by either implementation must be readable by
// the other, which is what lets an existing installation be ported without a
// migration and every account keeping its password.
//
// Three rules, unchanged from the original:
//
//   - passwords are never stored, only a salted hash;
//   - a viewer reads and exports, an operator acts, an admin manages accounts,
//     because acting is what costs money and moves state;
//   - in demo mode nobody spends the owner's model budget, whatever their role.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

const (
	SessionCookie = "costcrew_session"
	SessionHours  = 12
	MinPassword   = 10

	scryptN, scryptR, scryptP, keyLen = 1 << 14, 8, 1, 32
	pbkdf2Rounds                      = 480_000
)

var rank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

func validRole(r string) bool { _, ok := rank[r]; return ok }

type User struct {
	Username    string
	Role        string
	Created     float64
	LastLogin   sql.NullFloat64
	Failed      int
	LockedUntil float64
	hash        string
}

func (u *User) May(need string) bool {
	if u == nil {
		return false
	}
	return rank[u.Role] >= rank[need]
}

type Auth struct {
	st     *store.Store
	secret []byte
}

// New loads or creates the per-installation signing key. It is kept in a file
// beside the database rather than in it, so a database copied for inspection
// does not carry the key that signs its sessions.
func New(st *store.Store, dir string) (*Auth, error) {
	path := filepath.Join(dir, ".session-secret")
	key, err := os.ReadFile(path)
	if err != nil {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	}
	return &Auth{st: st, secret: key}, nil
}

// DemoMode: one switch, read from the environment, so a deployment cannot
// enable spending by forgetting a setting. It has to choose it.
func DemoMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COSTCREW_DEMO"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ----------------------------------------------------------------- password

func derive(password string, salt []byte, algo string) (string, error) {
	switch algo {
	case "scrypt":
		k, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, keyLen)
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(k), nil
	case "pbkdf2":
		// Kept for hashes the Python original wrote on a machine whose OpenSSL
		// had no scrypt. Nothing new is written with it.
		return hex.EncodeToString(
			pbkdf2.Key([]byte(password), salt, pbkdf2Rounds, keyLen, sha256.New)), nil
	}
	return "", fmt.Errorf("unknown algorithm %q", algo)
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	// scrypt always, unlike the original: it fell back to pbkdf2 only because
	// the Python that ships with macOS has no scrypt in its OpenSSL. Go carries
	// its own, so the weaker branch is not needed to start. Hashes already
	// written with pbkdf2 are still verified.
	h, err := derive(password, salt, "scrypt")
	if err != nil {
		return "", err
	}
	return "scrypt$" + hex.EncodeToString(salt) + "$" + h, nil
}

// VerifyPassword accepts whichever algorithm the hash was written with, so a
// database written by the Python original still works here.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 {
		return false
	}
	algo, saltHex, want := parts[0], parts[1], parts[2]
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	got, err := derive(password, salt, algo)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// -------------------------------------------------------------------- users

func (a *Auth) scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.Username, &u.hash, &u.Role, &u.Created, &u.LastLogin,
		&u.Failed, &u.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func (a *Auth) Get(username string) (*User, error) {
	return a.scanUser(a.st.DB().QueryRow(
		`SELECT username, pw_hash, role, created, last_login, failed, locked_until
		 FROM users WHERE username=?`, username))
}

func (a *Auth) Count() (int, error) {
	var n int
	err := a.st.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (a *Auth) Create(username, password, role string) (bool, error) {
	if strings.TrimSpace(username) == "" || len(password) < MinPassword || !validRole(role) {
		return false, nil
	}
	return a.create(username, password, role)
}

// create is Create without the length check, so the command line can make a
// demo account and the web forms still cannot.
func (a *Auth) create(username, password, role string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || !validRole(role) {
		return false, nil
	}
	if u, err := a.Get(username); err != nil || u != nil {
		return false, err
	}
	h, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := a.st.DB().Exec(
		`INSERT INTO users(username, pw_hash, role, created) VALUES (?,?,?,?)`,
		username, h, role, now); err != nil {
		return false, err
	}
	_, err = a.st.Journal("user_created", 0, map[string]any{"username": username, "role": role})
	return true, err
}

// SetPassword creates the account if it does not exist and resets it if it
// does, and it is deliberately reachable ONLY from the command line.
//
// A console with no email, no SMS and no recovery question still has to answer
// "I am locked out of my own machine", and the honest answer on a local
// installation is the shell: whoever can run the binary already owns the
// database it reads. Putting the same power behind a web form would be a
// different thing entirely, which is why there is no handler for it.
//
// The reset is journalled like every other change, so a password that changed
// is visible in the audit chain even though the password itself never is.
// allowWeak lets a demo account exist with a password nobody would accept on
// anything reachable. It is a parameter rather than a package flag so the
// weakening is visible at every call site, and there is exactly one.
func (a *Auth) SetPassword(username, password, role string, allowWeak bool) (created bool, err error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return false, errors.New("the account needs a name")
	}
	if len(password) < MinPassword && !allowWeak {
		return false, fmt.Errorf("the password must be at least %d characters", MinPassword)
	}
	u, err := a.Get(username)
	if err != nil {
		return false, err
	}
	if u == nil {
		if !validRole(role) {
			role = "admin"
		}
		return a.create(username, password, role)
	}
	h, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	if _, err := a.st.DB().Exec(`UPDATE users SET pw_hash=? WHERE username=?`, h, username); err != nil {
		return false, err
	}
	// Every session signed in under the old password ends here. A reset that
	// leaves the old sessions alive has not locked anybody out of anything.
	if _, err := a.st.DB().Exec(`DELETE FROM sessions WHERE username=?`, username); err != nil {
		return false, err
	}
	_, err = a.st.Journal("password_reset", 0, map[string]any{"username": username, "by": "command line"})
	return false, err
}

// ------------------------------------------------------------------ signup

func SignupCode() string { return strings.TrimSpace(os.Getenv("COSTCREW_SIGNUP_CODE")) }

// SignupOpen: registration is open when there is nobody yet, so the first
// account claims the installation, or when the owner has set a joining code.
func (a *Auth) SignupOpen() (bool, error) {
	n, err := a.Count()
	if err != nil {
		return false, err
	}
	return n == 0 || SignupCode() != "", nil
}

// Register returns (ok, message). The first account becomes admin; later ones
// are viewers until an admin promotes them, so a joining code cannot hand out
// power.
func (a *Auth) Register(username, password, code string) (bool, string, error) {
	n, err := a.Count()
	if err != nil {
		return false, "", err
	}
	first := n == 0
	if !first {
		if SignupCode() == "" {
			return false, "registration is closed; ask an admin for an account", nil
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(SignupCode())) != 1 {
			return false, "that joining code is not right", nil
		}
	}
	if first && SignupCode() != "" &&
		subtle.ConstantTimeCompare([]byte(code), []byte(SignupCode())) != 1 {
		return false, "that joining code is not right", nil
	}
	if len(password) < MinPassword {
		return false, "choose a password of at least 10 characters", nil
	}
	role := "viewer"
	if first {
		role = "admin"
	}
	ok, err := a.Create(username, password, role)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, "that name is taken, or the password is too short", nil
	}
	if _, err := a.st.Journal("user_registered", 0, map[string]any{
		"username": username, "role": role, "first": first}); err != nil {
		return false, "", err
	}
	if first {
		return true, "this installation is yours: you are the admin", nil
	}
	return true, "account created; an admin can raise your role", nil
}

// ------------------------------------------------------------ authenticate

// Authenticate returns (user, reason). A wrong password is slow to retry on
// purpose, and a missing account costs the same work as a real check so the
// two cannot be told apart by timing.
func (a *Auth) Authenticate(username, password string) (*User, string, error) {
	u, err := a.Get(username)
	if err != nil {
		return nil, "", err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if u == nil {
		if password == "" {
			password = "x"
		}
		_, _ = HashPassword(password)
		return nil, "unknown account or wrong password", nil
	}
	if u.LockedUntil > now {
		return nil, fmt.Sprintf("locked for another %ds after repeated failures",
			int(u.LockedUntil-now)), nil
	}
	if !VerifyPassword(password, u.hash) {
		failed := u.Failed + 1
		lock := 0.0
		if failed >= 3 {
			back := 10 * (1 << min(failed, 5))
			lock = now + float64(min(300, back))
		}
		if _, err := a.st.DB().Exec(
			`UPDATE users SET failed=?, locked_until=? WHERE username=?`,
			failed, lock, username); err != nil {
			return nil, "", err
		}
		_, _ = a.st.Journal("login_failed", 0, map[string]any{
			"username": username, "attempt": failed})
		return nil, "unknown account or wrong password", nil
	}
	if _, err := a.st.DB().Exec(
		`UPDATE users SET failed=0, locked_until=0, last_login=? WHERE username=?`,
		now, username); err != nil {
		return nil, "", err
	}
	return u, "", nil
}

// ---------------------------------------------------------------- sessions

func (a *Auth) StartSession(username string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := a.st.DB().Exec(`DELETE FROM sessions WHERE expires < ?`, now); err != nil {
		return "", err
	}
	if _, err := a.st.DB().Exec(
		`INSERT INTO sessions(token, username, created, expires) VALUES (?,?,?,?)`,
		token, username, now, now+SessionHours*3600); err != nil {
		return "", err
	}
	_, err := a.st.Journal("login", 0, map[string]any{"username": username})
	return token, err
}

func (a *Auth) SessionUser(token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	now := float64(time.Now().UnixNano()) / 1e9
	return a.scanUser(a.st.DB().QueryRow(
		`SELECT u.username, u.pw_hash, u.role, u.created, u.last_login, u.failed, u.locked_until
		 FROM sessions s JOIN users u ON u.username = s.username
		 WHERE s.token=? AND s.expires > ?`, token, now))
}

func (a *Auth) EndSession(token string) error {
	if token == "" {
		return nil
	}
	var username string
	err := a.st.DB().QueryRow(`SELECT username FROM sessions WHERE token=?`, token).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := a.st.DB().Exec(`DELETE FROM sessions WHERE token=?`, token); err != nil {
		return err
	}
	_, err = a.st.Journal("logout", 0, map[string]any{"username": username})
	return err
}

// -------------------------------------------------------------------- csrf

// CSRFToken is bound to the session, so a token lifted from one account is
// useless in another.
func (a *Auth) CSRFToken(session string) string {
	if session == "" {
		session = "anon"
	}
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(session))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (a *Auth) CSRFOK(session, given string) bool {
	return subtle.ConstantTimeCompare([]byte(a.CSRFToken(session)), []byte(given)) == 1
}

// ---------------------------------------------------------------- listing

// Summary is one account, without its hash.
type Summary struct {
	Username  string
	Role      string
	Created   float64
	LastLogin sql.NullFloat64
}

// LastLoginText renders the timestamp, or says never rather than 1970.
func (s Summary) LastLoginText() string {
	if !s.LastLogin.Valid || s.LastLogin.Float64 == 0 {
		return ""
	}
	return time.Unix(int64(s.LastLogin.Float64), 0).UTC().Format(time.RFC3339)
}

func (a *Auth) List() ([]Summary, error) {
	rows, err := a.st.DB().Query(`SELECT username, role, created, last_login
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.Username, &s.Role, &s.Created, &s.LastLogin); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *Auth) CountRole(role string) (int, error) {
	var n int
	err := a.st.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE role=?`, role).Scan(&n)
	return n, err
}

// SetRole changes what somebody may do. Journalled, because a change to who
// can spend money is exactly the kind of thing somebody asks about later.
func (a *Auth) SetRole(username, role string) error {
	if !validRole(role) {
		return fmt.Errorf("no such role: %q", role)
	}
	res, err := a.st.DB().Exec(`UPDATE users SET role=? WHERE username=?`, role, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no such account: %q", username)
	}
	_, err = a.st.Journal("user_role_changed", 0, map[string]any{
		"username": username, "role": role})
	return err
}
