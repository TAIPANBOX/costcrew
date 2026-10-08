package plainname

import (
	"strings"
	"testing"
)

// The rule tested once, directly. The FOCUS reader's
// TestXUnitIsRefusedUnlessItIsAPlainBoundedName and crew's
// TestUnitRuleTargetHostileInputs hold what each caller adds and the wording
// each one's page shows; this holds the rule they share.
func TestCheckRefusesEveryShapeANamePrintedElsewhereMustNotHave(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
	}{
		{"a plain slug", "acme-prod", ""},
		{"spaces and accents", "Équipe Données", ""},
		{"exactly the bound", strings.Repeat("a", UnitMaxBytes), ""},
		{"a formula character inside, not first", "a=b", ""},
		{"empty, which the caller decides about", "", ""},
		{"one byte over", strings.Repeat("a", UnitMaxBytes+1), "129 bytes, over the 128 byte limit"},
		{"not valid text", "acme\xff", "is not valid text"},
		{"a newline", "acme\nprod", "control, format or separator"},
		{"a NUL", "acme\x00", "control, format or separator"},
		{"a zero-width space", "acme​prod", "control, format or separator"},
		{"a right-to-left override", "‮acme", "control, format or separator"},
		{"a line separator", "acme prod", "control, format or separator"},
		{"a paragraph separator", "acme prod", "control, format or separator"},
		{"=", "=SUM(A1)", "opens as a formula"},
		{"+", "+1", "opens as a formula"},
		{"-", "-1", "opens as a formula"},
		{"@", "@acme", "opens as a formula"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Check("the name", c.in, UnitMaxBytes)
			if c.want == "" {
				if got != "" {
					t.Errorf("Check(%q) = %q, want it accepted", c.in, got)
				}
				return
			}
			if !strings.Contains(got, c.want) || !strings.HasPrefix(got, "the name ") {
				t.Errorf("Check(%q) = %q, want a reason beginning with the label and saying %q", c.in, got, c.want)
			}
		})
	}
}

func TestCheckHoldsTheBoundItIsGiven(t *testing.T) {
	if got := Check("label", strings.Repeat("a", 81), 80); !strings.Contains(got, "over the 80 byte limit") {
		t.Errorf("an 81-byte name against a bound of 80: %q", got)
	}
	if got := Check("label", strings.Repeat("a", 80), 80); got != "" {
		t.Errorf("an 80-byte name against a bound of 80: %q", got)
	}
}
