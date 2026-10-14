package web_test

// The audit page: the journal it verifies, and the count it shows of the
// governance stream it does not own. Both are read from files, so each test
// puts the file in the state the page must say something about.

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A journal the page cannot read is a refusal, never an audit that verified
// nothing and said it was clean.
func TestCovAuditSaysWhenTheJournalCannotBeRead(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	path := h.st.JournalPath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.get(t, "/audit")
	if code != http.StatusInternalServerError || !strings.Contains(body, "the journal could not be read") {
		t.Errorf("an unreadable journal: %d, want 500 saying it could not be read", code)
	}
}

var (
	auditMapped = regexp.MustCompile(`(\d+) of the (\d+) events on the stream`)
	auditOwn    = regexp.MustCompile(`The other (\d+) are this console`)
)

// Of the lines on the stream, the ones in the shared vocabulary (a run id is
// present) and this console's own are counted apart, and a line that does not
// parse is in neither count.
func TestCovAuditCountsTheStreamLinesApart(t *testing.T) {
	events := filepath.Join(t.TempDir(), "costcrew.ndjson")
	body := `{"type":"budget_threshold","run_id":"r1"}` + "\n" +
		`{"type":"practice_note"}` + "\n" +
		"\n" +
		"not json\n" +
		`{"type":"budget_threshold","run_id":"r2"}` + "\n"
	if err := os.WriteFile(events, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startStream(t, events)
	h.signUp(t, "owner", "owner-password-2026")

	code, page, _ := h.get(t, "/audit")
	if code != http.StatusOK {
		t.Fatalf("GET /audit with a stream: %d", code)
	}
	m := auditMapped.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the audit page does not say how many stream events are shared")
	}
	if m[1] != "2" || m[2] != "3" {
		t.Errorf("the page says %s of the %s stream events are shared, want 2 of 3", m[1], m[2])
	}
	o := auditOwn.FindStringSubmatch(page)
	if o == nil {
		t.Fatal("the audit page does not say how many stream events are this console's own")
	}
	if n, _ := strconv.Atoi(o[1]); n != 1 {
		t.Errorf("the page says %d events are this console's own, want 1", n)
	}
}
