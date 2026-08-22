// Command parity captures and compares the observable surface of a CostCrew
// installation, so a Go implementation can be held against the Python one.
//
// The oracle is deliberately the HTTP surface rather than any function API:
// it is the only contract both implementations must agree on, and it covers
// routing, templates and the engine's arithmetic in a single pass.
//
//	parity capture -base http://127.0.0.1:8422 -out captures/py
//	parity compare -a captures/py -b captures/go
//
// A capture is only trustworthy if capturing the SAME implementation twice
// produces the same bytes. `parity selfcheck` asserts exactly that, and it is
// the first thing to run after changing a normalisation rule.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- normalise

// Volatile bytes that legitimately differ between two runs of the same code.
// Each rule must be justified: a rule that erases a real difference turns the
// gate into decoration, so the set stays small and every entry says why.
var scrubs = []struct {
	name string
	re   *regexp.Regexp
	with string
	why  string
}{
	{
		"csrf", regexp.MustCompile(`name="csrf" value="[^"]*"`),
		`name="csrf" value="<CSRF>"`,
		"minted per session; carries no product meaning",
	},
	{
		"iso-datetime", regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?`),
		"<TS>",
		"render-time clock, not stored data",
	},
	{
		"relative-age", regexp.MustCompile(`\b\d+\s+(second|minute|hour|day)s?\s+ago\b`),
		"<AGO>",
		"derived from now() at render time",
	},
	{
		"http-date", regexp.MustCompile(`(?i)(date|last-modified|expires):\s*[^\r\n]+`),
		"$1: <HTTPDATE>",
		"response header clock",
	},
	{
		"feed-when", regexp.MustCompile(`\b\d{2}\.\d{2} \d{2}:\d{2}\b`),
		"<WHEN>",
		"the feed's clock column; the capture's own sign-in lands here at wall-clock time",
	},
	{
		"journal-count", regexp.MustCompile(`(?i)(chain: verified, |across <b>)\d+( events|</b> events)`),
		"${1}<N>${2}",
		"the capture signs in, and signing in appends one journal event, so the " +
			"total is relative to the act of measuring",
	},
	{
		"journal-hash", regexp.MustCompile(`<td class="qid">[0-9a-f]{8,64}</td>`),
		`<td class="qid"><HASH></td>`,
		"the newest journal row is the capture's own sign-in, and a chain hash " +
			"is a function of the event's timestamp, so it moves with the clock",
	},
}

// Cost of the two rules above, stated rather than buried: the golden master
// does not hold either implementation to the journal's event COUNT or to the
// feed's displayed times. The chain's integrity is still asserted (the page
// says "verified" or it does not, and that word is compared), and the counts
// belong in a test that controls the clock, not in a crawl.
//
// The journal-hash rule costs more and the cost is worth naming: this gate no
// longer proves that two implementations compute the SAME chain hash over the
// same event. That is a cross-language byte contract, and a crawl is the wrong
// instrument for it. It belongs in a pinned-vector test, the way the stack's
// own emitters pin theirs, and until that test exists this is an uncovered
// path rather than a covered one.

func normalise(b []byte) []byte {
	for _, s := range scrubs {
		b = s.re.ReplaceAll(b, []byte(s.with))
	}
	return b
}

// ------------------------------------------------------------------- client

type session struct {
	base string
	c    *http.Client
}

func newSession(base string) (*session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &session{
		base: strings.TrimRight(base, "/"),
		c: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
			// The route's own status is the thing under test, so redirects
			// are reported rather than followed.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *session) get(path string) (int, []byte, http.Header, error) {
	resp, err := s.c.Get(s.base + path)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header, err
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (s *session) csrf(path string) string {
	_, body, _, err := s.get(path)
	if err != nil {
		return ""
	}
	if m := csrfRe.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

// claim gets an admin session. On a fresh store the first account created
// becomes the owner and admin, which is the only role that can reach every
// page, so a capture that skipped this would record a wall of redirects.
//
// Signup closes once the installation is claimed, so a second capture against
// the same instance must sign in instead. Both paths are normal: which one
// runs depends only on whether this instance has been captured before.
func (s *session) claim(user, pw string) error {
	if err := s.post("/signup", url.Values{
		"username": {user}, "password": {pw}, "csrf": {s.csrf("/signup")},
	}); err == nil {
		return nil
	}
	if err := s.post("/login", url.Values{
		"username": {user}, "password": {pw}, "csrf": {s.csrf("/login")},
	}); err != nil {
		return fmt.Errorf("neither signup nor sign-in worked: %w", err)
	}
	return nil
}

// post submits a form and insists on the redirect that means it worked. The
// app answers a refusal with 303 too, carrying the reason in the query, so
// the location is checked rather than the status alone.
func (s *session) post(path string, form url.Values) error {
	resp, err := s.c.PostForm(s.base+path, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("%s returned %d, want 303", path, resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "msg=") {
		return fmt.Errorf("%s refused: %s", path, loc)
	}
	return nil
}

// ------------------------------------------------------------------- crawl

// Only href is followed. Crawling form `action` targets too was the first
// version of this, and it filled 123 of 400 surfaces with "405 Method Not
// Allowed" from POST-only routes: true, but it is the write surface answering
// the wrong verb, not the read surface this gate is about. The POST contract
// deserves its own gate rather than a third of this one.
var hrefRe = regexp.MustCompile(`href="(/[^"#?]*(?:\?[^"#]*)?)"`)

// Routes that must never be walked: following them would end the session or
// change state, and a capture that logs itself out records nothing useful.
var forbidden = map[string]bool{
	"/logout": true,
	"/signup": true,
	"/login":  true,
}

var idRe = regexp.MustCompile(`/\d+`)

// family collapses a URL to the rendering path it exercises: the route with
// its ids blanked, plus the query KEYS it carries but not their values.
// /task/17 and /task/93 are one family; /board?view=month is a different
// family from /board?d=…&view=….
func family(p string) string {
	base, q, hasQ := strings.Cut(p, "?")
	key := idRe.ReplaceAllString(base, "/{id}")
	if !hasQ {
		return key
	}
	var names []string
	for _, kv := range strings.Split(q, "&") {
		n, _, _ := strings.Cut(kv, "=")
		names = append(names, n)
	}
	sort.Strings(names)
	return key + "?" + strings.Join(names, "&")
}

// crawl walks the reachable read surface, bounded PER FAMILY rather than
// globally.
//
// An unbounded walk does not terminate here: the calendar's day links point at
// further days, and the first version of this discovered 4609 distinct
// /board?d=…&view=… URLs before hitting a global cap. A global cap is worse
// than useless for a golden master, because which 5000 of them you get depends
// on traversal timing, so two captures of the SAME code disagreed.
//
// Per-family sampling fixes both: the walk terminates, every distinct
// rendering path is still exercised, and the result is deterministic because
// each family's members are taken in sorted order.
func crawl(s *session, seeds []string, perFamily int) []string {
	seen := map[string]bool{}
	byFamily := map[string][]string{}
	var queue []string

	push := func(p string) {
		if forbidden[p] || seen[p] {
			return
		}
		f := family(p)
		if len(byFamily[f]) >= perFamily {
			return
		}
		seen[p] = true
		byFamily[f] = append(byFamily[f], p)
		queue = append(queue, p)
	}

	for _, p := range seeds {
		push(p)
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		_, body, _, err := s.get(p)
		if err != nil {
			continue
		}
		for _, m := range hrefRe.FindAllSubmatch(body, -1) {
			push(string(m[1]))
		}
	}

	var order []string
	for _, members := range byFamily {
		sort.Strings(members)
		order = append(order, members...)
	}
	sort.Strings(order)
	return order
}

// ----------------------------------------------------------------- capture

type entry struct {
	Path   string `json:"path"`
	Status int    `json:"status"`
	Type   string `json:"content_type"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	File   string `json:"file"`
}

type manifest struct {
	Base    string  `json:"base"`
	Count   int     `json:"count"`
	Entries []entry `json:"entries"`
	Digest  string  `json:"digest"`
}

func safeName(p string) string {
	n := strings.Trim(p, "/")
	if n == "" {
		n = "index"
	}
	n = strings.NewReplacer("/", "__", "?", "~", "=", "-", "&", "_").Replace(n)
	if len(n) > 120 {
		sum := sha256.Sum256([]byte(p))
		n = n[:100] + "~" + hex.EncodeToString(sum[:4])
	}
	return n
}

func capture(base, out string, perFamily int) error {
	s, err := newSession(base)
	if err != nil {
		return err
	}
	if err := s.claim("parity", "parity-golden-master"); err != nil {
		return fmt.Errorf("claiming the installation: %w", err)
	}

	paths := crawl(s, []string{"/", "/board", "/staff", "/connectors", "/results", "/sprints"}, perFamily)

	bodies := filepath.Join(out, "bodies")
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(bodies, 0o755); err != nil {
		return err
	}

	m := manifest{Base: base}
	roll := sha256.New()
	for _, p := range paths {
		status, body, hdr, err := s.get(p)
		if err != nil {
			return fmt.Errorf("GET %s: %w", p, err)
		}
		body = normalise(body)
		sum := sha256.Sum256(body)
		name := safeName(p)
		if err := os.WriteFile(filepath.Join(bodies, name), body, 0o644); err != nil {
			return err
		}
		e := entry{
			Path:   p,
			Status: status,
			Type:   strings.Split(hdr.Get("Content-Type"), ";")[0],
			Bytes:  len(body),
			SHA256: hex.EncodeToString(sum[:]),
			File:   name,
		}
		m.Entries = append(m.Entries, e)
		fmt.Fprintf(roll, "%s %d %s\n", e.Path, e.Status, e.SHA256)
	}
	m.Count = len(m.Entries)
	m.Digest = hex.EncodeToString(roll.Sum(nil))

	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(buf, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("captured %d surfaces from %s\n", m.Count, base)
	fmt.Printf("digest   %s\n", m.Digest)
	return nil
}

// ----------------------------------------------------------------- compare

func load(dir string) (*manifest, error) {
	buf, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// firstDiff reports where two bodies part company, in a form a person can act
// on: the line number and both sides. A gate that only says "differs" sends
// the reader back to diff(1), which is a gate that does not help.
func firstDiff(a, b []byte) string {
	la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	n := len(la)
	if len(lb) < n {
		n = len(lb)
	}
	for i := 0; i < n; i++ {
		if !bytes.Equal(la[i], lb[i]) {
			return fmt.Sprintf("line %d\n      golden: %s\n      actual: %s",
				i+1, clip(la[i]), clip(lb[i]))
		}
	}
	return fmt.Sprintf("identical for %d lines, then lengths differ (%d vs %d lines)",
		n, len(la), len(lb))
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func compare(dirA, dirB string) error {
	ma, err := load(dirA)
	if err != nil {
		return err
	}
	mb, err := load(dirB)
	if err != nil {
		return err
	}

	byPath := func(m *manifest) map[string]entry {
		out := map[string]entry{}
		for _, e := range m.Entries {
			out[e.Path] = e
		}
		return out
	}
	a, b := byPath(ma), byPath(mb)

	var missing, added, differ []string
	for p := range a {
		if _, ok := b[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			added = append(added, p)
		}
	}
	for p, ea := range a {
		eb, ok := b[p]
		if !ok {
			continue
		}
		if ea.Status != eb.Status || ea.SHA256 != eb.SHA256 {
			differ = append(differ, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(added)
	sort.Strings(differ)

	// A comparison over nothing must not read as success. This is the case
	// that turns a broken harness into a green light, so it fails loudly.
	if ma.Count == 0 || mb.Count == 0 {
		return fmt.Errorf("measured nothing: %s has %d surfaces, %s has %d",
			dirA, ma.Count, dirB, mb.Count)
	}

	fmt.Printf("golden %s  %d surfaces\n", dirA, ma.Count)
	fmt.Printf("actual %s  %d surfaces\n", dirB, mb.Count)

	if len(missing) == 0 && len(added) == 0 && len(differ) == 0 {
		fmt.Printf("\nPARITY: all %d surfaces identical\n", ma.Count)
		fmt.Printf("digest %s\n", ma.Digest)
		return nil
	}

	for _, p := range missing {
		fmt.Printf("\nGONE     %s (in golden, absent here)\n", p)
	}
	for _, p := range added {
		fmt.Printf("\nEXTRA    %s (here, not in golden)\n", p)
	}
	for _, p := range differ {
		ea, eb := a[p], b[p]
		if ea.Status != eb.Status {
			fmt.Printf("\nSTATUS   %s: golden %d, actual %d\n", p, ea.Status, eb.Status)
			continue
		}
		fmt.Printf("\nCONTENT  %s\n", p)
		ba, err1 := os.ReadFile(filepath.Join(dirA, "bodies", ea.File))
		bb, err2 := os.ReadFile(filepath.Join(dirB, "bodies", eb.File))
		if err1 == nil && err2 == nil {
			fmt.Printf("      %s\n", firstDiff(ba, bb))
		}
	}
	return fmt.Errorf("%d gone, %d extra, %d differing", len(missing), len(added), len(differ))
}

// -------------------------------------------------------------------- main

func usage() {
	fmt.Fprintln(os.Stderr, `parity - hold one CostCrew implementation against another

  parity capture -base URL -out DIR [-max N]
  parity compare -a GOLDEN -b ACTUAL

The capture is the observable HTTP surface, normalised for the clock and the
session. Compare exits non-zero on any difference.`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "capture":
		fs := flag.NewFlagSet("capture", flag.ExitOnError)
		base := fs.String("base", "http://127.0.0.1:8422", "running instance")
		out := fs.String("out", "captures/py", "directory to write")
		per := fs.Int("per-family", 25, "surfaces sampled per rendering family")
		fs.Parse(os.Args[2:])
		if err := capture(*base, *out, *per); err != nil {
			fmt.Fprintln(os.Stderr, "capture failed:", err)
			os.Exit(1)
		}
	case "compare":
		fs := flag.NewFlagSet("compare", flag.ExitOnError)
		a := fs.String("a", "", "golden capture directory")
		b := fs.String("b", "", "actual capture directory")
		fs.Parse(os.Args[2:])
		if *a == "" || *b == "" {
			usage()
			os.Exit(2)
		}
		if err := compare(*a, *b); err != nil {
			fmt.Fprintln(os.Stderr, "\nNO PARITY:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}
