package manifest_test

// A document this repository NAMES is a document a reader can reach.
//
// CLAUDE.md cites 21 distinct `*-SPEC.md` files by bare filename, and until
// 2026-09-03 not one of them was in any repository: they sat in one directory
// on their author's machine, so every citation led nowhere for anybody else.
// Nothing caught it, because a filename in prose is not a link and no gate
// had ever looked at prose.
//
// This is the gate that replaced that silence. It is deliberately narrow: it
// checks that a name is REACHABLE, never that the thing behind it says what
// the sentence claims, which nothing mechanical can. Two kinds of name pass,
// and the second exists because the specifications are genuinely elsewhere
// and private (CLAUDE.md's own "The specifications this file names are not in
// this repository" section is where that is explained to a reader). A third
// kind, a name that is neither in the tree nor a specification, is the rot
// this exists to catch: a doc renamed, moved or deleted while the prose that
// points at it stayed behind.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Sources scanned. Both are prose a person is told to read, and both cite
// documents by name. Go comments are deliberately NOT scanned: they cite the
// same specifications hundreds of times over, which would say nothing this
// does not already say, and the failure would name a file rather than the
// sentence that has gone stale.
var documentSources = []string{"CLAUDE.md", "README.md"}

// A Markdown filename as it appears in prose: bare, in backticks, or with a
// directory in front of it. Deliberately not a URL matcher; a link to
// somebody else's repository is not this gate's business.
var markdownName = regexp.MustCompile(`[A-Za-z0-9_./-]+\.md`)

// specElsewhere is the one exemption, and it is a PATTERN rather than a list
// of 21 names on purpose: a list would have to be edited every time a
// specification is written, which is exactly the kind of upkeep that rots.
// CLAUDE.md's own section says where these live and why they are not here.
func specElsewhere(name string) bool { return strings.HasSuffix(name, "-SPEC.md") }

func TestEveryDocumentThisRepositoryNamesCanBeFound(t *testing.T) {
	r := root(t)

	seen := map[string][]string{} // name -> the sources that cite it
	for _, src := range documentSources {
		raw, err := os.ReadFile(filepath.Join(r, src))
		if err != nil {
			t.Fatalf("%s: %v (this gate cannot judge a source it cannot read)", src, err)
		}
		for _, name := range markdownName.FindAllString(string(raw), -1) {
			seen[name] = append(seen[name], src)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no document name was found in any source, so this gate measured nothing")
	}

	var dangling []string
	for name, srcs := range seen {
		if specElsewhere(name) {
			continue
		}
		if _, err := os.Stat(filepath.Join(r, filepath.FromSlash(name))); err == nil {
			continue
		}
		dangling = append(dangling, name+" (named in "+strings.Join(srcs, ", ")+")")
	}
	sort.Strings(dangling)
	for _, d := range dangling {
		t.Errorf("this repository names a document nobody can open: %s. Either add "+
			"the file, or fix the sentence that points at it. A specification "+
			"belongs in TAIPANBOX/go-to-market-2026-09 and is named *-SPEC.md, "+
			"which this gate already allows", d)
	}
}

// The exemption is not a hole: a specification is exempt from EXISTING here,
// and never from being explained. This holds the explanation itself, so the
// section a reader is sent to cannot quietly disappear while the exemption
// stays behind and 21 bare filenames go back to meaning nothing.
func TestTheSpecificationsAreGivenAnAddress(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root(t), "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"The specifications this file names are not in this repository",
		"TAIPANBOX/go-to-market-2026-09",
		"private",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("CLAUDE.md no longer says %q, so the *-SPEC.md exemption in "+
				"TestEveryDocumentThisRepositoryNamesCanBeFound now excuses 21 "+
				"filenames that point nowhere a reader is told about", want)
		}
	}
}
