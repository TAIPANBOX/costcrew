package manifest_test

// No tracked file quotes the owner or attributes a decision to him by name.
//
// This repository is public. A decision the owner made is still recorded,
// because a later reader has to know it is a decision and not something to
// re-derive: it is written as `@decided YYYY-MM-DD` followed by a paraphrase
// in this repository's own words, and never edited afterwards. What is not
// written is his own wording, or his name as the one who said, asked or
// decided something. Until 2026-10-08 seventy-two markers carrying his first
// name and several dozen Ukrainian sentences in his words sat in 54 tracked
// files, scenario headers, Go comments, the role file and five frozen parity
// captures among them, and nothing would have stopped the next one.
//
// Four shapes are refused, each on the line it occurs on:
//
//  1. the old provenance marker, an at-sign followed by his first name, in
//     any case;
//  2. a guillemet on a line that also carries Cyrillic, the shape his quotes
//     took in the comments that held them;
//  3. Cyrillic in prose: anywhere in a non-Go text file, and inside a
//     COMMENT of a Go file. In this repository Ukrainian prose has only ever
//     been his words. A Go string literal may still carry Cyrillic, because
//     hostile-input tests need non-Latin text, and so may a file under a
//     testdata/ directory;
//  4. his first name capitalised as a word, outside an authorship line (one
//     that says copyright, author or maintainer): the shape of "he found",
//     "his call", "he clicked", which a list of verbs would never finish.
//
// URLs are blanked before 1, 2 and 4 run, the same way the document gate
// beside this file blanks them: the name inside somebody's address is an
// address, not an attribution. A lower-case username in test data (an owner
// account called by his first name) is neither a quote nor an attribution
// and passes.
//
// What this does NOT catch: an English quote of his words in ordinary
// quotation marks, an attribution that does not use his name ("the owner
// said"), and a paraphrase that is in fact a translation. Those are prose
// a reader judges; nothing mechanical can. It also reads only what git
// tracks, so an untracked file is invisible to it until it is added.

import (
	"bytes"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The name is assembled rather than written, so this file can describe the
// rule without being one of the files the rule refuses.
const ownerFirst = "Y" + "urii"

var (
	ownerMarker    = regexp.MustCompile(`(?i)@` + strings.ToLower(ownerFirst) + `\b`)
	ownerName      = regexp.MustCompile(`\b` + ownerFirst + `\b`)
	authorshipLine = regexp.MustCompile(`(?i)copyright|\x{00A9}|\(c\)|\bauthors?\b|\bmaintainers?\b|\bmaintained by\b`)
	guillemet      = regexp.MustCompile(`[\x{00AB}\x{00BB}]`)
	cyrillic       = regexp.MustCompile(`\p{Cyrillic}`)
)

// ownerQuoteFindings returns one line per offence in content, read as the
// file at path. It is the whole of the rule; the test below only feeds it
// every tracked file.
func ownerQuoteFindings(path string, content []byte) []string {
	var out []string
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		plain := urlSpan.ReplaceAllString(line, " ")
		at := fmt.Sprintf("%s:%d", path, i+1)
		if ownerMarker.MatchString(plain) {
			out = append(out, at+": the old provenance marker carrying the owner's name; write @decided and a paraphrase")
		}
		if guillemet.MatchString(plain) && cyrillic.MatchString(plain) {
			out = append(out, at+": a quotation in guillemets, in the owner's own language")
		}
		if ownerName.MatchString(plain) && !authorshipLine.MatchString(plain) {
			out = append(out, at+": the owner named as the one who said, asked, decided or found something")
		}
	}
	if strings.Contains(filepath.ToSlash(path), "testdata/") {
		return out
	}
	if strings.HasSuffix(path, ".go") {
		for _, l := range cyrillicInGoComments(content) {
			out = append(out, fmt.Sprintf("%s:%d: Ukrainian prose in a comment; a decision is paraphrased in English", path, l))
		}
		return out
	}
	for i, line := range lines {
		if cyrillic.MatchString(line) {
			out = append(out, fmt.Sprintf("%s:%d: Ukrainian prose; a decision is paraphrased in English", path, i+1))
		}
	}
	return out
}

// cyrillicInGoComments reads a Go file with the standard library's own
// scanner, so a comment is a comment and a string literal is a string
// literal, not a guess from where `//` happens to sit on a line.
func cyrillicInGoComments(src []byte) []int {
	fset := token.NewFileSet()
	file := fset.AddFile("x.go", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) {}, scanner.ScanComments)
	var lines []int
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return lines
		}
		if tok != token.COMMENT {
			continue
		}
		for j, l := range strings.Split(lit, "\n") {
			if cyrillic.MatchString(l) {
				lines = append(lines, fset.Position(pos).Line+j)
			}
		}
	}
}

