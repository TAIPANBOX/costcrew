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

	// The journal is a hash chain, and a chain has exactly one writer. Two
	// goroutines appending would interleave and fork it.
	jmu sync.Mutex
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "app.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db, dir: dir, journal: filepath.Join(dir, "events.ndjson")}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS users(
	  username TEXT PRIMARY KEY, pw_hash TEXT, role TEXT DEFAULT 'viewer',
	  created REAL, last_login REAL, failed INTEGER DEFAULT 0,
	  locked_until REAL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS sessions(
	  token TEXT PRIMARY KEY, username TEXT, created REAL, expires REAL);
	`)
	return err
}

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
	f, err := os.OpenFile(s.journal, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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

// pyFloat renders a float the way Python's json does: repr() shortest
// round-trip, but a value that is integral still carries its ".0", which Go's
// strconv drops.
func pyFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
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
