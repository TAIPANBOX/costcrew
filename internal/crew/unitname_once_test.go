package crew

// A unit's name is held to one rule in two places: the FOCUS reader, which
// writes x_unit into charges.team, and ParseUnitTarget, which judges a rule a
// person may stamp for that unit. Until 2026-10-08 each carried its own copy,
// so a change to one (a wider bound, one more formula character) would let a
// name in through the reader that the rule then refused, or the reverse.
// This reads the module's non-test source and requires the rule's two
// distinctive lines, the formula-prefix check and the format-character
// check that sits beside it, to be written in exactly one file.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTheUnitNameRuleIsWrittenOnce(t *testing.T) {
	root := filepath.Join("..", "..")
	var holders []string
	for _, dir := range []string{"internal", "tools", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), `strings.ContainsRune("=+-@", rune(s[0]))`) {
				rel, _ := filepath.Rel(root, p)
				holders = append(holders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(holders)
	if len(holders) != 1 || holders[0] != filepath.Join("internal", "plainname", "plainname.go") {
		t.Errorf("the plain-name rule is written in %d files %v, want once, in internal/plainname/plainname.go, "+
			"called by the FOCUS reader and by ParseUnitTarget", len(holders), holders)
	}
}
