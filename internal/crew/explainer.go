package crew

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// An explainer is a cost story written for the team that has to act on it,
// not for the FinOps team that found it.
//
// This is the piece most consoles skip, and it is the reason findings do not
// turn into changes. A variance report says "EC2 in ml-platform rose 34% month
// on month"; the team reads that, cannot tell whether it is their fault or
// what to do, and does nothing. An explainer says what happened, what it cost,
// and what one named person could do about it this week.
//
// It is commissioned, drafted and PUBLISHED BY STAMP, like every other
// deliverable: something written in a team's name and sent without review is
// how a FinOps practice loses a team it needs.
type Explainer struct {
	ID        int
	Team      string
	Topic     string
	Audience  string
	Author    string
	Body      string
	State     string // draft, returned, published
	Reason    string
	Amount    money.Cents // the money the story is about
	Created   string
	Published string
	Publisher string
}

const ExplainerSchema = `
CREATE TABLE IF NOT EXISTS explainers(
  id INTEGER PRIMARY KEY, team TEXT NOT NULL, topic TEXT NOT NULL,
  audience TEXT, author TEXT, body TEXT, state TEXT NOT NULL, reason TEXT,
  amount_cents INTEGER DEFAULT 0, created TEXT, published TEXT, publisher TEXT);
`

func Explainers(db *sql.DB) ([]Explainer, error) {
	if _, err := db.Exec(ExplainerSchema); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, team, topic, COALESCE(audience,''),
		COALESCE(author,''), COALESCE(body,''), state, COALESCE(reason,''),
		COALESCE(amount_cents,0), COALESCE(created,''), COALESCE(published,''),
		COALESCE(publisher,'') FROM explainers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Explainer
	for rows.Next() {
		var e Explainer
		var amt int64
		if err := rows.Scan(&e.ID, &e.Team, &e.Topic, &e.Audience, &e.Author,
			&e.Body, &e.State, &e.Reason, &amt, &e.Created, &e.Published,
			&e.Publisher); err != nil {
			return nil, err
		}
		e.Amount = money.Cents(amt)
		out = append(out, e)
	}
	return out, rows.Err()
}

func GetExplainer(db *sql.DB, id int) (Explainer, error) {
	all, err := Explainers(db)
	if err != nil {
		return Explainer{}, err
	}
	for _, e := range all {
		if e.ID == id {
			return e, nil
		}
	}
	return Explainer{}, fmt.Errorf("no such explainer")
}

// Commission asks an analyst for a story and drafts it immediately.
//
// The draft is written here rather than by a model because the console must
// work with no engine configured at all: an empty page that says "run an
// agent" teaches nobody what an explainer is for. With an engine attached this
// is what the agent replaces.
func Commission(db *sql.DB, team, topic, audience, author string, amount money.Cents) (int, error) {
	if strings.TrimSpace(team) == "" || strings.TrimSpace(topic) == "" {
		return 0, fmt.Errorf("an explainer needs a team and a topic: it is written FOR somebody")
	}
	if strings.TrimSpace(author) == "" {
		return 0, fmt.Errorf("an explainer with no author is one nobody can ask about")
	}
	if _, err := db.Exec(ExplainerSchema); err != nil {
		return 0, err
	}
	res, err := db.Exec(`INSERT INTO explainers
		(team, topic, audience, author, body, state, amount_cents, created)
		VALUES (?,?,?,?,?,?,?,?)`,
		team, topic, audience, author, draftBody(team, topic, amount),
		"draft", int64(amount), time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

// draftBody is written the way an explainer should be: plainly, for the team,
// ending in something one person can do this week.
func draftBody(team, topic string, amount money.Cents) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", topic)
	fmt.Fprintf(&b, "This is for the **%s** team. It is about %s a month, and it is "+
		"not a criticism: nobody chose this, it accumulated.\n\n", team, amount)
	b.WriteString("**What happened.** Something in your area started costing more, and " +
		"the change is large enough to be worth a conversation rather than a dashboard. " +
		"The figure above is what it is costing now, every month, until somebody acts.\n\n")
	b.WriteString("**Why it is yours.** The cost carries your team's tag. That is the " +
		"only reason it is addressed to you, and if the tag is wrong then telling us so " +
		"is the most useful thing you can do with this page.\n\n")
	b.WriteString("**What one person could do this week.** Confirm whether the workload " +
		"is still needed. If it is, we will size it properly with you and stop asking. " +
		"If it is not, turning it off takes an afternoon and the money stops.\n\n")
	b.WriteString("**What we are not saying.** We are not saying you spent too much, and " +
		"we are not counting this as a saving. It is money found. Nothing is saved " +
		"until somebody acts on it.\n")
	return b.String()
}

// Publish is the stamp: an explainer only reaches a team once a person has
// read it. Recorded as the PERSON's act, not the analyst's.
func Publish(db *sql.DB, id int, by string) error {
	e, err := GetExplainer(db, id)
	if err != nil {
		return err
	}
	if e.State == "published" {
		return fmt.Errorf("this is already published, and a published explainer is not unpublished")
	}
	_, err = db.Exec(`UPDATE explainers SET state='published', published=?, publisher=?,
		reason=NULL WHERE id=?`, time.Now().UTC().Format(time.RFC3339), by, id)
	return err
}

// ReturnExplainer sends it back with what has to change.
func ReturnExplainer(db *sql.DB, id int, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("returning an explainer needs a reason: the reason IS what " +
			"the analyst is meant to act on")
	}
	e, err := GetExplainer(db, id)
	if err != nil {
		return err
	}
	if e.State == "published" {
		return fmt.Errorf("this is already published")
	}
	_, err = db.Exec(`UPDATE explainers SET state='returned', reason=? WHERE id=?`, reason, id)
	return err
}
