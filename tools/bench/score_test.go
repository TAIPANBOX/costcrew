package main

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// -------------------------------------------------------------- namedCause

func TestNamedCausePrefersTheAnomalyExplainOption(t *testing.T) {
	body := "Some prose about the cause of nothing in particular.\n\n" +
		"```options\n{\"options\":[{\"class\":\"anomaly.explain\",\"summary\":\"the quarterly refresh\"}]}\n```\n"
	if got := namedCause(body); got != "the quarterly refresh" {
		t.Errorf("namedCause = %q, want the option's summary", got)
	}
}

func TestNamedCauseFallsBackToTheSentenceAfterCause(t *testing.T) {
	body := "## Findings\n\nThe cause: a quarterly model refresh that ran long. It is expected.\n"
	got := namedCause(body)
	if got != "a quarterly model refresh that ran long" {
		t.Errorf("namedCause = %q, want the sentence after \"cause\"", got)
	}
}

// "because" must never be mistaken for the word "cause".
func TestNamedCauseDoesNotMatchBecause(t *testing.T) {
	body := "Spend rose because a training run was left up over the weekend.\n"
	if got := namedCause(body); got != "" {
		t.Errorf("namedCause = %q, want empty: \"because\" contains \"cause\" as a "+
			"substring and must not be read as the word", got)
	}
}

func TestNamedCauseFallsBackToNoneEstablished(t *testing.T) {
	body := "## Findings\n\nWe looked and none established despite the evidence available.\n"
	if got := namedCause(body); got != "none established" {
		t.Errorf("namedCause = %q, want \"none established\"", got)
	}
}

// B7-SPEC.md section 5's own boundary: "a deliverable with no options block
// and no 'cause' sentence scores empty, not a crash".
func TestNamedCauseIsEmptyRatherThanCrashing(t *testing.T) {
	for _, body := range []string{
		"",
		"Just some ordinary prose about the series with no relevant word in it.",
		"```options\nnot even valid json\n```\n",
	} {
		got := namedCause(body)
		if got != "" {
			t.Errorf("namedCause(%q) = %q, want empty", body, got)
		}
	}
}

// Hostile: a 1 MB deliverable, from the mock or anywhere else, must not
// crash or hang the scorer.
func TestNamedCauseSurvivesAOneMegabyteDeliverable(t *testing.T) {
	huge := strings.Repeat("the series moved for reasons unrelated to any driver. ", 20_000)
	if len(huge) < 1_000_000 {
		t.Fatalf("fixture is only %d bytes, want at least 1 MB", len(huge))
	}
	got := namedCause(huge)
	if got != "" {
		t.Errorf("a 1 MB deliverable with no cause sentence and no options block "+
			"named a cause: %q", got)
	}
}

// -------------------------------------------------------------- causeMatches

func TestCauseMatchesRequiresEveryLongWord(t *testing.T) {
	label := "Quarterly model refresh, planned"
	if !causeMatches(label, "the cause was a quarterly model refresh that was planned in advance") {
		t.Error("every long word of the label appears in the named cause, and it was not matched")
	}
	if causeMatches(label, "the cause was a quarterly model refresh") {
		t.Error("\"planned\" is missing from the named cause, and it matched anyway")
	}
}

// B7-SPEC.md section 5's own boundary: "the driver label with a
// three-letter word scores on the longer words only".
func TestCauseMatchesIgnoresThreeLetterWords(t *testing.T) {
	// "the" and "and" are both three letters; neither has to appear.
	label := "The Big Move and the Storage Change"
	named := "big move storage change"
	if !causeMatches(label, named) {
		t.Errorf("a named cause missing only three-letter-or-shorter words of the "+
			"label was scored unmatched: label %q, named %q", label, named)
	}
}

func TestCauseMatchesIsCaseInsensitive(t *testing.T) {
	if !causeMatches("Quarterly Refresh", "a QUARTERLY REFRESH of the training set") {
		t.Error("cause matching is case-sensitive and should not be")
	}
}

