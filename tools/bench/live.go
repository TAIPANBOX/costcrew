package main

// The live path: -live with a real engine. NEVER run by this agent, in any
// test or otherwise -- the standing money rule this repository was given
// for B7 says so in so many words, and every test in this package proves it
// by never passing -live with anything but a fake local server.
//
// It does NOT reuse tools/run/live.go's call(): that function is entangled
// with TokenFuse gateway headers (gatewayHeaders, x-fuse-* -- a
// console-specific concern this bench has no -gateway flag for at all, and
// gateway_test.go is 294 lines proving that path works), and Go will not
// let a second "package main" import it regardless (confirmed empirically:
// `import ".../tools/run"` fails with "is a program, not an importable
// package"). Moving call() out from under that would have risked
// well-tested production code for a bench code path nobody may exercise in
// this delivery. So this is a small, separately-written, separately-tested
// caller instead, scoped to what the bench actually needs: it reuses
// internal/deliver.Prompt for the same prompt tools/run would send (this IS
// shared, safely, since Prompt carries no gateway concern at all), and
// implements only the Anthropic wire, which is what every worst-case price
// this package prints is computed against. -live with any other engine
// name is refused with a plain "not implemented" error rather than a
// half-built OpenRouter or Bedrock caller nobody can prove correct here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// liveResult is what a real call returned: the text and the token counts a
// price turns into a cost. Deliberately smaller than tools/run's
// callResult: this file never settles a budget or writes a draft.
type liveResult struct {
	Text      string
	InTokens  int
	OutTokens int
}

// anthropicAPIBase is a var so a test can point it at an httptest.Server
// instead of the real vendor: the only way to prove this file's request and
// response handling without ever making the network call the standing rule
// forbids.
var anthropicAPIBase = "https://api.anthropic.com"

// liveCall routes to the one engine this file knows how to call for real.
func liveCall(ctx context.Context, engine, model, prompt string, maxTok int) (liveResult, error) {
	if engine == "anthropic" {
		return liveCallAnthropic(ctx, model, prompt, maxTok)
	}
	return liveResult{}, fmt.Errorf(
		"the bench has no live caller for engine %q; only anthropic is implemented here "+
			"(tools/run's call() covers openrouter and bedrock too, but this file does not "+
			"reuse it -- see this file's own comment)", engine)
}

// anthropicBody mirrors tools/run/live.go's own anthropicBody: thinking
// off, so the call spends its budget on the deliverable rather than on
// reasoning nobody reads.
func anthropicBody(model, prompt string, maxTok int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
}

func liveCallAnthropic(ctx context.Context, model, prompt string, maxTok int) (liveResult, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return liveResult{}, fmt.Errorf("ANTHROPIC_API_KEY is not set in this process")
	}
	body, err := anthropicBody(model, prompt, maxTok)
	if err != nil {
		return liveResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicAPIBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return liveResult{}, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return liveResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return liveResult{}, fmt.Errorf("anthropic answered %d: %s", resp.StatusCode, trim(string(raw), 160))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return liveResult{}, fmt.Errorf("anthropic's answer did not parse: %w", err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		return liveResult{}, fmt.Errorf("anthropic returned no text")
	}
	return liveResult{Text: text.String(), InTokens: out.Usage.InputTokens, OutTokens: out.Usage.OutputTokens}, nil
}

// costMicros is the same arithmetic tools/run/loop.go's roundCostMicros
// uses: actual tokens at the model's own published price, in micro-dollars.
func costMicros(inTok, outTok int, p engines.Price) int64 {
	in := float64(inTok) / 1e6 * p.InPerM
	out := float64(outTok) / 1e6 * p.OutPerM
	return int64((in + out) * 1e6)
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
