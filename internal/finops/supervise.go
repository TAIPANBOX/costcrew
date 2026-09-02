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

// Dropped names one option the pass refused to carry forward, and why.
type Dropped struct {
	Option crew.Option
	Reason string
}

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
	Dropped  []Dropped
	Carried  []crew.Option
	Requests []RequestWritten
}

// Supervise runs the deterministic pass over one sprint's posted
// deliverables: collect every open option, drop the ones that contradict
// each other or a guard, rank the rest, apply what the supervisor's own job
// description decides alone, and carry everything else into one decision
// request per owner.
func Supervise(db *sql.DB, sprintID int, rec Recorder) (Pass, error) {
	var pass Pass

	opts, err := crew.OpenOptionsForSprint(db, sprintID)
	if err != nil {
		return pass, err
	}

	kept, dropped, err := dropUnfit(db, opts)
	if err != nil {
		return pass, err
	}
	for _, d := range dropped {
		if err := crew.MarkOptionDropped(db, d.Option.Artifact, d.Option.Ordinal, d.Reason); err != nil {
			return pass, err
		}
	}
	pass.Dropped = dropped

	rankOptions(kept)

	byOwner := map[string][]crew.Option{}
	for _, o := range kept {
		// The supervisor's OWN decides_alone list, exactly: nothing an
		// analyst's job description also decides alone bypasses this pass --
		// see applySideEffect's own comment and this function's package
		// comment for why an analyst's Post never reaches Apply on its own.
		if may, _ := crew.MayDecide("supervisor", o.Class); may {
			if err := Apply(db, o, "supervisor", rec); err != nil {
				return pass, err
			}
			pass.Applied = append(pass.Applied, o)
			continue
		}
		if err := crew.MarkOptionCarried(db, o.Artifact, o.Ordinal); err != nil {
			return pass, err
		}
		pass.Carried = append(pass.Carried, o)

		owner, err := ownerOfOption(db, o)
		if err != nil {
			return pass, err
		}
		byOwner[owner] = append(byOwner[owner], o)
	}

	owners := make([]string, 0, len(byOwner))
	for owner := range byOwner {
		owners = append(owners, owner)
	}
	sort.Strings(owners) // a map range would make two runs disagree about nothing but order

	lapses := time.Now().UTC().AddDate(0, 0, decisionLapseDays).Format("2006-01-02")
	for _, owner := range owners {
		ownerOpts := byOwner[owner]
		body, err := decisionRequestBody(db, sprintID, ownerOpts, lapses)
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

// dropUnfit is step 2: contradictions and over-guard figures, each marked
// with the reason it did not survive to be ranked.
//
// A contradiction is two open options ON THE SAME ANOMALY (the task's own
// anomaly id) whose summaries disagree: the options block carries no
// separate caused_by field of its own (B3-SPEC.md section 2's shape is
// class/summary/figure_cents/saving_cents/risk/needs/evidence), and an
// anomaly.explain option's summary IS its named cause, the same text
// internal/finops.Apply hands to anomaly.Explain as the reason. Two
// analysts naming a different cause for the same anomaly is read from that.
//
// Over-guard reads "the desk's monthly guard headroom" as the WRITING
// analyst's own guard (roles.yaml section 3's "Reads" bullet: "each
// analyst's skills, state, first-pass rate and guard headroom" -- an
// analyst-scoped figure, the same one the crew page already computes via
// crew.SpendInMonth against Analyst.Monthly), because there is no separate
// per-desk guard in this console today.
func dropUnfit(db *sql.DB, opts []crew.Option) (kept []crew.Option, dropped []Dropped, err error) {
	byAnomaly := map[string][]crew.Option{}
	anomalyOf := map[string]string{} // artifact:ordinal -> anomaly id, cached
	for _, o := range opts {
		taskID, terr := crew.TaskOfArtifact(db, o.Artifact)
		if terr != nil {
			return nil, nil, terr
		}
		t, terr := crew.GetTask(db, taskID)
		if terr != nil {
			return nil, nil, terr
		}
		if t.Anomaly != "" {
			byAnomaly[t.Anomaly] = append(byAnomaly[t.Anomaly], o)
		}
		anomalyOf[optionKey(o)] = t.Anomaly
	}

	contradicted := map[string]string{} // optionKey -> reason
	for anomalyID, group := range byAnomaly {
		summaries := map[string]bool{}
		for _, o := range group {
			summaries[strings.TrimSpace(o.Summary)] = true
		}
		if len(summaries) <= 1 {
			continue // every option on this anomaly agrees; not a contradiction
		}
		for _, o := range group {
			contradicted[optionKey(o)] = fmt.Sprintf(
				"contradicts another option on anomaly %s: this practice's "+
					"analysts named more than one cause for the same move", anomalyID)
		}
	}

	roster, err := crew.Roster(db)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]crew.Analyst{}
	for _, a := range roster {
		byName[a.Name] = a
	}
	// "This month" by the wall clock is usually empty: the seeded/generated
	// estate, and every sprint fixture in it, is dated in the past rather
	// than around today. The newest month any task actually has spend in is
	// the one a guard headroom check can mean something against, the same
	// reasoning internal/web/work.go's staff() page applies via
	// world.LastDay for the crew page's own over-guard figure.
	month, err := latestSpendMonth(db)
	if err != nil {
		return nil, nil, err
	}
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	spentByAnalyst, err := crew.SpendInMonth(db, month)
	if err != nil {
		return nil, nil, err
	}

	for _, o := range opts {
		key := optionKey(o)
		if reason, is := contradicted[key]; is {
			dropped = append(dropped, Dropped{Option: o, Reason: reason})
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
		a := byName[t.Assignee]
		if a.Monthly > 0 {
			headroom := a.Monthly - spentByAnalyst[a.Name]
			if money.Cents(o.FigureCents) > headroom {
				dropped = append(dropped, Dropped{Option: o, Reason: fmt.Sprintf(
					"%s is %s over %s's guard headroom for %s (%s left of %s)",
					money.Cents(o.FigureCents), money.Cents(o.FigureCents)-headroom,
					t.Assignee, month, headroom, a.Monthly)})
				continue
			}
		}
		kept = append(kept, o)
	}
	return kept, dropped, nil
}

func optionKey(o crew.Option) string { return fmt.Sprintf("%d:%d", o.Artifact, o.Ordinal) }

// latestSpendMonth is the newest sprint month with any spend at all, read
// from the tasks/sprints join crew.SpendInMonth itself already reads.
func latestSpendMonth(db *sql.DB) (string, error) {
	var m string
	err := db.QueryRow(`SELECT COALESCE(MAX(substr(s.start,1,7)),'')
		FROM tasks t JOIN sprints s ON s.id = t.sprint WHERE t.spent_cents > 0`).Scan(&m)
	return m, err
}

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
// date. The preference here is the deterministic ranking itself (the
// highest-ranked option, ranked by saving then risk): B4 step two replaces
// this paragraph with one a model writes from the same list, and this
// report's NOT PROVEN line says so.
func decisionRequestBody(db *sql.DB, sprintID int, opts []crew.Option, lapses string) (string, error) {
	label := sprintLabel(db, sprintID)
	var b strings.Builder
	fmt.Fprintf(&b, "## Decision needed: %d option(s) from %s\n\n", len(opts), label)
	b.WriteString("These are classes this practice's supervisor may not decide alone; " +
		"its own job description hands them to you.\n\n")

	if len(opts) > 0 {
		top := opts[0]
		taskID, _ := crew.TaskOfArtifact(db, top.Artifact)
		fmt.Fprintf(&b, "**Preference:** %s on task %d (%s), ranked highest by saving, "+
			"then by risk. This is a ranking, not a judgement: nobody has weighed the "+
			"reasons yet.\n\n", top.Class, taskID, top.Summary)
	}

	for _, o := range opts {
		taskID, _ := crew.TaskOfArtifact(db, o.Artifact)
		fmt.Fprintf(&b, "- **%s** (task %d): %s\n", o.Class, taskID, o.Summary)
		fmt.Fprintf(&b, "  Figure: %s. Saving: %s. Risk: %s. Needs: %s.\n",
			money.Cents(o.FigureCents), money.Cents(o.SavingCents), o.Risk, o.Needs)
	}

	fmt.Fprintf(&b, "\n**Lapses:** if nobody answers by %s, this decision request lapses.\n", lapses)
	return b.String(), nil
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
