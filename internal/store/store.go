// Package store owns the console's own state: accounts, sessions, the board,
// and the hash-chained journal.
//
// SQLite through a pure-Go driver, so the product is one static binary with no
// cgo. The Python original also kept a DuckDB file for the cost estate; that
// separation does not survive the port, because the estate is 48 704 rows and
// the queries over it are ordinary SQL with no window functions, which is well
// inside what SQLite does without a second engine in the process.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dir     string
	journal string

	// warnings are things Open could not do and chose not to die of, for the
	// caller to print. See Warnings.
	warnings []string

	// The journal is a hash chain, and a chain has exactly one writer. Two
	// goroutines appending would interleave and fork it.
	jmu sync.Mutex
}

// Modes of the files this console owns (invariant 63). A directory the store
// creates is 0700; the database, its -wal and -shm, and the journal are 0600.
// Passport files and the -stack-events file are not the store's and are
// deliberately left readable by the services they are written for.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

func Open(dir string) (*Store, error) {
	// MkdirAll gives every directory IT creates dirMode and touches none that
	// already exists. That is deliberate: the default -data is ".", the
	// operator's working directory, and chmod-ing somebody's cwd is not this
	// program's call. The files inside are what hold the data and are tightened
	// below whether or not the directory was new.
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "app.db")
	// Create app.db 0600 BEFORE SQLite sees it. SQLite gives -wal and -shm the
	// mode of the main file, so they are born 0600 too, instead of 0644 for the
	// moment between their creation and the chmod below. An empty file is a
	// valid new database.
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, fileMode); err != nil {
		return nil, err
	} else if err := f.Close(); err != nil {
		return nil, err
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, dir: dir, journal: filepath.Join(dir, "events.ndjson")}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	s.tighten(os.Chmod)
	return s, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

// Warnings are the things Open did not stop for. Today that is a file it could
// not make private (a chmod refused on a mount that does not allow it, or on a
// file owned by someone else), and a VACUUM it could not run. A caller prints
// them: a hardening step that fails silently is the same as no hardening.
func (s *Store) Warnings() []string { return append([]string(nil), s.warnings...) }

