package typryx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

// TestAPassRecordsWhatLeftForTypryx (invariant 92): the fields that left, and
// how many typryx held back, are kept on the anomaly's row beside the hint,
// so the anomaly's page can say what was sent; an ask that never reached
// typryx records that nothing left.
func TestAPassRecordsWhatLeftForTypryx(t *testing.T) {
	db := hintedDB(t)
	a := plant(t, db, "A-sent")
	f := newFake(t, []string{"anomaly", "recent_changes"}, answerWith("jev", "", "misconfiguration", 0.7))
	HintAnomalies(context.Background(), db, f.client(), []string{a.ID}, &capture{})
	h, ok, err := anomaly.HintOf(db, a.ID)
	if err != nil || !ok {
		t.Fatalf("no hint stored: %v", err)
	}
	if !h.FieldsKnown || h.FieldsSent != "anomaly,recent_changes" || h.HeldBack != 0 {
		t.Errorf("fields recorded with an answered hint: known %v, sent %q, held back %d", h.FieldsKnown, h.FieldsSent, h.HeldBack)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	down := plant(t, db, "A-down-pass")
	HintAnomalies(context.Background(), db, New(base, "", time.Second), []string{down.ID}, &capture{})
	h, ok, err = anomaly.HintOf(db, down.ID)
	if err != nil || !ok || h.Answered() {
		t.Fatalf("an unreachable typryx: %+v %v %v", h, ok, err)
	}
	if !h.FieldsKnown || h.FieldsSent != "" {
		t.Errorf("an ask that never reached typryx: known %v, sent %q, want known and nothing", h.FieldsKnown, h.FieldsSent)
	}
}
