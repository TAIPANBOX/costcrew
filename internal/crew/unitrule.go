package crew

// The unit-keyed shape of allocation.rule's target (costcrew#74).
//
// A customer unit is the Team the TokenFuse FOCUS reader wrote from a row's
// x_unit. Its name therefore comes out of a file somebody else produced and
// travels on into a CSV a spreadsheet opens and a statement a team reads, so
// a rule is only ever written for a name this console would put there, by a
// person's stamp, and only for a unit the reader actually wrote rows for.
//
// The checks live here, in one place, because two callers need the very same
// answer: ValidateAndSaveOptions (so an analyst's proposal that cannot be
// stamped is returned to the analyst rather than carried to an owner) and
// finops' apply (so a stamp on an option that was valid when written but is
// not any more, or that bypassed the save-time gate, changes nothing). Two
// copies of these rules is how a save-time gate and an apply-time one come
// to disagree about the same name.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

const (
	// UnitMaxBytes bounds a unit name. A gateway's unit is a short slug; the
	// bound is generous and exists so a hostile file cannot make a name that
	// fills a statement.
	UnitMaxBytes = 128
	// BusinessUnitMaxBytes bounds the label a rule gives a unit.
	BusinessUnitMaxBytes = 80

	// TokenFuseProvenance is the charges.provenance the FOCUS reader writes.
	// A unit exists, for this purpose, only where that reader wrote it.
	TokenFuseProvenance = "tokenfuse-focus"
)

// UnitTarget is the unit-keyed target of an allocation.rule option: the unit
// as the reader wrote it, and the business unit its spend is charged back
// under.
type UnitTarget struct {
	Unit         string
	BusinessUnit string
}

// ParseUnitTarget reads an allocation.rule target as the unit-keyed shape.
//
// isUnit is false, with no reason, for every target that is not a JSON
// object carrying a "unit" key: the rule-id shape (and a missing or
// malformed target) stays validateAllocationRuleTarget's to judge, exactly
// as before. isUnit is true once "unit" is present, and reason is then
// non-empty if the target is not a well-formed unit rule: any other key than
// the two, a key of the rule-id shape beside it, a name or label that is
// empty, padded, over its bound, not valid text, carries a control, format
// or separator character, or begins with a character a spreadsheet reads as
// a formula.
func ParseUnitTarget(raw json.RawMessage) (t UnitTarget, isUnit bool, reason string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return UnitTarget{}, false, ""
	}
	unitRaw, has := obj["unit"]
	if !has {
		return UnitTarget{}, false, ""
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys) // one answer for one input, not map order
	for _, k := range keys {
		switch k {
		case "unit", "business_unit":
		case "rule_id", "method", "share":
			return UnitTarget{}, true, "allocation.rule's target names both a unit and a rule " +
				"(" + k + "): a rule is keyed by one or the other, never both"
		default:
			return UnitTarget{}, true, fmt.Sprintf("allocation.rule's unit target has a field "+
				"%q that nothing reads; a unit rule is exactly {\"unit\", \"business_unit\"}", k)
		}
	}
	if err := json.Unmarshal(unitRaw, &t.Unit); err != nil {
		return UnitTarget{}, true, "allocation.rule's target.unit is not a string"
	}
	if r := namePlain("target.unit", t.Unit, UnitMaxBytes); r != "" {
		return UnitTarget{}, true, r
	}
	buRaw, has := obj["business_unit"]
	if !has {
		return UnitTarget{}, true, "allocation.rule's unit target names no business_unit: " +
			`{"unit": ..., "business_unit": ...}`
	}
	if err := json.Unmarshal(buRaw, &t.BusinessUnit); err != nil {
		return UnitTarget{}, true, "allocation.rule's target.business_unit is not a string"
	}
	if r := namePlain("target.business_unit", t.BusinessUnit, BusinessUnitMaxBytes); r != "" {
		return UnitTarget{}, true, r
	}
	return t, true, ""
}

// namePlain is the one rule for a name this console prints: present, exactly
// as typed (no padding to disagree with the reader's own trimmed value),
// valid text within its bound, no control, format (zero-width, text
// direction) or line-separating character, and not starting with a
// character that makes a spreadsheet read the cell as a formula.
func namePlain(label, s string, max int) string {
	if s == "" || strings.TrimSpace(s) != s {
		return fmt.Sprintf("allocation.rule's %s %q is empty or padded with whitespace", label, s)
	}
	if len(s) > max {
		return fmt.Sprintf("allocation.rule's %s is %d bytes, over the %d byte limit", label, len(s), max)
	}
	if !utf8.ValidString(s) {
		return fmt.Sprintf("allocation.rule's %s is not valid text", label)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return fmt.Sprintf("allocation.rule's %s %q carries a control, format or "+
				"separator character", label, s)
		}
	}
	if strings.ContainsRune("=+-@", rune(s[0])) {
		return fmt.Sprintf("allocation.rule's %s %q begins with %q, which a spreadsheet "+
			"opens as a formula", label, s, s[:1])
	}
	return ""
}

// UnitRuleRefusal is the part of the answer that needs the store: whether
// this unit is one a rule may be written for. Empty means it is. The unit
// must have at least one row the TokenFuse reader wrote (a generated row, or
// no row at all, is not a unit anybody sent), and must not be a roster team,
// whose name the showback already gives to the roster's own row.
func UnitRuleRefusal(db *sql.DB, unit string) (string, error) {
	for _, tm := range world.Teams {
		if tm.Name == unit {
			return fmt.Sprintf("%q is a roster team, not a customer unit: its spend is "+
				"already that team's own row", unit), nil
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges WHERE team=? AND provenance=?`,
		unit, TokenFuseProvenance).Scan(&n); err != nil {
		return "", fmt.Errorf("looking for the rows of unit %q: %w", unit, err)
	}
	if n == 0 {
		return fmt.Sprintf("no charge row written by the TokenFuse reader carries the unit %q, "+
			"so there is nothing to charge back under a rule", unit), nil
	}
	return "", nil
}
