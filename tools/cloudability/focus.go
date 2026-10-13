package main

// The FOCUS 1.1 columns this drop carries, in one table, and the check every
// drop passes before a byte of it is written.
//
// Read from the specification itself: tag v1.1 of
// github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec, the pages under
// specification/columns and specification/attributes, on 2026-10-13. Every
// requirement quoted in a comment below is from there. Cloudability's Custom
// Data FOCUS ingress takes 1.0 or 1.1 and the manifest's focus_version has to
// match exactly; this drop is 1.1.
//
// Which columns, and why each value is the true one for an LLM gateway:
//
//   - The 21 columns 1.1 makes Mandatory are all here.
//   - Conditional columns are here when their condition holds for a gateway:
//     ConsumedQuantity and ConsumedUnit (the gateway measures usage),
//     ResourceId (an agent is what incurs the charge), SubAccountId and
//     SubAccountName (the gateway's unit groups agents for cost allocation)
//     and Tags (the gateway sets them). The rest are left out because their
//     condition does not hold: no published unit price (ListUnitPrice,
//     ContractedUnitPrice), no commitment discounts, no capacity
//     reservations, no regions, no SKU list, no resource types.
//   - The two Recommended columns that apply, ChargeFrequency and
//     ServiceSubcategory.
//   - Custom columns after them, prefixed x_ as 1.1 requires.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/plainname"
)

// focusVersion is the manifest's focus_version, and the version every rule in
// this file was read from. Cloudability accepts "1.0" and "1.1" and nothing
// else, matched exactly.
const focusVersion = "1.1"

type level string

const (
	mandatory   level = "Mandatory"
	conditional level = "Conditional"
	recommended level = "Recommended"
	custom      level = "Custom" // an x_ column of this drop's own
)

type kind int

const (
	text     kind = iota // a string, held to the printed-name rule
	decimal              // FOCUS Numeric Format, non-negative here
	positive             // a decimal that FOCUS requires to be greater than zero
	integer              // a whole count, in a custom column
	instant              // FOCUS Date/Time Format
	currency             // ISO 4217
	enum                 // one of allowed
	keyValue             // FOCUS Key-Value Format: a JSON object of scalars
)

// column is one column of the drop: what FOCUS 1.1 says of it, and how this
// drop fills it. nullable is whether FOCUS 1.1 allows a null in it on the
// rows this drop writes, which are all ChargeCategory "Usage" and never a
// correction (several columns allow a null only on other kinds of row).
type column struct {
	id       string
	level    level
	nullable bool
	kind     kind
	allowed  []string
	fill     func(r *row) string
}

// row is one group of calls, filled into every column of the table.
type row struct {
	day, nextDay             string // the UTC day the calls settled on, and the day after
	periodStart, periodEnd   string // the calendar month, UTC
	unit, agent, model, key  string
	provider                 string
	cost                     string // the exact sum of the calls' cost, six decimals
	tokensIn, tokensOut      int64
	calls, blocked, estimate int64
	accountID, accountName   string
}

// serviceCategories is ServiceCategory's whole allowed list in 1.1.
var serviceCategories = []string{
	"AI and Machine Learning", "Analytics", "Business Applications", "Compute", "Databases",
	"Developer Tools", "Multicloud", "Identity", "Integration", "Internet of Things",
	"Management and Governance", "Media", "Migration", "Mobile", "Networking", "Security",
	"Storage", "Web", "Other",
}

// aiSubcategories is ServiceSubcategory's allowed values whose parent is "AI
// and Machine Learning", the only category this drop writes. 1.1 lists about
// ninety subcategories and requires each to sit under its own parent, so a
// subcategory of any other category is refused here, not merely unlisted.
var aiSubcategories = []string{
	"AI Platforms", "Bots", "Generative AI", "Machine Learning", "Natural Language Processing",
	"Other (AI and Machine Learning)",
}

