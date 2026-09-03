package deliver

// The one metered call path: everything that can spend money, shared
// between tools/run and tools/bench, so both are metered through the same
// TokenFuse gateway with the same wire and the same refusals.
//
// B6B-SPEC.md. Moved from tools/run/live.go's call() and everything it
// needed to spend correctly: the three engine bodies (this file's
// callAnthropic and callOpenRouter, bedrock.go's callBedrock), the Anthropic
// request builder and its x-fuse-* headers, and the parse of a gateway's
// 402 refusal. tools/run keeps a one-line wrapper at its old call site, the
// same move packet() and prompt() made in B7 (packet.go/mandate.go's own
// comments describe the same shape). tools/bench, which briefly grew a
// private caller of its own and had it refused in review (coordinator pass
// on PR #25, 2026-09-03 -- see CLAUDE.md invariant 29), now calls this
// directly behind -live.
//
// What did NOT move, and why: execute(), runBudget, gatewayHeadersFor,
// gatewayConfig and the bus all stay in tools/run -- they are the RUN's own
// orchestration (a task's guard, a run's ceiling, the estate bus), not part
// of making one call. tools/run keeps gatewayHeaders and callResult as type
// aliases of this file's Gateway and Result (`type gatewayHeaders =
// deliver.Gateway`), so gatewayHeadersFor and every existing test
// constructing a gatewayHeaders{} literal needed no change at all.
//
// loop.go's own anthropicRound/openRouterRound (the tool-loop's round
// functions) are a SEPARATE, pre-existing implementation from B2 that
// already duplicated the Anthropic wire independently of call() -- its own
// package comment says so ("call(), callAnthropic and callOpenRouter... are
// UNCHANGED... still what runs for an engine this loop does not cover").
// Production reaches call() (now this file's Call) only for bedrock and any
// engine outside the loop; anthropic and openrouter traffic in tools/run
// goes through loop.go's own request-building, untouched by this move.
// gateway_test.go's three execute()-based tests (TestAGatewayCallCarriesThe
// AnalystsIdentity and its two neighbours) exercise THAT path and so stay in
// tools/run unchanged, proving nothing about the wire moved there either.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Gateway is what ONE call tells TokenFuse: who is asking, on whose run,
// and what it may spend. Renamed, once, from tools/run's own gatewayHeaders
// (B6B-SPEC.md section 2: "one gateway type" replacing the two this runner
// used to carry -- this one, and gatewayConfig, the run-wide URL/host/
// ceiling setup a caller still builds a Gateway FROM but that never itself
// crosses this file's boundary). Built fresh per call because the budget is
// the tighter of a run's ceiling and THIS call's own guard, which differs
// call to call even when the run id and the agent id do not.
type Gateway struct {
	URL       string // empty means "do not route through a gateway at all"
	RunID     string
	AgentID   string
	BudgetUSD string // already formatted, two decimals minimum

	// ParentRunID is sent only when non-empty. Neither caller today has a
	// notion of a parent run (tools/run: crew.Analyst.Parent names who an
	// agent acts on behalf of, not which run started this one; tools/bench:
	// one invocation is one run, nothing starts it), and this field must
	// never be invented for either. It exists so a caller that DOES have
	// one someday can set it without another signature change.
	ParentRunID string

	// x-fuse-outcome is deliberately not a field here and is never sent by
	// this file. It is TokenFuse's opaque tag for how a call ended, and
	// nothing here has anything worth reporting there yet: adding it later
	// is an additive change, and sending an empty or guessed value now
	// would be worse than the header's plain absence.
}

// Result is what came back, and what it actually cost. Renamed, once, from
// tools/run's own callResult. ActualMicros is always zero coming out of
// Call: it always was, even before this move -- callAnthropic, callOpenRouter
// and callBedrock have never populated it (only loop.go's own round
// functions do, from InTokens/OutTokens via ActualMicros below, for the two
// engines the tool loop covers). A caller outside a tool loop (tools/bench's
// live path, or tools/run's bedrock route) that needs the real cost computes
// it itself from InTokens/OutTokens via ActualMicros.
type Result struct {
	Text         string
	InTokens     int
	OutTokens    int
	ActualMicros int64
}

// GatewayRefusal marks an error that came from the GATEWAY refusing a call
// over budget (an HTTP 402), never from the call merely failing. Renamed,
// once, from tools/run's own refusal type, which stays exactly where it was
// (tools/run/live.go): it is that package's own run-stopping classification
// (spend()'s loop stops the whole run on one, and marks a single task
// blocked on anything else), and this file has no notion of a "run" to
// stop. tools/run's call() wrapper translates a GatewayRefusal it gets back
// from Call into its own local refusal{} at the one call site that needs to
// know the difference.
type GatewayRefusal struct{ error }

