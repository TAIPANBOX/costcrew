package deliver

// THE GATE of invariant 70 for the packet and the prompt: build every packet
// section from a fully populated installation in every mode, and require that
// no real identifier from the store is in what would leave the process.
//
// The identifiers it checks against are NOT the ones the masker was written
// from. internal/promptfixture walks the schema and counts every text column
// as an identifier unless it is listed, by name, as something else; a column
// added tomorrow fails TestEveryTextColumnIsClassified until somebody decides
// what it is. So a leak through a column the masker's author forgot is a
// failure here, not a green line.

import (
	"database/sql"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/promptfixture"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// installation is one populated store, the roster read from it, and the
// identifiers and free text in it, enumerated by the schema walk.
type installation struct {
	db     *sql.DB
	roster []crew.Analyst
	check  *promptfixture.Checker

	mu    sync.Mutex
	cases []packetCase
	built map[PromptData][]builtPacket
}

// One installation for the whole test binary: building it takes most of a
// second and nothing here writes to it.
var shared struct {
	once sync.Once
	in   *installation
	st   *store.Store
	dir  string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.st != nil {
		shared.st.Close()
	}
	if shared.dir != "" {
		os.RemoveAll(shared.dir)
	}
	os.Exit(code)
}

func newInstallation(t *testing.T) *installation {
	t.Helper()
	shared.once.Do(func() {
		dir, err := os.MkdirTemp("", "costcrew-deliver-gate-")
		if err != nil {
			shared.err = err
			return
		}
		shared.dir = dir
		st, err := promptfixture.Build(dir)
		if err != nil {
			shared.err = err
			return
		}
		shared.st = st
		shared.in, shared.err = describe(st.DB())
	})
	if shared.err != nil {
		t.Fatalf("building the installation: %v", shared.err)
	}
	return shared.in
}

func describe(db *sql.DB) (*installation, error) {
	in := &installation{db: db, built: map[PromptData][]builtPacket{}}
	var err error
	if in.roster, err = crew.Roster(db); err != nil {
		return nil, err
	}
	in.check, err = promptfixture.NewChecker(db)
	return in, err
}

// policy installs a policy of the given mode over the installation for the
// life of the test.
func (in *installation) policy(t *testing.T, mode PromptData) *Policy {
	t.Helper()
	p, err := NewPolicy(mode, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Bind(in.db)
	t.Cleanup(SetActivePolicy(p))
	return p
}

// packetCase is one packet to build: some analyst, some task.
type packetCase struct {
	task    crew.Task
	analyst crew.Analyst
}

type builtPacket struct {
	c    packetCase
	text string
}

// packets is every case built under mode, once.
func (in *installation) packets(t *testing.T, mode PromptData) []builtPacket {
	t.Helper()
	in.mu.Lock()
	defer in.mu.Unlock()
	if got, ok := in.built[mode]; ok {
		return got
	}
	if in.cases == nil {
		cs, err := in.makeCases()
		if err != nil {
			t.Fatal(err)
		}
		in.cases = cs
	}
	in.policy(t, mode)
	out := make([]builtPacket, 0, len(in.cases))
	for _, c := range in.cases {
		out = append(out, builtPacket{c, Packet(in.db, c.task, c.analyst, false)})
	}
	in.built[mode] = out
	return out
}

// makeCases is every analyst of the roster against its own tasks, a plain
// task of its desk, and a task whose title names a closed period (which is
// what makes the chargeback analyst's close pack appear). Between them every
// skill-gated section is reachable.
func (in *installation) makeCases() ([]packetCase, error) {
	tasks, err := crew.Tasks(in.db, crew.TaskFilter{})
	if err != nil {
		return nil, err
	}
	byAssignee := map[string][]crew.Task{}
	for _, tk := range tasks {
		byAssignee[tk.Assignee] = append(byAssignee[tk.Assignee], tk)
	}
	var out []packetCase
	for _, a := range in.roster {
		// Every analyst as if it were on the rota: a suspended one is handed no
		// figures at all, and its sections would then never be looked at.
		a.State = "active"
		n := 0
		for _, tk := range byAssignee[a.Name] {
			if tk.Anomaly == "" && n > 0 {
				continue
			}
			out = append(out, packetCase{tk, a})
			if n++; n >= 3 {
				break
			}
		}
		out = append(out,
			packetCase{crew.Task{ID: 900001, Title: "Weekly work", Desk: a.Desk}, a},
			packetCase{crew.Task{ID: 900002, Title: "Month-end close pack 2026-07", Desk: a.Desk}, a})
	}
	return out, nil
}

// leaks is the independent checker's verdict on text.
func (in *installation) leaks(text, own string) []string { return in.check.Leaks(text, own) }

// sectionHeaders is the inventory of what a packet can say. The full-mode
// union must hold every one of them, or the fixture is not exercising a
// section and the gate would be green over it.
var sectionHeaders = []string{
	"The anomaly", "The series", "Drivers on this service and desk",
	"The team's month", "The last posted explanation on this service",
	"The executive pack", "Rightsizing recommendations on",
	"The SaaS renewal calendar", "Commitments (", "Data quality (",
	"The desk's month", "Forecasting (", "The AI desk's month",
	"Unit economics, cost per outcome", "The provider's own budget recommendation",
	"The close pack", "Customer units", "What you posted on this desk before",
	"A typed hint (a suggestion, not a finding)",
}

func headersIn(ps []builtPacket) map[string]bool {
	seen := map[string]bool{}
	for _, bp := range ps {
		for _, h := range sectionHeaders {
			if strings.Contains(bp.text, h) {
				seen[h] = true
			}
		}
	}
	return seen
}

func TestTheFixtureExercisesEverySectionInFullMode(t *testing.T) {
	in := newInstallation(t)
	seen := headersIn(in.packets(t, PromptFull))
	for _, h := range sectionHeaders {
		if !seen[h] {
			t.Errorf("no packet of the fixture carries the %q section, so the gate would never look at it", h)
		}
	}
}

// A restricting mode changes names and drops text. It must not garble this
// console's own words: several analysts are called "renewals", "commitments",
// "governance", and a scrub that did not respect whole words would turn the
// section headers into tokens.
func TestMaskedPacketsKeepEverySectionHeaderTheirModeSends(t *testing.T) {
	in := newInstallation(t)
	masked := headersIn(in.packets(t, PromptMasked))
	for _, h := range sectionHeaders {
		if !masked[h] {
			t.Errorf("masked mode lost the %q section header from every packet", h)
		}
	}
	// aggregates drops the sections that are rows, and nothing else
	agg := headersIn(in.packets(t, PromptAggregates))
	rows := map[string]bool{
		"Drivers on this service and desk": true, "The last posted explanation on this service": true,
		"Rightsizing recommendations on": true, "The SaaS renewal calendar": true,
		"Unit economics, cost per outcome": true, "What you posted on this desk before": true,
		"A typed hint (a suggestion, not a finding)": true,
	}
	for _, h := range sectionHeaders {
		switch {
		case rows[h] && agg[h]:
			t.Errorf("aggregates sent the row-level section %q", h)
		case !rows[h] && !agg[h]:
			t.Errorf("aggregates dropped the %q section, which is a total and must stay", h)
		}
	}
}

// TestEveryTextColumnIsClassified is the half of the gate that makes a new
// column impossible to forget.
func TestEveryTextColumnIsClassified(t *testing.T) {
	in := newInstallation(t)
	un, err := promptfixture.UnclassifiedColumns(in.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) > 0 {
		t.Errorf("text columns with no decision on whether they are identifiers: %s", strings.Join(un, ", "))
	}
}

// TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets is the gate. Every
// packet case, both modes, byte for byte.
func TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets(t *testing.T) {
	in := newInstallation(t)
	if n := in.check.Identifiers(); n < 100 {
		t.Fatalf("the installation holds only %d identifiers; the gate would measure nothing", n)
	}
	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		t.Run(string(mode), func(t *testing.T) {
			built, withTokens := 0, 0
			for _, bp := range in.packets(t, mode) {
				p, c := bp.text, bp.c
				if p == "" || p == noFiguresSentence {
					continue
				}
				built++
				if strings.Contains(p, "team-") || strings.Contains(p, "desk-") {
					withTokens++
				}
				if len(p) > packetMaxBytes {
					t.Errorf("%s / %s: a %s packet is %d bytes, over the %d cap", c.analyst.Name, c.task.Title, mode, len(p), packetMaxBytes)
				}
				if l := in.leaks(p, c.analyst.Name); len(l) > 0 {
					t.Errorf("%s / %q (%s mode) leaks %d value(s): %s\n--- packet\n%s",
						c.analyst.Name, c.task.Title, mode, len(l), strings.Join(first(l, 6), "; "), p)
				}
			}
			// Aggregates builds fewer packets than masked on purpose: an
			// analyst whose every section is a row has nothing left to send.
			if built < 60 || withTokens < 40 {
				t.Fatalf("only %d packets were built and %d carried a token; the gate measured too little", built, withTokens)
			}
		})
	}
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// The same installation in full mode DOES carry what the other two must not;
// without this the leak check could be passing because the checker is blind.
func TestTheLeakCheckSeesAFullModePacket(t *testing.T) {
	in := newInstallation(t)
	found := 0
	for _, bp := range in.packets(t, PromptFull) {
		if len(in.leaks(bp.text, bp.c.analyst.Name)) > 0 {
			found++
		}
	}
	if found < 100 {
		t.Errorf("only %d full-mode packets register as carrying identifiers; the checker would not notice a leak", found)
	}
}

// Free text is not sent under masked at all, and says it was withheld.
func TestFreeTextIsWithheldUnderMaskedAndAggregates(t *testing.T) {
	in := newInstallation(t)
	fullHas := map[string]bool{}
	for _, bp := range in.packets(t, PromptFull) {
		for _, m := range promptfixture.FreeTextMarkers {
			if strings.Contains(bp.text, m) {
				fullHas[m] = true
			}
		}
	}
	for _, m := range promptfixture.FreeTextMarkers {
		if !fullHas[m] {
			t.Fatalf("full mode never prints the %q free text, so this test cannot show it being withheld", m)
		}
	}

	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		standIns := 0
		for _, bp := range in.packets(t, mode) {
			for _, m := range promptfixture.FreeTextMarkers {
				if strings.Contains(bp.text, m) {
					t.Fatalf("%s mode sent free text %q:\n%s", mode, m, bp.text)
				}
			}
			standIns += strings.Count(bp.text, WithheldFreeText) + strings.Count(bp.text, WithheldLabel)
		}
		if mode == PromptMasked && standIns == 0 {
			t.Error("masked mode withheld free text but never said so: no packet carries a stand-in")
		}
	}
}

// Aggregates carry no row-level section at all.
func TestAggregatesCarryNoRowLevelSections(t *testing.T) {
	in := newInstallation(t)
	rowLevel := []string{
		"Drivers on this service and desk", "The last posted explanation", "What you posted on this desk before",
		"Rightsizing recommendations on", "The SaaS renewal calendar", "By agent, top ten by cost",
		"By model, top ten by cost", "Unit economics, cost per outcome", "Invoice reconciliation\ninvoice ",
		"utilisation: used over committed, per commitment", "Buy or wait", "The last posted explanations on the desks",
		"service:   ", "driver:    ", "caused by:",
	}
	// A token of a kind that names a row-level thing. Matched as a token (hex
	// digits after the dash), not as a substring: "run-rate" and "per-agent"
	// are this console's own words.
	rowToken := regexp.MustCompile(`\b(svc|agent|user|inv|vendor|product|cmt|res|model|run|host)-[0-9a-f]{4,}\b`)
	for _, bp := range in.packets(t, PromptAggregates) {
		p, c := bp.text, bp.c
		for _, h := range rowLevel {
			if strings.Contains(p, h) {
				t.Errorf("%s / %q: aggregates sent row-level text %q:\n%s", c.analyst.Name, c.task.Title, h, p)
			}
		}
		if tok := rowToken.FindString(p); tok != "" {
			t.Errorf("%s / %q: aggregates sent the token %q, which names a row-level thing:\n%s", c.analyst.Name, c.task.Title, tok, p)
		}
	}
}

// Money, dates and counts stay: masking changes names and nothing else.
func TestMaskedPacketKeepsMoneyDatesAndCounts(t *testing.T) {
	db, task, a := goldenPacketInputs(t)
	full := Packet(db, task, a, false)

	p, err := NewPolicy(PromptMasked, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Bind(db)
	defer SetActivePolicy(p)()
	masked := Packet(db, task, a, false)
	if masked == full {
		t.Fatal("masked mode changed nothing about a packet full of team and service names")
	}

	// A figure is a number with a decimal point (money, a ratio, a z-score) or
	// a date. A digit inside a name ("Amazon EC2") is not one, and is allowed
	// to go with the name.
	figure := regexp.MustCompile(`\d+\.\d+|\d{4}-\d{2}(?:-\d{2})?`)
	have := map[string]int{}
	for _, n := range figure.FindAllString(masked, -1) {
		have[n]++
	}
	figures := figure.FindAllString(full, -1)
	if len(figures) < 40 {
		t.Fatalf("the golden packet holds only %d figures; the test would measure too little", len(figures))
	}
	for _, n := range figures {
		if have[n] == 0 {
			t.Errorf("the figure or date %q is in the full packet and gone from the masked one", n)
			continue
		}
		have[n]--
	}
	for _, header := range []string{"The anomaly", "The series", "The team's month", "The desk's month", "Forecasting"} {
		if !strings.Contains(masked, header) {
			t.Errorf("masking dropped the %q section header", header)
		}
	}
}
