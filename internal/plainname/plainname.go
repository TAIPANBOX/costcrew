// Package plainname is the one rule for a name this console prints after
// reading it from somewhere it does not control (invariant 86).
//
// A customer unit's name arrives in a file a gateway wrote (x_unit), becomes
// charges.team, and is then listed and linked on /chargeback and /allocation,
// carried into a CSV a spreadsheet opens and named in a statement a team
// reads. Two places judge it: the FOCUS reader, before the row is kept, and
// crew.ParseUnitTarget, before a rule for the unit can be stamped. Each used to
// carry its own copy of this check, and two copies of a rule is how a reader
// comes to accept a name the rule then refuses. Both call Check.
package plainname

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// UnitMaxBytes bounds a unit name. A gateway's unit is a short slug; the bound
// is generous and exists so a hostile file cannot make a name that fills a
// statement.
const UnitMaxBytes = 128

// Check says what is wrong with s as a printed name, or "" when nothing is:
// at most max bytes, valid text, no control, format (zero-width, text
// direction) or line-separating character, and not beginning with a character
// a spreadsheet reads as a formula. label begins every reason, so each caller
// names the field the way its own page does.
//
// Whether an empty or padded name is allowed is the caller's to decide before
// calling: a FOCUS row with no unit is a row nobody attributed, and a rule for
// a unit must name one.
func Check(label, s string, max int) string {
	if len(s) > max {
		return fmt.Sprintf("%s is %d bytes, over the %d byte limit", label, len(s), max)
	}
	if !utf8.ValidString(s) {
		return fmt.Sprintf("%s is not valid text", label)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return fmt.Sprintf("%s %q carries a control, format or separator character", label, s)
		}
	}
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return fmt.Sprintf("%s %q begins with %q, which a spreadsheet opens as a formula", label, s, s[:1])
	}
	return ""
}
