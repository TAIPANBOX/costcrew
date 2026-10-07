package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/agent-stack-go/passport"
)

const supervisorYAML = `name: supervisor
role: Crew supervisor
mission: |
  Plan the sprint and answer for the crew.
desk: ops
model:
  provider: anthropic
  model: claude-sonnet
skills:
  base: [planning]
  specialized: [triage]
permissions: [read-figures, propose]
boundaries:
  tools: [figures, variance]
  actions: |
    Never commits money.
budget:
  per_task_usd: 1.5
  monthly_usd: 40
`

const triageYAML = `name: triage-aws
role: AWS triage analyst
mission: Find what moved on the AWS bill.
desk: aws
model:
  provider: openrouter
  model: a-very-long-model-name-that-will-not-fit-in-the-column
skills:
  base: [reading]
permissions: [read-figures]
budget:
  per_task_usd: 0.25
  monthly_usd: 0
`

const forecasterYAML = `name: forecaster
role: Forecaster
desk: gcp
`

// The mtime is the passport's created_at; fixing it makes that checkable.
var hired = time.Date(2026, 3, 9, 8, 30, 0, 0, time.UTC)

func writeCrew(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, "jd", name+".yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, hired, hired); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func standardCrew(t *testing.T) string {
	return writeCrew(t, map[string]string{
		"supervisor": supervisorYAML,
		"triage-aws": triageYAML,
		"forecaster": forecasterYAML,
	})
}

func invoke(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	code = run(args, &so, &se)
	return code, so.String(), se.String()
}

func readPassport(t *testing.T, path string) passport.Passport {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("%s should end in a newline", path)
	}
	p, err := passport.Parse(raw)
	if err != nil {
		t.Fatalf("%s is not a valid Passport: %v\n%s", path, err, raw)
	}
	return p
}

// ------------------------------------------------------------------ list

func TestListShowsEveryAnalystWithItsEngineAndBudgets(t *testing.T) {
	code, out, errOut := invoke(t, "list", "-crew", standardCrew(t))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"ANALYST", "DESK", "ENGINE", "PER TASK", "MONTHLY",
		"3 analysts on 3 desks: aws, gcp, ops",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	sup := lineStarting(t, out, "supervisor")
	for _, want := range []string{"ops", "anthropic claude-sonnet", "1.50", "40.00"} {
		if !strings.Contains(sup, want) {
			t.Errorf("supervisor line lacks %q: %s", want, sup)
		}
	}
	// An engine name longer than its column is cut with an ellipsis rather than
	// pushing the figures out of line.
	tri := lineStarting(t, out, "triage-aws")
	if !strings.Contains(tri, "openrouter a-very-long-mode…") || strings.Contains(tri, "will-not-fit") {
		t.Errorf("long engine not truncated to 28: %s", tri)
	}
	if !strings.Contains(tri, "0.25") || !strings.Contains(tri, "0.00") {
		t.Errorf("triage budgets: %s", tri)
	}
	// Sorted by file name.
	if strings.Index(out, "forecaster") > strings.Index(out, "supervisor") {
		t.Errorf("analysts are not listed in file order:\n%s", out)
	}
}

func lineStarting(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no line starting %q in:\n%s", prefix, out)
	return ""
}

func TestAWrongCrewPathIsRefusedNotReportedAsZeroAgents(t *testing.T) {
	for _, args := range [][]string{
		{"list", "-crew", t.TempDir()},
		{"connect", "-crew", t.TempDir(), "-owner", "o", "-all", "-out", t.TempDir()},
		{"emit", "-crew", t.TempDir(), "-all", "-out", filepath.Join(t.TempDir(), "e.ndjson")},
	} {
		code, out, errOut := invoke(t, args...)
		if code != 1 || !strings.Contains(errOut, "no job descriptions under") ||
			!strings.Contains(errOut, "is -crew pointing at a CostCrew checkout?") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args[0], code, out, errOut)
		}
		if strings.Contains(out, "connected 0") {
			t.Errorf("%v: a crew of zero reads as success: %q", args[0], out)
		}
	}
}