// columns is the drop's column set, in the order they are written: the FOCUS
// columns alphabetically (1.1 allows it and puts them first), then the custom
// ones.
var columns = []column{
	// "MUST match the sum of the invoices received for that billing period
	// for a billing account." The gateway's figure is its own settlement of
	// each call at its price book, the same amount it charged against the
	// budget; whether the vendor's invoice agrees is what CostCrew's
	// reconciliation page measures, not something this drop can know.
	{id: "BilledCost", level: mandatory, kind: decimal, fill: func(r *row) string { return r.cost }},
	{id: "BillingAccountId", level: mandatory, kind: text, fill: func(r *row) string { return r.accountID }},
	// Allows nulls; "MUST NOT be null when the provider supports assigning a
	// display name". The operator gives one with -billing-account-name or
	// none is written.
	{id: "BillingAccountName", level: mandatory, nullable: true, kind: text, fill: func(r *row) string { return r.accountName }},
	{id: "BillingCurrency", level: mandatory, kind: currency, fill: func(*row) string { return "USD" }},
	{id: "BillingPeriodEnd", level: mandatory, kind: instant, fill: func(r *row) string { return r.periodEnd }},
	{id: "BillingPeriodStart", level: mandatory, kind: instant, fill: func(r *row) string { return r.periodStart }},
	{id: "ChargeCategory", level: mandatory, kind: enum,
		allowed: []string{"Usage", "Purchase", "Tax", "Credit", "Adjustment"},
		fill:    func(*row) string { return "Usage" }},
	// "MUST be null when it is not a correction."
	{id: "ChargeClass", level: mandatory, nullable: true, kind: enum, allowed: []string{"Correction"},
		fill: func(*row) string { return "" }},
	{id: "ChargeDescription", level: mandatory, nullable: true, kind: text,
		fill: func(r *row) string { return "Calls to " + r.model + " through the TokenFuse gateway" }},
	{id: "ChargeFrequency", level: recommended, kind: enum, allowed: []string{"One-Time", "Recurring", "Usage-Based"},
		fill: func(*row) string { return "Usage-Based" }},
	{id: "ChargePeriodEnd", level: mandatory, kind: instant, fill: func(r *row) string { return r.nextDay }},
	{id: "ChargePeriodStart", level: mandatory, kind: instant, fill: func(r *row) string { return r.day }},
	// "MUST NOT be null and MUST be a valid positive decimal value if
	// ChargeCategory is Usage." Requests, not tokens: a call the gateway
	// settled at its fallback price (x_cost_basis "estimated") has no token
	// count, so a row of only such calls would carry a zero, and a row
	// exists only when at least one call went through, so the count of calls
	// is positive on every row.
	{id: "ConsumedQuantity", level: conditional, kind: positive, fill: func(r *row) string { return fmt.Sprint(r.calls) }},
	{id: "ConsumedUnit", level: conditional, kind: text, fill: func(*row) string { return "Requests" }},
	// "If negotiated discounts are not applicable, the Contracted Cost
	// defaults to the List Cost." The gateway prices at one price book and
	// knows of no discount, so list, contracted and effective are the
	// settled figure. No unit price is claimed: the gateway prices input and
	// output tokens differently and its export carries one amount per call.
	{id: "ContractedCost", level: mandatory, kind: decimal, fill: func(r *row) string { return r.cost }},
	{id: "EffectiveCost", level: mandatory, kind: decimal, fill: func(r *row) string { return r.cost }},
	// The provider, publisher and invoice issuer are the model's vendor as
	// the gateway names it: TokenFuse infers it from the model name and
	// writes the same value in all three of its own columns; the reader keeps
	// it as ai_calls.provider. A reseller in between is not something the
	// gateway records.
	{id: "InvoiceIssuerName", level: mandatory, kind: text, fill: func(r *row) string { return r.provider }},
	{id: "ListCost", level: mandatory, kind: decimal, fill: func(r *row) string { return r.cost }},
	// Tokens are what the gateway priced. A call settled at its fallback
	// price adds none (it parsed none); x_EstimatedCalls counts those.
	{id: "PricingQuantity", level: mandatory, kind: decimal, fill: func(r *row) string { return fmt.Sprint(r.tokensIn + r.tokensOut) }},
	{id: "PricingUnit", level: mandatory, kind: text, fill: func(*row) string { return "Tokens" }},
	{id: "ProviderName", level: mandatory, kind: text, fill: func(r *row) string { return r.provider }},
	{id: "PublisherName", level: mandatory, kind: text, fill: func(r *row) string { return r.provider }},
	// The agent is what incurred the charge. No ResourceName: 1.1 forbids
	// one that only repeats a ResourceId nobody named interactively.
	{id: "ResourceId", level: conditional, nullable: true, kind: text, fill: func(r *row) string { return r.agent }},
	{id: "ServiceCategory", level: mandatory, kind: enum, allowed: serviceCategories,
		fill: func(*row) string { return "AI and Machine Learning" }},
	// The console's own name for this spend (connectors.deriveCharges writes
	// "<provider> API"), so the same money carries the same service name in
	// CostCrew and in Cloudability.
	{id: "ServiceName", level: mandatory, kind: text, fill: func(r *row) string { return r.provider + " API" }},
	{id: "ServiceSubcategory", level: recommended, kind: enum, allowed: aiSubcategories,
		fill: func(*row) string { return "Generative AI" }},
	// The gateway's unit (x_unit, resolved server-side from its identity map,
	// what CostCrew calls a customer unit) is the grouping it allocates cost
	// by, so it is the sub account. A call no unit matched belongs to none,
	// and 1.1 says the column MUST then be null.
	{id: "SubAccountId", level: conditional, nullable: true, kind: text, fill: func(r *row) string { return r.unit }},
	{id: "SubAccountName", level: conditional, nullable: true, kind: text, fill: func(r *row) string { return r.unit }},
	{id: "Tags", level: conditional, nullable: true, kind: keyValue, fill: tags},
	{id: "x_AgentId", level: custom, kind: text, fill: func(r *row) string { return r.agent }},
	{id: "x_Unit", level: custom, nullable: true, kind: text, fill: func(r *row) string { return r.unit }},
	{id: "x_Model", level: custom, kind: text, fill: func(r *row) string { return r.model }},
	{id: "x_KeyId", level: custom, nullable: true, kind: text, fill: func(r *row) string { return r.key }},
	{id: "x_Calls", level: custom, kind: integer, fill: func(r *row) string { return fmt.Sprint(r.calls) }},
	// A blocked call cost nothing and consumed nothing: it is counted here
	// and nowhere else on the row.
	{id: "x_BlockedCalls", level: custom, kind: integer, fill: func(r *row) string { return fmt.Sprint(r.blocked) }},
	{id: "x_EstimatedCalls", level: custom, kind: integer, fill: func(r *row) string { return fmt.Sprint(r.estimate) }},
	{id: "x_TokensIn", level: custom, kind: integer, fill: func(r *row) string { return fmt.Sprint(r.tokensIn) }},
	{id: "x_TokensOut", level: custom, kind: integer, fill: func(r *row) string { return fmt.Sprint(r.tokensOut) }},
}

