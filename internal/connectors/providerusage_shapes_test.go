package connectors

// Every way a field can have the wrong shape, each refused with its own
// reason. One table rather than one file per case: the property is that the
// reason NAMES the field and what was wrong with it, which a reader of the
// refusal needs to fix the export.

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestEveryFieldShapeIsRefusedWithItsOwnReason(t *testing.T) {
	aBucket := `"starting_at":"2026-10-01T00:00:00Z","ending_at":"2026-10-02T00:00:00Z"`
	aPage := func(results string) string {
		return `{"data":[{` + aBucket + `,"results":[` + results + `]}],"has_more":false}`
	}
	oPage := func(bucket string) string {
		return `{"object":"page","data":[` + bucket + `],"has_more":false}`
	}
	oBucket := func(results string) string {
		return `{"object":"bucket","start_time":1790812800,"end_time":1790899200,"results":[` + results + `]}`
	}
	usage := `"cache_read_input_tokens":0,"output_tokens":1`
	for _, c := range []struct {
		name, id, report, body, reason string
	}{
		{"a page that is not an object", "anthropic-usage", "cost", `[1]`, "page 1 is a number"},
		{"an empty list of pages", "anthropic-usage", "cost", `[]`, "an empty list of pages"},
		{"no has_more", "anthropic-usage", "cost", `{"data":[]}`, "has_more is missing"},
		{"has_more not a bool", "anthropic-usage", "cost", `{"data":[],"has_more":"no"}`, "has_more is a string"},
		{"no data", "anthropic-usage", "cost", `{"has_more":false}`, "data is missing"},
		{"a bucket that is not an object", "anthropic-usage", "cost", `{"data":[7],"has_more":false}`, "bucket 1 is a number"},
		{"no starting_at", "anthropic-usage", "cost", `{"data":[{"ending_at":"x","results":[]}],"has_more":false}`, "starting_at is missing"},
		{"starting_at not RFC 3339", "anthropic-usage", "cost", `{"data":[{"starting_at":"2026-10-01","ending_at":"2026-10-02T00:00:00Z","results":[]}],"has_more":false}`, "is not RFC 3339"},
		{"ending_at not RFC 3339", "anthropic-usage", "cost", `{"data":[{"starting_at":"2026-10-01T00:00:00Z","ending_at":"soon","results":[]}],"has_more":false}`, "ending_at \"soon\" is not RFC 3339"},
		{"a year out of range", "anthropic-usage", "cost", `{"data":[{"starting_at":"1999-10-01T00:00:00Z","ending_at":"1999-10-02T00:00:00Z","results":[]}],"has_more":false}`, "outside 2000 to 2099"},
		{"no results", "anthropic-usage", "cost", `{"data":[{` + aBucket + `}],"has_more":false}`, "results is missing"},
		{"a result that is not an object", "anthropic-usage", "cost", aPage(`"x"`), "result 1 is a string"},
		{"no currency", "anthropic-usage", "cost", aPage(`{"amount":"1"}`), "currency is missing"},
		{"no amount", "anthropic-usage", "cost", aPage(`{"currency":"USD"}`), "amount is missing"},
		{"model not a string", "anthropic-usage", "cost", aPage(`{"amount":"1","currency":"USD","model":7}`), "model is a number, want a string or null"},
		{"token_type not a string", "anthropic-usage", "cost", aPage(`{"amount":"1","currency":"USD","token_type":[]}`), "token_type is an array"},
		{"cost_type not a string", "anthropic-usage", "cost", aPage(`{"amount":"1","currency":"USD","cost_type":true}`), "cost_type is true or false"},
		{"workspace not a string", "anthropic-usage", "cost", aPage(`{"amount":"1","currency":"USD","workspace_id":{}}`), "workspace_id is an object"},
		{"an amount that is not a decimal", "anthropic-usage", "cost", aPage(`{"amount":"1,5","currency":"USD"}`), "is not a decimal number"},
		{"an id over 256 bytes", "anthropic-usage", "cost", aPage(`{"amount":"1","currency":"USD","model":"` + strings.Repeat("m", 257) + `"}`), "257 bytes, at most 256"},
		{"invalid UTF-8", "anthropic-usage", "cost", aPage("{\"amount\":\"1\",\"currency\":\"USD\",\"model\":\"\xff\"}"), "not valid UTF-8"},
		{"no uncached_input_tokens", "anthropic-usage", "usage", aPage(`{` + usage + `}`), "uncached_input_tokens is missing"},
		{"api_key_id not a string", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,` + usage + `,"api_key_id":1}`), "api_key_id is a number"},
		{"workspace_id not a string in usage", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,` + usage + `,"workspace_id":1}`), "workspace_id is a number"},
		{"model not a string in usage", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,` + usage + `,"model":1}`), "model is a number"},
		{"cache_creation not an object", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,` + usage + `,"cache_creation":3}`), "cache_creation is a number"},
		{"a cache count not a number", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,` + usage + `,"cache_creation":{"ephemeral_5m_input_tokens":"3"}}`), "cache_creation.ephemeral_5m_input_tokens is a string"},
		{"no output_tokens", "anthropic-usage", "usage", aPage(`{"uncached_input_tokens":1,"cache_read_input_tokens":0}`), "output_tokens is missing"},
		{"openai: a bucket with no object", "openai-usage", "cost", oPage(`{"start_time":1790812800,"end_time":1790899200,"results":[]}`), "object is not \"bucket\""},
		{"openai: no start_time", "openai-usage", "cost", oPage(`{"object":"bucket","end_time":1,"results":[]}`), "start_time is missing"},
		{"openai: start_time not whole", "openai-usage", "cost", oPage(`{"object":"bucket","start_time":1.5,"end_time":1,"results":[]}`), "start_time is not a whole number of seconds"},
		{"openai: end_time out of range", "openai-usage", "cost", oPage(`{"object":"bucket","start_time":1790812800,"end_time":99999999999999999,"results":[]}`), "end_time \"99999999999999999\" is out of range"},
		{"openai: no end_time", "openai-usage", "cost", oPage(`{"object":"bucket","start_time":1790812800,"results":[]}`), "end_time is missing"},
		{"openai: no result object", "openai-usage", "cost", oPage(oBucket(`{"amount":{"value":1,"currency":"usd"}}`)), "object is missing"},
		{"openai: no amount", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result"}`)), "amount is missing"},
		{"openai: amount not an object", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":1}`)), "amount is a number, want an object"},
		{"openai: no currency", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"value":1}}`)), "amount.currency is missing"},
		{"openai: no value", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"currency":"usd"}}`)), "amount.value is missing"},
		{"openai: an absurd value", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"value":1e30,"currency":"usd"}}`)), "over the cap"},
		{"openai: line_item not a string", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"value":1,"currency":"usd"},"line_item":1}`)), "line_item is a number"},
		{"openai: api_key_id not a string", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"value":1,"currency":"usd"},"api_key_id":1}`)), "api_key_id is a number"},
		{"openai: project_id not a string", "openai-usage", "cost", oPage(oBucket(`{"object":"organization.costs.result","amount":{"value":1,"currency":"usd"},"project_id":1}`)), "project_id is a number"},
		{"openai: no input_tokens", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","output_tokens":1}`)), "input_tokens is missing"},
		{"openai: no output_tokens", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","input_tokens":1}`)), "output_tokens is missing"},
		{"openai: cached not a number", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","input_tokens":1,"output_tokens":1,"input_cached_tokens":"1"}`)), "input_cached_tokens is a string"},
		{"openai: usage model not a string", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","input_tokens":1,"output_tokens":1,"model":1}`)), "model is a number"},
		{"openai: usage key not a string", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","input_tokens":1,"output_tokens":1,"api_key_id":1}`)), "api_key_id is a number"},
		{"openai: usage project not a string", "openai-usage", "usage", oPage(oBucket(`{"object":"organization.usage.completions.result","input_tokens":1,"output_tokens":1,"project_id":1}`)), "project_id is a number"},
		{"openai: no result object in usage", "openai-usage", "usage", oPage(oBucket(`{"input_tokens":1,"output_tokens":1}`)), "object is missing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ParseProviderUsageFile(c.id, c.report, strings.NewReader(c.body), int64(len(c.body)))
			if err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("err = %v, want one naming %q", err, c.reason)
			}
		})
	}
}

