package connectors

// The reconciliation: the provider's own cost per model per day, set beside
// the sum of the gateway's per-call rows (ai_calls) for the same provider,
// model and day, with the difference as its own figure and a status that
// says which side is short.
//
// The gap is never absorbed. A row whose two sides differ by less than the
// tolerance is called matched, and its gap is still printed; the total line
// carries the window's whole gap; and a model only one side knows about is a
// row of its own rather than a figure folded into another model's. That is
// the whole point of a control: per-agent attribution comes only from the
// gateway's rows, so money the provider billed and the gateway never saw is
// money no agent is charged with, and it has to be visible as such.
//
// STATUSES
//
//	provider missing  the provider's cost report has not been read for that
//	                  day at all. Not the same as "the provider billed
//	                  zero", which is a covered day with no row for the model.
//	matched           |gateway - provider| <= tolerance
//	gateway under     the gateway recorded less than the provider billed
//	gateway over      the gateway recorded more than the provider billed
//
// The tolerance is the larger of tolerance_cents (default 1 cent) and
// tolerance_bp of the provider's figure (default 50 basis points, 0.5%),
// both settings on the connector, integer arithmetic throughout. A gateway
// that prices calls from its own book is not expected to agree with the
// invoice to the micro-dollar; one that disagrees by more than half a
// percent on a day is worth a person's look.
//
// SCOPE
//
// The gateway's rows carry no API key, so "the same key" is a scope the
// operator states on the connector: api_key_ids keeps only the provider rows
// for those keys, and workspace_ids (Anthropic) or project_ids (OpenAI) only
// those scopes. Anthropic's cost report carries no key at all (only a
// workspace), so for Anthropic the key filter narrows the TOKENS and not the
// money, and the page says so rather than pretending otherwise.

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultToleranceCents = 1
	DefaultToleranceBP    = 50
	// reconcileDefaultDays is the window when none is asked for: the last
	// 31 days ending on the latest day either side has.
	reconcileDefaultDays = 31
	reconcileMaxDays     = 366
)

// Reconciliation statuses, exactly as the page and the CSV print them.
const (
	StatusMatched         = "matched"
	StatusGatewayUnder    = "gateway under"
	StatusGatewayOver     = "gateway over"
	StatusProviderMissing = "provider missing"
)

// ReconRow is one model on one day.
type ReconRow struct {
	Day, Model string
	// ProviderMicros is the provider's cost; GatewayMicros the sum of the
	// gateway's unblocked rows; GapMicros is gateway minus provider, so a
	// negative gap is money the gateway never recorded.
	ProviderMicros, GatewayMicros, GapMicros int64
	// Covered is whether the provider's cost report was read for this day.
	Covered bool
	Status  string
	// The token figures sit beside the money for a reader to compare. They
	// do not decide the status: the gateway's tokens_in is whatever the
	// gateway counted, and whether that includes cache reads is the
	// gateway's definition, not the provider's.
	ProviderIn, ProviderOut, GatewayIn, GatewayOut int64
	UsageCovered                                   bool
}

// Reconciliation is one connector over one window.
type Reconciliation struct {
	Connector, Vendor, GatewayProvider string
	From, To                           string
	Rows                               []ReconRow
	// Totals over the window, every row included, matched ones too.
	ProviderMicros, GatewayMicros, GapMicros int64
	Counts                                   map[string]int
	ToleranceCents, ToleranceBP              int64
	// Scope says, in a sentence, which provider rows were kept.
	Scope string
	// DaysCovered and DaysMissing count the window's days by whether the
	// provider's cost report was read for them.
	DaysCovered, DaysMissing int
	// Empty is true when neither side has a single row in the window.
	Empty bool
}

// ReconcileConnectors lists the connectors a reconciliation exists for, in
// the order a page offers them.
func ReconcileConnectors() []Connector {
	var out []Connector
	for _, id := range []string{anthropicUsageSpec.id, openaiUsageSpec.id} {
		if c, ok := Get(id); ok {
			out = append(out, c)
		}
	}
	return out
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}

