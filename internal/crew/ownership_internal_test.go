package crew

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func ownershipDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// One call, and it is expected to stand up everything it needs. That it
	// does is the point of the assertion below rather than an accident of the
	// order things happen to be called in elsewhere.
	if err := EnsureOwnershipHistory(st.DB()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tasks", "analysts"} {
		var n int
		if err := st.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
			table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("EnsureOwnershipHistory left no %s table (%v); it touches "+
				"both and must not depend on another startup step running first",
				table, err)
		}
	}
	return st.DB()
}

// ownerAt has to see writes made in its own transaction.
//
// That is the whole reason it exists beside OwnerOf. The seeding paths build
// the roster and the board together inside one transaction, and a read on the
// *sql.DB from inside that transaction does not see the roster rows that
// transaction has just written: every charge would then be stamped with an
// empty owner and the history would start out blank on a fresh install.
func TestOwnerAtSeesItsOwnTransaction(t *testing.T) {
	db := ownershipDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO analysts(name, role, desk, state, owner) VALUES (?,?,?,?,?)`,
		"newcomer", "analyst", "ai", "active", "j.ashby"); err != nil {
		t.Fatal(err)
	}
	if got := ownerAt(tx, "newcomer"); got != "j.ashby" {
		t.Errorf("ownerAt read %q inside the transaction that wrote the agent; "+
			"every charge stamped in a seeding transaction would be blank", got)
	}
	// And the same read from outside that transaction correctly sees nothing,
	// which is what makes the paragraph above true rather than a guess.
	if got := OwnerOf(db, "newcomer"); got != "" {
		t.Errorf("OwnerOf read %q for an agent whose insert has not committed; "+
			"then ownerAt would not be needed and this test is testing nothing",
			got)
	}
}

// An agent that is not there is not an error and not a panic.
func TestOwnerAtOnAnAgentThatIsNotThere(t *testing.T) {
	db := ownershipDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if got := ownerAt(tx, "nobody-hired-this"); got != "" {
		t.Errorf("ownerAt invented %q for an agent that does not exist", got)
	}
	if got := OwnerOf(db, "nobody-hired-this"); got != "" {
		t.Errorf("OwnerOf invented %q for an agent that does not exist", got)
	}
}

// An agent whose owner is NULL reads as empty rather than failing the scan.
//
// Every row written before the column existed is in this state until the
// backfill runs, and a scan error there would take down the page rather than
// show the gap.
func TestOwnerAtOnANullOwner(t *testing.T) {
	db := ownershipDB(t)
	if _, err := db.Exec(
		`INSERT INTO analysts(name, role, desk, state) VALUES (?,?,?,?)`,
		"unclaimed", "analyst", "ai", "active"); err != nil {
		t.Fatal(err)
	}
	if got := OwnerOf(db, "unclaimed"); got != "" {
		t.Errorf("OwnerOf read %q from a NULL owner", got)
	}
}

// charge puts one task on the board, owned by whoever the caller says, in the
// sprint given. The sprint carries the month, which is what the period filter
// reads: a charge is dated by the sprint it belongs to, not by a column on
// itself.
func charge(t *testing.T, db *sql.DB, sprint int, agent, owner string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks
		(sprint, title, assignee, desk, state, budget_cents, spent_cents, owner)
		VALUES (?,?,?,'ai','posted',0,?,?)`,
		sprint, "work for "+agent, agent, cents, owner); err != nil {
		t.Fatal(err)
	}
}

func sprintIn(t *testing.T, db *sql.DB, id int, month string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sprints(id, label, start, finish, state) VALUES (?,?,?,?, 'closed')`,
		id, month, month+"-01", month+"-28"); err != nil {
		t.Fatal(err)
	}
}

// SpendByOwner counts the owner RECORDED on the charge.
//
// This is the whole point of the column. The agent below is owned by
// t.langley now and every one of its charges was authorised by j.ashby, so a
// function that read the roster would answer t.langley and rewrite what two
// people are answerable for.
func TestSpendByOwnerReadsTheChargeNotTheRoster(t *testing.T) {
	db := ownershipDB(t)
	sprintIn(t, db, 1, "2026-07")
	if _, err := db.Exec(
		`INSERT INTO analysts(name, role, desk, state, owner) VALUES (?,?,?,?,?)`,
		"handed-over", "analyst", "ai", "active", "t.langley"); err != nil {
		t.Fatal(err)
	}
	charge(t, db, 1, "handed-over", "j.ashby", 1000)
	charge(t, db, 1, "handed-over", "j.ashby", 500)

	got, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if got["j.ashby"] != 1500 {
		t.Errorf("j.ashby authorised 1500 and carries %d", got["j.ashby"])
	}
	if got["t.langley"] != 0 {
		t.Errorf("t.langley holds the agent today and carries %d for charges "+
			"somebody else authorised", got["t.langley"])
	}
}

// The period filter selects, and it selects by the sprint's month.
func TestSpendByOwnerFiltersByMonth(t *testing.T) {
	db := ownershipDB(t)
	sprintIn(t, db, 1, "2026-06")
	sprintIn(t, db, 2, "2026-07")
	charge(t, db, 1, "a", "j.ashby", 700)
	charge(t, db, 2, "a", "j.ashby", 300)

	june, err := SpendByOwner(db, "2026-06")
	if err != nil {
		t.Fatal(err)
	}
	july, err := SpendByOwner(db, "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	ever, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if june["j.ashby"] != 700 || july["j.ashby"] != 300 {
		t.Errorf("June %d and July %d, want 700 and 300",
			june["j.ashby"], july["j.ashby"])
	}
	// The months have to add up to the whole, or one of them is dropping work.
	if june["j.ashby"]+july["j.ashby"] != ever["j.ashby"] {
		t.Errorf("the months sum to %d and everything is %d",
			june["j.ashby"]+july["j.ashby"], ever["j.ashby"])
	}
	// A month with nothing in it is an empty result, not last month's.
	if v, ok := SpendByOwnerMust(t, db, "2026-05")["j.ashby"]; ok && v != 0 {
		t.Errorf("May reports %d against an empty month", v)
	}
}

func SpendByOwnerMust(t *testing.T, db *sql.DB, period string) map[string]money.Cents {
	t.Helper()
	out, err := SpendByOwner(db, period)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Work charged to nobody comes back under "", so a caller can see it.
//
// Dropping it in the query would make every total quietly short by the amount
// nobody is answerable for, which is exactly the amount worth knowing about.
func TestSpendByOwnerKeepsWhatBelongsToNobody(t *testing.T) {
	db := ownershipDB(t)
	sprintIn(t, db, 1, "2026-07")
	charge(t, db, 1, "a", "j.ashby", 400)
	charge(t, db, 1, "", "", 60) // an unassigned task, owner empty
	if _, err := db.Exec(`INSERT INTO tasks
		(sprint, title, state, spent_cents) VALUES (1,'no owner column at all','posted',9)`); err != nil {
		t.Fatal(err) // owner NULL rather than empty, which is the pre-backfill shape
	}

	got, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if got[""] != 69 {
		t.Errorf("work belonging to nobody totals %d, want 69: both the empty "+
			"string and NULL have to land in the same bucket", got[""])
	}
	// And nothing has gone missing between the two views.
	var total int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(spent_cents),0) FROM tasks`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	var sum money.Cents
	for _, v := range got {
		sum += v
	}
	if int64(sum) != total {
		t.Errorf("the owners add up to %d and the board holds %d", sum, total)
	}
}

