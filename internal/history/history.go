// Package history gives a fresh installation a past.
//
// A console whose every anomaly is open, whose forecast table is empty and
// whose explainers have never been written shows a product nobody has used.
// Worse, it hides exactly the states a governance console exists for: the
// dismissal with a reason, the forecast that was wrong and by how much, the
// explainer that came back for a rewrite.
//
// Everything here is a DECISION, so everything here is recorded the way a
// person's decision would be: through the same functions the handlers call, so
// the journal, the state machine and the refusals all apply. Nothing is
// written straight into a table.
//
// Deterministic: every choice comes from a hash of the thing being decided.
package history

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func pick(s string, n int) int {
	if n <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(n))
}

// Counts is what a run of Seed did, so the caller can say so rather than
// claim it.
type Counts struct {
	Triaged, Explained, Accepted, Dismissed int
	Forecasts, Explainers, Comments         int
}

// Seed writes the past. It is idempotent by inspection: each part checks
// whether it has already been done and returns without touching anything.
func Seed(db *sql.DB, rec anomaly.Recorder) (Counts, error) {
	var c Counts
	if err := seedAnomalyDecisions(db, rec, &c); err != nil {
		return c, fmt.Errorf("deciding anomalies: %w", err)
	}
	if err := seedForecasts(db, &c); err != nil {
		return c, fmt.Errorf("freezing forecasts: %w", err)
	}
	if err := seedExplainers(db, &c); err != nil {
		return c, fmt.Errorf("commissioning explainers: %w", err)
	}
	if err := seedComments(db, &c); err != nil {
		return c, fmt.Errorf("writing comments: %w", err)
	}
	return c, nil
}

// ------------------------------------------------------- anomaly decisions

var explanations = []string{
	"The %s desk moved the %s workload off the shared cluster on the %s. " +
		"The rise is the move, and it reverses when the migration finishes.",
	"A retraining run was started by hand and left running over the weekend. " +
		"Confirmed with the team; the instance has been stopped.",
	"Price, not volume: the provider's list price for this meter rose on the 1st. " +
		"Volume is flat to within two percent.",
	"A backfill nobody scheduled. It completed, and the cost with it.",
	"Volume, not price: requests to this service are up by a third since the launch. " +
		"This is the launch being paid for, and it was budgeted.",
}

var dismissals = []string{
	"Below the money floor once the shared cost is allocated back. " +
		"Real, and not worth an analyst's afternoon.",
	"The same movement as last month's, already explained under that finding. " +
		"Closing this one so the queue does not carry it twice.",
	"A one-day spike that reversed the next day. The detector is right that it moved; " +
		"nothing was spent that is worth recovering.",
}

// seedAnomalyDecisions moves findings through the states a real week produces.
//
// A queue where everything is open is not a queue anybody has worked. The
// split is deliberate: most findings get an owner, most owned ones get an
// answer, a few of those are accepted and a couple are dismissed with the
// reason the console refuses to store without.
func seedAnomalyDecisions(db *sql.DB, rec anomaly.Recorder, c *Counts) error {
	all, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		return err
	}
	// Already worked? Then this has run, and re-running it would move a
	// decision somebody made.
	for _, a := range all {
		if a.State != anomaly.Open {
			return nil
		}
	}
	roster, err := crew.Roster(db)
	if err != nil {
		return err
	}
	// Who can take a finding: an active analyst on that desk, or any active
	// one when the desk has nobody free.
	byDesk := map[string][]string{}
	var anyone []string
	for _, a := range roster {
		if a.State != "active" {
			continue
		}
		if strings.HasPrefix(a.Name, "triage-") || strings.HasPrefix(a.Name, "investigator-") {
			byDesk[a.Desk] = append(byDesk[a.Desk], a.Name)
		}
		anyone = append(anyone, a.Name)
	}
	for k := range byDesk {
		sort.Strings(byDesk[k])
	}
	sort.Strings(anyone)

	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	for i, a := range all {
		who := byDesk[a.Source]
		if len(who) == 0 {
			who = anyone
		}
		if len(who) == 0 {
			return nil
		}
		analyst := who[pick(a.ID+"|who", len(who))]

		// One in six is left untouched, because a queue nobody is behind on is
		// not a queue either.
		if pick(a.ID+"|open", 6) == 0 {
			continue
		}
		if err := anomaly.Assign(db, a.ID, analyst, rec); err != nil {
			return err
		}
		c.Triaged++

		if pick(a.ID+"|answer", 5) == 0 {
			continue // taken, not yet answered
		}
		reason := fmt.Sprintf(explanations[pick(a.ID+"|why", len(explanations))],
			a.Source, a.Service, a.Day)
		if err := anomaly.Explain(db, a.ID, reason, rec); err != nil {
			return err
		}
		c.Explained++

		switch {
		case pick(a.ID+"|close", 4) == 0 && i%3 != 0:
			if err := anomaly.Dismiss(db, a.ID,
				dismissals[pick(a.ID+"|dismiss", len(dismissals))], rec); err != nil {
				return err
			}
			c.Dismissed++
		case pick(a.ID+"|close", 4) > 1:
			if err := anomaly.Accept(db, a.ID,
				"Answer stands. Nothing to recover, and the cause is understood.", rec); err != nil {
				return err
			}
			c.Accepted++
		}
	}
	return nil
}

