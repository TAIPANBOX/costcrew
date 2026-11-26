package crew_test

// Error and boundary paths of the crew's decisions: settling live spend,
// dropping retired rights, blocking and returning work, moving an analyst,
// and marking an option. Each test reads back what was written, so a
// decision that silently did nothing (or did the wrong thing) goes red.

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func spentAndBooked(t *testing.T, db *sql.DB, id int) (spent, booked int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT spent_cents, live_cents FROM tasks WHERE id=?`, id).
		Scan(&spent, &booked); err != nil {
		t.Fatal(err)
	}
	return spent, booked
}

// The run's total is rounded up once and handed out by largest remainder;
// a second settle writes nothing, and new spend books only the difference.
func TestSettleLiveSpendRoundsTheRunOnceAndBooksOnlyTheDifference(t *testing.T) {
	db := optionsTestDB(t)
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second ensure must not fail on the columns it added.
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatalf("second EnsureLiveSpendLedger: %v", err)
	}

	// Nothing live yet: nothing to book.
	if got, err := crew.SettleLiveSpend(db); err != nil || got != 0 {
		t.Fatalf("empty ledger settled %d, %v; want 0, nil", got, err)
	}

	a, b, c := plantPlainTask(t, db), plantPlainTask(t, db), plantPlainTask(t, db)
	set := func(id int, micros int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE tasks SET live_micros=? WHERE id=?`, micros, id); err != nil {
			t.Fatal(err)
		}
	}
	// 0.40 + 0.40 + 0.30 of a cent: the run cost 1.1 cents, rounded up to 2.
	// The two largest remainders (a and b, tied, broken by id) get them.
	set(a, 4_000)
	set(b, 4_000)
	set(c, 3_000)

	got, err := crew.SettleLiveSpend(db)
	if err != nil {
		t.Fatal(err)
	}
	if got != money.Cents(2) {
		t.Fatalf("booked %d cents for a run of 1.1 cents, want 2 (rounded up once)", got)
	}
	for _, w := range []struct {
		id    int
		spent int64
	}{{a, 1}, {b, 1}, {c, 0}} {
		if spent, booked := spentAndBooked(t, db, w.id); spent != w.spent || booked != w.spent {
			t.Errorf("task %d: spent %d booked %d, want %d", w.id, spent, booked, w.spent)
		}
	}

	// Settling again changes nothing.
	if _, err := crew.SettleLiveSpend(db); err != nil {
		t.Fatal(err)
	}
	if spent, _ := spentAndBooked(t, db, a); spent != 1 {
		t.Errorf("a second settle moved task %d to %d cents: it is not idempotent", a, spent)
	}

	// c spends another cent: the run is now 2.1 cents, rounded up to 3. c's
	// whole cent is its own, and only that difference is written.
	set(c, 13_000)
	got, err = crew.SettleLiveSpend(db)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("booked %d after c's extra cent, want 3", got)
	}
	if spent, booked := spentAndBooked(t, db, c); spent != 1 || booked != 1 {
		t.Errorf("task c: spent %d booked %d, want 1 and 1", spent, booked)
	}
	if spent, _ := spentAndBooked(t, db, a); spent != 1 {
		t.Errorf("task a moved to %d cents when only c spent more", spent)
	}
}

func TestSettleLiveSpendWithoutTheLedgerFails(t *testing.T) {
	db := optionsTestDB(t) // no EnsureLiveSpendLedger: no live_micros column
	if _, err := crew.SettleLiveSpend(db); err == nil {
		t.Fatal("settled against a tasks table with no live_micros column")
	}
}

func TestDropRetiredRightsRemovesOnlyTheRetiredOne(t *testing.T) {
	db, _ := withRoster(t)
	if _, err := db.Exec(`UPDATE analysts SET rights='figures-read, requests-read ,sql-readonly'
		WHERE name='triage-aws'`); err != nil {
		t.Fatal(err)
	}
	n, err := crew.DropRetiredRights(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("changed %d analysts, want exactly the one holding requests-read", n)
	}
	a, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Rights, ","); got != "figures-read,sql-readonly" {
		t.Errorf("rights after the drop: %q, want figures-read,sql-readonly", got)
	}
	other, err := crew.GetAnalyst(db, "triage-gcp")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(other.Rights, ","); got != "figures-read,propose-only,sql-readonly" {
		t.Errorf("an analyst without the retired right was rewritten: %q", got)
	}
	// Nothing left to drop.
	if n, err := crew.DropRetiredRights(db); err != nil || n != 0 {
		t.Errorf("second run changed %d (%v), want 0", n, err)
	}
}

