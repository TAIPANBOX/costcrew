package deliver

// Pseudonyms: the key, the list of names to mask, the matcher that replaces
// them, and the reversal.
//
// A pseudonym is `kind-` and the first four or more hex digits of an
// HMAC-SHA256, keyed with a per-installation secret kept in the data
// directory, over the kind and the name. So it is stable across the rounds of
// a tool loop, across the tasks of a run and across runs (same key, same
// name, same token), readable (team-7f3a says what kind of thing it is), and
// useless to anybody without the key, who cannot test a guessed name against
// it. Four digits hold 65,536 values, so a large installation WILL see two
// names land on one prefix; the second is given a longer token, and a token
// once handed out is never handed to a different name.
//
// The list of names comes from the store, not from the text: a name is
// masked wherever it appears, in whatever section prints it, including a
// section that does not exist yet. What the scrub cannot do is recognise a
// name nobody wrote down, which is why free text is withheld rather than
// scrubbed (promptdata.go).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// KeyFileName is the per-installation key, in the data directory.
const KeyFileName = "prompt-data.key"

type kindName string

// The kinds, in the order a name that is two kinds at once is given one: a
// desk called "aws" is a desk before it is anything else.
const (
	kindDesk       kindName = "desk"
	kindTeam       kindName = "team"
	kindService    kindName = "svc"
	kindAgent      kindName = "agent"
	kindUser       kindName = "user"
	kindInvoice    kindName = "inv"
	kindCommitment kindName = "cmt"
	kindResource   kindName = "res"
	kindVendor     kindName = "vendor"
	kindProduct    kindName = "product"
	kindModel      kindName = "model"
	kindRun        kindName = "run"
	kindHost       kindName = "host"
)

// tokenRe finds a token in an answer. Case-insensitive because a model that
// starts a sentence with one capitalises it.
var tokenRe = regexp.MustCompile(`(?i)(team|desk|svc|agent|user|inv|vendor|product|cmt|res|model|run|host)-[0-9a-f]{4,}`)

// A Policy is the mode, and for a restricting mode the key and the names.
type Policy struct {
	mode PromptData
	key  []byte

	mu  sync.Mutex
	db  *sql.DB
	fwd map[string]string // name -> token, every one ever handed out
	rev map[string]string // token (lower case) -> name
}

type entry struct {
	kind  kindName
	value string
}

// NewPolicy builds a policy. A restricting mode loads the key from dataDir,
// creating it (32 random bytes, hex, mode 0600) on the first start; full mode
// has no use for one and writes none.
func NewPolicy(mode PromptData, dataDir string) (*Policy, error) {
	if _, err := ParsePromptData(string(mode)); err != nil {
		return nil, err
	}
	p := &Policy{mode: mode, fwd: map[string]string{}, rev: map[string]string{}}
	if mode == PromptFull {
		return p, nil
	}
	key, err := loadOrCreateKey(dataDir)
	if err != nil {
		return nil, err
	}
	p.key = key
	return p, nil
}

// Bind gives the policy the store whose names it masks.
func (p *Policy) Bind(db *sql.DB) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.db = db
	p.mu.Unlock()
}

func loadOrCreateKey(dir string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("prompt data: no data directory to keep the pseudonym key in")
	}
	// 0700, the mode store.Open gives a directory it makes (invariant 63): this
	// runs before the store is opened, and the directory it makes must not be
	// the one that is readable by everybody. A directory that already exists is
	// not changed, as there.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, KeyFileName)
	if raw, err := readKey(path); !errors.Is(err, fs.ErrNotExist) {
		return raw, err
	}
	// First start: write a complete file under a name of its own and link it
	// into place, so two processes starting together agree on one key and
	// neither ever reads half of the other's.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, KeyFileName+".tmp-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.WriteString(hex.EncodeToString(secret) + "\n"); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return readKey(path)
}