func TestParseProviderUsageFileCountsWhatItRead(t *testing.T) {
	body := `{"data":[{"starting_at":"2026-10-01T00:00:00Z","ending_at":"2026-10-02T00:00:00Z","results":[
		{"amount":"1","currency":"USD","model":"a","token_type":"output_tokens"},
		{"amount":"2","currency":"USD","model":"a","token_type":"output_tokens","context_window":"200k-1M"},
		{"amount":"3","currency":"USD","model":"b","token_type":"output_tokens"}]}],"has_more":false}`
	rows, days, err := ParseProviderUsageFile("anthropic-usage", "cost", strings.NewReader(body), int64(len(body)))
	if err != nil || rows != 2 || len(days) != 1 || days[0] != "2026-10-01" {
		t.Errorf("rows %d days %v err %v; want 2 rows (two context windows of one model summed) on one day", rows, days, err)
	}
	if _, _, err := ParseProviderUsageFile("opencost", "cost", strings.NewReader(body), 1); err == nil {
		t.Error("a connector that is not a provider-usage reader parsed a file")
	}
	if _, _, err := ParseProviderUsageFile("anthropic-usage", "invoice", strings.NewReader(body), 1); err == nil ||
		!strings.Contains(err.Error(), `no report "invoice"`) {
		t.Errorf("an unknown report: %v", err)
	}
}