func TestCauseMatchesIsFalseOnAnEmptyNamedCause(t *testing.T) {
	if causeMatches("Quarterly model refresh, planned", "") {
		t.Error("an empty named cause scored matched")
	}
}

// The mutant B7-SPEC.md section 5 names by name: "score cause by substring
// of the whole deliverable instead of the named cause". A deliverable can
// carry the driver's own words somewhere in its body (in the task
// description it was handed back, in an aside) without EVER naming them as
// the cause; scoreDeliverable must judge the extracted named cause, not the
// raw body, or every case would look solved by an analyst who never
// actually answered the question.
func TestScoreJudgesTheNamedCauseNotTheWholeBody(t *testing.T) {
	an := anomaly.Anomaly{
		ID: "A-mut1", Source: "gcp", Service: "GKE", Day: "2026-06-22",
		Driver: "Quarterly model refresh, planned",
	}
	body := "The task on your desk mentions \"Quarterly model refresh, planned\" " +
		"only because that phrase was in the title we were given. " +
		"The cause: an unrelated retry loop that nobody has explained yet.\n"
	got := scoreDeliverable(an, "one-time", body, 0)
	if got.CauseMatched {
		t.Errorf("the driver's own words appear in the body but are never named as "+
			"the cause, and the score still read matched (named cause was %q)", got.NamedCause)
	}
	if got.NamedCause == "" {
		t.Fatal("this fixture's own \"cause:\" sentence was not extracted at all, so " +
			"this test cannot tell a correct scorer from a substring-matching one")
	}
}

// ------------------------------------------------------------ trueKindFor

func TestTrueKindForReadsTheRegistryByLabel(t *testing.T) {
	drivers := []world.Driver{
		{Start: "2026-06-22", End: "2026-06-22", Scope: "GKE", Label: "Quarterly model refresh, planned", Kind: "one-time"},
		{Start: "2025-06-01", End: "2026-08-15", Scope: "GPU training cluster", Label: "Scheduled weekly training window", Kind: "recurring"},
	}
	if kind, ok := trueKindFor(drivers, "Quarterly model refresh, planned"); !ok || kind != "one-time" {
		t.Errorf("trueKindFor = %q, %v, want one-time, true", kind, ok)
	}
	if kind, ok := trueKindFor(drivers, "Scheduled weekly training window"); !ok || kind != "recurring" {
		t.Errorf("trueKindFor = %q, %v, want recurring, true", kind, ok)
	}
	if _, ok := trueKindFor(drivers, "no such label"); ok {
		t.Error("trueKindFor found a label that is not in the registry")
	}
}

// -------------------------------------------------------------- dayNamed

func TestDayNamedAcceptsISOAndALongForm(t *testing.T) {
	if !dayNamed("the move landed on 2026-06-22 and nowhere else", "2026-06-22") {
		t.Error("the ISO form was not recognised")
	}
	if !dayNamed("It happened on June 22, 2026, during the batch window.", "2026-06-22") {
		t.Error("a natural long-form date was not recognised")
	}
	if dayNamed("nothing about a date here", "2026-06-22") {
		t.Error("no date at all scored as named")
	}
}

// -------------------------------------------------------------- kindRight

func TestKindRightReadsTheStructuredOptionClassFirst(t *testing.T) {
	body := "This prose incorrectly calls it recurring, but the option says otherwise.\n" +
		"```options\n{\"options\":[{\"class\":\"driver.one-time\",\"summary\":\"x\"}]}\n```\n"
	opts, _, _ := crew.ParseOptions(body)
	if !kindRight(body, opts, "one-time") {
		t.Error("kindRight did not prefer the structured driver.one-time option over the prose")
	}
	if kindRight(body, opts, "recurring") {
		t.Error("kindRight matched the prose's wrong word instead of the structured option")
	}
}

func TestKindRightFallsBackToProseWithNoOptionClass(t *testing.T) {
	if !kindRight("this was a one-time event, not a rhythm", nil, "one-time") {
		t.Error("the prose named the right kind and was not credited")
	}
	if kindRight("this was a one-time event", nil, "recurring") {
		t.Error("the prose did not name recurring and was credited anyway")
	}
}