// Call routes to the engine the analyst (or, for the bench, the case) was
// hired with, exactly as tools/run's own call() always has.
//
// gw is only used by the Anthropic route. OpenRouter and Bedrock are
// unchanged: TokenFuse speaks the Anthropic Messages API at /v1/messages and
// nothing OpenAI-shaped, so those two keep calling their own hosts directly
// until it grows a route for them.
func Call(ctx context.Context, engine, model, prompt string, maxTok int, gw Gateway) (Result, error) {
	switch engine {
	case "openrouter":
		return callOpenRouter(ctx, model, prompt, maxTok)
	case "anthropic":
		return callAnthropic(ctx, model, prompt, maxTok, gw)
	case "bedrock":
		return callBedrock(ctx, model, prompt, maxTok)
	}
	return Result{}, fmt.Errorf("no caller is written for engine %q", engine)
}

// anthropicBody is the request body, separate so the one thing that is easy
// to get silently wrong can be tested without spending anything.
//
// Thinking is turned OFF explicitly. Four tasks on a full run came back with
// "no text", and the reason, once the error said it, was exact:
//
//	stop_reason "max_tokens", blocks: thinking, 1200 output tokens
//
// The model spent the entire budget reasoning and never reached the answer.
// What this runner wants is the deliverable, not the reasoning, so the fix is
// to ask for the deliverable. It also makes the calls CHEAPER, which is the
// unusual direction for a fix.
func anthropicBody(model, prompt string, maxTok int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
}

// anthropicRequest builds the outbound request, separately from sending it,
// so the URL and every header can be asserted without a network call and
// without a key.
//
// gw.URL empty routes to api.anthropic.com exactly as this runner did before
// it knew a gateway existed. gw.URL set routes to <gateway>/v1/messages
// instead and carries the x-fuse-* headers TokenFuse reads for metering and
// attribution; the API key travels in x-api-key exactly as today, which is
// what lets the gateway pass it through unchanged to the real upstream.
func anthropicRequest(ctx context.Context, key, model, prompt string, maxTok int, gw Gateway) (*http.Request, error) {
	body, err := anthropicBody(model, prompt, maxTok)
	if err != nil {
		return nil, err
	}
	endpoint := "https://api.anthropic.com/v1/messages"
	if gw.URL != "" {
		endpoint = gw.URL + "/v1/messages"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if gw.URL != "" {
		// TokenFuse refuses a call with no run id (400 metering_required), so
		// this is never conditional on RunID being non-empty: openBus mints
		// one for every invocation now, on or off the estate bus, for exactly
		// this reason, and callAnthropic below refuses first when a caller
		// somehow still reaches here with one empty.
		req.Header.Set("x-fuse-run-id", gw.RunID)
		req.Header.Set("x-fuse-agent-id", gw.AgentID)
		req.Header.Set("x-fuse-budget-usd", gw.BudgetUSD)
		if gw.ParentRunID != "" {
			req.Header.Set("x-fuse-parent-run-id", gw.ParentRunID)
		}
		// x-fuse-outcome is intentionally not set. See Gateway's own comment.
	}
	return req, nil
}

func callAnthropic(ctx context.Context, model, prompt string, maxTok int, gw Gateway) (Result, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return Result{}, fmt.Errorf("ANTHROPIC_API_KEY is not set in this process")
	}
	// Boundary (B6B-SPEC.md section 4): a gateway TokenFuse cannot meter is
	// refused before the request is even built, never let through to
	// surface as the gateway's OWN 400 metering_required. Both tools/run and
	// tools/bench get this for free because both reach the gateway only
	// through Call: tools/run's run id is minted unconditionally (bus.go's
	// newRunID) and its agent id always comes from an analyst price() has
	// already required to be named, so this never fires there in practice;
	// tools/bench mints its own run id and reads a case's analyst fresh, so
	// this is where the check actually earns its place.
	if gw.URL != "" {
		if gw.RunID == "" {
			return Result{}, fmt.Errorf("the gateway is set but this call's run id is " +
				"empty: TokenFuse refuses a call with no run id")
		}
		if gw.AgentID == "" {
			return Result{}, fmt.Errorf("the gateway is set but this call's agent id is " +
				"empty: TokenFuse cannot attribute spend with no agent id")
		}
	}
	req, err := anthropicRequest(ctx, key, model, prompt, maxTok, gw)
	if err != nil {
		return Result{}, err
	}

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// A 402 from the GATEWAY is a budget refusal, not an ordinary failed
	// call, and tools/run's call() wrapper turns this into its own local
	// refusal{} type, which is what makes spend()'s loop stop the run
	// instead of marking the task blocked -- the reading a budget event
	// deserves over "this analyst's call failed".
	//
	// Gated on gw.URL != "" so a 402 that somehow came from api.anthropic.com
	// directly (Anthropic does not use this status, but nothing here should
	// assume that forever) is never misread as a gateway refusal it did not
	// send.
	if resp.StatusCode == http.StatusPaymentRequired && gw.URL != "" {
		return Result{}, GatewayRefusal{ParseGatewayRefusal(raw)}
	}
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("anthropic answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Result{}, fmt.Errorf("anthropic answered 200 with an empty body")
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("anthropic's answer did not parse: %w", err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		// Say WHY. "returned no text" blocked four tasks on a full run and
		// named nothing a person could act on: a refusal, a turn that stopped
		// on max_tokens before writing a word, and an empty content array are
		// three different problems wearing one message.
		kinds := make([]string, 0, len(out.Content))
		for _, c := range out.Content {
			kinds = append(kinds, c.Type)
		}
		where := "no content blocks at all"
		if len(kinds) > 0 {
			where = "blocks: " + strings.Join(kinds, ", ")
		}
		return Result{}, fmt.Errorf(
			"anthropic returned no text (stop_reason %q, %s, %d output tokens)",
			out.StopReason, where, out.Usage.OutputTokens)
	}
	return Result{
		Text:      text.String(),
		InTokens:  out.Usage.InputTokens,
		OutTokens: out.Usage.OutputTokens,
	}, nil
}

// callOpenRouter is the OpenAI-shaped route.
func callOpenRouter(ctx context.Context, model, prompt string, maxTok int) (Result, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		return Result{}, fmt.Errorf("OPENROUTER_API_KEY is not set in this process")
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// The body can echo a request, so only the status and a short prefix
		// travel: a key does not end up in a log through an error message.
		return Result{}, fmt.Errorf("the router answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Result{}, fmt.Errorf("the router answered 200 with an empty body")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("the router's answer did not parse: %w", err)
	}
	if len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("the router returned no answer")
	}
	return Result{
		Text:      out.Choices[0].Message.Content,
		InTokens:  out.Usage.PromptTokens,
		OutTokens: out.Usage.CompletionTokens,
	}, nil
}