func validDay(s string) bool {
	if len(s) != 10 {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Year() >= usageFirstYear && t.Year() <= usageLastYear
}

// Reconcile builds the reconciliation for one provider-usage connector.
// from and to are inclusive "2006-01-02" days, or empty for the default
// window. It only reads: no table is created and nothing is written, so a
// GET of the page cannot change the store.
func Reconcile(db *sql.DB, id, from, to string) (Reconciliation, error) {
	spec, ok := usageSpecFor(id)
	if !ok {
		return Reconciliation{}, fmt.Errorf("no reconciliation for %q", id)
	}
	if (from != "" && !validDay(from)) || (to != "" && !validDay(to)) {
		return Reconciliation{}, errors.New("from and to are days written 2006-01-02")
	}
	conn, err := Load(db, id)
	if err != nil {
		return Reconciliation{}, err
	}
	conf, err := parseUsageConfig(spec, conn.Config)
	if err != nil {
		return Reconciliation{}, fmt.Errorf("the connector's settings: %w", err)
	}
	rec := Reconciliation{Connector: id, Vendor: spec.vendor, GatewayProvider: spec.gatewayProvider,
		Counts: map[string]int{}, ToleranceCents: conf.tolCents, ToleranceBP: conf.tolBP}
	rec.Scope = scopeSentence(spec, conf)

	hasProvider, err := tableExists(db, "provider_usage")
	if err != nil {
		return rec, err
	}
	hasGateway, err := tableExists(db, "ai_calls")
	if err != nil {
		return rec, err
	}

	if to == "" || from == "" {
		latest, err := latestDay(db, spec, hasProvider, hasGateway)
		if err != nil {
			return rec, err
		}
		if latest == "" {
			rec.Empty = true
			rec.From, rec.To = from, to
			return rec, nil
		}
		if to == "" {
			to = latest
		}
		if from == "" {
			t, _ := time.Parse("2006-01-02", to)
			from = t.AddDate(0, 0, -(reconcileDefaultDays - 1)).Format("2006-01-02")
		}
	}
	if from > to {
		return rec, errors.New("from is after to")
	}
	tf, _ := time.Parse("2006-01-02", from)
	tt, _ := time.Parse("2006-01-02", to)
	if tt.Sub(tf) > (reconcileMaxDays-1)*24*time.Hour {
		return rec, fmt.Errorf("the window is longer than %d days", reconcileMaxDays)
	}
	rec.From, rec.To = from, to

	type dm struct{ day, model string }
	rows := map[dm]*ReconRow{}
	get := func(day, model string) *ReconRow {
		k := dm{day, model}
		r, ok := rows[k]
		if !ok {
			r = &ReconRow{Day: day, Model: model}
			rows[k] = r
		}
		return r
	}

	covered := map[string]bool{}
	usageCovered := map[string]bool{}
	if hasProvider {
		dq, err := db.Query(`SELECT report, day FROM provider_usage_days
			WHERE connector=? AND day BETWEEN ? AND ?`, id, from, to)
		if err != nil {
			return rec, err
		}
		for dq.Next() {
			var report, day string
			if err := dq.Scan(&report, &day); err != nil {
				dq.Close()
				return rec, err
			}
			if report == "cost" {
				covered[day] = true
			} else {
				usageCovered[day] = true
			}
		}
		dq.Close()
		if err := dq.Err(); err != nil {
			return rec, err
		}

		where, args := providerFilter(spec, conf, id, from, to)
		q, err := db.Query(`SELECT report, day, model, token_type, SUM(tokens), SUM(cost_microusd)
			FROM provider_usage WHERE `+where+`
			GROUP BY report, day, model, token_type`, args...)
		if err != nil {
			return rec, fmt.Errorf("summing the provider's rows: %w", err)
		}
		for q.Next() {
			var report, day, model, tt string
			var tokens, micros int64
			if err := q.Scan(&report, &day, &model, &tt, &tokens, &micros); err != nil {
				q.Close()
				return rec, err
			}
			r := get(day, model)
			if report == "cost" {
				r.ProviderMicros += micros
				continue
			}
			switch {
			case isInputType(spec, tt):
				r.ProviderIn += tokens
			case tt == "output_tokens":
				r.ProviderOut += tokens
			}
		}
		q.Close()
		if err := q.Err(); err != nil {
			return rec, fmt.Errorf("summing the provider's rows: %w", err)
		}
	}

	if hasGateway {
		// Blocked calls cost nothing and the gateway's own export already
		// writes them as zero; excluded anyway, so a blocked row with a
		// reserved estimate in it can never count as spend.
		q, err := db.Query(`SELECT day, model, SUM(billed_microusd), SUM(tokens_in), SUM(tokens_out)
			FROM ai_calls WHERE blocked=0 AND lower(COALESCE(provider,''))=lower(?) AND day BETWEEN ? AND ?
			GROUP BY day, model`, spec.gatewayProvider, from, to)
		if err != nil {
			return rec, fmt.Errorf("summing the gateway's rows: %w", err)
		}
		for q.Next() {
			var day, model string
			var micros, in, out int64
			if err := q.Scan(&day, &model, &micros, &in, &out); err != nil {
				q.Close()
				return rec, err
			}
			r := get(day, model)
			r.GatewayMicros += micros
			r.GatewayIn += in
			r.GatewayOut += out
		}
		q.Close()
		if err := q.Err(); err != nil {
			return rec, fmt.Errorf("summing the gateway's rows: %w", err)
		}
	}

	for d := tf; !d.After(tt); d = d.AddDate(0, 0, 1) {
		if covered[d.Format("2006-01-02")] {
			rec.DaysCovered++
		} else {
			rec.DaysMissing++
		}
	}

	for _, r := range rows {
		r.Covered = covered[r.Day]
		r.UsageCovered = usageCovered[r.Day]
		r.GapMicros = r.GatewayMicros - r.ProviderMicros
		r.Status = reconStatus(r.Covered, r.ProviderMicros, r.GapMicros, conf.tolCents, conf.tolBP)
		rec.Rows = append(rec.Rows, *r)
		var okP, okG bool
		rec.ProviderMicros, okP = addMicros(rec.ProviderMicros, r.ProviderMicros)
		rec.GatewayMicros, okG = addMicros(rec.GatewayMicros, r.GatewayMicros)
		if !okP || !okG {
			return rec, errors.New("the window's totals add up past what 64 bits can hold")
		}
		rec.Counts[r.Status]++
	}
	rec.GapMicros = rec.GatewayMicros - rec.ProviderMicros
	sort.Slice(rec.Rows, func(i, j int) bool {
		if rec.Rows[i].Day != rec.Rows[j].Day {
			return rec.Rows[i].Day < rec.Rows[j].Day
		}
		return rec.Rows[i].Model < rec.Rows[j].Model
	})
	rec.Empty = len(rec.Rows) == 0
	return rec, nil
}

func addMicros(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// reconStatus is the whole rule, in one place.
func reconStatus(covered bool, provider, gap, tolCents, tolBP int64) string {
	if !covered {
		return StatusProviderMissing
	}
	tol := tolCents * 10_000
	ap := provider
	if ap < 0 {
		ap = -ap
	}
	// ap is at most a sum of capped rows; bp at most 10 000. Divide first so
	// the product cannot overflow, then add back the remainder's share.
	if rel := ap/10_000*tolBP + ap%10_000*tolBP/10_000; rel > tol {
		tol = rel
	}
	ag := gap
	if ag < 0 {
		ag = -ag
	}
	switch {
	case ag <= tol:
		return StatusMatched
	case gap < 0:
		return StatusGatewayUnder
	default:
		return StatusGatewayOver
	}
}

func isInputType(spec usageSpec, tt string) bool {
	for _, t := range spec.inputTypes {
		if t == tt {
			return true
		}
	}
	return false
}

// providerFilter is the WHERE clause the operator's scope settings imply.
func providerFilter(spec usageSpec, conf usageConfig, id, from, to string) (string, []any) {
	where := []string{"connector=?", "day BETWEEN ? AND ?"}
	args := []any{id, from, to}
	if len(conf.keys) > 0 {
		in := strings.TrimSuffix(strings.Repeat("?,", len(conf.keys)), ",")
		clause := "api_key_id IN (" + in + ")"
		if !spec.costHasKey {
			// The cost report carries no key for this vendor: the key filter
			// applies to the token rows only.
			clause = "(report='cost' OR " + clause + ")"
		}
		where = append(where, clause)
		for _, k := range conf.keys {
			args = append(args, k)
		}
	}
	if len(conf.scopes) > 0 {
		in := strings.TrimSuffix(strings.Repeat("?,", len(conf.scopes)), ",")
		where = append(where, "scope IN ("+in+")")
		for _, s := range conf.scopes {
			args = append(args, s)
		}
	}
	return strings.Join(where, " AND "), args
}

func scopeSentence(spec usageSpec, conf usageConfig) string {
	var parts []string
	if len(conf.keys) == 0 {
		parts = append(parts, "every API key the provider reports")
	} else {
		s := "API key" + map[bool]string{true: "s ", false: " "}[len(conf.keys) > 1] + strings.Join(conf.keys, ", ")
		if !spec.costHasKey {
			s += " (tokens only: this provider's cost report carries no key, so its money is not narrowed by key)"
		}
		parts = append(parts, s)
	}
	if len(conf.scopes) == 0 {
		parts = append(parts, "every "+spec.scopeName)
	} else {
		parts = append(parts, spec.scopeName+" "+strings.Join(conf.scopes, ", "))
	}
	return "Provider rows kept: " + strings.Join(parts, "; ") + ". Gateway rows kept: provider " +
		spec.gatewayProvider + ", blocked calls excluded."
}

func latestDay(db *sql.DB, spec usageSpec, hasProvider, hasGateway bool) (string, error) {
	latest := ""
	if hasProvider {
		var d sql.NullString
		if err := db.QueryRow(`SELECT MAX(day) FROM provider_usage_days WHERE connector=?`, spec.id).Scan(&d); err != nil {
			return "", err
		}
		if d.Valid && d.String > latest {
			latest = d.String
		}
	}
	if hasGateway {
		var d sql.NullString
		if err := db.QueryRow(`SELECT MAX(day) FROM ai_calls WHERE blocked=0 AND lower(COALESCE(provider,''))=lower(?)`,
			spec.gatewayProvider).Scan(&d); err != nil {
			return "", err
		}
		if d.Valid && d.String > latest {
			latest = d.String
		}
	}
	if latest != "" && !validDay(latest) {
		return "", fmt.Errorf("the latest day on record, %q, is not a day", truncateForMessage(latest))
	}
	return latest, nil
}
