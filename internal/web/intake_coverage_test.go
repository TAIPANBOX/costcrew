package web_test

// The budget intake, from a file to a set of budgets. The check shows what a
// file would do and writes nothing; the apply writes exactly the file that was
// checked, and only if the fingerprint shown then is the one it carries now.

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var covFingerprintField = regexp.MustCompile(`name="fingerprint" value="([^"]+)"`)

func TestCovIntakeWritesOnlyTheFileThatWasChecked(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.au.Create("reader", "reader-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	const file = "platform,team,month,budget_usd\naws,ml-platform,2026-10,1200\n"

	// A viewer reads the intake and sets nothing: neither the check nor the
	// apply will act for them.
	viewer := h.as(t, "reader", "reader-password-2026")
	refuse := "your account may read and export, but not act"
	code, loc := viewer.post(t, "/intake/check", url.Values{
		"csrf": {viewer.csrf(t, "/intake")}, "pasted": {file}})
	if code != http.StatusSeeOther || !covHasMsg(loc, refuse) {
		t.Errorf("a viewer's check: %d %s, want the refusal", code, loc)
	}
	code, loc = viewer.post(t, "/intake/apply", url.Values{
		"csrf": {viewer.csrf(t, "/intake")}, "file": {file}, "fingerprint": {"x"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, refuse) {
		t.Errorf("a viewer's apply: %d %s, want the refusal", code, loc)
	}

	// A file that is not a budget file is named as such at the check.
	code, loc = h.post(t, "/intake/check", url.Values{
		"csrf": {h.csrf(t, "/intake")}, "pasted": {"this is not a budget file\n"}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Errorf("a file that is not budgets: %d %s, want a message", code, loc)
	}

	// The check shows the file and its fingerprint, and writes nothing.
	csrf := h.csrf(t, "/intake")
	resp, err := h.c.PostForm(h.srv.URL+"/intake/check", url.Values{"csrf": {csrf}, "pasted": {file}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := covFingerprintField.FindStringSubmatch(string(page))
	if m == nil {
		t.Fatal("the check page carries no fingerprint to apply against")
	}
	fp := m[1]
	if n := covBudgetCents(t, h, "aws", "ml-platform", "2026-10"); n != 0 {
		t.Fatalf("the check wrote a budget of %d before anything was applied", n)
	}

	// A file that changed after it was checked is not written.
	code, loc = h.post(t, "/intake/apply", url.Values{
		"csrf": {csrf}, "file": {strings.Replace(file, "1200", "1300", 1)}, "fingerprint": {fp}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "this is not the file that was checked") {
		t.Errorf("an apply of a changed file: %d %s, want the mismatch refusal", code, loc)
	}
	if n := covBudgetCents(t, h, "aws", "ml-platform", "2026-10"); n != 0 {
		t.Errorf("the refused apply still wrote a budget of %d", n)
	}

	// A file that does not parse is refused at the apply too.
	code, loc = h.post(t, "/intake/apply", url.Values{
		"csrf": {csrf}, "file": {"garbage\n"}, "fingerprint": {"x"}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Errorf("an unreadable apply: %d %s, want a message", code, loc)
	}

	// The file that was checked is the one written, and it is journalled.
	code, loc = h.post(t, "/intake/apply", url.Values{
		"csrf": {csrf}, "file": {file}, "fingerprint": {fp}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/budgets") {
		t.Errorf("the checked apply: %d %s, want to the budgets page", code, loc)
	}
	if n := covBudgetCents(t, h, "aws", "ml-platform", "2026-10"); n != 120000 {
		t.Errorf("the checked file set %d cents, want 120000 (1200 dollars)", n)
	}
	journalled := false
	recs, err := h.st.JournalTail(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Event == "budgets_set" {
			journalled = true
		}
	}
	if !journalled {
		t.Error("setting budgets from a file left no budgets_set event in the journal")
	}
}

func covBudgetCents(t *testing.T, h *harness, source, team, month string) int64 {
	t.Helper()
	var cents int64
	err := h.st.DB().QueryRow(
		`SELECT budget_cents FROM budgets WHERE source=? AND team=? AND month=?`,
		source, team, month).Scan(&cents)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return cents
}
