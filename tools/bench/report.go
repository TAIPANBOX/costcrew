package main

// The fixed report shape. B7-SPEC.md section 2: one table, one line per
// case below it, exit 0.

import (
	"fmt"
	"io"
)

// caseResult is one scored case, ready to print.
type caseResult struct {
	Case  knownCase
	Score score
}

// printDriverReport is the shape B7-SPEC.md section 2 gives verbatim:
//
//	BENCH  fixture, seed 7, 20 cases, skill triage, engine <name>
//	       service  day   kind  cause   cost/task
//	       19/20    17/20 14/20 12/20   0.41 c
//	       accuracy (cause) 60%, at USD 0.082 total
//
// followed by one line per case: anomaly id, the truth, the named cause,
// the four booleans, the cost. note, when non-empty, is the clamping
// sentence -n's own boundary rule asks for ("-n larger than the fixture
// holds is clamped and said"), printed once, right after the header.
func printDriverReport(w io.Writer, seed int64, results []caseResult, skill, engine string, note string) {
	fmt.Fprintf(w, "BENCH  fixture, seed %d, %d cases, skill %s, engine %s\n",
		seed, len(results), skill, engine)
	if note != "" {
		fmt.Fprintln(w, note)
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "       no known case has an eligible analyst for this skill; nothing to score")
		return
	}

	var svc, day, kind, cause int
	var totalMicros int64
	for _, r := range results {
		if r.Score.ServiceNamed {
			svc++
		}
		if r.Score.DayNamed {
			day++
		}
		if r.Score.KindRight {
			kind++
		}
		if r.Score.CauseMatched {
			cause++
		}
		totalMicros += r.Score.CostMicros
	}
	n := len(results)
	avgCents := microsToCents(totalMicros) / float64(n)

	fmt.Fprintf(w, "       service  day   kind  cause   cost/task\n")
	fmt.Fprintf(w, "       %d/%-4d %d/%-4d %d/%-4d %d/%-4d %.2f c\n",
		svc, n, day, n, kind, n, cause, n, avgCents)
	fmt.Fprintf(w, "       accuracy (cause) %d%%, at USD %.4f total\n",
		percent(cause, n), microsToUSD(totalMicros))

	for _, r := range results {
		fmt.Fprintf(w, "  %-14s truth=%-42q named=%-42q  service=%s day=%s kind=%s cause=%s  cost=%.4f c\n",
			r.Case.Anomaly.ID, r.Case.Anomaly.Driver, r.Score.NamedCause,
			yn(r.Score.ServiceNamed), yn(r.Score.DayNamed), yn(r.Score.KindRight), yn(r.Score.CauseMatched),
			microsToCents(r.Score.CostMicros))
	}
}

// printStampReport is the imported-data shape: no service/day/kind/cause
// dimension exists without a known cause to check against, so the one
// thing scored is whether a deliverable was posted (accepted first pass)
// or returned.
func printStampReport(w io.Writer, seed int64, cases []stampCase, skill, engine string, note string) {
	fmt.Fprintf(w, "BENCH  stamps, seed %d, %d cases, skill %s, engine %s\n",
		seed, len(cases), skill, engine)
	if note != "" {
		fmt.Fprintln(w, note)
	}
	if len(cases) == 0 {
		fmt.Fprintln(w, "       no stamped case (posted or returned) matches this skill and engine")
		return
	}
	posted := 0
	for _, c := range cases {
		if c.Outcome == outcomePosted {
			posted++
		}
	}
	fmt.Fprintf(w, "       posted (accepted first pass) %d/%d\n", posted, len(cases))
	fmt.Fprintf(w, "       accuracy (posted) %d%%\n", percent(posted, len(cases)))
	for _, c := range cases {
		fmt.Fprintf(w, "  task %-6d anomaly %-14s %s\n", c.Task.ID, c.Task.Anomaly, c.Outcome)
	}
}

// printWorstCasePrice is what runs instead of a call, every time -live is
// absent and -engine names something other than mock or mock-oracle:
// B7-SPEC.md section 2's own words, "the worst-case price of the run (N
// times the analyst's PerTask worst case, the same arithmetic tools/run
// prices with)". Summed per selected case rather than one figure times N,
// which is the same total when every case's packet is close to the same
// size and a MORE accurate one when it is not.
func printWorstCasePrice(w io.Writer, n int, engine, model string, worstMicros int64) {
	fmt.Fprintf(w, "-live was not given. Without it this bench refuses every engine but "+
		"mock and mock-oracle.\n")
	fmt.Fprintf(w, "Worst case for %d case(s) on %s/%s: USD %.4f. -live is refused for "+
		"every real engine until the shared TokenFuse caller is wired, and this agent "+
		"never adds it either way.\n", n, engine, model, microsToUSD(worstMicros))
}

func percent(part, whole int) int {
	if whole == 0 {
		return 0
	}
	return part * 100 / whole
}

func microsToCents(micros int64) float64 { return float64(micros) / 10_000 }
func microsToUSD(micros int64) float64   { return float64(micros) / 1_000_000 }

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}
