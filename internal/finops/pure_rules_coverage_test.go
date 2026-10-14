package finops

import (
	"strings"
	"testing"
)

// The rules in this file are judged on values written here, not on a store,
// so each branch of a small decision is the thing that decides the answer.

func TestGradeNamesTheLadderRungAtEachEdge(t *testing.T) {
	cases := []struct {
		errPct float64
		want   string
	}{
		{0, "trusted"},
		{LadderTrusted, "trusted"},
		{LadderTrusted + 0.01, "working"},
		{13, "working"},
		{LadderWorking, "working"},
		{LadderWorking + 0.01, "starting"},
		{LadderStart, "starting"},
		{LadderStart + 0.01, "off"},
	}
	for _, c := range cases {
		if got := grade(c.errPct); got != c.want {
			t.Errorf("grade(%v) = %q, want %q", c.errPct, got, c.want)
		}
	}
}

func TestNextDayAdvancesOneCalendarDayAndKeepsAnUnparseableDay(t *testing.T) {
	if got := nextDay("2026-02-28"); got != "2026-03-01" {
		t.Errorf("nextDay(2026-02-28) = %q, want 2026-03-01 across the month end", got)
	}
	if got := nextDay("not-a-day"); got != "not-a-day" {
		t.Errorf("nextDay of an unparseable day = %q, want it returned unchanged", got)
	}
}

func TestInclusiveDaysBetweenCountsBothEndsAndNeverGoesBelowOne(t *testing.T) {
	if got := inclusiveDaysBetween("2026-03-05", "2026-03-05"); got != 1 {
		t.Errorf("a one-day window counted %d days, want 1", got)
	}
	if got := inclusiveDaysBetween("2026-03-01", "2026-03-03"); got != 3 {
		t.Errorf("1 to 3 March counted %d days, want 3 (both ends)", got)
	}
	if got := inclusiveDaysBetween("2026-03-10", "2026-03-01"); got != 1 {
		t.Errorf("an end before its start counted %d days, want the floor of 1", got)
	}
	if got := inclusiveDaysBetween("bad", "2026-03-01"); got != 1 {
		t.Errorf("an unparseable start counted %d days, want the floor of 1", got)
	}
	if got := inclusiveDaysBetween("2026-03-01", "bad"); got != 1 {
		t.Errorf("an unparseable end counted %d days, want the floor of 1", got)
	}
}

func TestDaysInMonthKnowsLeapYearsAndFallsBackToThirty(t *testing.T) {
	if got := daysInMonth("2026-02"); got != 28 {
		t.Errorf("February 2026 has %d days, want 28", got)
	}
	if got := daysInMonth("2028-02"); got != 29 {
		t.Errorf("February 2028 has %d days, want 29", got)
	}
	if got := daysInMonth("2026-04"); got != 30 {
		t.Errorf("April 2026 has %d days, want 30", got)
	}
	if got := daysInMonth("March"); got != 30 {
		t.Errorf("an unparseable period counted %d days, want the fallback of 30", got)
	}
}

func TestSignedDaysKeepsTheSignAndZeroesAnUnparseableDate(t *testing.T) {
	if got := signedDays("2026-03-01", "2026-03-11"); got != 10 {
		t.Errorf("answered 1 March, start 11 March: %d, want +10", got)
	}
	if got := signedDays("2026-03-11", "2026-03-01"); got != -10 {
		t.Errorf("answered 11 March, start 1 March: %d, want -10 (after the start)", got)
	}
	if got := signedDays("bad", "2026-03-01"); got != 0 {
		t.Errorf("an unparseable answer day gave %d, want 0", got)
	}
	if got := signedDays("2026-03-01", "bad"); got != 0 {
		t.Errorf("an unparseable start gave %d, want 0", got)
	}
}

func TestMethodNoteNamesEachMethodAndPassesAnUnknownOneThrough(t *testing.T) {
	cases := map[Method]string{
		Proportional:      "split by each team's direct usage",
		Even:              "split equally between the teams on the desk",
		Unallocated:       "left where it is, and counted as unallocated",
		Method("made-up"): "made-up",
	}
	for m, want := range cases {
		if got := MethodNote(m); got != want {
			t.Errorf("MethodNote(%q) = %q, want %q", m, got, want)
		}
	}
}

