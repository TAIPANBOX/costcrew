package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	caseRe = regexp.MustCompile(`(?m)^\tcase "([a-z]+)":\n((?:\t\t.*\n|\n)*)`)
	flagRe = regexp.MustCompile(`fs\.(?:String|Int|Bool)\("([^"]+)"`)
)

// The usage text is the only documentation a person running this has, and it
// said -max N for a flag that has been -per-family since the crawl was bounded
// per family: anybody who followed it got "flag provided but not defined".
// Every flag a subcommand defines is named on that subcommand's usage line,
// and no line names a flag nobody defines.
func TestTheUsageNamesEveryFlagEachSubcommandDefines(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(usageText, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "parity" {
			lines[f[1]] = l
		}
	}
	subs := caseRe.FindAllStringSubmatch(string(src), -1)
	if len(subs) < 4 {
		t.Fatalf("found %d subcommands in main.go; the scan is broken", len(subs))
	}
	for _, m := range subs {
		sub, body := m[1], m[2]
		line, ok := lines[sub]
		if !ok {
			t.Errorf("the usage has no line for %q", sub)
			continue
		}
		defined := map[string]bool{}
		for _, f := range flagRe.FindAllStringSubmatch(body, -1) {
			defined[f[1]] = true
			if !regexp.MustCompile(`(^|[\s\[])-` + regexp.QuoteMeta(f[1]) + `\b`).MatchString(line) {
				t.Errorf("parity %s defines -%s and its usage line does not name it: %q", sub, f[1], line)
			}
		}
		for _, named := range regexp.MustCompile(`[\s\[]-([a-z-]+)`).FindAllStringSubmatch(line, -1) {
			if !defined[named[1]] {
				t.Errorf("parity %s's usage names -%s, which it does not define: %q", sub, named[1], line)
			}
		}
	}
}

// Two fresh installs of the same binary have two journals, so the chain hash
// on /audit differs between them by construction. The scrub for it matched the
// Python original's markup (<td class="qid">) and nothing the Go console
// renders, so every Go-against-Go comparison reported /audit as a difference
// that was not one.
func TestTheJournalHashIsScrubbedInTheMarkupTheGoConsoleRenders(t *testing.T) {
	page := func(hash string) []byte {
		return []byte(`<tr>
        <td class="tight">07.10 12:00</td>
        <td><strong>login</strong></td>
        <td>parity</td>
        <td class="tight"><code>` + hash + `</code></td>
      </tr>`)
	}
	a := normalise(page("0123456789abcdef"))
	b := normalise(page("fedcba9876543210"))
	if string(a) != string(b) {
		t.Errorf("two chain hashes still differ after normalising:\n%s\n%s", a, b)
	}
	// Only the hash: a <code> cell that is not one stays as it was.
	other := []byte(`<td class="tight"><code>sql-readonly</code></td>`)
	if got := normalise(other); string(got) != string(other) {
		t.Errorf("a code cell that is not a hash was scrubbed: %s", got)
	}
	// And the old markup, which captures/golden still holds, is still scrubbed.
	old := normalise([]byte(`<td class="qid">0123456789abcdef</td>`))
	if strings.Contains(string(old), "0123456789abcdef") {
		t.Errorf("the original's markup is no longer scrubbed: %s", old)
	}
}
