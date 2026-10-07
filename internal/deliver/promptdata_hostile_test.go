package deliver

// Hostile input for the policy: the model's answer is bytes from outside the
// process, and the names in the store are data that anybody who can write a
// team, a service or an invoice id chose.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

func TestReidentifyingAHostileAnswerNeitherPanicsNorGrows(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	tok := p.maskWith("ml-platform", []entry{{kindTeam, "ml-platform"}}, nil)

	cases := map[string]string{
		"invalid utf-8":       "\xff\xfe" + tok + "\xc3",
		"nul bytes":           tok + "\x00" + tok,
		"zero-width joiners":  "\u200b" + tok + "\u200d",
		"combining marks":     tok + "́",
		"a token glued left":  "x" + tok,
		"a token glued right": tok + "z",
		"one more hex digit":  tok + "a",
		"empty":               "",
		"just a dash":         "team-",
	}
	for name, in := range cases {
		got := p.Reidentify(in)
		if len(got) > len(in)+len("ml-platform")*3 {
			t.Errorf("%s: %d bytes in, %d out", name, len(in), len(got))
		}
	}
	if got := p.Reidentify("x" + tok); got != "x"+tok {
		t.Errorf("a token glued to a letter was replaced: %q", got)
	}
	if got := p.Reidentify(tok + "a"); got != tok+"a" {
		t.Errorf("a token with one more hex digit was replaced: %q", got)
	}
	if got := p.Reidentify("\xff" + tok); got != "\xffml-platform" {
		t.Errorf("a token after an invalid byte was not recognised: %q", got)
	}

	// two megabytes of tokens and noise: linear, not quadratic
	big := strings.Repeat(tok+" and some words, ", 100_000)
	back := p.Reidentify(big)
	if strings.Count(back, "ml-platform") != 100_000 {
		t.Errorf("a large answer lost or gained names: %d", strings.Count(back, "ml-platform"))
	}
	if strings.Contains(back, tok) {
		t.Error("a large answer still holds a token")
	}
}

// A name that looks like a token must not be turned into a different name by a
// second re-identification. There is only ever one, and this holds why.
func TestANameShapedLikeATokenSurvivesTheRoundTrip(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	// some real token, team-xxxx, then a REAL team whose name is that token
	lookalike := p.maskWith("other-team", []entry{{kindTeam, "other-team"}}, nil)
	es := []entry{{kindTeam, lookalike}}
	masked := p.maskWith("the "+lookalike+" team", es, nil)
	if masked == "the "+lookalike+" team" {
		t.Fatal("a team named like a token was not masked")
	}
	if got := p.Reidentify(masked); got != "the "+lookalike+" team" {
		t.Errorf("the round trip changed a name shaped like a token: %q", got)
	}
}

func TestANameStoredWithSpacesRoundItIsStillMasked(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindTeam, "  ml-platform \t"}, {kindService, "\nAmazon EC2\n"}}
	got := p.maskWith("ml-platform spent on Amazon EC2", es, nil)
	if strings.Contains(got, "ml-platform") || strings.Contains(got, "Amazon") {
		t.Errorf("a name stored with whitespace round it was not masked: %q", got)
	}
}

// If the names cannot be read, nothing is sent rather than text with names in it.
func TestAnUnreadableStoreSendsNothingRatherThanAnUnmaskedText(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	st.Close()
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	p.Bind(db)
	got := p.MaskText("ml-platform spent more")
	if got != maskFailed || strings.Contains(got, "ml-platform") {
		t.Errorf("a policy that could not read the store returned %q", got)
	}
}

func TestMaskingHostileDataNeitherPanicsNorHidesTheRestOfTheText(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{
		{kindTeam, "\xff\xfe broken"}, {kindTeam, "emoji \U0001F680 team"}, {kindService, "line\nbreak"},
		{kindService, strings.Repeat("long ", 10_000)}, {kindUser, "quote\"s and \\ back"},
		{kindTeam, "ml-platform"},
	}
	text := "before \xff\xfe broken, emoji \U0001F680 team; line\nbreak; quote\"s and \\ back; ml-platform; after"
	got := p.maskWith(text, es, nil)
	for _, name := range []string{"emoji", "line\nbreak", "quote\"s", "ml-platform"} {
		if strings.Contains(got, name) {
			t.Errorf("%q survived: %q", name, got)
		}
	}
	if !strings.HasPrefix(got, "before ") || !strings.HasSuffix(got, "; after") {
		t.Errorf("the text around the names was damaged: %q", got)
	}
}