// binary is git's own heuristic: a NUL in the first 8000 bytes. The two
// diagrams under docs/ hold guillemet BYTES by chance and no text at all.
func binary(content []byte) bool {
	head := content
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

func TestNoTrackedFileQuotesOrAttributesTheOwner(t *testing.T) {
	r := root(t)
	ls := exec.Command("git", "ls-files", "-z")
	ls.Dir = r
	raw, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v; with no list of tracked files this gate measured nothing", err)
	}
	var scanned, features, goFiles int
	var findings []string
	for _, p := range strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(r, p))
		if err != nil {
			continue // tracked but deleted in the working tree
		}
		if binary(content) {
			continue
		}
		scanned++
		if strings.HasSuffix(p, ".feature") {
			features++
		}
		if strings.HasSuffix(p, ".go") {
			goFiles++
		}
		findings = append(findings, ownerQuoteFindings(p, content)...)
	}
	// A walk that read nothing reports nothing, which is the silent pass
	// every gate here is held against.
	if scanned < 100 || features == 0 || goFiles == 0 {
		t.Fatalf("measured nothing: %d text files, %d feature files, %d Go files", scanned, features, goFiles)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

// The real tree is clean, so without these the gate could never be seen to
// go red, and a regression in the rule would pass the test above by seeing
// nothing. Every string that would trip the rule is assembled, for the same
// reason as ownerFirst.
func TestTheOwnerQuoteWalkSeesEveryShapeItNames(t *testing.T) {
	marker := "@" + strings.ToLower(ownerFirst)
	cases := []struct{ name, path, text string }{
		{"the marker in a scenario header", "features/x.feature", "  " + marker + " 2026-09-02\n"},
		{"the marker capitalised", "CLAUDE.md", "`@" + ownerFirst + " 2026-09-02`: a decision\n"},
		{"the marker in a Go comment", "internal/x/x.go", "package x\n\n// " + marker + " 2026-09-02: words\n"},
		{"the marker in a Go string", "internal/x/x.go", "package x\n\nvar s = \"" + marker + "\"\n"},
		{"the marker beside a URL, not in it", "README.md", "see https://example.com/notes " + marker + "\n"},
		{"guillemets around Cyrillic in a Go string", "internal/x/x.go", "package x\n\nvar s = \"\u00abслово\u00bb\"\n"},
		{"a closing guillemet ending a quote begun on the line above", "internal/x/x.go", "package x\n\n// a\n// слово\u00bb\n"},
		{"Cyrillic in a Go line comment", "internal/x/x.go", "package x\n\n// лишай як є\n"},
		{"Cyrillic in a Go block comment", "internal/x/x.go", "package x\n\n/*\n лишай як є\n*/\n"},
		{"Cyrillic after code on the same line", "internal/x/x.go", "package x\n\nvar n = 1 // лишай\n"},
		{"Cyrillic in a scenario's docstring", "features/x.feature", "  \"\"\"\n  більш повною мірою\n  \"\"\"\n"},
		{"Cyrillic in a CSS comment", "internal/web/assets/app.css", "/* лишай як є */\n"},
		{"Cyrillic in a shell comment", "scripts/x.sh", "# геркін-тести\n"},
		{"the name as the one who found something", "internal/x/x.go", "package x\n\n// Found by " + ownerFirst + " reading the code.\n"},
		{"the name as the one who asked", "features/x.feature", "# Every scenario comes from something " + ownerFirst + " asked for\n"},
		{"the name's possessive", "internal/x/x.go", "package x\n\n// invariant 19 is " + ownerFirst + "'s call\n"},
	}
	for _, c := range cases {
		if got := ownerQuoteFindings(c.path, []byte(c.text)); len(got) == 0 {
			t.Errorf("%s: not refused\n%s", c.name, c.text)
		}
	}
}

func TestTheOwnerQuoteWalkLeavesTheseAlone(t *testing.T) {
	cases := []struct{ name, path, text string }{
		{"the name as copyright holder", "LICENSE", "Copyright 2026 " + ownerFirst + " Kostiuk\n"},
		{"the name as copyright holder, with the sign", "NOTICE", "\u00a9 2026 " + ownerFirst + " Kostiuk\n"},
		{"the name as author", "README.md", "Author: " + ownerFirst + " Kostiuk\n"},
		{"the name in a URL", "README.md", "https://github.com/" + ownerFirst + "-K/costcrew\n"},
		{"the marker shape inside a URL", "README.md", "<https://example.com/@" + strings.ToLower(ownerFirst) + "/notes>\n"},
		{"a lower-case username in test data", "internal/x/x_test.go", "package x\n\nvar owner = \"" + strings.ToLower(ownerFirst) + "\"\n"},
		{"Cyrillic as hostile input in a Go string", "internal/x/x_test.go", "package x\n\nvar team = \"Київ east\"\n"},
		{"Cyrillic in a raw Go string", "internal/x/x_test.go", "package x\n\nvar q = `SELECT * FROM таблиця`\n"},
		{"Cyrillic in a testdata fixture", "internal/x/testdata/units.csv", "unit\nклієнт-1\n"},
		{"a paraphrased decision", "features/x.feature", "  @decided 2026-09-02\n  \"\"\"\n  An agent does not buy anything on its own.\n  \"\"\"\n"},
		{"guillemets around English", "docs/x.md", "the \u00abfigures\u00bb tab\n"},
		{"the owner named by role", "CLAUDE.md", "The owner halved the two money thresholds.\n"},
	}
	for _, c := range cases {
		if got := ownerQuoteFindings(c.path, []byte(c.text)); len(got) != 0 {
			t.Errorf("%s: refused\n%s\n%s", c.name, c.text, strings.Join(got, "\n"))
		}
	}
}
