// The supervisor's one planning packet, prompt and price. B4-STEP-TWO-SPEC.md
// section 2. A NEW file, deliberately not packet.go: packet.go's own Packet
// is the TASK packet, gated on figures-read and built from one crew.Task;
// PlanPacket below is a different shape entirely -- the goal, the
// deterministic plan's own items, and the roster -- and packet.go is left
// exactly as it was, per the instruction this step was given.
//
// internal/crew/roles.yaml's own supervisor `reads` bullet (grepped before
// writing this, per the spec's own instruction, because the roles data is
// the authority) already lists what this packet carries: "the goal it was
// given; open unowned anomalies; blocked and returned tasks with their
// reasons; cadence-due work per analyst; every posted deliverable's
// options; each analyst's skills, state, ... guard headroom". The first
// four of those five collapse into ONE thing here, the deterministic Plan's
// own Items: crew.Propose has already gathered exactly those four sources
// (plan.go's sources 1 through 5) into that slice with a Why per item, so
// there is no second read of the anomalies, the blocked tasks or the
// decision requests to be done here -- which is also why PlanPacket takes
// db only for signature symmetry with Packet's own shape; nothing in its
// body dereferences it today.
package deliver

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// PlanPacket builds the supervisor's one packet: the goal (verbatim), every
// deterministic item numbered #1..#n with its title, source (Why), assignee,
// desk and budget, the roster as one line per ACTIVE analyst (name, desk,
// skills, engine, headroom this month), and the supervisor's own job
// description block, in that order.
//
// Bounded to packetMaxBytes like the task packet, items first, roster
// second, the job description never trimmed. This is why PlanPacket does
// not simply join its sections and call BoundBytes the way Packet does:
// BoundBytes cuts from the END of the joined string, which would cut the
// job description first, because it is deliberately the LAST section here
// too -- assemblePlanPacket below reserves room for the goal and the job
// description and cuts items, then roster, out of what is left.
func PlanPacket(db *sql.DB, deterministic crew.Plan, roster []crew.Analyst, spent map[string]money.Cents) string {
	goalBlock := fmt.Sprintf("The goal\n%s\n", deterministic.Goal)
	itemsBlock := planItemsBlock(deterministic.Items)
	rosterBlock := planRosterBlock(roster, spent)
	jobDescBlock := JobDescriptionBlock("supervisor", "management")
	return assemblePlanPacket(goalBlock, itemsBlock, rosterBlock, jobDescBlock)
}

func planItemsBlock(items []crew.PlanItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThe deterministic plan's own items\n")
	for i, it := range items {
		fmt.Fprintf(&b, "#%d %s\n", i+1, it.Title)
		fmt.Fprintf(&b, "   why:      %s\n", it.Why)
		fmt.Fprintf(&b, "   assignee: %s\n", orDash(it.Assignee))
		fmt.Fprintf(&b, "   desk:     %s\n", orDash(it.Desk))
		fmt.Fprintf(&b, "   budget:   %s\n", it.Budget)
	}
	return b.String()
}

