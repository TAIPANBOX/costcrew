package finops

// Supervise is the supervisor's pass: B3-SPEC.md section 4, step one
// (deterministic, as crew.Propose already is), run WITHOUT a model call.
// Step one and a half of B4's plan item.
//
// `@yurii 2026-09-02`: "він має давати на вибір якісь певні рішення, які він
// вважає за потрібне спочатку супервайзеру, тобто головному агенту, а вже
// той має запитувати юзера, користувача, власника цих агентів, що робити
// далі." And: "супервайзер питає власника тільки тоді, коли він сам не може
// вирішити це питання, тобто, що стосується безпосередньо взаємодії людей
// або прийняття якихось ключових рішень, а не щоразу, коли в агента
// виникають якісь спірні моменти."
//
// Two properties this file holds that are easy to get wrong, both found in
// review of the first version:
//
//  1. Options in ONE deliverable are ALTERNATIVES, never independent
//     actions -- "давати на вибір якісь певні рішення" is offering a
//     CHOICE. So this never applies more than one option of one deliverable:
//     it decides (or carries) the whole group together, and applying one
//     always marks the rest not_chosen (crew.LiveRivalsOf, called from
//     Apply).
//  2. Nothing is dropped. The first version dropped contradictions and
//     over-guard figures; roles.yaml's own job description for the
//     supervisor says otherwise for both. A contradiction -- two
//     deliverables' anomaly.explain options naming a different cause for
//     the same anomaly -- is exactly "any question two analysts answer
//     differently on the same evidence", one of the supervisor's own
//     hands_to_owner_conditions: both sides are carried, addressed to ONE
//     owner as one question. And "the desk's monthly guard headroom" the
//     first version compared a cloud figure against was the wrong number
//     entirely (an LLM-spend guard, not a chargeback threshold); the real
//     gate is roles.yaml's T.anomaly, read from the roles data, and a
//     figure over it is carried, never dropped, even for a class the
//     supervisor's own job description would otherwise decide alone.

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// decisionLapseDays is section 4's "the date after which it lapses (7
// days)".
const decisionLapseDays = 7

// RequestWritten is one owner's decision request, for the caller (the
// runner's -supervise mode, the console's button) to report.
type RequestWritten struct {
	Owner   string
	Options int
}

// Pass is what one run of Supervise did, so a caller can print or journal it
// without re-deriving anything from the database.
type Pass struct {
	Applied  []crew.Option
	Carried  []crew.Option
	Requests []RequestWritten
}

