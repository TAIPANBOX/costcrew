package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// source is idryx's `agents` file as this tool writes it, decoded with the
// field names idryx itself reads, not this tool's own struct.
type source struct {
	Agents []struct {
		ID         string   `json:"id"`
		Runtime    string   `json:"runtime"`
		OnBehalfOf string   `json:"onBehalfOf"`
		Owner      string   `json:"owner"`
		Created    string   `json:"created"`
		Tools      []string `json:"tools"`
	} `json:"agents"`
}

func invoke(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	code = run(args, &so, &se)
	return code, so.String(), se.String()
}

// consoleWith builds a data directory whose roster holds exactly the given
// rows, so the expected file can be written down by hand rather than derived
// from the code under test.
func consoleWith(t *testing.T, rows ...[]any) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	if _, err := db.Exec(crew.RosterSchema); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		insert(t, db, r...)
	}
	return dir
}

func insert(t *testing.T, db *sql.DB, v ...any) {
	t.Helper()
	// name, owner, parent, hired, rights
	if _, err := db.Exec(`INSERT INTO analysts(name, state, owner, parent, hired, rights, skills)
		VALUES (?, 'active', ?, ?, ?, ?, 'figures-read')`, v...); err != nil {
		t.Fatal(err)
	}
}

func decode(t *testing.T, raw string) source {
	t.Helper()
	var s source
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("output is not idryx's agents source: %v\n%s", err, raw)
	}
	return s
}

// The file carries what idryx asks for and nothing about how well an agent
// does its job: id under the given authority, runtime, owner, the parent as an
// agent:// URI, the hire date in RFC 3339, and the RIGHTS as tools.
func TestTheRosterIsWrittenAsAnIdryxAgentsSource(t *testing.T) {
	dir := consoleWith(t,
		[]any{"supervisor", "ops@example.test", nil, "2026-03-01", "figures-read,propose-only"},
		[]any{"triage-aws", "fin@example.test", "supervisor", "2026-04-15", "figures-read"},
	)
	code, out, errOut := invoke(t, "-data", dir, "-host", "crew.example.test")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got := decode(t, out)
	if len(got.Agents) != 2 {
		t.Fatalf("%d agents, want 2:\n%s", len(got.Agents), out)
	}
	sup, tri := got.Agents[0], got.Agents[1]
	if sup.ID != "agent://crew.example.test/supervisor" || tri.ID != "agent://crew.example.test/triage-aws" {
		t.Errorf("ids = %q, %q", sup.ID, tri.ID)
	}
	if sup.Runtime != "costcrew" || tri.Runtime != "costcrew" {
		t.Errorf("runtime = %q, %q", sup.Runtime, tri.Runtime)
	}
	if sup.Owner != "ops@example.test" || tri.Owner != "fin@example.test" {
		t.Errorf("owners = %q, %q", sup.Owner, tri.Owner)
	}
	if sup.OnBehalfOf != "" {
		t.Errorf("the supervisor acts for nobody, got onBehalfOf %q", sup.OnBehalfOf)
	}
	if tri.OnBehalfOf != "agent://crew.example.test/supervisor" {
		t.Errorf("onBehalfOf = %q, want the supervisor's agent:// URI", tri.OnBehalfOf)
	}
	if sup.Created != "2026-03-01T00:00:00Z" || tri.Created != "2026-04-15T00:00:00Z" {
		t.Errorf("created = %q, %q; idryx reads RFC 3339, not a bare date", sup.Created, tri.Created)
	}
	if strings.Join(sup.Tools, "|") != "figures-read|propose-only" || strings.Join(tri.Tools, "|") != "figures-read" {
		t.Errorf("tools = %v, %v; they are the rights", sup.Tools, tri.Tools)
	}
	if strings.Contains(out, "skills") || strings.Contains(out, "mission") {
		t.Errorf("a skill or a mission leaked into an identity file:\n%s", out)
	}
}