// tagPrefix is the provider-specified prefix 1.1 requires on every tag a
// provider defines ("Provider-defined tags MUST be prefixed with a
// provider-specified tag key prefix"). These tags are the gateway's.
const tagPrefix = "tokenfuse/"

// tags is the row's Tags cell: the agent, the model, and the unit and the
// credential when the gateway named them. A tag with nothing to say is left
// out rather than written empty. Keys are sorted (encoding/json sorts a
// map's), so the cell is the same bytes on every run.
func tags(r *row) string {
	m := map[string]string{tagPrefix + "agent": r.agent, tagPrefix + "model": r.model}
	if r.unit != "" {
		m[tagPrefix+"unit"] = r.unit
	}
	if r.key != "" {
		m[tagPrefix+"key_id"] = r.key
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m) // a map of strings always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// header is the drop's first line.
func header() []string {
	h := make([]string, len(columns))
	for i, c := range columns {
		h[i] = c.id
	}
	return h
}

// fill is one row of the drop, in column order.
func (r *row) fill() []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = c.fill(r)
	}
	return out
}

// textMaxBytes bounds any text cell. The gateway's own values are short
// (agent ids are URIs, model names a few dozen bytes); the bound is so a
// hostile export cannot write a cell that fills a page in Cloudability.
const textMaxBytes = 512