// Supervise runs the deterministic pass over one sprint's posted
// deliverables: collect every open option, decide each deliverable's whole
// choice together (apply the top-ranked one when the supervisor's own job
// description allows it and its figure is within T.anomaly, otherwise carry
// every option of that deliverable), and carry a contradiction between two
// deliverables to one owner as one question. Nothing is dropped.
func Supervise(db *sql.DB, sprintID int, rec Recorder) (Pass, error) {
	var pass Pass

	opts, err := crew.OpenOptionsForSprint(db, sprintID)
	if err != nil {
		return pass, err
	}
	rankOptions(opts)

	redirectOwner, note, err := contradictionRouting(db, opts)
	if err != nil {
		return pass, err
	}

	groupOrder, groups := groupByArtifact(opts)
	tAnomaly, _ := crew.ThresholdFor("T.anomaly") // zero value if missing: 0 cents is conservative, see below

	byOwner := map[string][]crew.Option{}
	ownerOrder := make([]string, 0)
	notes := map[string]string{}

	for _, artID := range groupOrder {
		group := groups[artID]
		top := group[0] // rankOptions already sorted opts; groupByArtifact preserves that order within each group

		may, _ := crew.MayDecide("supervisor", top.Class)
		// A figure over T.anomaly is a key decision, carried even for a
		// class the supervisor's own job description would otherwise
		// decide alone. tAnomaly.ValueCents is 0 when the threshold is
		// somehow missing from roles.yaml (it never is: mustLoadRoles
		// would already have panicked), which makes every real figure
		// "over" it -- the conservative direction, carrying rather than
		// silently applying past a threshold that could not be read.
		withinThreshold := top.FigureCents <= tAnomaly.ValueCents

		if may && withinThreshold {
			if err := Apply(db, top, "supervisor", rec); err != nil {
				return pass, err
			}
			pass.Applied = append(pass.Applied, top)
			continue
		}

		owner, err := ownerOfOption(db, top)
		if err != nil {
			return pass, err
		}
		if redirect, is := redirectOwner[artID]; is {
			owner = redirect
		}
		for _, o := range group {
			if err := crew.MarkOptionCarried(db, o.Artifact, o.Ordinal); err != nil {
				return pass, err
			}
			pass.Carried = append(pass.Carried, o)
			if n, is := note[optionKey(o)]; is {
				notes[optionKey(o)] = n
			}
		}
		if _, seen := byOwner[owner]; !seen {
			ownerOrder = append(ownerOrder, owner)
		}
		byOwner[owner] = append(byOwner[owner], group...)
	}
	sort.Strings(ownerOrder) // a stable, deterministic order independent of map iteration

	defaultLapses := time.Now().UTC().AddDate(0, 0, decisionLapseDays).Format("2006-01-02")
	for _, owner := range ownerOrder {
		ownerOpts := byOwner[owner]

		// The FIRST lapse date this request was ever given, kept across
		// every rewrite (crew.WriteDecisionRequest's own comment says why):
		// read it before writing anything, so the body this pass renders
		// names the date that write is about to (not) change, never a
		// fresh one.
		lapses := defaultLapses
		if existing, found, err := crew.ExistingLapses(db, sprintID, owner); err != nil {
			return pass, err
		} else if found && existing != "" {
			lapses = existing
		}

		body, err := decisionRequestBody(db, sprintID, ownerOpts, notes, lapses)
		if err != nil {
			return pass, err
		}
		if _, err := crew.WriteDecisionRequest(db, sprintID, owner, body, lapses); err != nil {
			return pass, err
		}
		if rec != nil {
			_ = rec.Emit("decision_requested", "supervisor", "info", map[string]any{
				"sprint": sprintID, "owner": owner, "options": len(ownerOpts), "lapses": lapses,
			}, nil)
		}
		pass.Requests = append(pass.Requests, RequestWritten{Owner: owner, Options: len(ownerOpts)})
	}
	return pass, nil
}

// groupByArtifact splits already-ranked options by their own deliverable,
// preserving the rank order both across groups (groupOrder) and within one
// (each group's own slice): options in one deliverable are decided
// together, never as independent rows.
func groupByArtifact(ranked []crew.Option) (order []int, groups map[int][]crew.Option) {
	groups = map[int][]crew.Option{}
	for _, o := range ranked {
		if _, ok := groups[o.Artifact]; !ok {
			order = append(order, o.Artifact)
		}
		groups[o.Artifact] = append(groups[o.Artifact], o)
	}
	return order, groups
}