func readKey(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("prompt data: %s is mode %04o and must be 0600: anyone who can read it can "+
			"test a guessed name against the tokens. chmod 600 it, or delete it to rotate every pseudonym",
			path, fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 || len(strings.TrimSpace(string(raw))) != 64 {
		return nil, fmt.Errorf("prompt data: %s is not 64 hex digits; refusing to start rather than make a "+
			"new key, which would change every pseudonym. Move it aside to start again", path)
	}
	return key, nil
}

// -------------------------------------------------------------- the names

// neverMasked are words that are not identifiers of any installation: the job
// of the supervisor, the placeholder owner, the supervisor's own desk and the
// word the job descriptions use for the owner. The prompts use them as
// ordinary words, so masking them would garble the instructions and hide
// nothing.
//
// The FOCUS charge categories are here too. A service is allowed to be called
// "Tax" (the generated estate has one), and an allocation rule is for the
// category "Tax"; masking the word would turn the rule's category into a token
// and hide nothing, since the word is the specification's, not the
// installation's.
var neverMasked = map[string]bool{
	"supervisor": true, "unclaimed": true, "management": true, "owner": true, "*": true,
	"Usage": true, "Purchase": true, "Tax": true, "Credit": true, "Adjustment": true,
}

type source struct {
	kind          kindName
	table, column string
	split         string // split a value on this and take each part
}

// sources is where a name can be found. The kinds come in priority order, so
// the first one to claim a name names it.
//
// It is deliberately wide: every column that holds something a person would
// call a name, a code or an id of a thing in the installation, including the
// ones no section prints today.
var sources = []source{
	{kindDesk, "charges", "source", ""}, {kindDesk, "budgets", "source", ""},
	{kindDesk, "anomalies", "source", ""}, {kindDesk, "attribution", "source", ""},
	{kindDesk, "drivers", "source", ""}, {kindDesk, "forecasts", "source", ""},
	{kindDesk, "chargeback", "source", ""}, {kindDesk, "allocation_rules", "source", ""},
	{kindDesk, "commitments", "source", ""}, {kindDesk, "recommendations", "desk", ""},
	{kindDesk, "analysts", "desk", ""}, {kindDesk, "tasks", "desk", ""},
	{kindDesk, "desk_halts", "desk", ""},

	{kindTeam, "charges", "team", ""}, {kindTeam, "budgets", "team", ""},
	{kindTeam, "anomalies", "team", ""}, {kindTeam, "attribution", "team", ""},
	{kindTeam, "ai_calls", "team", ""}, {kindTeam, "chargeback", "team", ""},
	{kindTeam, "budget_recommendations", "team", ""}, {kindTeam, "explainers", "team", ""},
	{kindTeam, "teams", "name", ""},

	{kindService, "charges", "service", ""}, {kindService, "anomalies", "service", ""},
	{kindService, "attribution", "service", ""}, {kindService, "drivers", "scope", ""},

	{kindAgent, "ai_calls", "agent", ""}, {kindAgent, "attribution", "agent", ""},
	{kindAgent, "analysts", "name", ""}, {kindAgent, "analysts", "parent", ""},
	{kindAgent, "tasks", "assignee", ""}, {kindAgent, "artifacts", "author", ""},
	{kindAgent, "comments", "author", ""}, {kindAgent, "plan_asks", "analyst", ""},
	{kindAgent, "anomalies", "caused_by", ""}, {kindAgent, "desk_halts", "applied_by", ""},
	{kindAgent, "desk_halts", "suspended", ","},

	{kindUser, "users", "username", ""}, {kindUser, "sessions", "username", ""},
	{kindUser, "analysts", "owner", ""}, {kindUser, "tasks", "owner", ""},
	{kindUser, "teams", "owner", ""}, {kindUser, "decision_requests", "owner", ""},
	{kindUser, "desk_halts", "owner", ""}, {kindUser, "artifacts", "stamper", ""},
	{kindUser, "artifact_options", "decided_by", ""}, {kindUser, "artifact_options", "on_behalf_of", ""},
	{kindUser, "explainers", "author", ""}, {kindUser, "explainers", "publisher", ""},
	{kindUser, "forecasts", "frozen_by", ""}, {kindUser, "chargeback", "closed_by", ""},
	{kindUser, "anomalies", "handled_by", ""},

	{kindInvoice, "charges", "invoice_id", ""}, {kindInvoice, "ai_calls", "invoice_id", ""},
	{kindCommitment, "commitments", "id", ""},
	{kindResource, "recommendations", "resource", ""}, {kindResource, "recommendations", "id", ""},
	{kindResource, "recommendations", "source_file", ""}, {kindResource, "budget_recommendations", "source_file", ""},
	{kindVendor, "licences", "vendor", ""}, {kindVendor, "ai_calls", "provider", ""},
	{kindVendor, "recommendations", "provider", ""}, {kindVendor, "budget_recommendations", "provider", ""},
	{kindProduct, "licences", "product", ""},
	{kindModel, "ai_calls", "model", ""}, {kindModel, "charges", "model", ""},
	{kindRun, "ai_calls", "run_id", ""}, {kindRun, "ai_calls", "parent_run_id", ""},
}

// collect reads every name the installation holds, in priority order, and
// returns an error if a read failed for any reason other than a table or
// column that this installation does not have (an older database).
func collect(db *sql.DB) ([]entry, error) {
	var out []entry
	// The vocabulary every installation shares comes first and needs no store:
	// the teams and desks of the generated estate.
	for _, d := range world.Desks {
		out = append(out, entry{kindDesk, d.Name})
	}
	for _, t := range world.Teams {
		out = append(out, entry{kindTeam, t.Name})
	}
	if db == nil {
		return out, nil
	}
	for _, s := range sources {
		rows, err := db.Query(fmt.Sprintf(`SELECT DISTINCT %q FROM %q WHERE %q IS NOT NULL AND %q <> ''`,
			s.column, s.table, s.column, s.column))
		if err != nil {
			if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such column") {
				continue
			}
			return nil, fmt.Errorf("reading %s.%s: %w", s.table, s.column, err)
		}
		var vals []string
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			if v.Valid {
				vals = append(vals, v.String)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s.%s: %w", s.table, s.column, err)
		}
		sort.Strings(vals)
		for _, v := range vals {
			if s.split != "" {
				for _, part := range strings.Split(v, s.split) {
					out = append(out, entry{s.kind, part})
				}
				continue
			}
			out = append(out, entry{s.kind, v})
			// An agent id is a URI whose host and last segment are names too.
			if strings.HasPrefix(v, "agent://") {
				rest := strings.TrimPrefix(v, "agent://")
				if i := strings.Index(rest, "/"); i > 0 {
					out = append(out, entry{kindHost, rest[:i]})
					if j := strings.LastIndex(rest, "/"); j > i {
						out = append(out, entry{kindAgent, rest[j+1:]})
					}
				}
			}
		}
	}
	// A driver scoped to every service is not a service.
	return out, nil
}