func TestTooManyPagesOrValuesAreRefused(t *testing.T) {
	pages := "[" + strings.TrimSuffix(strings.Repeat(`{"data":[],"has_more":true},`, usageMaxPagesPerFile+1), ",") + "]"
	if _, _, err := ParseProviderUsageFile("anthropic-usage", "cost", strings.NewReader(pages), 1<<20); err == nil ||
		!strings.Contains(err.Error(), "pages, at most") {
		t.Errorf("too many pages: %v", err)
	}
	// A small file claiming to be small, holding more values than its size allows.
	many := "[" + strings.TrimSuffix(strings.Repeat("0,", usageMaxNodesPerMB+5), ",") + "]"
	if _, _, err := ParseProviderUsageFile("anthropic-usage", "cost", strings.NewReader(many), 10); err == nil ||
		!strings.Contains(err.Error(), "more than") {
		t.Errorf("too many values: %v", err)
	}
}

func TestASumThatWouldWrapIsRefused(t *testing.T) {
	p := &parsedUsageFile{rows: map[usageKey]*usageRow{}}
	r := usageRow{report: "usage", day: "d", model: "m", tokenType: "output_tokens", tokens: math.MaxInt64 - 1}
	if err := p.add(r); err != nil {
		t.Fatal(err)
	}
	r.tokens = 2
	if err := p.add(r); err == nil {
		t.Error("two token counts summing past int64 were added")
	}
	c := usageRow{report: "cost", day: "d", model: "m", tokenType: "x", micros: math.MinInt64 + 1}
	if err := p.add(c); err != nil {
		t.Fatal(err)
	}
	c.micros = -2
	if err := p.add(c); err == nil {
		t.Error("two credits summing past int64 were added")
	}
	if _, ok := addMicros(math.MaxInt64, 1); ok {
		t.Error("addMicros wrapped silently")
	}
	if _, ok := addMicros(math.MinInt64, -1); ok {
		t.Error("addMicros wrapped silently below zero")
	}
	if v, ok := addMicros(2, -3); !ok || v != -1 {
		t.Errorf("addMicros(2, -3) = %d, %v", v, ok)
	}
	s := &usageSummary{models: map[string]bool{}}
	if err := s.addRow(anthropicUsageSpec, &usageRow{report: "cost", micros: math.MaxInt64}); err != nil {
		t.Fatal(err)
	}
	if err := s.addRow(anthropicUsageSpec, &usageRow{report: "cost", micros: 1}); err == nil {
		t.Error("the folder's cost wrapped silently")
	}
	if err := s.addRow(anthropicUsageSpec, &usageRow{report: "usage", tokenType: "output_tokens", tokens: math.MaxInt64}); err != nil {
		t.Fatal(err)
	}
	if err := s.addRow(anthropicUsageSpec, &usageRow{report: "usage", tokenType: "output_tokens", tokens: 1}); err == nil {
		t.Error("the folder's tokens wrapped silently")
	}
}

func TestTheRefusalListIsBounded(t *testing.T) {
	var xs []string
	for i := 0; i < usageRefusalsShown+7; i++ {
		xs = append(xs, fmt.Sprint(i))
	}
	got := boundedList(xs)
	if len(got) != usageRefusalsShown+1 || got[len(got)-1] != "and 7 more" {
		t.Errorf("boundedList kept %d entries ending %q", len(got), got[len(got)-1])
	}
}

func TestReconcileConnectorsAreTheTwoProviderReaders(t *testing.T) {
	var ids []string
	for _, c := range ReconcileConnectors() {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "anthropic-usage,openai-usage" {
		t.Errorf("ReconcileConnectors = %v", ids)
	}
}

// With only the gateway's rows, the default window still ends on the
// gateway's latest day, and every one of its rows reads provider missing.
func TestReconcileWithOnlyTheGatewaysRows(t *testing.T) {
	db := openTestStore(t).DB()
	gatewayCall(t, db, 1, "2026-09-30", "OpenAI", "gpt-5-mini", 10, 0, 0, false)
	rec, err := Reconcile(db, "openai-usage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.To != "2026-09-30" || len(rec.Rows) != 1 || rec.Rows[0].Status != StatusProviderMissing {
		t.Errorf("to %s rows %+v", rec.To, rec.Rows)
	}
	if !strings.Contains(rec.Scope, "every API key") || !strings.Contains(rec.Scope, "every project") {
		t.Errorf("scope sentence %q", rec.Scope)
	}
	// A gateway day that is not a day is refused rather than used as a window.
	if _, err := db.Exec(`UPDATE ai_calls SET day='someday'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(db, "openai-usage", "", ""); err == nil || !strings.Contains(err.Error(), "is not a day") {
		t.Errorf("a malformed latest day: %v", err)
	}
	// A bad saved setting is named rather than ignored.
	if err := Save(db, "openai-usage", map[string]string{"project_ids": "a b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(db, "openai-usage", "", ""); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Errorf("a bad setting: %v", err)
	}
}