func TestAJobDescriptionThatCannotBeReadNamesTheFile(t *testing.T) {
	broken := writeCrew(t, map[string]string{"a": "name: [unterminated\n"})
	code, _, errOut := invoke(t, "list", "-crew", broken)
	if code != 1 || !strings.Contains(errOut, "a.yaml") {
		t.Errorf("malformed yaml: exit %d, stderr %q", code, errOut)
	}
	nameless := writeCrew(t, map[string]string{"b": "role: Somebody\ndesk: aws\n"})
	code, _, errOut = invoke(t, "list", "-crew", nameless)
	if code != 1 || !strings.Contains(errOut, "b.yaml: no name field") {
		t.Errorf("nameless: exit %d, stderr %q", code, errOut)
	}
}

// ---------------------------------------------------------------- connect

func TestConnectAllWritesOneValidPassportPerAnalyst(t *testing.T) {
	crew, out := standardCrew(t), filepath.Join(t.TempDir(), "passports")
	code, stdout, errOut := invoke(t, "connect", "-crew", crew, "-out", out,
		"-owner", "finops@example.test", "-host", "crew.example.test", "-all")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(stdout, `connected 3 of 3 analysts, attestation "none"`) {
		t.Errorf("summary missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "means the id is a name this installation chose") {
		t.Errorf("attestation none is not explained:\n%s", stdout)
	}
	for _, name := range []string{"supervisor", "triage-aws", "forecaster"} {
		if !strings.Contains(stdout, filepath.Join(out, name+".json")) {
			t.Errorf("%s not reported as written:\n%s", name, stdout)
		}
	}

	sup := readPassport(t, filepath.Join(out, "supervisor.json"))
	if sup.ID != "agent://crew.example.test/supervisor" || sup.Owner != "finops@example.test" ||
		sup.DisplayName != "Crew supervisor" || sup.Runtime != "costcrew" {
		t.Errorf("supervisor passport = %+v", sup)
	}
	if sup.Parent != "" {
		t.Errorf("the supervisor has no parent, got %q", sup.Parent)
	}
	if sup.Attestation == nil || sup.Attestation.Method != "none" {
		t.Errorf("attestation = %+v", sup.Attestation)
	}
	if len(sup.Models) != 1 || sup.Models[0].Provider != "anthropic" || sup.Models[0].Model != "claude-sonnet" {
		t.Errorf("models = %+v", sup.Models)
	}
	if sup.CreatedAt != hired.Format(time.RFC3339) {
		t.Errorf("created_at = %q, want the file's mtime %q", sup.CreatedAt, hired.Format(time.RFC3339))
	}
	if len(sup.Filesystem) != 0 {
		t.Errorf("a filesystem scope nobody meant was declared: %+v", sup.Filesystem)
	}
	want := map[string]string{
		"desk":                "ops",
		"mission":             "Plan the sprint and answer for the crew.", // trailing newline trimmed
		"actions":             "Never commits money.",
		"tools":               "figures,variance",
		"permissions":         "read-figures,propose",
		"skills":              "planning,triage",
		"budget_per_task_usd": "1.50",
		"budget_monthly_usd":  "40.00",
	}
	for k, v := range want {
		if sup.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, sup.Labels[k], v)
		}
	}

	tri := readPassport(t, filepath.Join(out, "triage-aws.json"))
	if tri.Parent != "agent://crew.example.test/supervisor" {
		t.Errorf("parent = %q", tri.Parent)
	}
	if tri.Labels["budget_per_task_usd"] != "0.25" {
		t.Errorf("per-task label = %q", tri.Labels["budget_per_task_usd"])
	}
	// A budget of zero is not a budget and is not claimed; an empty field is
	// absent, not an empty label.
	for _, k := range []string{"budget_monthly_usd", "actions", "tools"} {
		if _, ok := tri.Labels[k]; ok {
			t.Errorf("label %s = %q should be absent", k, tri.Labels[k])
		}
	}
	bare := readPassport(t, filepath.Join(out, "forecaster.json"))
	if len(bare.Models) != 0 || bare.DisplayName != "Forecaster" {
		t.Errorf("a job description with no model must not invent one: %+v", bare)
	}
}

