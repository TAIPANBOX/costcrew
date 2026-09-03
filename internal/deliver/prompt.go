package deliver

// The prompt, and the job-description block inside it.
//
// Moved here from tools/run/live.go's prompt()/optionsBlockInstructions()
// and tools/run/mandate.go's jobDescriptionBlock()/annotatedNever(): see
// packet.go's package comment for why. tools/run keeps thin wrappers with
// the old unexported names at the old call sites, so nothing there had to
// change beyond one line each.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// Prompt is what the analyst is asked, built only from what the console
// holds.
//
// The task, and the brief the analyst was hired with. Nothing else: an
// analyst without figures-read is not handed figures, and this is where
// that rule is kept rather than hoped for.
//
// packetText is the TASK PACKET (packet.go), inserted here rather than
// built by this function: tools/run's estimator captures it ONCE, at price
// time, and carries it unchanged into execute()'s actual call, since the
// estate can move between pricing a run and executing it and a bound only
// true of a moment ago is not a bound. An empty packetText renders nothing,
// which is right both for a task with no figures section to show and for
// every existing caller that has never heard of a packet.
func Prompt(t crew.Task, a crew.Analyst, today, packetText string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, %s on the %s desk of a FinOps practice.\n", a.Name, a.Role, a.Desk)
	if a.Mission != "" {
		fmt.Fprintf(&b, "Your brief: %s\n", a.Mission)
	}
	b.WriteString(JobDescriptionBlock(a.Name, a.Desk))
	b.WriteString(packetText)
	fmt.Fprintf(&b, "\nThe task on your desk is %q.\n", t.Title)
	if t.Goal != "" {
		fmt.Fprintf(&b, "What it asks for: %s\n", t.Goal)
	}
	// The date, because it asked for one and got no answer.
	//
	// A live run produced "**Date:** [Today's Date]" on the face of a
	// deliverable a person was meant to read. A model has no clock, so the
	// choices are to give it the date or to have it guess; and this console's
	// whole argument is that a figure nobody can check is worse than no figure.
	fmt.Fprintf(&b, "\nToday is %s.\n", today)

	// The format, kept to what the console renders.
	//
	// The renderer is deliberately tiny and now covers headings, rules, lists,
	// bold and italic. Asking for a narrow format is cheaper than widening it
	// further, and the renderer holds either way: a model that ignores this
	// still has to come out readable.
	b.WriteString("Use plain prose with ## headings, **bold** and simple " +
		"- bullets. No tables, no code fences.\n")

	b.WriteString(optionsBlockInstructions(a.Name, a.Desk))

	b.WriteString("\nWrite the deliverable. Be specific, say what you do not know, " +
		"and do not invent a number you were not given.\n")
	return b.String()
}

// optionsBlockInstructions tells the model the one shape it must not use
// prose for: the options block, fenced and tagged, at the very end. The
// classes it may name come from the SAME job description JobDescriptionBlock
// already printed above ("You may decide alone" / "You hand to the
// supervisor") -- this repeats them as a closed list next to the JSON shape
// itself, from the same roles.yaml data, so the vocabulary the model sees has
// one source rather than two texts that could drift (B3-SPEC.md section 2:
// "the prompt tells the model the block's shape and the classes it may name,
// from the same roles data").
//
// Empty when the role matches no family, the same additive rule
// JobDescriptionBlock and Packet already hold: nothing here should tell a
// model to produce a shape this console cannot check.
func optionsBlockInstructions(name, desk string) string {
	r, ok := crew.RoleForDesk(name, desk)
	if !ok {
		return ""
	}
	legal := crew.ValidClassesFor(r)
	if len(legal) == 0 {
		if crew.AllowsNoOptions(r) {
			return "\nThis role's deliverable is prose; it needs no options block.\n"
		}
		return ""
	}
	classes := make([]string, 0, len(legal))
	for c := range legal {
		classes = append(classes, c)
	}
	sort.Strings(classes)

	var b strings.Builder
	b.WriteString("\nEnd the deliverable with a fenced block tagged options, JSON, " +
		"naming one to three courses of action -- never one you have already taken:\n")
	b.WriteString("```options\n")
	b.WriteString(`{"options": [{"class": "...", "summary": "...", "figure_cents": 0, ` +
		`"saving_cents": 0, "risk": "low|medium|high", "needs": "nothing|a person to ...", ` +
		"\"evidence\": [\"...\"]}]}\n")
	b.WriteString("```\n")
	fmt.Fprintf(&b, "class must be one of: %s. figure_cents and saving_cents are whole "+
		"numbers of cents, never a decimal. This deliverable proposes; it never applies "+
		"anything itself.\n", strings.Join(classes, ", "))
	if crew.AllowsNoOptions(r) {
		b.WriteString("Zero options is fine here if there is nothing to decide.\n")
	}
	return b.String()
}

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

// JobDescriptionBlock is "Your job description": the fields roles.yaml
// carries for this role, in the same words the card shows, closed by the
// three sentences MayDecide and Escalates are built to hold -- what it may
// decide alone, what it hands up, and what it never does.
//
// Empty when name/desk match no role family (a hire made by hand, before a
// family existed for it): the block is additive, so an analyst with nothing
// on file gets no block rather than a misleading one, and Prompt's bound
// (tools/run's tokens(Prompt(...))) still covers exactly what was sent
// because it counts Prompt's own output, this block included, byte for
// byte.
func JobDescriptionBlock(name, desk string) string {
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
