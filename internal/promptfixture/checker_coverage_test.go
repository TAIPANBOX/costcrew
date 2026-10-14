package promptfixture

import (
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// newChecker builds the fixture and the checker over it. The caller closes the store.
func newChecker(t *testing.T) (*Checker, *sql.DB, func()) {
	t.Helper()
	st := fixtureCopy(t)
	c, err := NewChecker(st.DB())
	if err != nil {
		st.Close()
		t.Fatalf("NewChecker: %v", err)
	}
	return c, st.DB(), func() { st.Close() }
}

func hasPrefixIn(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// A prompt the gate has to refuse names the leak, where it was found, so a
// reader of a failing gate can see which column or marker reached the model.
func TestTheCheckerNamesEachKindOfLeak(t *testing.T) {
	c, _, done := newChecker(t)
	defer done()

	if got := c.Leaks("the invoice INV-2026-07-00417 was paid", ""); !reflect.DeepEqual(got,
		[]string{`identifier "INV-2026-07-00417" (charges.invoice_id)`}) {
		t.Errorf("an invoice id in text: Leaks = %q, want the one identifier and its column", got)
	}
	if got := c.Leaks("the note says "+PastBodyMarker, ""); !containsString(got, "free-text marker "+PastBodyMarker) {
		t.Errorf("a past-body marker: Leaks = %q, want the free-text marker named", got)
	}
	if got := c.Leaks(OperatorGoalText, ""); !containsString(got, "free-text marker "+OperatorGoalText) {
		t.Errorf("the operator's typed goal: Leaks = %q, want the free-text marker named", got)
	}
	if got := c.Leaks("token "+SecretToken, ""); !containsString(got, "secret "+SecretToken) {
		t.Errorf("a session token: Leaks = %q, want the secret named", got)
	}
	if got := c.Leaks("hash "+SecretHash, ""); !containsString(got, "secret "+SecretHash) {
		t.Errorf("a password hash: Leaks = %q, want the secret named", got)
	}
	if len(c.free) == 0 {
		t.Fatal("the checker holds no free text to look for; the fixture is not planting any")
	}
	if got := c.Leaks("the text quotes "+c.free[0], ""); !hasPrefixIn(got, "free text ") {
		t.Errorf("free text %q: Leaks = %q, want a free-text entry", c.free[0][:24], got)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// A text with nothing of the installation in it is not reported. Without
// this, a checker that reports everything would pass every other test here.
func TestTheCheckerLeavesCleanTextAlone(t *testing.T) {
	c, _, done := newChecker(t)
	defer done()
	const clean = "Quarterly summary of the figures, rounded to the nearest unit."
	if got := c.Leaks(clean, ""); len(got) != 0 {
		t.Errorf("clean text: Leaks = %q, want nothing", got)
	}
}

// The working analyst's own name is left out of the search on purpose: a
// restricting mode keeps the name of the person who asked. Any other name
// still counts.
func TestTheWorkingAnalystsOwnNameIsNotAHit(t *testing.T) {
	c, _, done := newChecker(t)
	defer done()
	const text = "alice filed the variance note"
	if got := c.Leaks(text, "alice"); hasPrefixIn(got, `identifier "alice"`) {
		t.Errorf("own name passed as own: Leaks = %q, want alice left out", got)
	}
	if got := c.Leaks(text, ""); !hasPrefixIn(got, `identifier "alice"`) {
		t.Errorf("no own name: Leaks = %q, want alice reported", got)
	}
	if got := c.Leaks("bob filed the variance note", "alice"); !hasPrefixIn(got, `identifier "bob"`) {
		t.Errorf("another name with alice as own: Leaks = %q, want bob still reported", got)
	}
}

// A one-letter identifier would match inside every word of a prompt, so the
// search skips it. A two-letter one is searched for.
func TestAOneLetterIdentifierIsSkippedAndATwoLetterOneIsNot(t *testing.T) {
	c := &Checker{
		ids:   []string{"x", "ai"},
		idsAt: map[string]string{"x": "test.one", "ai": "test.two"},
	}
	got := c.Leaks("x and ai and a box", "")
	if !reflect.DeepEqual(got, []string{`identifier "ai" (test.two)`}) {
		t.Errorf("Leaks = %q, want only the two-letter identifier", got)
	}
}

// Every distinct identifier is indexed once, and the checker holds exactly the
// values Of finds for identifiers, so no identifier the schema walk found is
// dropped on the way in.
func TestTheCheckerIndexesEachIdentifierOnce(t *testing.T) {
	c, db, done := newChecker(t)
	defer done()
	vals, err := Of(db, ID)
	if err != nil {
		t.Fatal(err)
	}
	want := Distinct(vals)
	if c.Identifiers() != len(want) {
		t.Errorf("Identifiers() = %d, want %d (the distinct identifiers the schema walk found)", c.Identifiers(), len(want))
	}
	seen := map[string]bool{}
	for _, id := range c.ids {
		if seen[id] {
			t.Errorf("identifier %q indexed twice", id)
		}
		seen[id] = true
	}
	if len(want) < 100 {
		t.Errorf("only %d identifiers; the fixture is meant to be a whole installation", len(want))
	}
}

// The checker keeps free text of 24 bytes or more, the length a marker-sized
// phrase needs to be recognisable, and leaves shorter phrases alone. The
// fixture holds a short phrase, so the filter is exercised rather than vacuous.
func TestTheCheckerSearchesOnlyFreeTextOfAtLeast24Bytes(t *testing.T) {
	c, db, done := newChecker(t)
	defer done()
	const short = "Read 1 file, 2 rows."
	free, err := Of(db, Free)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(Distinct(free), short) {
		t.Fatalf("the fixture no longer holds the short free text %q this test relies on", short)
	}
	for _, f := range c.free {
		if len(f) < 24 {
			t.Errorf("free text %q is %d bytes; the checker keeps only 24 or more", f, len(f))
		}
	}
	if got := c.Leaks("the run said "+short, ""); hasPrefixIn(got, "free text ") {
		t.Errorf("short free text: Leaks = %q, want no free-text entry", got)
	}
}

// Of is the one function every class of value passes through, so its own
// rules are pinned here: a name the prompt code calls "not an identifier" is
// not reported, the values come back sorted, and each value keeps every column
// it was found in.
func TestOfLeavesOutTheNamesThatAreNotIdentifiers(t *testing.T) {
	_, db, done := newChecker(t)
	defer done()
	var tax int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges WHERE service = 'Tax'`).Scan(&tax); err != nil {
		t.Fatal(err)
	}
	if tax == 0 {
		t.Fatal("the fixture holds no service named Tax, so the exclusion is not being tested")
	}
	ids, err := Of(db, ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range ids {
		if NotIdentifiers[v.Text] {
			t.Errorf("%q from %s came back as an identifier; NotIdentifiers excludes it", v.Text, v.Origin)
		}
	}
}

func TestOfReturnsEveryColumnAValueWasFoundIn(t *testing.T) {
	_, db, done := newChecker(t)
	defer done()
	ids, err := Of(db, ID)
	if err != nil {
		t.Fatal(err)
	}
	var origins []string
	for _, v := range ids {
		if v.Text == "alice" {
			origins = append(origins, v.Origin)
		}
	}
	for _, want := range []string{"users.username", "tasks.owner", "teams.owner"} {
		if !containsString(origins, want) {
			t.Errorf("alice's origins = %v; want %s among them", origins, want)
		}
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool {
		if ids[i].Text != ids[j].Text {
			return ids[i].Text < ids[j].Text
		}
		return ids[i].Origin < ids[j].Origin
	}) {
		t.Error("Of returned its values out of order")
	}
}

// A store the walk cannot read is an error. A gate that skipped it would be
// green over the table it never looked at.
func TestAStoreTheWalkCannotReadIsAnError(t *testing.T) {
	st := fixtureCopy(t)
	db := st.DB()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := UnclassifiedColumns(db); err == nil {
		t.Error("UnclassifiedColumns on a closed store returned no error")
	}
	if _, err := Of(db, ID); err == nil {
		t.Error("Of on a closed store returned no error")
	}
	if _, err := NewChecker(db); err == nil {
		t.Error("NewChecker on a closed store returned no error")
	}
}

// Exactly the columns that are not classified are named. A column added to the
// schema tomorrow has to show up here, and a column that is classified must not.
func TestAColumnNobodyClassifiedIsNamed(t *testing.T) {
	st := fixtureCopy(t)
	defer st.Close()
	db := st.DB()
	if _, err := db.Exec(`ALTER TABLE charges ADD COLUMN surprise TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE scratch_untyped(note)`); err != nil {
		t.Fatal(err)
	}
	got, err := UnclassifiedColumns(db)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"charges.surprise", "scratch_untyped.note"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnclassifiedColumns = %q, want %q", got, want)
	}
}

// A column whose name the walk cannot put in a query is classified as free
// text here on purpose, so Of has to reach it. It must fail, not skip it, and
// NewChecker must fail with it. Not parallel: it adds to Classes for this test.
func TestAColumnTheWalkCannotQueryIsAnError(t *testing.T) {
	st := fixtureCopy(t)
	defer st.Close()
	db := st.DB()
	if _, err := db.Exec(`ALTER TABLE charges ADD COLUMN "odd""name" TEXT`); err != nil {
		t.Fatal(err)
	}
	const key = `charges.odd"name`
	Classes[key] = Free
	defer delete(Classes, key)

	if _, err := Of(db, Free); err == nil {
		t.Error("Of returned no error for a free-text column it cannot query; it skipped it")
	}
	if _, err := NewChecker(db); err == nil {
		t.Error("NewChecker returned no error when a free-text column cannot be read")
	}
}

// A table whose name the walk cannot put in a PRAGMA is an error from every
// walk, not a table the gate quietly does not see.
func TestATableTheWalkCannotReadIsAnError(t *testing.T) {
	st := fixtureCopy(t)
	defer st.Close()
	db := st.DB()
	if _, err := db.Exec(`CREATE TABLE "odd""table"(note TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := UnclassifiedColumns(db); err == nil {
		t.Error("UnclassifiedColumns returned no error for a table it cannot read")
	}
	if _, err := Of(db, ID); err == nil {
		t.Error("Of returned no error for a table it cannot read")
	}
}