func TestConnectNamesTheAttestationItWasGivenAndDropsTheNoteForIt(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p")
	code, stdout, errOut := invoke(t, "connect", "-crew", standardCrew(t), "-out", out,
		"-owner", "o", "-agent", "triage-aws", "-attestation", "spiffe-svid")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(stdout, `connected 1 of 3 analysts, attestation "spiffe-svid"`) {
		t.Errorf("summary:\n%s", stdout)
	}
	if strings.Contains(stdout, "means the id is a name") {
		t.Errorf("the 'declared' note belongs to attestation none only:\n%s", stdout)
	}
	if p := readPassport(t, filepath.Join(out, "triage-aws.json")); p.Attestation.Method != "spiffe-svid" {
		t.Errorf("method = %q", p.Attestation.Method)
	}
	if _, err := os.Stat(filepath.Join(out, "supervisor.json")); err == nil {
		t.Errorf("an analyst who was not asked for was connected")
	}
}

func TestConnectByDeskTakesOnlyThatDesk(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p")
	code, stdout, _ := invoke(t, "connect", "-crew", standardCrew(t), "-out", out, "-owner", "o", "-desk", "aws")
	if code != 0 || !strings.Contains(stdout, "connected 1 of 3 analysts") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 || entries[0].Name() != "triage-aws.json" {
		t.Errorf("wrote %v", entries)
	}
}

// Selection errors are refused WHOLE: connecting three of four requested
// agents is how a crew ends up half-governed unnoticed.
func TestSelectionErrorsAreRefusedAndWriteNothing(t *testing.T) {
	crew := standardCrew(t)
	cases := []struct {
		name string
		sel  []string
		want string
	}{
		{"no selection", nil, "choose who to connect: -all, -desk <name>, or -agent <a,b>"},
		{"unknown desk", []string{"-desk", "mars"}, `no analyst is on desk "mars"; desks present: aws, gcp, ops`},
		{"one agent unknown", []string{"-agent", "supervisor, nobody ,triage-aws,ghost"}, "no such analyst: ghost, nobody"},
	}
	for _, tc := range cases {
		out := filepath.Join(t.TempDir(), "p")
		args := append([]string{"connect", "-crew", crew, "-out", out, "-owner", "o"}, tc.sel...)
		code, stdout, errOut := invoke(t, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: exit %d, stderr %q, want %q", tc.name, code, errOut, tc.want)
		}
		if strings.Contains(stdout, "connected") {
			t.Errorf("%s: claims a connection: %q", tc.name, stdout)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: wrote into %s anyway", tc.name, out)
		}
	}
}

func TestConnectWithoutAnOwnerIsAUsageErrorAndWritesNothing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p")
	code, stdout, errOut := invoke(t, "connect", "-crew", standardCrew(t), "-out", out, "-all")
	if code != 2 || stdout != "" || !strings.Contains(errOut, "-owner is required") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, errOut)
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("wrote without an owner")
	}
}

func TestDryRunSaysWhatItWouldWriteAndWritesNothing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p")
	code, stdout, errOut := invoke(t, "connect", "-crew", standardCrew(t), "-out", out,
		"-owner", "o", "-host", "crew.example.test", "-all", "-dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"would write " + filepath.Join(out, "triage-aws.json"),
		"agent://crew.example.test/triage-aws  parent=agent://crew.example.test/supervisor  attestation=none",
		"agent://crew.example.test/supervisor  parent=(none, this is the supervisor)  attestation=none",
		"would connect 3 of 3 analysts",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry run lacks %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("a dry run created %s", out)
	}
}

// The tool round-trips what it built through the contract's own parser, so a
// document the contract would refuse is refused here and nothing is written
// for it: an upper-case host is not a valid agent:// authority.
func TestADocumentTheContractWouldRefuseIsNeverWritten(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p")
	code, _, errOut := invoke(t, "connect", "-crew", standardCrew(t), "-out", out,
		"-owner", "o", "-host", "Bad Host", "-all")
	if code != 1 || !strings.Contains(errOut, "the document this tool built is not a valid Passport") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Errorf("wrote %d files for a host the contract refuses", len(entries))
	}
}

// --------------------------------------------------------------------- emit

type line struct {
	event string
	ts    float64
	data  map[string]any
}