func TestValidMethodsIsTheThreeMethodsInOrder(t *testing.T) {
	got := ValidMethods()
	want := []Method{Proportional, Even, Unallocated}
	if len(got) != len(want) {
		t.Fatalf("ValidMethods() has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ValidMethods()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMethodFromMatchesIgnoringCaseAndRefusesAnythingElse(t *testing.T) {
	for _, s := range []string{"EVEN-SPLIT", "even-split", "Even-Split"} {
		m, err := MethodFrom(s)
		if err != nil || m != Even {
			t.Errorf("MethodFrom(%q) = %q, %v; want Even, nil", s, m, err)
		}
	}
	if m, err := MethodFrom("unallocated"); err != nil || m != Unallocated {
		t.Errorf("MethodFrom(unallocated) = %q, %v; want Unallocated, nil", m, err)
	}
	if _, err := MethodFrom("per-head"); err == nil {
		t.Error("MethodFrom(per-head) accepted a method that does not exist")
	}
	if _, err := MethodFrom(""); err == nil {
		t.Error("MethodFrom(empty) accepted an empty method")
	}
}

func TestKPICountsSplitsBlockedFromReportingAndCountsTheMeeting(t *testing.T) {
	ks := []KPI{
		{ID: "a", Meets: true},
		{ID: "b", Meets: false},
		{ID: "c", Blocked: "no data yet", Meets: true},
		{ID: "d", Blocked: "no data yet"},
	}
	reporting, blocked, meeting := KPICounts(ks)
	if reporting != 2 || blocked != 2 || meeting != 1 {
		t.Errorf("KPICounts = reporting %d, blocked %d, meeting %d; want 2, 2, 1",
			reporting, blocked, meeting)
	}
	r, b, m := KPICounts(nil)
	if r != 0 || b != 0 || m != 0 {
		t.Errorf("KPICounts(nil) = %d, %d, %d; want all zero", r, b, m)
	}
}

func TestLevelReadsTheHighestRungThatHolds(t *testing.T) {
	cases := []struct {
		run, walk, crawl bool
		want             int
	}{
		{true, true, true, 3},
		{false, true, true, 2},
		{false, false, true, 1},
		{false, false, false, 0},
	}
	for _, c := range cases {
		if got := level(c.run, c.walk, c.crawl); got != c.want {
			t.Errorf("level(run=%v, walk=%v, crawl=%v) = %d, want %d",
				c.run, c.walk, c.crawl, got, c.want)
		}
	}
}

func TestRiskRankOrdersLowMediumHighAndTreatsAnythingElseAsWorst(t *testing.T) {
	cases := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3, "": 3}
	for risk, want := range cases {
		if got := riskRank(risk); got != want {
			t.Errorf("riskRank(%q) = %d, want %d", risk, got, want)
		}
	}
}

func TestReasonCountLabelAndMeaningCoverEveryCase(t *testing.T) {
	notInExport := ReasonCount{Known: false, Reason: "budget_exceeded"}
	if got := notInExport.Label(); got != "not in this export" {
		t.Errorf("an unknown-reason row is labelled %q", got)
	}
	if got := notInExport.Meaning(); got == "" || !strings.Contains(got, "before TokenFuse 1.7.0") {
		t.Errorf("a row from before 1.7.0 says %q, not that the export predates it", got)
	}

	none := ReasonCount{Known: true, Reason: ""}
	if got := none.Label(); got != "none given" {
		t.Errorf("a known blank reason is labelled %q, want \"none given\"", got)
	}
	if got := none.Meaning(); got != "Blocked with no reason in the row." {
		t.Errorf("a blank reason means %q", got)
	}

	identity := ReasonCount{Known: true, Reason: IdentityMismatch}
	if got := identity.Label(); got != IdentityMismatch {
		t.Errorf("identity mismatch is labelled %q", got)
	}
	if got := identity.Meaning(); !strings.Contains(got, "nothing was spent") {
		t.Errorf("identity mismatch means %q, which does not say nothing was spent", got)
	}

	run := ReasonCount{Known: true, Reason: "budget_exceeded"}
	if got := run.Meaning(); !strings.Contains(got, "The run's budget") {
		t.Errorf("budget_exceeded means %q", got)
	}

	unit := ReasonCount{Known: true, Reason: "unit_budget_exceeded"}
	if got := unit.Meaning(); !strings.Contains(got, "The business unit's budget") {
		t.Errorf("unit_budget_exceeded means %q", got)
	}

	other := ReasonCount{Known: true, Reason: "rate_limited"}
	if got := other.Meaning(); !strings.Contains(got, "Refused by the gateway for the reason it names") {
		t.Errorf("an unnamed reason means %q", got)
	}
}

func TestOnlyIdentityRefusalsNeedsAtLeastOneAndOnlyIdentity(t *testing.T) {
	if (AgentAIRow{}).OnlyIdentityRefusals() {
		t.Error("a row with no blocked calls is only identity refusals")
	}
	onlyIdentity := AgentAIRow{Blocked: []ReasonCount{{Known: true, Reason: IdentityMismatch, Calls: 2}}}
	if !onlyIdentity.OnlyIdentityRefusals() {
		t.Error("a row blocked only for identity was not recognised as such")
	}
	mixed := AgentAIRow{Blocked: []ReasonCount{
		{Known: true, Reason: IdentityMismatch, Calls: 1},
		{Known: true, Reason: "budget_exceeded", Calls: 1},
	}}
	if mixed.OnlyIdentityRefusals() {
		t.Error("a row with a budget block read as only identity refusals")
	}
	unknown := AgentAIRow{Blocked: []ReasonCount{{Known: false, Reason: IdentityMismatch, Calls: 1}}}
	if unknown.OnlyIdentityRefusals() {
		t.Error("an identity reason from an export that did not carry reasons was trusted")
	}
}

func TestResultsReturnAndFirstPassRefuseWhenThereIsNothingToDivideBy(t *testing.T) {
	if _, ok := (Results{FoundMonthly: 500}).Return(); ok {
		t.Error("a return was reported with no crew spend to divide by")
	}
	got, ok := Results{FoundMonthly: 500, CrewSpend: 100}.Return()
	if !ok || got != 5 {
		t.Errorf("return = %v, %v; want 5, true", got, ok)
	}
	if _, ok := (Results{}).FirstPass(); ok {
		t.Error("a first-pass rate was reported with nothing judged")
	}
	rate, ok := Results{Posted: 3, Returned: 1}.FirstPass()
	if !ok || rate != 75 {
		t.Errorf("first pass = %v, %v; want 75, true", rate, ok)
	}
}

func TestLevelsNamesTheFourRungsInOrder(t *testing.T) {
	got := Levels()
	want := []string{"none", "crawl", "walk", "run"}
	if len(got) != len(want) {
		t.Fatalf("Levels() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Levels()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