// --------------------------------------------------------------- forecasts

// seedForecasts records what the forecaster said, month by month, on the 12th.
//
// The 12th and not the 30th: a forecast frozen from a whole month equals the
// actual, which would fill the accuracy table with grades nobody earned. Made
// on the 12th it is wrong by a real amount, and the amount is what the page is
// for.
func seedForecasts(db *sql.DB, c *Counts) error {
	done, err := finops.FrozenPeriods(db)
	if err != nil {
		return err
	}
	if len(done) > 0 {
		return nil
	}
	// Every month the estate has data for, oldest first, including the open
	// one: a forecast for the month in progress is the normal case.
	rows, err := db.Query(`SELECT DISTINCT substr(day,1,7) FROM charges ORDER BY 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var months []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return err
		}
		months = append(months, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range months {
		if err := finops.FreezeAsAt(db, m, "forecaster", 12); err != nil {
			// A month with fewer than twelve days of data cannot carry a
			// forecast made on the twelfth, and saying nothing about it is
			// right. Every other month still gets one.
			continue
		}
		c.Forecasts++
	}
	return nil
}

// -------------------------------------------------------------- explainers

var explainerTopics = []struct{ Topic, Team string }{
	{"Why your bill moved this month", ""},
	{"What a savings plan actually commits you to", ""},
	{"Reading your showback line by line", ""},
	{"Why storage costs what it does", ""},
	{"What we mean by a fully loaded cost", ""},
	{"Rightsizing: what we check before we ask", ""},
}

func seedExplainers(db *sql.DB, c *Counts) error {
	have, err := crew.Explainers(db)
	if err != nil {
		return err
	}
	if len(have) > 0 {
		return nil
	}
	roster, err := crew.Roster(db)
	if err != nil {
		return err
	}
	var authors []string
	for _, a := range roster {
		if a.State == "active" && contains(a.Skills, "showback-narration") {
			authors = append(authors, a.Name)
		}
	}
	if len(authors) == 0 {
		for _, a := range roster {
			if a.State == "active" {
				authors = append(authors, a.Name)
			}
		}
	}
	sort.Strings(authors)
	if len(authors) == 0 {
		return nil
	}

	for i, t := range explainerTopics {
		team := world.Teams[pick(t.Topic+"|team", len(world.Teams))].Name
		author := authors[pick(t.Topic+"|author", len(authors))]
		amount := money.Cents(30000 + pick(t.Topic+"|cost", 40000))
		id, err := crew.Commission(db, team, t.Topic, "the team", author, amount)
		if err != nil {
			return err
		}
		c.Explainers++
		// Two published, one sent back for a rewrite with the reason, the
		// rest left in draft. That spread is the point: an explainer queue
		// where everything is published has never been reviewed.
		switch i % 3 {
		case 0:
			if err := crew.Publish(db, id, "supervisor"); err != nil {
				return err
			}
		case 1:
			if err := crew.ReturnExplainer(db, id,
				"Opens with the method. Open with what it cost them and what to do about it, "+
					"and put the method at the end."); err != nil {
				return err
			}
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- comments

var commentLines = []struct{ Who, Body string }{
	{"supervisor", "Take this one first. It is the largest open number on the desk."},
	{"", "Checked against the invoice: the figure ties to the cent. Posting."},
	{"", "Blocked on the tagging feed. I can give a number, I cannot give a team."},
	{"supervisor", "Returned. The saving is written as saved; it is found until somebody acts."},
	{"", "Re-ranked by money rather than by z-score, as asked. Ready for another look."},
	{"supervisor", "Agreed with the cause. Accepting, and the explainer can reuse this."},
}

// seedComments puts a conversation on the work.
//
// A task with a state and no discussion tells you what happened and never why.
// The why is the part somebody re-reads six months later.
func seedComments(db *sql.DB, c *Counts) error {
	// The board's schema, not this package's: comments belong to the crew and
	// the table may not exist yet on a store whose board has not been seeded.
	if _, err := db.Exec(crew.Schema); err != nil {
		return err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	tasks, err := crew.Tasks(db, crew.TaskFilter{})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		key := fmt.Sprintf("%d|%s", t.ID, t.Title)
		// About a third of the work carries a conversation. All of it would be
		// as unlike a real board as none of it.
		if pick(key+"|has", 3) != 0 {
			continue
		}
		n := 1 + pick(key+"|n", 3)
		for i := 0; i < n; i++ {
			line := commentLines[pick(key+fmt.Sprint(i), len(commentLines))]
			who := line.Who
			if who == "" {
				who = t.Assignee
			}
			if who == "" {
				continue
			}
			if err := crew.Comment_(db, t.ID, who, line.Body); err != nil {
				return err
			}
			c.Comments++
		}
	}
	return nil
}

// Now is here so a caller can stamp a run without this package reaching for
// the wall clock in the middle of a deterministic seeding.
func Now() string { return time.Now().UTC().Format("2006-01-02") }
