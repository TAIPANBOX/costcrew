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
// somebody else's repository is not this gate's business, which is why the
// URLs are taken out of the prose before this runs (documentNames below).
var markdownName = regexp.MustCompile(`[A-Za-z0-9_./-]+\.md`)

// A Markdown link whose destination is a URL, text and destination together:
// the text may itself read like a filename (`[estate-gates/PROVEN.md](https://...)`),
// and a reader reaches that document through the link, so neither half is a
// name this repository owes.
var externalLink = regexp.MustCompile(`\[[^\]\n]*\]\(https?://[^)\s]*\)`)

// A bare URL, from its scheme to the first character that cannot be part of
// one: whitespace, or the closer of the angle-bracket, parenthesis or backtick
// form it sits in. The trailing period of a sentence is swallowed with it,
// which costs nothing here: nothing after the scheme is a name this repository
// owes.
var urlSpan = regexp.MustCompile("https?://[^\\s)>\\]\"'`]+")

// documentNames is every document a piece of prose names, as this gate reads
// it. A URL is not one: a link into another repository is that repository's
// business, and its path happens to satisfy markdownName's character class
// and to end in `.md`, so external links are blanked first, then bare URLs,
// and only then does the matcher run. What is left is judged as before: a
// bare name, a name in backticks, a relative Markdown link's own destination.
func documentNames(text string) []string {
	text = externalLink.ReplaceAllString(text, " ")
	text = urlSpan.ReplaceAllString(text, " ")
	return markdownName.FindAllString(text, -1)
}

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
		for _, name := range documentNames(string(raw)) {
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

// A link to somebody else's repository is not this gate's business, and the
// gate's own comment said so from the day it was written while the matcher
// read the raw bytes: `https://github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md`
// contains `//github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md`, which the
// character class accepts and which ends in `.md`, so a README that linked
// straight to a public record in another repository was refused as if it had
// cited a file that ought to be here. The first real citation of that shape
// (2026-09-18, the appliance write-up) had to be reworded to link to the
// repository instead of the file. A URL is skipped whole; a bare name beside
// it is still judged, and a relative Markdown link is a name like any other.
func TestALinkToAnotherRepositorysDocumentIsNotThisGatesBusiness(t *testing.T) {
	prose := "Full detail in " +
		"[estate-gates/PROVEN.md](https://github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md) " +
		"and <https://example.org/notes/README.md>, in `docs/stack-connection.md` here, " +
		"in DRIVER-WINDOW-SPEC.md, in http://example.org/a/b/CHANGELOG.md, and in " +
		"[the connection](docs/stack-connection.md)."
	got := documentNames(prose)
	want := []string{"docs/stack-connection.md", "DRIVER-WINDOW-SPEC.md", "docs/stack-connection.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("documentNames read the prose as naming %q, want %q: a URL's own path is "+
			"not a document this repository names, and everything beside it still is", got, want)
	}
	for _, name := range got {
		if strings.Contains(name, "github.com") || strings.Contains(name, "example.org") {
			t.Errorf("%q was reported as a document name; it is the tail of a URL", name)
		}
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