// ParseGatewayRefusal reads TokenFuse's 402 body into the sentence a person
// reads. The body is untrusted input from a process this runner does not
// control: a body that is not the documented shape still produces a readable
// sentence rather than a panic or a silently empty one. Exported because
// loop.go's own anthropicRound (a separate, pre-existing implementation,
// see this file's package comment) reads a 402 from the SAME gateway on its
// own wire and has always used this parse; tools/run keeps a one-line
// wrapper of the same unexported name so that call site needed no change.
func ParseGatewayRefusal(raw []byte) error {
	var out struct {
		Error struct {
			Type      string  `json:"type"`
			BudgetUSD float64 `json:"budget_usd"`
			SpentUSD  float64 `json:"spent_usd"`
			Reason    string  `json:"reason"`
			RunID     string  `json:"run_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error.Reason == "" {
		return fmt.Errorf("the gateway refused this call with 402, and its body "+
			"was not the documented shape: %s", trim(strings.TrimSpace(string(raw)), 200))
	}
	return fmt.Errorf("the gateway refused this call: %s (budget %.4f, spent %.4f, run %s)",
		out.Error.Reason, out.Error.BudgetUSD, out.Error.SpentUSD, out.Error.RunID)
}

// NormalizeGateway validates -gateway and strips a trailing slash, so
// <gateway>/v1/messages never doubles a slash. A scheme other than http(s) is
// refused here, before either binary does anything, rather than surfacing as
// a confusing dial error on the first call somebody happens to make.
// Exported so tools/bench validates -gateway "the same way" tools/run does
// (B6B-SPEC.md section 2); tools/run keeps a one-line wrapper of the old
// unexported name so its own existing tests needed no change.
func NormalizeGateway(raw string) (string, error) {
	// The literal empty string, and only that, means "not configured": it is
	// what an unset flag defaulting to GatewayEnvDefault() carries when
	// COSTCREW_GATEWAY is not set either. Anything else that trims down to
	// nothing (whitespace on its own) was NOT left unset; it is refused
	// rather than silently read the same as "off", which would turn a typo
	// or a bad script interpolation into a quiet fallback to
	// api.anthropic.com nobody asked for.
	if raw == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if trimmed == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("-gateway %q is not an http(s) URL", raw)
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// GatewayEnvDefault backs -gateway's default with COSTCREW_GATEWAY, so an
// installation can set it once rather than on every invocation's command
// line, in both binaries. Exported for the same reason NormalizeGateway is;
// tools/run keeps a one-line wrapper of the old unexported name.
func GatewayEnvDefault() string {
	return strings.TrimSpace(os.Getenv("COSTCREW_GATEWAY"))
}

// GatewayBudgetUSD is the tighter of a run's ceiling and one call's own
// guard, formatted the way TokenFuse's x-fuse-budget-usd wants it. Sending
// the wider of the two would let the gateway wave through a call this
// runner's own reservation would already have refused, which would make the
// header a decoration rather than a real second bound. Exported: tools/run
// keeps a one-line wrapper (used from gatewayHeadersFor, which layers a
// task's own guard on top); tools/bench, which has no per-case guard the way
// a crew task does, calls it with the whole run's own worst case in both
// arguments, which is the same "no guard, use the run figure" fallback this
// formula has always given.
func GatewayBudgetUSD(runCeiling, taskGuard money.Cents) string {
	b := runCeiling
	if taskGuard > 0 && taskGuard < b {
		b = taskGuard
	}
	return b.String()
}

// trim is a small, deliberately duplicated copy of tools/run/main.go's own
// trim: a message-formatting helper, not part of "spending correctly", and
// tools/run's copy stays there unchanged because loop.go, execute() and
// report() all still use it for things that never moved.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