// An empty board is an empty answer, not an error and not a nil map somebody
// has to check before indexing.
func TestSpendByOwnerOnAnEmptyBoard(t *testing.T) {
	db := ownershipDB(t)
	got, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("SpendByOwner returned a nil map on an empty board")
	}
	if len(got) != 0 {
		t.Errorf("an empty board reports %v", got)
	}
	if got["anybody"] != 0 {
		t.Errorf("an absent owner reads as %d", got["anybody"])
	}
}

// The migration runs on every start, so running it again must be harmless.
//
// Two specific ways it could not be: the ALTER fails on the second run because
// the column is already there, which is why the duplicate-column case is
// swallowed; and the backfill runs again over rows whose owner has since been
// changed by a transfer, which would undo every handover on restart. The
// second is guarded by WHERE owner IS NULL and is the one worth pinning,
// because "backfill from the roster" is a sentence that reads as harmless.
func TestEnsureOwnershipHistoryIsSafeToRunAgain(t *testing.T) {
	db := ownershipDB(t)
	sprintIn(t, db, 1, "2026-07")
	if _, err := db.Exec(
		`INSERT INTO analysts(name, role, desk, state, owner) VALUES (?,?,?,?,?)`,
		"held", "analyst", "ai", "active", "t.langley"); err != nil {
		t.Fatal(err)
	}
	// A charge that has been handed over: the roster says t.langley, the
	// charge says j.ashby, and they are meant to differ.
	charge(t, db, 1, "held", "j.ashby", 250)

	for i := 0; i < 3; i++ {
		if err := EnsureOwnershipHistory(db); err != nil {
			t.Fatalf("run %d: %v", i+2, err)
		}
	}
	got, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if got["j.ashby"] != 250 {
		t.Errorf("after three more runs the charge belongs to %v; a restart "+
			"re-derived it from the roster and undid the handover", got)
	}
}

// The backfill fills what was never stamped, and only that.
//
// Every row written before the column existed has a NULL owner. Left alone,
// the estate would show its entire history as belonging to nobody, which is
// worse than the current-owner guess it replaces: at least the guess adds up.
func TestEnsureOwnershipHistoryBackfillsOnlyWhatIsUnstamped(t *testing.T) {
	db := ownershipDB(t)
	sprintIn(t, db, 1, "2026-07")
	if _, err := db.Exec(
		`INSERT INTO analysts(name, role, desk, state, owner) VALUES (?,?,?,?,?)`,
		"old-hand", "analyst", "ai", "active", "a.whitfield"); err != nil {
		t.Fatal(err)
	}
	// One row from before the column existed, and one already stamped to
	// somebody else.
	if _, err := db.Exec(`INSERT INTO tasks
		(sprint, title, assignee, desk, state, spent_cents)
		VALUES (1,'from before','old-hand','ai','posted',100)`); err != nil {
		t.Fatal(err)
	}
	charge(t, db, 1, "old-hand", "j.calder", 40)

	if err := EnsureOwnershipHistory(db); err != nil {
		t.Fatal(err)
	}
	got, err := SpendByOwner(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if got["a.whitfield"] != 100 {
		t.Errorf("the unstamped charge went to %v; it should follow the roster, "+
			"which is the only answer available about a row written before the "+
			"column existed", got)
	}
	if got["j.calder"] != 40 {
		t.Errorf("the already-stamped charge was overwritten: %v", got)
	}
	if got[""] != 0 {
		t.Errorf("%d is still stamped to nobody after the backfill", got[""])
	}
}