// planRosterBlock lists every ACTIVE analyst, one line each: name, desk,
// skills, engine, headroom this month. A suspended, restricted, onboarding
// or probation analyst is never a legal re-route target (section 3: "state
// active"), so it is left off the packet the same way candidatesWithSkill
// (plan.go) already leaves it out of routing.
func planRosterBlock(roster []crew.Analyst, spent map[string]money.Cents) string {
	var b strings.Builder
	b.WriteString("\nThe roster, active analysts only\n")
	for _, a := range roster {
		if a.State != "active" {
			continue
		}
		headroom := a.Monthly - spent[a.Name]
		fmt.Fprintf(&b, "%-24s desk:%-12s engine:%-12s headroom this month:%-10s skills:%s\n",
			a.Name, orDash(a.Desk), a.Engine, headroom, strings.Join(a.Skills, ","))
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// assemblePlanPacket joins the four sections under packetMaxBytes, cutting
// ITEMS first and the ROSTER second when the whole does not fit, and never
// cutting goalBlock or jobDescBlock.
func assemblePlanPacket(goalBlock, itemsBlock, rosterBlock, jobDescBlock string) string {
	whole := goalBlock + itemsBlock + rosterBlock + jobDescBlock
	if len(whole) <= packetMaxBytes {
		return whole
	}
	reserved := len(goalBlock) + len(jobDescBlock)
	budget := packetMaxBytes - reserved
	if budget < 0 {
		budget = 0
	}
	items := boundOrDrop(itemsBlock, budget)
	rosterBudget := budget - len(items)
	if rosterBudget < 0 {
		rosterBudget = 0
	}
	roster := boundOrDrop(rosterBlock, rosterBudget)
	return goalBlock + items + roster + jobDescBlock
}

// boundOrDrop is BoundBytes, except when max cannot even hold the
// truncation note. BoundBytes on its own would then still return the note
// ALONE, which is LONGER than max: measured on the seeded estate (70 items,
// 39 active analysts), the packet this produced was 12,332 bytes against a
// 12,288 cap, 44 over -- exactly len(truncatedNote) -- because a rosterBudget
// that had shrunk to 0 still got BoundBytes(roster, 0), which returns
// truncatedNote whole rather than nothing. Dropping the section entirely
// when there is no room even for its own "cut" note is the same
// "additive, never misleading" rule Packet already holds for an empty
// section: nothing is better than a fragment claiming to be more than it is,
// and it is the only way assemblePlanPacket's own bound actually holds.
func boundOrDrop(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < len(truncatedNote) {
		return ""
	}
	return BoundBytes(s, max)
}

// PlanPrompt is the supervisor's one planning ask: a short persona line, the
// packet (PlanPacket's own output), and the fenced ```plan block
// crew.ValidatePlanAnswer parses -- mirroring optionsBlockInstructions'
// shape (a closed instruction block naming the JSON fields and the rules
// that bound them) for a different wire shape. Not Prompt: a plan ask is
// not "the task on your desk is ...", and its answer is this block, never
// the options block optionsBlockInstructions writes for an ordinary
// deliverable.
func PlanPrompt(sup crew.Analyst, packetText string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, planning the crew's next sprint.\n", sup.Name)
	b.WriteString(packetText)
	b.WriteString("\nReturn the same items, by their #ref, with a why per item. You may " +
		"re-route an item to a different active holder of its own skill, on the same desk, " +
		"re-order, drop items, or lower a budget; you may never invent an item with no ref, " +
		"raise a budget, or route to anybody not active.\n")
	b.WriteString("\nEnd your answer with a fenced block tagged plan, JSON:\n")
	b.WriteString("```plan\n")
	b.WriteString(`{"items": [{"ref": 3, "assignee": "investigator-gcp", ` +
		`"budget_cents": 1500, "why": "..."}]}` + "\n")
	b.WriteString("```\n")
	b.WriteString("ref is the deterministic item's own number; an item with no ref is " +
		"refused as invented work. assignee, if given, must be an active analyst holding " +
		"that item's own skill on its own desk. budget_cents is a whole number of cents " +
		"and may only go down from the item it replaces, never up. why is required, at " +
		"most 240 bytes. Zero items is a legal answer: write nothing this sprint if there " +
		"is nothing to add.\n")
	return b.String()
}

// PlanWorstCase prices the one planning call the way EstimateWorstCase
// prices a task call: deliver.WorstCaseMicros, the same formula, over the
// prompt actually about to be sent.
//
// It does not call EstimateWorstCase itself. That function builds its
// packet from Packet(db, t, a, false), which is gated on figures-read and
// reads a crew.Task -- the supervisor DOES hold figures-read (roles.yaml:
// both sprint-planning and routing grant it), so EstimateWorstCase would
// not refuse, but it would price a synthetic, near-empty task's packet
// rather than PlanPacket's own roster-and-items content, understating the
// real prompt by however much smaller an empty task's packet is than the
// real one -- exactly the kind of bound tokens.go's own history (a "worst
// case" the first live call stepped over) exists to prevent. @claude
// 2026-09-03: the spec's own words point at EstimateWorstCase by name; this
// is the deviation from that literal reading, and the report's NOT PROVEN
// line says so again.
func PlanWorstCase(sup crew.Analyst, planPrompt string, maxOutputTokens int) (worstMicros int64, model string, priced bool) {
	model = engines.DefaultModel(sup.Engine)
	metered, known := engines.Metered(sup.Engine)
	if !known || !metered {
		return 0, model, false
	}
	p, ok := engines.PriceFor(sup.Engine, model)
	if !ok {
		return 0, model, false
	}
	return WorstCaseMicros(Tokens(planPrompt), maxOutputTokens, p), model, true
}