// tighten makes every file the store owns 0600. A file that is not there yet is
// not a problem (the journal does not exist until the first append, and -wal
// and -shm only while a connection is open); any other failure is a warning.
func (s *Store) tighten(chmod func(string, os.FileMode) error) {
	db := filepath.Join(s.dir, "app.db")
	for _, p := range []string{db, db + "-wal", db + "-shm", s.journal} {
		if err := chmod(p, fileMode); err != nil && !os.IsNotExist(err) {
			s.warnings = append(s.warnings, fmt.Sprintf(
				"could not make %s private (0600): %v", p, err))
		}
	}
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS users(
	  username TEXT PRIMARY KEY, pw_hash TEXT, role TEXT DEFAULT 'viewer',
	  created REAL, last_login REAL, failed INTEGER DEFAULT 0,
	  locked_until REAL DEFAULT 0);
	`); err != nil {
		return err
	}
	return s.migrateSessions()
}

// sessionsSchema holds the SHA-256 of a session cookie and never the cookie
// (invariant 61). The column used to be called token and held the cookie
// itself, so anybody able to read app.db held every live login.
const sessionsSchema = `CREATE TABLE IF NOT EXISTS sessions(
	  token_hash TEXT PRIMARY KEY, username TEXT, created REAL, expires REAL)`

// migrateSessions creates the sessions table, or, when it finds the clear-text
// one, ends every session in it and erases them.
//
// Existing rows are not carried over, because they cannot be: the cookie is the
// only thing that would let a row be hashed, and it is exactly what must not be
// kept. Everybody signs in again once. The old table is dropped under
// secure_delete so its pages are overwritten rather than merely unlinked, the
// file is VACUUMed so a copy left in a freed page from an earlier edit goes
// too, and the write-ahead log is checkpointed and truncated so the old pages
// do not survive there either. The sign-out of everybody is journaled.
//
// It runs once: a database that already has token_hash is left alone, so a
// restart signs nobody out.
func (s *Store) migrateSessions() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info('sessions')`)
	if err != nil {
		return err
	}
	exists, current := false, false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		exists = true
		if name == "token_hash" {
			current = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if current {
		return nil
	}
	if !exists {
		_, err := conn.ExecContext(ctx, sessionsSchema)
		return err
	}

	var ended int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&ended); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete=ON`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE sessions`); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if _, err := conn.ExecContext(ctx, sessionsSchema); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf(
			"old session tokens were dropped but the database could not be vacuumed, "+
				"so a copy may remain in free space: %v", err))
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf(
			"old session tokens were dropped but the write-ahead log could not be truncated: %v", err))
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete=OFF`); err != nil {
		return err
	}
	_, err = s.Journal("sessions_reset", 0, map[string]any{
		"ended":  ended,
		"reason": "session tokens are now stored as hashes; everybody signs in again once",
	})
	return err
}

// JournalPath is where the hash chain lives, exported so a caller can refuse
// to point anything else at it.
func (s *Store) JournalPath() string { return s.journal }

// ------------------------------------------------------------------ journal

// Journal appends one record and returns its hash.
//
// The record shape and the hash are the Python original's, byte for byte,
// because the audit page renders them and a chain that disagrees across
// implementations is a chain nobody can verify: keys sorted, non-ASCII left
// alone, the timestamp rounded to milliseconds, and the hash the first 16 hex
// characters of the SHA-256 over the record WITHOUT its own hash field.
func (s *Store) Journal(event string, ts float64, data map[string]any) (string, error) {
	s.jmu.Lock()
	defer s.jmu.Unlock()

	prev := "genesis"
	if raw, err := os.ReadFile(s.journal); err == nil {
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if last := lines[len(lines)-1]; strings.TrimSpace(last) != "" {
			var rec struct {
				Hash string `json:"hash"`
			}
			if json.Unmarshal([]byte(last), &rec) == nil && rec.Hash != "" {
				prev = rec.Hash
			} else {
				// The original says "recovered" rather than starting a new
				// genesis, so a verifier can tell a restart from a forged head.
				prev = "recovered"
			}
		}
	}
	if ts == 0 {
		ts = float64(time.Now().UnixNano()) / 1e9
	}
	ts = math.Round(ts*1000) / 1000
	if data == nil {
		data = map[string]any{}
	}
	// Hash what will be READ, not what was passed in.
	//
	// A verifier re-derives the hash from the line it reads back, where JSON
	// has turned every number into a float64. An int64 written as 34805 comes
	// back as 34805.0 and the two canonical forms differ, so the chain breaks
	// at the first entry carrying a whole number. It never fired while only
	// sign-ins were journalled, because those are all strings; it broke the
	// moment the console started recording what it decided.
	//
	// Normalising here makes writer and verifier agree by construction rather
	// than by both callers remembering to pass float64.
	data, err := asRead(data)
	if err != nil {
		return "", err
	}

	body, err := canonical(map[string]any{
		"ts": ts, "event": event, "data": data, "prev": prev,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])[:16]

	full, err := canonical(map[string]any{
		"ts": ts, "event": event, "data": data, "prev": prev, "hash": hash,
	})
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(s.journal, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(full, '\n')); err != nil {
		return "", err
	}
	return hash, nil
}

// canonical reproduces json.dumps(obj, sort_keys=True, ensure_ascii=False).
//
// Go's encoding/json already sorts map keys and leaves non-ASCII alone, but it
// differs from Python in three ways that all change bytes: it escapes <, > and
// & by default, it puts no space after ": " or ", ", and it renders floats in
// its own shortest form. Each is handled rather than hoped about, because the
// hash is taken over these exact bytes.
func canonical(v map[string]any) ([]byte, error) {
	var b strings.Builder
	if err := writeValue(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func writeValue(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k)
			b.WriteString(": ")
			if err := writeValue(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case string:
		writeString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		b.WriteString(pyFloat(t))
	default:
		return fmt.Errorf("journal: no canonical form for %T", v)
	}
	return nil
}

// pyFloat renders a float the way Python's json does, which is repr(), which
// is the shortest string that round-trips. Three rules, and each one was a
// failing vector before it was a line of code:
//
//  1. an integral value keeps its ".0", which Go's shortest form drops;
//  2. everything else is POSITIONAL, not %g. Go's %g renders 1780301400.123
//     as 1.780301400123e+09, and every journal timestamp is in that range;
//  3. Python does switch to scientific below 1e-4 or at 1e17 and above. No
//     journal value has ever been near either, so this branch is defensive
//     rather than exercised, and it is written out instead of left to chance.
func pyFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	if a := math.Abs(f); a != 0 && (a < 1e-4 || a >= 1e17) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// ------------------------------------------------------------ journal read

// Record is one journal entry as it was written.
type Record struct {
	TS    float64        `json:"ts"`
	Event string         `json:"event"`
	Data  map[string]any `json:"data"`
	Prev  string         `json:"prev"`
	Hash  string         `json:"hash"`
}

func (r Record) When() string {
	return time.Unix(int64(r.TS), 0).UTC().Format("2006-01-02 15:04")
}

// JournalTail reads the last n entries, newest first.
func (s *Store) JournalTail(n int) ([]Record, error) {
	lines, err := s.journalLines()
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Record, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var r Record
		if json.Unmarshal([]byte(lines[i]), &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// VerifyChain walks the whole journal and re-derives every hash.
//
// It reports WHERE the chain first fails rather than a bare false. "Broken"
// with no position is a sentence nobody can act on, and the position is the
// only part that tells you what was edited.
func (s *Store) VerifyChain() (ok bool, n int, breakAt string, err error) {
	lines, err := s.journalLines()
	if err != nil {
		return false, 0, "", err
	}
	prev := "genesis"
	for _, line := range lines {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return false, n, "a line that is not valid JSON", nil
		}
		n++
		if r.Prev != prev && prev != "genesis" {
			return false, n, r.When() + " (" + r.Event + ")", nil
		}
		body, err := canonical(map[string]any{
			"ts": r.TS, "event": r.Event, "data": r.Data, "prev": r.Prev,
		})
		if err != nil {
			return false, n, r.When(), nil
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:])[:16] != r.Hash {
			return false, n, r.When() + " (" + r.Event + ")", nil
		}
		prev = r.Hash
	}
	return true, n, "", nil
}

func (s *Store) journalLines() ([]string, error) {
	raw, err := os.ReadFile(s.journal)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// asRead round-trips a payload through JSON, so what is hashed is exactly what
// a reader will see. See the note in Journal for why this is not decoration.
func asRead(data map[string]any) (map[string]any, error) {
	buf, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, err
	}
	return out, nil
}
