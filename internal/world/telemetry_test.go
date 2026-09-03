package world

// C6-SPEC.md. Licence.NoticeDeadline is read by internal/finops.Licences and
// internal/deliver's renewalsSection on an IMPORTED row; these two tests
// hold the computation directly, in the package that owns it, rather than
// only through those two callers.

import "testing"

func TestLicenceNoticeDeadlineIsRenewsMinusNoticeDays(t *testing.T) {
	l := Licence{Renews: "2026-09-03", NoticeDays: 15}
	if got := l.NoticeDeadline(); got != "2026-08-19" {
		t.Errorf("NoticeDeadline() = %q, want 2026-08-19 (15 days before 2026-09-03)", got)
	}
}

func TestLicenceNoticeDeadlineOfZeroDaysIsTheRenewalDateItself(t *testing.T) {
	l := Licence{Renews: "2026-12-02", NoticeDays: 0}
	if got := l.NoticeDeadline(); got != "2026-12-02" {
		t.Errorf("NoticeDeadline() = %q, want 2026-12-02 (no notice period at all)", got)
	}
}

// A generated row (world.Licences' own buildLicences) never sets Renews to
// anything but a real vendor date, so this is a row NOTHING in this
// package produces; it exists because a caller handed one to this method by
// mistake must get "", never a wrong date computed from an empty string.
func TestLicenceNoticeDeadlineOnAnUnparseableRenewsIsEmpty(t *testing.T) {
	for _, renews := range []string{"", "not-a-date", "2026-13-40"} {
		l := Licence{Renews: renews, NoticeDays: 10}
		if got := l.NoticeDeadline(); got != "" {
			t.Errorf("NoticeDeadline() on Renews=%q = %q, want empty", renews, got)
		}
	}
}
