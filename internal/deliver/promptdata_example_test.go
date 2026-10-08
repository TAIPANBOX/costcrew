package deliver

// The mode line every masked or aggregates prompt carries shows the model what
// a pseudonym looks like. Until 2026-10-08 its example was team-7f3a, which has
// a real token's shape (team- and four hex digits), so a real team could draw
// it, one name in 65,536. Measured on a live run on 2026-10-07: a model copied
// the example into an aggregates draft, where it stayed as a stray token; had a
// real team held that token, the draft would have been re-identified as naming
// that team. The example is now a shape no token can take, and Reidentify
// never reads it as one.

import (
	"regexp"
	"strings"
	"testing"
)

var modeLineExampleRe = regexp.MustCompile(`such as ([A-Za-z0-9-]+)`)

func modeLineExample(t *testing.T, mode PromptData) string {
	t.Helper()
	p, err := NewPolicy(mode, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := modeLineExampleRe.FindStringSubmatch(p.ModeLine())
	if m == nil {
		t.Fatalf("the %s mode line shows no example token: %q", mode, p.ModeLine())
	}
	return m[1]
}

func TestTheModeLineExampleHasAShapeNoRealTokenCanTake(t *testing.T) {
	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		ex := modeLineExample(t, mode)
		if tokenRe.MatchString(ex) {
			t.Errorf("%s: the mode line's example %q has a real token's shape, so a real name can draw it "+
				"and a model that copies the example names that team", mode, ex)
		}
		if !strings.HasPrefix(ex, "team-") {
			t.Errorf("%s: the example %q no longer says what kind of thing a token names", mode, ex)
		}
	}
}

// Even if a real team had drawn the very string the example shows (planted
// here by hand, since the HMAC cannot be steered to it), a draft that repeats
// the example keeps it as written: the example is never a name.
func TestReidentifyNeverReadsTheModeLineExampleAsAName(t *testing.T) {
	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		ex := modeLineExample(t, mode)
		p, err := NewPolicy(mode, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		real := p.maskWith("ml-platform", []entry{{kindTeam, "ml-platform"}}, nil)
		p.rev[strings.ToLower(ex)] = "data-eng"
		in := "Pseudonyms look like " + ex + "; the spend that moved was " + real + "'s."
		want := "Pseudonyms look like " + ex + "; the spend that moved was ml-platform's."
		if got := p.Reidentify(in); got != want {
			t.Errorf("%s: Reidentify(%q)\n = %q\nwant %q: the example was read as a team's token", mode, in, got, want)
		}
	}
}
