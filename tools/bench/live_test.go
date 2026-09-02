package main

// Tests for the live caller. None of them, ever, reach the real vendor:
// anthropicAPIBase is repointed at a local httptest.Server for the length
// of each test and restored immediately after, which is the only way to
// prove this file's request and response handling without doing the one
// thing the standing rule for this command forbids. No test here, or
// anywhere in this package, ever passes -live with a real engine name.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// withFakeAnthropic points anthropicAPIBase at a local server for the
// length of the test and restores the real default after, so a test
// failure or panic can never leave a later test (or, worse, a later manual
// run) pointed at localhost by accident.
func withFakeAnthropic(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := anthropicAPIBase
	anthropicAPIBase = srv.URL
	t.Cleanup(func() {
		anthropicAPIBase = prev
		srv.Close()
	})
	return srv
}

func TestLiveCallAnthropicSendsTheRightRequest(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	var gotPath, gotKey string
	var gotBody struct {
		Model    string `json:"model"`
		MaxTok   int    `json:"max_tokens"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	withFakeAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"the deliverable"}],
			"usage":{"input_tokens":50,"output_tokens":20}}`))
	})

	res, err := liveCallAnthropic(context.Background(), "claude-sonnet-5", "explain it", 1200)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("x-api-key = %q, want test-key", gotKey)
	}
	if gotBody.Model != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", gotBody.Model)
	}
	if gotBody.MaxTok != 1200 {
		t.Errorf("max_tokens = %d, want 1200", gotBody.MaxTok)
	}
	if gotBody.Thinking.Type != "disabled" {
		t.Errorf("thinking.type = %q, want disabled: the same reason tools/run's own "+
			"anthropicBody turns it off", gotBody.Thinking.Type)
	}
	if res.Text != "the deliverable" || res.InTokens != 50 || res.OutTokens != 20 {
		t.Errorf("liveCallAnthropic = %+v, want the fake server's own response parsed back", res)
	}
}

func TestLiveCallAnthropicRefusesWithNoKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := liveCallAnthropic(context.Background(), "claude-sonnet-5", "x", 100); err == nil {
		t.Error("no key was set and the call was not refused")
	}
}

func TestLiveCallAnthropicSurfacesAnErrorStatus(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	withFakeAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	})
	_, err := liveCallAnthropic(context.Background(), "claude-sonnet-5", "x", 100)
	if err == nil {
		t.Fatal("a 429 was not surfaced as an error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error does not name the status: %v", err)
	}
}

// liveCall refuses any engine it has no caller for, plainly, rather than
// pretending to support openrouter or bedrock here: those stay tools/run's
// own call(), which this file does not reuse (see this file's package
// comment).
func TestLiveCallRefusesEnginesItDoesNotImplement(t *testing.T) {
	for _, engine := range []string{"openrouter", "bedrock", "claude-cli"} {
		if _, err := liveCall(context.Background(), engine, "m", "p", 100); err == nil {
			t.Errorf("liveCall(%q) was not refused", engine)
		}
	}
}

func TestCostMicrosMatchesTheSameFormulaToolsRunUses(t *testing.T) {
	// $3.00 in per million, $15.00 out per million (anthropic/claude-sonnet-5,
	// internal/engines/prices.go): 1000 in-tokens and 500 out-tokens is
	// 3.00e-3 + 7.50e-3 dollars, i.e. exactly 10,500 micros by hand.
	//
	// want is a hand-computed constant, not a second float64 expression:
	// two independently written expressions that are mathematically the
	// same order of operations can still round to adjacent int64s one ULP
	// apart depending on whether the compiler folds them at compile time or
	// emits an FMA at runtime, and `go test -cover`'s instrumentation
	// changes exactly that -- @measured, this test flaked between 10499 and
	// 10500 depending on -cover alone, 2026-09-03. A tolerance is the
	// correct fix, not chasing codegen: a real formula bug misses by far
	// more than one part in ten thousand.
	p := engines.Price{InPerM: 3.00, OutPerM: 15.00}
	got := costMicros(1000, 500, p)
	const want, tolerance = 10_500, 2
	if diff := got - want; diff < -tolerance || diff > tolerance {
		t.Errorf("costMicros(1000, 500, ...) = %d, want %d +/- %d", got, want, tolerance)
	}
	if got <= 0 {
		t.Fatal("a real call with real tokens costs zero micros, which cannot be right")
	}
}