func journal(t *testing.T, crew string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(crew, "events.ndjson"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rec(kind string, ts int64, assignee string) string {
	data := map[string]any{"task": 7}
	if assignee != "" {
		data["assignee"] = assignee
	}
	b, _ := json.Marshal(map[string]any{"event": kind, "ts": ts, "data": data, "hash": "h", "prev": "p"})
	return string(b)
}

func readEvents(t *testing.T, path string) []event.Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := event.VerifyChain(bytes.NewReader(raw))
	if err != nil || !rep.Ok() {
		t.Fatalf("the chain does not verify: %v %+v", err, rep)
	}
	var out []event.Event
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		e, err := event.Unmarshal([]byte(l))
		if err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

func TestEmitWritesAChainedEventPerSelectedAnalystsJournalLine(t *testing.T) {
	crew := standardCrew(t)
	journal(t, crew,
		rec("task_assigned", 1_780_000_000, "triage-aws"),
		rec("guard_blocked", 1_780_000_060, "triage-aws"),
		rec("sprint_opened", 1_780_000_100, ""), // nobody's event
		rec("budget_raised", 1_780_000_120, "supervisor"),
		rec("task_returned", 1_780_000_180, "forecaster"), // not selected
		`{ this is not json`,
	)
	dest := filepath.Join(t.TempDir(), "events", "costcrew.ndjson")
	code, stdout, errOut := invoke(t, "emit", "-crew", crew, "-out", dest,
		"-host", "crew.example.test", "-agent", "triage-aws,supervisor")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"wrote 3 events for 2 analysts -> " + dest,
		"skipped 2 lines with no analyst on them, 1 malformed",
		"verify with: agent-conform -chain " + dest,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	evs := readEvents(t, dest)
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3", len(evs))
	}
	type exp struct {
		typ, agent, severity string
		behalf               bool
	}
	for i, w := range []exp{
		{"task_assigned", "agent://crew.example.test/triage-aws", event.SeverityInfo, true},
		{"guard_blocked", "agent://crew.example.test/triage-aws", event.SeverityHigh, true},
		{"budget_raised", "agent://crew.example.test/supervisor", event.SeverityMedium, false},
	} {
		e := evs[i]
		if e.Type != w.typ || e.AgentID != w.agent || e.Severity != w.severity {
			t.Errorf("event %d = %s %s %s, want %+v", i, e.Type, e.AgentID, e.Severity, w)
		}
		if e.Source != "costcrew" || e.Schema != event.SchemaV02 {
			t.Errorf("event %d source/schema = %q/%q", i, e.Source, e.Schema)
		}
		if (len(e.OnBehalfOf) == 1 && e.OnBehalfOf[0] == "agent://crew.example.test/supervisor") != w.behalf {
			t.Errorf("event %d on_behalf_of = %v, want supervisor=%v", i, e.OnBehalfOf, w.behalf)
		}
	}
	if evs[0].TS != time.Unix(1_780_000_000, 0).UTC().Format(time.RFC3339) {
		t.Errorf("ts = %q", evs[0].TS)
	}
	if evs[0].Data["assignee"] != "triage-aws" {
		t.Errorf("the journal's data was not carried: %v", evs[0].Data)
	}
}

// Severity is raised only where an event carries a governance meaning, so a
// notifier is not trained to be ignored.
func TestSeverityOfRaisesOnlyGovernanceEvents(t *testing.T) {
	for kind, want := range map[string]string{
		"task_blocked":        event.SeverityHigh,
		"agent_suspended":     event.SeverityHigh,
		"task_returned":       event.SeverityMedium,
		"option_rejected":     event.SeverityMedium,
		"budgets_set":         event.SeverityMedium,
		"guard_tripped":       event.SeverityMedium,
		"task_assigned":       event.SeverityInfo,
		"deliverable_posted":  event.SeverityInfo,
		"":                    event.SeverityInfo,
		"blocked_and_guarded": event.SeverityHigh, // blocked wins over guard
	} {
		if got := severityOf(kind); got != want {
			t.Errorf("severityOf(%q) = %q, want %q", kind, got, want)
		}
	}
}

// Emitting twice into the same file continues ONE chain; it does not restart
// it or break it.
func TestEmitAppendsToTheChainRatherThanRestartingIt(t *testing.T) {
	crew := standardCrew(t)
	journal(t, crew, rec("task_assigned", 1_780_000_000, "triage-aws"))
	dest := filepath.Join(t.TempDir(), "e.ndjson")
	args := []string{"emit", "-crew", crew, "-out", dest, "-agent", "triage-aws"}
	for i := 0; i < 2; i++ {
		if code, _, errOut := invoke(t, args...); code != 0 {
			t.Fatalf("run %d: exit %d: %s", i+1, code, errOut)
		}
	}
	raw, _ := os.ReadFile(dest)
	rep, err := event.VerifyChain(bytes.NewReader(raw))
	if err != nil || !rep.Ok() || rep.Lines != 2 || rep.Chained != 1 || len(rep.HeadLines) != 1 {
		t.Errorf("two runs should be one chain of 2 lines: %v %+v", err, rep)
	}
}

// Writing zero events is a failure with the counts in it, never "wrote 0".
func TestEmitThatMeasuredNothingFails(t *testing.T) {
	crew := standardCrew(t)
	journal(t, crew, rec("task_assigned", 1, "forecaster"), rec("sprint_opened", 2, ""))
	dest := filepath.Join(t.TempDir(), "e.ndjson")
	code, stdout, errOut := invoke(t, "emit", "-crew", crew, "-out", dest, "-agent", "triage-aws")
	if code != 1 || !strings.Contains(errOut, "measured nothing: 2 journal lines carried no event for the 1 selected analysts") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(stdout, "wrote") {
		t.Errorf("claims a write: %q", stdout)
	}
}

func TestEmitErrorsAreNamed(t *testing.T) {
	crew := standardCrew(t) // no events.ndjson yet
	dest := filepath.Join(t.TempDir(), "e.ndjson")
	code, _, errOut := invoke(t, "emit", "-crew", crew, "-out", dest, "-all")
	if code != 1 || !strings.Contains(errOut, "reading the crew's journal") {
		t.Errorf("no journal: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = invoke(t, "emit", "-crew", crew, "-out", dest)
	if code != 1 || !strings.Contains(errOut, "choose who to connect") {
		t.Errorf("no selection: exit %d, stderr %q", code, errOut)
	}
	// An output path whose parent is a file cannot be created.
	journal(t, crew, rec("task_assigned", 1, "triage-aws"))
	blocker := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = invoke(t, "emit", "-crew", crew, "-out", filepath.Join(blocker, "e.ndjson"), "-all")
	if code != 1 || !strings.HasPrefix(errOut, "stack: ") {
		t.Errorf("unwritable: exit %d, stderr %q", code, errOut)
	}
}

// Read-only against the installation: none of the three verbs changes a byte
// under -crew.
func TestNothingUnderTheCrewDirectoryIsEverChanged(t *testing.T) {
	crew := standardCrew(t)
	journal(t, crew, rec("task_assigned", 1_780_000_000, "triage-aws"))
	before := snapshot(t, crew)
	invoke(t, "list", "-crew", crew)
	invoke(t, "connect", "-crew", crew, "-out", t.TempDir(), "-owner", "o", "-all")
	invoke(t, "emit", "-crew", crew, "-out", filepath.Join(t.TempDir(), "e.ndjson"), "-all")
	if after := snapshot(t, crew); after != before {
		t.Errorf("the installation changed:\nbefore %s\nafter  %s", before, after)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(p)
		fmt.Fprintf(&b, "%s %d %d %x\n", p, fi.Size(), fi.ModTime().UnixNano(), raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// -------------------------------------------------------------------- usage

func TestUsageAndFlagErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}} {
		code, out, errOut := invoke(t, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "stack list    -crew <dir>") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	code, _, errOut := invoke(t, "list", "-nope")
	if code != 2 || !strings.Contains(errOut, "nope") {
		t.Errorf("bad flag: exit %d, stderr %q", code, errOut)
	}
	if code, _, _ := invoke(t, "emit", "-h"); code != 0 {
		t.Errorf("-h exited %d", code)
	}
}