// usable says whether a value is a name worth masking: not empty, not a
// single character, not a bare number short enough to be an amount, not one
// of the words every installation shares.
func usable(v string) bool {
	if v != strings.TrimSpace(v) || utf8.RuneCountInString(v) < 2 || neverMasked[v] {
		return false
	}
	allDigits := true
	for _, r := range v {
		if !unicode.IsDigit(r) {
			allDigits = false
			break
		}
	}
	return !allDigits || len(v) >= 3
}

// ----------------------------------------------------------------- tokens

// tokenFor hands out the token for a name, giving the same name the same
// token every time and a different name never the same one. p.mu is held.
func (p *Policy) tokenFor(kind kindName, value string) string {
	if t, ok := p.fwd[value]; ok {
		return t
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	digest := hex.EncodeToString(mac.Sum(nil))
	for n := 4; n <= len(digest); n++ {
		cand := string(kind) + "-" + digest[:n]
		if _, taken := p.rev[cand]; !taken {
			p.fwd[value] = cand
			p.rev[cand] = value
			return cand
		}
	}
	// Every prefix of the digest is held by another name: an HMAC collision.
	// Not reachable; fail closed rather than hand out a token twice.
	panic("prompt data: no free token for " + string(kind))
}

// dictionary maps every usable name to its token. p.mu is held.
func (p *Policy) dictionary(es []entry, keep []string) map[string]string {
	dict := make(map[string]string, len(es)+len(keep))
	for _, e := range es {
		// A name stored with spaces round it ("ml-platform ") is printed
		// without them wherever a section puts it in a column, so the name to
		// look for is the trimmed one.
		v := strings.TrimSpace(e.value)
		if !usable(v) {
			continue
		}
		if _, claimed := dict[v]; claimed {
			continue
		}
		dict[v] = p.tokenFor(e.kind, v)
	}
	// The working analyst keeps its own name: it is told who it is. Written
	// last so it wins, and mapped to itself so that the longest-match rule
	// below walks past it instead of masking a desk name inside it.
	for _, k := range keep {
		if k != "" {
			dict[k] = k
		}
	}
	// This console's own stand-in sentences travel through the mask inside
	// the sections that carry them. A team called "free" must not turn
	// "free text" in them into a token.
	for _, k := range []string{WithheldFreeText, WithheldLabel, maskFailed} {
		dict[k] = k
	}
	return dict
}

// MaskText replaces every name the bound store holds. keep lists names left
// alone (the working analyst's own). Full mode returns text unchanged.
func (p *Policy) MaskText(text string, keep ...string) string {
	if p.Full() || text == "" {
		return text
	}
	p.mu.Lock()
	db := p.db
	p.mu.Unlock()
	return p.maskStore(db, text, keep)
}

// maskStore is MaskText over a given store, for a caller that has one in
// hand (Packet).
func (p *Policy) maskStore(db *sql.DB, text string, keep []string) string {
	if p.Full() || text == "" {
		return text
	}
	es, err := collect(db)
	if err != nil {
		return maskFailed
	}
	return p.maskWith(text, es, keep)
}

// maskWith masks text with an explicit list of names. The tests use it; so
// does everything else, through maskStore.
func (p *Policy) maskWith(text string, es []entry, keep []string) string {
	if p.Full() {
		return text
	}
	p.mu.Lock()
	dict := p.dictionary(es, keep)
	p.mu.Unlock()
	return newMatcher(dict).apply(text)
}

// Reidentify puts the real name back wherever the model wrote a token this
// policy handed out. A token it did not hand out, or that is not whole (one
// more hex digit, a letter glued on), is the model's own and is left as
// written. Full mode returns text unchanged.
func (p *Policy) Reidentify(text string) string {
	if p.Full() || text == "" {
		return text
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// A token handed out earlier is in p.rev. A process that only ever
	// re-identifies (it did not build the prompt) learns the tokens of the
	// names it can read.
	if es, err := collect(p.db); err == nil {
		p.dictionary(es, nil)
	}
	var b strings.Builder
	last := 0
	for _, m := range tokenRe.FindAllStringIndex(text, -1) {
		s, e := m[0], m[1]
		if s > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text[:s]); isWord(r) {
				continue
			}
		}
		if e < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[e:]); isWord(r) {
				continue
			}
		}
		real, ok := p.rev[strings.ToLower(text[s:e])]
		if !ok {
			continue
		}
		b.WriteString(text[last:s])
		b.WriteString(real)
		last = e
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// ---------------------------------------------------------------- matching

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