// contradictionRouting finds anomaly.explain options that disagree across
// DIFFERENT deliverables on the SAME anomaly: roles.yaml's own
// hands_to_owner_conditions, "any question two analysts answer differently
// on the same evidence". Neither side is ever dropped or auto-applied
// (anomaly.explain is not in the supervisor's own decides_alone list, so
// the ordinary per-deliverable logic above already carries both); what this
// adds is making sure both sides land in ONE decision request rather than
// two. redirectOwner, keyed by artifact id, names the owner a contradicted
// deliverable's WHOLE choice must route to instead of its own natural
// owner: the higher-ranked side's. note, keyed by option, names the other
// analyst for the decision request body.
func contradictionRouting(db *sql.DB, ranked []crew.Option) (redirectOwner map[int]string, note map[string]string, err error) {
	redirectOwner = map[int]string{}
	note = map[string]string{}

	byAnomaly := map[string][]crew.Option{}
	for _, o := range ranked {
		if o.Class != "anomaly.explain" {
			continue
		}
		taskID, terr := crew.TaskOfArtifact(db, o.Artifact)
		if terr != nil {
			return nil, nil, terr
		}
		t, terr := crew.GetTask(db, taskID)
		if terr != nil {
			return nil, nil, terr
		}
		if t.Anomaly == "" {
			continue
		}
		byAnomaly[t.Anomaly] = append(byAnomaly[t.Anomaly], o) // preserves rank order
	}

	for anomalyID, group := range byAnomaly {
		// One representative per deliverable (its lowest-ordinal
		// anomaly.explain option on this anomaly): two alternatives of ONE
		// deliverable naming different causes are a choice, not a
		// contradiction with each other, the same reasoning
		// crew.LiveRivalsOf's own comment gives.
		byArtifact := map[int]crew.Option{}
		order := make([]int, 0)
		for _, o := range group {
			if _, ok := byArtifact[o.Artifact]; !ok {
				order = append(order, o.Artifact)
			}
			cur, ok := byArtifact[o.Artifact]
			if !ok || o.Ordinal < cur.Ordinal {
				byArtifact[o.Artifact] = o
			}
		}
		if len(order) < 2 {
			continue
		}
		summaries := map[string]bool{}
		for _, artID := range order {
			summaries[strings.TrimSpace(byArtifact[artID].Summary)] = true
		}
		if len(summaries) <= 1 {
			continue // every deliverable that named a cause for this anomaly agrees
		}

		// order is in rank order (group was built from `ranked`), so
		// order[0]'s deliverable is the higher-ranked one: its owner is
		// where the whole question goes.
		winnerArtifact := order[0]
		winnerTaskID, terr := crew.TaskOfArtifact(db, winnerArtifact)
		if terr != nil {
			return nil, nil, terr
		}
		winnerOwner, terr := crew.TaskOwner(db, winnerTaskID)
		if terr != nil {
			return nil, nil, terr
		}

		for i, artID := range order {
			rivalArtID := order[0]
			if i == 0 {
				rivalArtID = order[1]
			}
			rival := byArtifact[rivalArtID]
			rivalTaskID, terr := crew.TaskOfArtifact(db, rival.Artifact)
			if terr != nil {
				return nil, nil, terr
			}
			rivalTask, terr := crew.GetTask(db, rivalTaskID)
			if terr != nil {
				return nil, nil, terr
			}

			redirectOwner[artID] = winnerOwner
			note[optionKey(byArtifact[artID])] = fmt.Sprintf(
				"two analysts answered differently on anomaly %s: %s (task %d) says %q",
				anomalyID, rivalTask.Assignee, rivalTaskID, rival.Summary)
		}
	}
	return redirectOwner, note, nil
}

func optionKey(o crew.Option) string { return fmt.Sprintf("%d:%d", o.Artifact, o.Ordinal) }

// rankOptions is step 3: saving_cents descending, then risk ascending
// (low, medium, high, anything else last).
func rankOptions(opts []crew.Option) {
	sort.SliceStable(opts, func(i, j int) bool {
		if opts[i].SavingCents != opts[j].SavingCents {
			return opts[i].SavingCents > opts[j].SavingCents
		}
		return riskRank(opts[i].Risk) < riskRank(opts[j].Risk)
	})
}

func riskRank(risk string) int {
	switch risk {
	case "low":
		return 0
	case "medium":
		return 1
	case "high":
		return 2
	}
	return 3
}

// ownerOfOption is the owner a carried option's decision request belongs to:
// tasks.owner of the task the option's deliverable answers, per B3-SPEC.md
// section 4 step 5 ("the owner of the analyst that wrote it, per
// tasks.owner").
func ownerOfOption(db *sql.DB, o crew.Option) (string, error) {
	taskID, err := crew.TaskOfArtifact(db, o.Artifact)
	if err != nil {
		return "", err
	}
	owner, err := crew.TaskOwner(db, taskID)
	if err != nil {
		return "", err
	}
	if owner == "" {
		owner = "unclaimed"
	}
	return owner, nil
}

