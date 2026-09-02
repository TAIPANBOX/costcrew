package main

// The prompt packet's job-description block.
//
// prompt() in live.go makes one call into this file, right after it writes
// the mission ("Your brief: ..."): B1A-SPEC.md section 2.2 asks for "a block
// 'Your job description' with the same fields in the same words" as the
// card (internal/web/analyst.go), and "You may decide alone: ...; you hand
// to the supervisor: ...; you never: ...". Both read internal/crew/roles.go,
// which is the one source; nothing here is typed independently of it.

import (
	"fmt"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// neverAnnotations pairs a never-verb with the decision class it is this
// practice's word for, where section 1 of ROLES-2026-09.md defines one that
// matches it exactly: "purchase" changes is "money committed to a provider
// or vendor", which is what "commit money" means. Annotating the never line
// with the class ids is why an analyst reading its own prompt sees the same
// vocabulary MayDecide and Escalates enforce, rather than a synonym of it,
// and it is why TestThePromptCarriesTheJobDescription finds "purchase" under
// "never" as well as under "hands up".
var neverAnnotations = map[string]string{
	"commit money":      "purchase",
	"change a resource": "infra.change",
	"contact a vendor":  "vendor.negotiate",
	"send a message to a person outside the crew": "message.team",
}

// annotatedNever is crew.NeverFullText() with each mapped verb followed by
// its class id in parentheses. A plain string replacement, not a rebuild
// from crew.Never(), so a change to the sentence in roles.yaml (the sixth
// clause included) is carried here without this file also needing an edit.
func annotatedNever() string {
	s := crew.NeverFullText()
	for verb, class := range neverAnnotations {
		s = strings.Replace(s, verb, verb+" ("+class+")", 1)
	}
	return s
}

// jobDescriptionBlock is "Your job description": the fields roles.yaml
// carries for this role, in the same words the card shows, closed by the
// three sentences MayDecide and Escalates are built to hold -- what it may
// decide alone, what it hands up, and what it never does.
//
// Empty when name/desk match no role family (a hire made by hand, before a
// family existed for it): the block is additive, so an analyst with nothing
// on file gets no block rather than a misleading one, and prompt()'s bound
// (main.go's tokens(prompt(...))) still covers exactly what was sent because
// it counts prompt()'s own output, this block included, byte for byte.
func jobDescriptionBlock(name, desk string) string {
	r, ok := crew.RoleForDesk(name, desk)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nYour job description\n")
	if r.Reads != "" {
		fmt.Fprintf(&b, "Reads: %s\n", r.Reads)
	}
	if r.Cadence != "" || r.Audience != "" {
		fmt.Fprintf(&b, "Reports: %s to %s\n", r.Cadence, r.Audience)
	}
	if r.Owes != "" {
		fmt.Fprintf(&b, "Owes: %s\n", r.Owes)
	}
	if len(r.DecidesAlone) > 0 {
		fmt.Fprintf(&b, "You may decide alone: %s (%s).\n",
			strings.Join(r.DecidesAlone, ", "), r.DecidesAloneText)
	}
	if len(r.HandsUp) > 0 {
		fmt.Fprintf(&b, "You hand to the supervisor: %s (%s).\n",
			strings.Join(r.HandsUp, ", "), r.HandsUpText)
	}
	if len(r.HandsToOwner) > 0 {
		fmt.Fprintf(&b, "You hand to the owner: %s (%s).\n",
			strings.Join(r.HandsToOwner, ", "), r.HandsToOwnerText)
	}
	fmt.Fprintf(&b, "You never: %s\n", annotatedNever())
	if r.QualityBar != "" {
		fmt.Fprintf(&b, "Quality bar: %s\n", r.QualityBar)
	}
	return b.String()
}
