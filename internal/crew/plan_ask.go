package crew

// The supervisor's one planning call: parsing and validating its answer.
// B4-STEP-TWO-SPEC.md sections 2 and 3.
//
// Mirrors options.go's ParseOptions deliberately: a fenced block, JSON,
// size-capped before it is ever parsed, the same hostile-input shape,
// because this is the same kind of problem wearing a different vocabulary --
// options.go checks a CLASS a model wrote against a role's job description;
// this checks a REF against the deterministic plan it was asked to route,
// and an ASSIGNEE and a BUDGET against the roster it was shown. There is no
// ValidateAndSave here, because nothing is saved: the deliverable this step
// produces is a Plan, held in memory until a person approves it through the
// unchanged crew.Approve, never written to the board on its own -- the same
// argument plan.go's own package comment already makes about Propose.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

const (
	// planBlockMaxBytes mirrors options.go's optionsBlockMaxBytes: a number a
	// hostile 1 MB block is checked against before anything tries to parse
	// it. Section 3's own words: "at most 64 KB, the same hostile-input
	// tests ParseOptions already has".
	planBlockMaxBytes = 64 * 1024

	// planWhyMaxBytes is section 3's own number: "at most 240 bytes".
	planWhyMaxBytes = 240
)

// planFence finds the LAST ```plan ... ``` block in the model's answer, the
// same "last match wins" rule optionsFence uses and for the same reason: a
// model that echoes the shape earlier in its own prose (explaining the
// format to itself) does not get read as the answer.
var planFence = regexp.MustCompile("(?s)```plan[ \\t]*\\r?\\n(.*?)```")

// rawPlanItem is the wire shape a model writes. Ref and BudgetCents are
// json.Number for the same reason rawOption's fields are (options.go):
// encoding/json refuses a JSON STRING into a json.Number field (catching "a
// string where an integer goes") and Number.Int64() refuses anything with a
// decimal point or exponent, where an int64 field would have silently
// accepted a numeric-looking JSON string and a float64 field would have
// silently rounded a fractional one instead of refusing it. An absent field
// decodes as the empty json.Number, read below as "not given" rather than
// as zero, because zero is itself a legal ref-less-ness signal only for
// budget_cents.
type rawPlanItem struct {
	Ref         json.Number `json:"ref"`
	Assignee    string      `json:"assignee"`
	BudgetCents json.Number `json:"budget_cents"`
	Why         string      `json:"why"`
}

// PlanAnswerItem is one validated item of the model's plan: Ref resolved to
// the deterministic plan's own numbering (1-indexed, PlanItem[Ref-1] is the
// item it replaces), Assignee and Budget checked against the roster and
// that deterministic item, Why bounded and non-empty.
type PlanAnswerItem struct {
	Ref      int
	Assignee string
	Budget   money.Cents
	Why      string
}