type node struct {
	next map[byte]*node
	key  string // set on a node where a name ends
	tok  string
}

type matcher struct{ root *node }

func newMatcher(dict map[string]string) *matcher {
	m := &matcher{root: &node{}}
	for k, tok := range dict {
		n := m.root
		for i := 0; i < len(k); i++ {
			if n.next == nil {
				n.next = map[byte]*node{}
			}
			c := n.next[k[i]]
			if c == nil {
				c = &node{}
				n.next[k[i]] = c
			}
			n = c
		}
		n.key, n.tok = k, tok
	}
	return m
}

// apply replaces, left to right, the longest name that starts at each
// position and is a whole name: one whose first character is not preceded by
// a letter or a digit if it is itself one, and likewise at its end. A name
// glued into a longer word ("laws" holds "aws") is not that name; a name
// beside a hyphen, an underscore, a slash or a dash is.
func (m *matcher) apply(text string) string {
	if len(m.root.next) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	n := len(text)
	for i := 0; i < n; {
		if m.root.next[text[i]] != nil {
			if end, tok := m.longest(text, i); end > i {
				b.WriteString(tok)
				i = end
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func (m *matcher) longest(text string, i int) (end int, tok string) {
	first, _ := utf8.DecodeRuneInString(text[i:])
	if isWord(first) && i > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:i]); isWord(r) {
			return 0, ""
		}
	}
	nd := m.root
	for j := i; j < len(text); j++ {
		nd = nd.next[text[j]]
		if nd == nil {
			break
		}
		if nd.key == "" {
			continue
		}
		e := j + 1
		last, _ := utf8.DecodeLastRuneInString(nd.key)
		if isWord(last) && e < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[e:]); isWord(r) {
				continue
			}
		}
		if allDigits(nd.key) && besideAnAmount(text, i, e) {
			continue
		}
		end, tok = e, nd.tok
	}
	return end, tok
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// besideAnAmount is whether the digits at text[i:e] are part of a number
// written with a point or a comma ("1,042.50", "1042.50"), which an invoice
// number is not.
func besideAnAmount(text string, i, e int) bool {
	if i >= 2 && (text[i-1] == '.' || text[i-1] == ',') && text[i-2] >= '0' && text[i-2] <= '9' {
		return true
	}
	if e+1 < len(text) && (text[e] == '.' || text[e] == ',') && text[e+1] >= '0' && text[e+1] <= '9' {
		return true
	}
	return false
}
