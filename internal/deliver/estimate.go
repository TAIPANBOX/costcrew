package deliver

// The worst-case pricing arithmetic tools/run's own price() has always done,
// shared here (B5-SPEC.md section 3 point 3) for the same reason Packet,
// Prompt and Tokens already are: Go refuses to import a second "package
// main", so internal/web's /cadence page cannot call tools/run's price()
// directly, and a second, hand-copied formula is exactly the drift B7-SPEC.md
// moved the packet builder here to avoid in the first place.

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// WorstCaseMicros is one call's worst case, in micro-dollars: the prompt at
// the engine's input price, plus the full output cap at its output price,
// because how long an answer runs is not known before it is asked for. The
// same formula tools/run/main.go's price() has always used, extracted rather
// than reimplemented.
func WorstCaseMicros(promptTokens, maxOutputTokens int, p engines.Price) int64 {
	in := float64(promptTokens) / 1e6 * p.InPerM
	out := float64(maxOutputTokens) / 1e6 * p.OutPerM
	return int64((in + out) * 1e6)
}

// estimateDate is the fixed, non-moving date tools/run's own price() sends
// its estimate-only prompt with: the estimate must not change because the
// clock did, and every date is the same ten bytes. Shared so the console's
// preview and the runner's own preflight price the identical prompt.
const estimateDate = "0000-00-00"

// EstimateWorstCase prices one task for one analyst the way tools/run's own
// price() does: the packet's bytes, the prompt built around them, and the
// engine's published rate. It does not know or care about a task's own
// per-task guard (tools/run's price() layers that comparison on top for its
// own Verdict/Refused fields); this returns the same worstMicros/model/priced
// triple price() computes before it ever looks at a guard, which is exactly
// what a preview that has no task guard yet (B5-SPEC.md's due list, priced
// before a sprint exists) and the runner's own precheck both need.
//
// priced is false, and worstMicros and model carry no meaning, when the
// engine is unknown, unmetered, or has no published price -- the same three
// refusal reasons price() names, collapsed to one bool because a preview row
// has no verdict sentence to fill in, only a price or a dash.
func EstimateWorstCase(db *sql.DB, t crew.Task, a crew.Analyst, maxOutputTokens int) (worstMicros int64, model string, priced bool) {
	model = engines.DefaultModel(a.Engine)
	metered, known := engines.Metered(a.Engine)
	if !known || !metered {
		return 0, model, false
	}
	p, ok := engines.PriceFor(a.Engine, model)
	if !ok {
		return 0, model, false
	}
	pk := Packet(db, t, a, false)
	promptTokens := Tokens(Prompt(t, a, estimateDate, pk))
	return WorstCaseMicros(promptTokens, maxOutputTokens, p), model, true
}
