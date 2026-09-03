package main

// The bench's own half that can spend, wired through the shared caller
// (B6B-SPEC.md): -live with a real engine builds one Gateway per case and
// calls internal/deliver.Call, the same door tools/run's own execute()
// calls through. This file is the only production file under tools/bench
// that touches money at all -- see live_test.go's own
// TestNoFileInThisPackageCanMakeAnHTTPRequest for the structural property
// every file in this package (this one included) must hold: no direct HTTP
// client of any kind, no model-provider credential read, anywhere. The only
// way out is deliver.Call. (That test's own forbidden-word list is why this
// paragraph spells the package path out instead of naming it directly.)

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// gatewayFor builds ONE case's Gateway from the run-wide URL/run id/host/
// budget and that case's own analyst. The run id, the host and the budget
// are the same for every case in one bench invocation: there is no
// per-case guard a bench case's own price can be checked against the way a
// crew task's Budget is, so the whole run's own worst case stands in for
// it, the same "no task guard, use the run figure" fallback
// GatewayBudgetUSD already gives a caller that passes a zero task guard.
// The agent id is the one thing that actually varies case to case.
//
// host is now a real argument, never AgentURI's own "" default (coordinator
// review of PR #29, 2026-09-03): a bare "" reads as costcrew.local
// regardless of what trust domain the console this bench stands in for
// actually runs under, so a live run's spend would be filed under an
// agent id TokenFuse's own trace, and the console's own bus, would not
// recognise as the same installation. main.go's run() requires -stack-host
// whenever -gateway is set, before this is ever called.
func gatewayFor(url, runID, host, analystName, budgetUSD string) deliver.Gateway {
	return deliver.Gateway{
		URL:       url,
		RunID:     runID,
		AgentID:   stack.AgentURI(host, analystName),
		BudgetUSD: budgetUSD,
	}
}

// benchRunID names one bench invocation's live scoring, the same shape
// tools/run/bus.go's own newRunID mints for a crew run: lowercase, no
// colons or slashes, inside 64 bytes, the contract TokenFuse's own
// x-fuse-run-id needs. A different prefix (bench- rather than crew-) so a
// person reading TokenFuse's own trace can tell which binary a run came
// from.
func benchRunID() string {
	return fmt.Sprintf("bench-%d", time.Now().UTC().Unix())
}

// budgetUSDFor turns a worst-case in micro-dollars into the two-decimal
// string TokenFuse's x-fuse-budget-usd wants, rounded UP to the cent: a
// budget is a ceiling on what a call may cost, and rounding it down would
// occasionally understate a real fraction of a cent this run's own worst
// case already accounted for.
func budgetUSDFor(worstMicros int64) string {
	return money.Cents((worstMicros + 9_999) / 10_000).String()
}

// scoreLive runs every selected case for real, through the shared caller,
// and scores each deliverable the same way scoreMock does. Priced once, up
// front (worstCaseMicros, the same arithmetic tools/run prices with), and
// that single figure is what every case's own x-fuse-budget-usd names.
//
// A case that fails aborts the whole run with the case named in the error,
// rather than a partial report standing next to a silently dropped case:
// unlike tools/run's spend(), which continues other tasks and marks one
// blocked on the board, this bench has no board and no per-case retry story
// to fall back on -- see the report's own NOT PROVEN line.
func scoreLive(db *sql.DB, cases []knownCase, engine, model string, p engines.Price, maxTok int, gatewayURL, host string) ([]caseResult, error) {
	worst, err := worstCaseMicros(db, cases, engine, model, p, maxTok)
	if err != nil {
		return nil, err
	}
	budgetUSD := budgetUSDFor(worst)
	runID := benchRunID()

	drivers, err := estate.Drivers(db)
	if err != nil {
		return nil, err
	}

	out := make([]caseResult, 0, len(cases))
	for _, c := range cases {
		gw := gatewayFor(gatewayURL, runID, host, c.Analyst.Name, budgetUSD)
		sent := promptFor(db, c.Anomaly, c.Analyst)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		res, callErr := deliver.Call(ctx, engine, model, sent, maxTok, gw)
		cancel()
		if callErr != nil {
			return nil, fmt.Errorf("case %s: %w", c.Anomaly.ID, callErr)
		}

		trueKind, _ := trueKindFor(drivers, c.Anomaly.Driver)
		costMicros := deliver.ActualMicros(res.InTokens, res.OutTokens, p)
		out = append(out, caseResult{Case: c, Score: scoreDeliverable(c.Anomaly, trueKind, res.Text, costMicros)})
	}
	return out, nil
}
