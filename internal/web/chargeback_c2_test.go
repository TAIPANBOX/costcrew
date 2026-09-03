package web_test

// The chargeback page: C2-SPEC.md section 2, "the chargeback page shows the
// last close pack's figures beside the live ones." Red against unchanged
// code: today the page never mentions any period but the one it is showing.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func TestChargebackPageShowsTheLastCloseBesideTheLive(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	months, err := finops.Months(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(months) < 3 {
		t.Skip("the seeded estate needs at least three distinct months for this test")
	}
	closeMonth := months[len(months)-1] // the oldest: definitely not what's being viewed
	viewMonth := months[1]              // "the last closed one" period() defaults to
	if err := finops.Close(h.st.DB(), closeMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	frozen, err := finops.FrozenPeriod(h.st.DB(), closeMonth)
	if err != nil {
		t.Fatal(err)
	}

	// closeMonth alone is a weak check: every month, closed or not, already
	// appears as an <option> in the period selector. The frozen TOTAL beside
	// it is what only "shows the last close pack's figures" can produce.
	_, body, _ := h.get(t, "/chargeback?period="+viewMonth)
	if !strings.Contains(body, closeMonth) {
		t.Errorf("viewing %s, the page does not mention the last closed period %s:\n%s",
			viewMonth, closeMonth, body)
	}
	if !strings.Contains(body, frozen.Total.String()) {
		t.Errorf("viewing %s, the page does not carry the last close's own total %s:\n%s",
			viewMonth, frozen.Total, body)
	}
}

func TestChargebackPageSaysNoPreviousCloseWhenNoneExists(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	closed, err := finops.ClosedPeriods(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Skip("this test needs an estate with nothing closed yet")
	}

	_, body, _ := h.get(t, "/chargeback")
	if !strings.Contains(strings.ToLower(body), "no period has been closed") {
		t.Errorf("with nothing ever closed, the page does not say so:\n%s", body)
	}
}