// decisionRequestBody is section 4's fixed shape: the question, the options
// with their figures, the supervisor's preference and why, and the lapse
// date. Grouped by deliverable -- options in one deliverable are a CHOICE,
// not a flat list of independent asks -- with a note under an option that
// is one side of a "two analysts answered differently" question. The
// preference here is the deterministic ranking itself (the highest-ranked
// carried option overall): B4 step two replaces this paragraph with one a
// model writes from the same list, and this report's NOT PROVEN line says
// so.
func decisionRequestBody(db *sql.DB, sprintID int, opts []crew.Option, notes map[string]string, lapses string) (string, error) {
	label := sprintLabel(db, sprintID)
	var b strings.Builder
	fmt.Fprintf(&b, "## Decision needed: %d option(s) from %s\n\n", len(opts), label)
	b.WriteString("These are classes this practice's supervisor may not decide alone, " +
		"figures above T.anomaly even where it could, or a question two analysts " +
		"answered differently; its own job description hands them to you.\n\n")

	if len(opts) > 0 {
		top := opts[0]
		taskID, _ := crew.TaskOfArtifact(db, top.Artifact)
		fmt.Fprintf(&b, "**Preference:** %s on task %d (%s), ranked highest by saving, "+
			"then by risk. This is a ranking, not a judgement: nobody has weighed the "+
			"reasons yet.\n\n", top.Class, taskID, top.Summary)
	}

	order, byArtifact := groupByArtifact(opts)
	for _, artID := range order {
		group := byArtifact[artID]
		taskID, _ := crew.TaskOfArtifact(db, artID)
		if len(group) > 1 {
			fmt.Fprintf(&b, "### Task %d: choose at most one\n", taskID)
		} else {
			fmt.Fprintf(&b, "### Task %d\n", taskID)
		}
		for _, o := range group {
			fmt.Fprintf(&b, "- **%s** (option %d): %s\n", o.Class, o.Ordinal, o.Summary)
			fmt.Fprintf(&b, "  Figure: %s. Saving: %s. Risk: %s. Needs: %s.\n",
				money.Cents(o.FigureCents), money.Cents(o.SavingCents), o.Risk, o.Needs)
			if n, is := notes[optionKey(o)]; is {
				fmt.Fprintf(&b, "  %s\n", n)
			}
		}
		b.WriteString("\n")
	}

	// Named as the supervisor's own deadline, never as a promise that
	// something enforces it: heraldyx's and agent-passport's own words for
	// this event are "names a date after which it counts as lapsed;
	// nothing enforces that date", and this console says the same thing
	// about itself rather than a stronger one it does not keep. There is no
	// sweeper; the only thing that ever re-reads this date is the
	// supervisor's own NEXT pass, which is what turns "answer by" into
	// "unanswered since" once today is past it, and lapses itself never
	// moves once a request is first written (crew.WriteDecisionRequest).
	if isStale(lapses) {
		fmt.Fprintf(&b, "**Unanswered since %s.** This is the supervisor's own deadline, now "+
			"past; nothing enforces it.\n", lapses)
	} else {
		fmt.Fprintf(&b, "**Answer by %s.** This is the supervisor's own deadline; nothing "+
			"enforces it.\n", lapses)
	}
	return b.String(), nil
}

// isStale is true once today is past lapses. Both are "2006-01-02", so a
// plain string comparison is exact and needs no parsing.
func isStale(lapses string) bool {
	return time.Now().UTC().Format("2006-01-02") > lapses
}

func sprintLabel(db *sql.DB, sprintID int) string {
	sprints, err := crew.Sprints(db)
	if err != nil {
		return fmt.Sprintf("sprint %d", sprintID)
	}
	for _, s := range sprints {
		if s.ID == sprintID {
			return s.Label
		}
	}
	return fmt.Sprintf("sprint %d", sprintID)
}