func TestBlockNeedsAReasonAndAnOpenTask(t *testing.T) {
	db := optionsTestDB(t)
	id := plantPlainTask(t, db)

	if err := crew.Block(db, id, "   "); !errors.Is(err, crew.ErrNeedReason) {
		t.Errorf("blank reason: %v, want ErrNeedReason", err)
	}
	if err := crew.Block(db, id+100, "waiting on the tagging feed"); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("missing task: %v, want ErrNotFound", err)
	}
	if err := crew.Block(db, id, "waiting on the tagging feed"); err != nil {
		t.Fatal(err)
	}
	got, err := crew.GetTask(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != crew.Blocked || got.Reason != "waiting on the tagging feed" {
		t.Errorf("after Block: state %q reason %q", got.State, got.Reason)
	}

	if _, err := db.Exec(`UPDATE tasks SET state='posted' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := crew.Block(db, id, "too late"); !errors.Is(err, crew.ErrSettled) {
		t.Errorf("blocking posted work: %v, want ErrSettled", err)
	}
}

func TestReturnRefusesWhatItMayNotDecide(t *testing.T) {
	db := optionsTestDB(t)
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	task := plantPlainTask(t, db)
	art := plantDraftArtifact(t, db, task, "a deliverable")

	if err := crew.Return(db, art, "", "owner"); !errors.Is(err, crew.ErrNeedReason) {
		t.Errorf("blank reason: %v, want ErrNeedReason", err)
	}
	if err := crew.Return(db, art, "rewrite it", "no-such-role"); !errors.Is(err, crew.ErrMayNotDecide) {
		t.Errorf("unknown link: %v, want ErrMayNotDecide", err)
	}
	if err := crew.Return(db, art+100, "rewrite it", "owner"); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("missing artifact: %v, want ErrNotFound", err)
	}

	if err := crew.Return(db, art, "open with the cost", "owner"); err != nil {
		t.Fatal(err)
	}
	tk, err := crew.GetTask(db, task)
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != crew.Returned || tk.Reason != "open with the cost" {
		t.Errorf("task after Return: state %q reason %q", tk.State, tk.Reason)
	}
	as, err := crew.Artifacts(db, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || as[0].State != crew.ReturnedDraft || as[0].Reason != "open with the cost" {
		t.Errorf("artifact after Return: %+v", as)
	}

	if err := crew.Post(db, art, "a-person", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := crew.Return(db, art, "again", "owner"); !errors.Is(err, crew.ErrSettled) {
		t.Errorf("returning a posted draft: %v, want ErrSettled", err)
	}
}

func TestTransferRefusals(t *testing.T) {
	db, _ := withRoster(t)
	cases := []struct {
		name, who, desk, owner, parent, want string
	}{
		{"unknown analyst", "nobody-here", "gcp", "", "", "no such analyst"},
		{"nothing changes", "triage-aws", "", "", "", "nothing would change"},
		{"under itself", "triage-aws", "", "", "triage-aws", "cannot act under itself"},
		{"unknown parent", "triage-aws", "", "", "ghost", `no analyst called "ghost"`},
		{"a loop", "partner-aws", "", "", "triage-aws", "would be a loop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := crew.Transfer(db, c.who, c.desk, c.owner, c.parent, "a-person")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Transfer: %v, want an error containing %q", err, c.want)
			}
		})
	}
	// Refused transfers wrote nothing.
	a, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if a.Desk != "aws" || a.Parent != "partner-aws" {
		t.Errorf("a refused transfer moved triage-aws: desk %q parent %q", a.Desk, a.Parent)
	}
}

// Moving off a desk whose partner it answered to, onto a desk with no
// partner, puts it under the supervisor rather than leaving the old partner.
func TestTransferToADeskWithNoPartnerAnswersToTheSupervisor(t *testing.T) {
	db, _ := withRoster(t)
	if _, err := crew.Transfer(db, "triage-aws", "onprem", "", "", "a-person"); err != nil {
		t.Fatal(err)
	}
	a, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if a.Desk != "onprem" || a.Parent != "supervisor" {
		t.Errorf("after the move: desk %q parent %q, want onprem under supervisor", a.Desk, a.Parent)
	}
}

func TestValidateAndSaveOptionsRefusesAnUnknownWriterAndAnEmptyBlock(t *testing.T) {
	db := optionsTestDB(t)
	task := plantPlainTask(t, db)

	art := plantDraftArtifact(t, db, task, validOptionsBlock)
	refused, reason, err := crew.ValidateAndSaveOptions(db, art, "a-hand-hire", validOptionsBlock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused || !strings.Contains(reason, "no job description is on file") {
		t.Errorf("an unknown writer's block: refused=%v reason=%q", refused, reason)
	}

	empty := "## Nothing to decide\n\n```options\n{\"options\": []}\n```\n"
	art2 := plantDraftArtifact(t, db, task, empty)
	refused, reason, err = crew.ValidateAndSaveOptions(db, art2, "investigator-aws", empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused || !strings.Contains(reason, "the block named zero") {
		t.Errorf("an investigator's empty block: refused=%v reason=%q", refused, reason)
	}
}

func TestMarkOptionAppliedAndRefused(t *testing.T) {
	db := optionsTestDB(t)
	task := plantPlainTask(t, db)
	art := plantDraftArtifact(t, db, task, validOptionsBlock)
	if refused, reason, err := crew.ValidateAndSaveOptions(db, art, "investigator-aws", validOptionsBlock, nil); err != nil || refused {
		t.Fatalf("setup: refused=%v reason=%q err=%v", refused, reason, err)
	}
	opts, err := crew.Options(db, art)
	if err != nil || len(opts) != 1 {
		t.Fatalf("setup: %d options, %v", len(opts), err)
	}
	ord := opts[0].Ordinal

	if err := crew.MarkOptionRefused(db, art, ord, "a-person", " "); !errors.Is(err, crew.ErrNeedReason) {
		t.Errorf("refusal without a reason: %v, want ErrNeedReason", err)
	}
	if err := crew.MarkOptionApplied(db, art, ord+7, "a-person"); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("applying an option that does not exist: %v, want ErrNotFound", err)
	}
	if err := crew.MarkOptionRefused(db, art, ord, "a-person", "the cause is elsewhere"); err != nil {
		t.Fatal(err)
	}
	opts, _ = crew.Options(db, art)
	if opts[0].State != crew.OptionRefused || opts[0].Reason != "the cause is elsewhere" || opts[0].DecidedBy != "a-person" {
		t.Errorf("after refusal: %+v", opts[0])
	}
	if err := crew.MarkOptionApplied(db, art, ord, "a-person"); err != nil {
		t.Fatal(err)
	}
	opts, _ = crew.Options(db, art)
	if opts[0].State != crew.OptionApplied || opts[0].Reason != "" {
		t.Errorf("after apply: state %q reason %q", opts[0].State, opts[0].Reason)
	}
}