// ValidatePlanAnswer parses and validates the model's answer against the
// deterministic plan it was asked to route and the roster it was shown.
//
// found is false when no fenced ```plan block exists in body at all, which
// is not itself a refusal -- the caller decides what an absent block means
// (this step never asks a role whether it "allows no options" the way
// options.go's AllowsNoOptions does; a plan ask with no block at all is
// simply not a plan answer). reason is non-empty exactly when the answer --
// structurally, or against the deterministic plan and the roster -- is
// refused; section 3's own rule is that a refused answer is shown WHOLE
// with the reason, "never partially applied", so items is always nil
// alongside a non-empty reason: nothing here returns a partial list.
func ValidatePlanAnswer(body string, deterministic Plan, roster []Analyst, spent map[string]money.Cents) (items []PlanAnswerItem, found bool, reason string) {
	m := planFence.FindStringSubmatch(body)
	if m == nil {
		return nil, false, ""
	}
	found = true
	raw := m[1]
	if len(raw) > planBlockMaxBytes {
		return nil, found, fmt.Sprintf(
			"the plan block is %d bytes, over the %d byte limit", len(raw), planBlockMaxBytes)
	}

	var block struct {
		Items []rawPlanItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		return nil, found, "the plan block is not valid JSON: " + err.Error()
	}
	n := len(deterministic.Items)
	if len(block.Items) > n {
		return nil, found, fmt.Sprintf(
			"%d items, at most %d allowed: the deterministic plan has %d", len(block.Items), n, n)
	}

	byName := make(map[string]Analyst, len(roster))
	for _, a := range roster {
		byName[a.Name] = a
	}

	seenRef := map[int]bool{}
	out := make([]PlanAnswerItem, 0, len(block.Items))
	for i, r := range block.Items {
		// One gate, one line, covering three shapes at once: the field
		// absent (empty json.Number, ParseInt refuses ""), a non-numeric
		// ref, and a ref out of range. An item without a ref is invented
		// work (section 3's own words), and gates-have-teeth.sh's own
		// "accept an item without a ref" case plants its fault on this
		// exact line, forcing refInvalid to false while every identifier it
		// reads stays referenced (so the mutation still compiles): that is
		// the whole reason this is one boolean rather than three separate
		// checks a mutation could neuter one at a time and still be caught
		// by the other two.
		ref, refErr := r.Ref.Int64()
		refInvalid := refErr != nil || ref < 1 || ref > int64(n)
		if refInvalid {
			return nil, found, fmt.Sprintf(
				"item %d names no valid ref (want a whole number 1..%d): "+
					"an item without a ref is invented work", i+1, n)
		}
		if seenRef[int(ref)] {
			return nil, found, fmt.Sprintf("ref %d is named more than once", ref)
		}
		seenRef[int(ref)] = true
		det := deterministic.Items[ref-1]

		why := r.Why
		if strings.TrimSpace(why) == "" {
			return nil, found, fmt.Sprintf("item %d (ref %d) names no why", i+1, ref)
		}
		if len(why) > planWhyMaxBytes {
			return nil, found, fmt.Sprintf(
				"item %d (ref %d)'s why is %d bytes, over the %d byte limit", i+1, ref, len(why), planWhyMaxBytes)
		}

		assignee := strings.TrimSpace(r.Assignee)
		if assignee == "" {
			assignee = det.Assignee
		}
		if assignee != det.Assignee {
			// Re-routing is only ever checked against the SAME pool the
			// deterministic route itself chose from -- candidatesWithSkill
			// on the item's own Skill and Desk -- never guessed at. An item
			// with no Skill (blocked rework, cadence-due, returned work, a
			// decision request: plan.go's own PlanItem.Skill comment names
			// all four) has no such pool, so a changed assignee on one of
			// those is refused outright rather than checked against
			// nothing.
			if det.Skill == "" || !inSkillPool(det.Skill) {
				return nil, found, fmt.Sprintf(
					"item %d (ref %d) was not routed by a skill this plan can re-route by; "+
						"it may not be reassigned away from %s", i+1, ref, det.Assignee)
			}
			a, ok := byName[assignee]
			if !ok {
				return nil, found, fmt.Sprintf(
					"item %d (ref %d) names %q, not on the roster", i+1, ref, assignee)
			}
			if a.State != "active" {
				return nil, found, fmt.Sprintf(
					"item %d (ref %d) routes to %q, which is not active", i+1, ref, assignee)
			}
			if !hasSkill(a.Skills, det.Skill) {
				return nil, found, fmt.Sprintf(
					"item %d (ref %d) routes to %q, which does not hold %s", i+1, ref, assignee, det.Skill)
			}
			if det.Desk != "" && a.Desk != det.Desk {
				return nil, found, fmt.Sprintf(
					"item %d (ref %d) routes to %q, which is not on the %s desk", i+1, ref, assignee, det.Desk)
			}
		}

		a, ok := byName[assignee]
		if !ok {
			// Reachable when assignee == det.Assignee and the deterministic
			// plan itself names somebody the CURRENT roster no longer
			// carries (an analyst removed between Propose and this call):
			// re-checked here regardless of whether the assignee changed,
			// because this function trusts the roster it was handed, never
			// the deterministic plan's own say-so about who still exists.
			return nil, found, fmt.Sprintf("item %d (ref %d) names %q, not on the roster", i+1, ref, assignee)
		}
		if headroomOf(a, spent) <= 0 {
			return nil, found, fmt.Sprintf(
				"item %d (ref %d) routes to %q, which has no headroom left this month", i+1, ref, assignee)
		}

		budget := det.Budget
		if r.BudgetCents != "" {
			v, err := r.BudgetCents.Int64()
			if err != nil {
				return nil, found, fmt.Sprintf("item %d (ref %d)'s budget_cents %v", i+1, ref, err)
			}
			budget = money.Cents(v)
		}
		if budget < 0 {
			return nil, found, fmt.Sprintf("item %d (ref %d)'s budget_cents is negative", i+1, ref)
		}
		if budget > a.PerTask {
			return nil, found, fmt.Sprintf(
				"item %d (ref %d)'s budget_cents %s is over %s's own per-task guard %s",
				i+1, ref, budget, assignee, a.PerTask)
		}
		if budget > det.Budget {
			return nil, found, fmt.Sprintf(
				"item %d (ref %d)'s budget_cents %s is over the deterministic item's own %s: budget_cents may only go down",
				i+1, ref, budget, det.Budget)
		}

		out = append(out, PlanAnswerItem{Ref: int(ref), Assignee: assignee, Budget: budget, Why: why})
	}
	return out, found, ""
}

// inSkillPool answers whether skill is one of SkillPool's own entries, which
// is how ValidatePlanAnswer tells a real skill token (routedItem's class for
// source 1 and goal rules (a)/(c)) apart from a roster NAME carried in the
// same field (goal rule (b), addNameGoalItem): a roster name is never a
// member of SkillPool, by construction (invariant 21, CLAUDE.md), so no
// second flag is needed on PlanItem to remember which kind of string its
// own Skill field holds.
func inSkillPool(skill string) bool {
	for _, s := range SkillPool {
		if s == skill {
			return true
		}
	}
	return false
}
