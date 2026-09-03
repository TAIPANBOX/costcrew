package main

// Per-case orchestration: build the task and the bench packet the same way
// production would, then synthesize a mock deliverable (mock.go). B7-SPEC.md
// section 2, steps 2 and 3. There is no live path here: see main.go's run()
// for why -live with a real engine refuses before reaching any of this.

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// taskFor is the in-memory (never inserted) task a case's packet and
// prompt are built against: title and goal in the same shape crew.Seed
// would have written from an anomaly.AnomalySeed, so a bench packet looks
// exactly like a real one down to the sentence the model is asked to
// answer.
func taskFor(an anomaly.Anomaly) crew.Task {
	return crew.Task{
		Title: fmt.Sprintf("Explain the %s move on %s", an.Service, an.Day),
		Goal: fmt.Sprintf("%s %s of baseline on the %s desk. Say what happened, "+
			"whether it recurs, and what it would take to stop it.",
			an.Excess.Abs(), map[bool]string{true: "above", false: "below"}[an.Excess >= 0], an.Source),
		Desk:    an.Source,
		Anomaly: an.ID,
	}
}

// promptFor builds the SAME packet and prompt production would (B7-SPEC.md
// section 2 step 2 and 3), hideDriver=true: the one thing that makes this a
// bench rather than a demo of an analyst reading its own answer key.
func promptFor(db *sql.DB, an anomaly.Anomaly, a crew.Analyst) string {
	t := taskFor(an)
	packet := deliver.Packet(db, t, a, true)
	return deliver.Prompt(t, a, time.Now().Format("2006-01-02"), packet)
}

// scoreMock runs -engine mock or mock-oracle over every case: no prompt is
// even built beyond what mockDeliverable itself needs (the anomaly and the
// true kind), because neither mock reads one. Cost is always zero: nothing
// here calls anything, so nothing here spends anything.
func scoreMock(db *sql.DB, cases []knownCase, engine string) ([]caseResult, error) {
	drivers, err := estate.Drivers(db)
	if err != nil {
		return nil, err
	}
	oracle := engine == engineMockOracle
	out := make([]caseResult, 0, len(cases))
	for _, c := range cases {
		trueKind, _ := trueKindFor(drivers, c.Anomaly.Driver)
		body := mockDeliverable(c.Anomaly, trueKind, oracle)
		out = append(out, caseResult{Case: c, Score: scoreDeliverable(c.Anomaly, trueKind, body, 0)})
	}
	return out, nil
}

// worstCaseMicros prices every selected case with the SAME arithmetic
// tools/run's price() uses (internal/deliver.Tokens over internal/deliver's
// own Prompt, at the model's published rate, output at the full -max-tokens
// cap): B7-SPEC.md section 2's "the same arithmetic tools/run prices with",
// realized as a sum over the actual selected cases rather than one figure
// times N, which is the same total when every packet is close to the same
// size and a more accurate one when it is not.
func worstCaseMicros(db *sql.DB, cases []knownCase, engine, model string, p engines.Price, maxTok int) (int64, error) {
	var total int64
	for _, c := range cases {
		sent := promptFor(db, c.Anomaly, c.Analyst)
		promptTokens := deliver.Tokens(sent)
		in := float64(promptTokens) / 1e6 * p.InPerM
		out := float64(maxTok) / 1e6 * p.OutPerM
		total += int64((in + out) * 1e6)
	}
	_ = engine // kept for the signature's symmetry with scoreLive; the price already names the model
	return total, nil
}