// Sorted by name whatever order the rows were hired in, so the same roster
// gives the same file every time.
func TestAgentsAreSortedByName(t *testing.T) {
	dir := consoleWith(t,
		[]any{"zulu", "o", nil, "2026-01-01", "figures-read"},
		[]any{"alpha", "o", nil, "2026-01-02", "figures-read"},
		[]any{"mike", "o", nil, "2026-01-03", "figures-read"},
	)
	_, out, _ := invoke(t, "-data", dir)
	var names []string
	for _, a := range decode(t, out).Agents {
		names = append(names, strings.TrimPrefix(a.ID, "agent://costcrew.local/"))
	}
	if strings.Join(names, ",") != "alpha,mike,zulu" {
		t.Errorf("order = %v", names)
	}
}

// A hire date that is not a date is left out, not passed on in a shape idryx
// would misread; an agent with no rights carries no tools (the file writes
// that as null today, which this test deliberately does not pin: whether
// idryx wants null or [] is its reader's to say).
func TestBadDatesAndEmptyRightsAreLeftHonest(t *testing.T) {
	dir := consoleWith(t,
		[]any{"agent-a", "o", nil, "March 2026", ""},
		[]any{"agent-b", "o", nil, "", "figures-read"},
	)
	_, out, _ := invoke(t, "-data", dir)
	got := decode(t, out)
	for _, a := range got.Agents {
		if a.Created != "" {
			t.Errorf("%s: created %q for a hire date idryx cannot read", a.ID, a.Created)
		}
	}
	if len(got.Agents[0].Tools) != 0 || len(got.Agents[1].Tools) != 1 {
		t.Errorf("tools = %v, %v", got.Agents[0].Tools, got.Agents[1].Tools)
	}
}

// With no one on the roster the file is still an agents source, with an empty
// list rather than null.
func TestAnEmptyRosterIsAnEmptyList(t *testing.T) {
	code, out, _ := invoke(t, "-data", consoleWith(t))
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, `"agents": []`) {
		t.Errorf("want an empty agents list, got:\n%s", out)
	}
}

// -out writes the file, says how many on stderr, and leaves stdout empty.
func TestOutWritesTheFileAndSaysHowMany(t *testing.T) {
	dir := consoleWith(t, []any{"solo", "o", nil, "2026-01-01", "figures-read"})
	dest := filepath.Join(t.TempDir(), "agents.json")
	code, out, errOut := invoke(t, "-data", dir, "-out", dest)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout should be empty when -out is a file, got %q", out)
	}
	if !strings.Contains(errOut, "1 agents written to "+dest) {
		t.Errorf("stderr = %q", errOut)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, string(raw)); len(got.Agents) != 1 || got.Agents[0].ID != "agent://costcrew.local/solo" {
		t.Errorf("file = %s", raw)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("the file should end in a newline")
	}
}

// A destination that cannot be written is a failure with the tool's name on
// it, not a silent zero.
func TestAnUnwritableDestinationFails(t *testing.T) {
	dir := consoleWith(t)
	dest := filepath.Join(t.TempDir(), "no-such-dir", "agents.json")
	code, _, errOut := invoke(t, "-data", dir, "-out", dest)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.HasPrefix(errOut, "idryxsource: ") {
		t.Errorf("stderr = %q", errOut)
	}
}

// A data directory that holds no roster table (a console that never seeded
// one) fails by saying so; a data path that is a file cannot be opened.
func TestAStoreWithNoRosterAndAnUnopenableStoreFail(t *testing.T) {
	code, out, errOut := invoke(t, "-data", t.TempDir())
	if code != 1 || out != "" || !strings.HasPrefix(errOut, "idryxsource: ") {
		t.Errorf("no roster: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = invoke(t, "-data", file)
	if code != 1 || !strings.HasPrefix(errOut, "idryxsource: ") {
		t.Errorf("unopenable: exit %d, stderr %q", code, errOut)
	}
}

// A flag the tool does not have is the usage error, exit 2, and nothing is
// read; -h is not a failure.
func TestFlagErrorsAreUsageErrors(t *testing.T) {
	code, out, errOut := invoke(t, "-nope")
	if code != 2 || out != "" || !strings.Contains(errOut, "nope") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code, _, _ := invoke(t, "-h"); code != 0 {
		t.Errorf("-h exited %d, want 0", code)
	}
}