var (
	numberShape  = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	integerShape = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	instantShape = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	focusID      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// validateDrop holds a drop to FOCUS 1.1 before it is written: every
// mandatory column present, FOCUS columns before custom ones and custom ones
// prefixed x_, no null where 1.1 forbids one on a Usage row, every value in
// its format and every restricted value in its list, and no text cell a
// spreadsheet would run. It reads the drop as data, header and rows, and not
// the table that made it, so a table that lost a column or a fill that writes
// the wrong thing is caught here rather than by Cloudability.
func validateDrop(hdr []string, rows [][]string) error {
	byID := map[string]column{}
	for _, c := range columns {
		byID[c.id] = c
	}
	present := map[string]bool{}
	seenCustom := false
	for _, h := range hdr {
		if present[h] {
			return fmt.Errorf("column %s appears twice", h)
		}
		present[h] = true
		if strings.HasPrefix(h, "x_") {
			seenCustom = true
			continue
		}
		if seenCustom {
			return fmt.Errorf("FOCUS column %s comes after a custom column; 1.1 puts custom columns last", h)
		}
		if _, known := byID[h]; !known || !focusID.MatchString(h) {
			return fmt.Errorf("column %s is not a FOCUS 1.1 column this drop defines, and a custom column must be prefixed x_", h)
		}
	}
	for _, c := range columns {
		if c.level == mandatory && !present[c.id] {
			return fmt.Errorf("the mandatory FOCUS 1.1 column %s is missing", c.id)
		}
	}
	for _, id := range mandatoryColumns {
		if !present[id] {
			return fmt.Errorf("the mandatory FOCUS 1.1 column %s is missing", id)
		}
	}
	for n, rec := range rows {
		if len(rec) != len(hdr) {
			return fmt.Errorf("row %d has %d cells and the header %d", n+1, len(rec), len(hdr))
		}
		for i, v := range rec {
			c, ok := byID[hdr[i]]
			if !ok {
				return fmt.Errorf("row %d: column %s is not one this drop defines", n+1, hdr[i])
			}
			if err := checkCell(c, v); err != nil {
				return fmt.Errorf("row %d: %w", n+1, err)
			}
		}
	}
	return nil
}

// mandatoryColumns is every column FOCUS 1.1 marks Mandatory, listed apart
// from the table so that removing a row of the table cannot also remove the
// requirement (each specification page's "Feature level | Mandatory").
var mandatoryColumns = []string{
	"BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency",
	"BillingPeriodEnd", "BillingPeriodStart", "ChargeCategory", "ChargeClass",
	"ChargeDescription", "ChargePeriodEnd", "ChargePeriodStart", "ContractedCost",
	"EffectiveCost", "InvoiceIssuerName", "ListCost", "PricingQuantity", "PricingUnit",
	"ProviderName", "PublisherName", "ServiceCategory", "ServiceName",
}

func checkCell(c column, v string) error {
	if v == "" {
		if c.nullable {
			return nil
		}
		return fmt.Errorf("%s is null, which FOCUS 1.1 does not allow on a Usage row", c.id)
	}
	switch c.kind {
	case decimal, positive:
		// Never negative here: every amount and quantity this drop writes is
		// a sum of non-negative ones, and a leading minus is also how a
		// spreadsheet formula can begin.
		if !numberShape.MatchString(v) {
			return fmt.Errorf("%s %q is not a non-negative decimal in FOCUS's numeric format", c.id, v)
		}
		if c.kind == positive && strings.Trim(v, "0.") == "" {
			return fmt.Errorf("%s is %s; FOCUS 1.1 requires a positive value on a Usage row", c.id, v)
		}
	case integer:
		if !integerShape.MatchString(v) {
			return fmt.Errorf("%s %q is not a whole count", c.id, v)
		}
	case instant:
		if !instantShape.MatchString(v) {
			return fmt.Errorf("%s %q is not a UTC date and time as FOCUS writes one (YYYY-MM-DDTHH:mm:ssZ)", c.id, v)
		}
	case currency:
		if !currencyCode.MatchString(v) {
			return fmt.Errorf("%s %q is not a three-letter ISO 4217 code", c.id, v)
		}
	case enum:
		for _, a := range c.allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("%s %q is not one of the values FOCUS 1.1 allows: %s", c.id, v, strings.Join(c.allowed, ", "))
	case keyValue:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return fmt.Errorf("%s %q is not a JSON object", c.id, v)
		}
		for k, val := range m {
			switch val.(type) {
			case string, float64, bool, nil:
			default:
				return fmt.Errorf("%s key %q holds an object or an array, which FOCUS's key-value format forbids", c.id, k)
			}
		}
	case text:
		if reason := plainname.Check(c.id, v, textMaxBytes); reason != "" {
			return fmt.Errorf("%s", reason)
		}
	}
	return nil
}
