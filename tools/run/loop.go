package main

// The tool loop: call(), Anthropic and OpenRouter routes, made able to ask
// for a tool and be answered.
//
// B2-SPEC.md section 3.4. Every prompt now carries the catalogue
// (tools.go); while the model asks for tools and rounds < maxToolRounds,
// each call is dispatched (dispatch.go), its result appended to the
// conversation, and the round sent again; the LAST round is sent with no
// tools at all, so the model has no choice left but to answer.
//
// call(), callAnthropic and callOpenRouter (live.go) are UNCHANGED: they
// are still the single-shot path, still directly tested by
// anthropic_test.go and bedrock_test.go, and still what runs for an engine
// this loop does not cover. Bedrock is that engine here. Its caller
// (bedrock.go's bedrockRequest) does build a real Converse body -- the
// type is literally *bedrockruntime.ConverseInput -- but it sets no
// ToolConfig and callBedrock parses no tool_use content block from the
// response; adding that is a third, structurally different tool-call shape
// (the AWS SDK's document.Interface, neither Anthropic's JSON nor OpenAI's)
// that section 3.1's catalogue never asks for -- it renders exactly two
// shapes, Anthropic and OpenAI-style -- and no test in section 4 exercises
// it. So Bedrock keeps calling call() directly, one round, no tools, same
// as before this file existed.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// maxToolRounds bounds the loop: on the LAST round (round == maxToolRounds)
// no tools are offered, so the model cannot ask for one and has to answer.
const maxToolRounds = 6

// loopsFor is how many model calls one execute() of this task can make: the
// loop's ceiling for the two engines it covers, one call for every other
// engine, exactly as before this file existed.
func loopsFor(engine string) int {
	switch engine {
	case "anthropic", "openrouter":
		return maxToolRounds
	}
	return 1
}

// requestedCall is one tool call a model asked for, in a provider-neutral
// shape: an id the provider wants echoed back with the result, the tool
// name, and its arguments as the raw JSON the model produced.
type requestedCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// roundResult is what one round, on either provider, comes back with:
// either Text (the model answered) or Calls (it wants tools), the usage
// THIS round cost, never both meaningfully at once though a provider can
// technically send text alongside a tool_use block.
type roundResult struct {
	Text      string
	InTokens  int
	OutTokens int
	Calls     []requestedCall
}

func roundCostMicros(inTok, outTok int, p engines.Price) int64 {
	in := float64(inTok) / 1e6 * p.InPerM
	out := float64(outTok) / 1e6 * p.OutPerM
	return int64((in + out) * 1e6)
}

// runToolLoop is what execute() calls instead of call() now. For anthropic
// and openrouter it drives the loop; for everything else it is call(),
// unchanged, wrapped so execute() has one call site regardless of engine.
func runToolLoop(ctx context.Context, db, roDB *sql.DB, e estimate, sentPrompt string,
	maxTok int, gw gatewayHeaders, a crew.Analyst, b bus) (callResult, error) {
	switch e.Engine {
	case "anthropic":
		return anthropicToolLoop(ctx, db, roDB, e, sentPrompt, maxTok, gw, a, b)
	case "openrouter":
		return openRouterToolLoop(ctx, db, roDB, e, sentPrompt, maxTok, a, b)
	default:
		return call(ctx, e.Engine, e.Model, sentPrompt, maxTok, gw)
	}
}

// dispatchAll runs every requested call through dispatch() and returns the
// text each one produced, in the order asked for -- the shape both
// providers' tool-result messages need, whatever wire form they carry it
// in.
func dispatchAll(ctx context.Context, db, roDB *sql.DB, a crew.Analyst, calls []requestedCall, b bus) []dispatchResult {
	out := make([]dispatchResult, len(calls))
	for i, c := range calls {
		out[i] = dispatch(ctx, db, roDB, a, c.Name, c.Args, b)
	}
	return out
}

// ------------------------------------------------------------- anthropic

// anthropicBlock is one block of one message's content, in the union shape
// the Messages API uses for both directions: text, tool_use (what the
// model sends when it wants a tool) and tool_result (what this runner
// sends back). Fields absent for a block's own Type are simply omitted by
// the omitempty tags rather than given a second, block-specific type.
type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

func anthropicRoundBody(model string, messages []anthropicMsg, tools []map[string]any, maxTok int) ([]byte, error) {
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

func anthropicRoundRequest(ctx context.Context, key, model string, messages []anthropicMsg,
	tools []map[string]any, maxTok int, gw gatewayHeaders) (*http.Request, error) {
	body, err := anthropicRoundBody(model, messages, tools, maxTok)
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
		// Every round carries the same three headers: the run and the
		// analyst do not change mid-task, and the budget is the same
		// figure gatewayHeadersFor already worked out once for this task.
		req.Header.Set("x-fuse-run-id", gw.RunID)
		req.Header.Set("x-fuse-agent-id", gw.AgentID)
		req.Header.Set("x-fuse-budget-usd", gw.BudgetUSD)
		if gw.ParentRunID != "" {
			req.Header.Set("x-fuse-parent-run-id", gw.ParentRunID)
		}
	}
	return req, nil
}

// anthropicRound sends one round and returns the model's answer or its
// requested tools, together with the assistant's own content blocks
// exactly as the API sent them: the caller appends those, unmodified, as
// the next "assistant" message before it can send a "user" message of
// tool_result blocks, which is what the Messages API requires.
func anthropicRound(ctx context.Context, model string, messages []anthropicMsg,
	tools []map[string]any, maxTok int, gw gatewayHeaders) (roundResult, []anthropicBlock, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return roundResult{}, nil, fmt.Errorf("ANTHROPIC_API_KEY is not set in this process")
	}
	req, err := anthropicRoundRequest(ctx, key, model, messages, tools, maxTok, gw)
	if err != nil {
		return roundResult{}, nil, err
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return roundResult{}, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusPaymentRequired && gw.URL != "" {
		return roundResult{}, nil, refusal{parseGatewayRefusal(raw)}
	}
	if resp.StatusCode != 200 {
		return roundResult{}, nil, fmt.Errorf("anthropic answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return roundResult{}, nil, fmt.Errorf("anthropic answered 200 with an empty body")
	}

	var out struct {
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return roundResult{}, nil, fmt.Errorf("anthropic's answer did not parse: %w", err)
	}

	rr := roundResult{InTokens: out.Usage.InputTokens, OutTokens: out.Usage.OutputTokens}
	var text strings.Builder
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "tool_use":
			rr.Calls = append(rr.Calls, requestedCall{ID: c.ID, Name: c.Name, Args: c.Input})
		}
	}
	rr.Text = text.String()
	if len(rr.Calls) == 0 && rr.Text == "" {
		return rr, out.Content, fmt.Errorf(
			"anthropic returned no text and asked for no tool (stop_reason %q, %d output tokens)",
			out.StopReason, out.Usage.OutputTokens)
	}
	return rr, out.Content, nil
}

func anthropicToolLoop(ctx context.Context, db, roDB *sql.DB, e estimate, prompt string,
	maxTok int, gw gatewayHeaders, a crew.Analyst, b bus) (callResult, error) {
	messages := []anthropicMsg{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: prompt}}}}
	var totalIn, totalOut int
	var totalActual int64

	for round := 1; round <= maxToolRounds; round++ {
		var tools []map[string]any
		if round < maxToolRounds {
			tools = anthropicTools()
		}
		rr, assistantBlocks, err := anthropicRound(ctx, e.Model, messages, tools, maxTok, gw)
		totalIn += rr.InTokens
		totalOut += rr.OutTokens
		totalActual += roundCostMicros(rr.InTokens, rr.OutTokens, e.Price)
		res := callResult{InTokens: totalIn, OutTokens: totalOut, ActualMicros: totalActual}
		if err != nil {
			return res, err
		}
		if len(rr.Calls) == 0 {
			res.Text = rr.Text
			return res, nil
		}

		messages = append(messages, anthropicMsg{Role: "assistant", Content: assistantBlocks})
		results := dispatchAll(ctx, db, roDB, a, rr.Calls, b)
		var toolMsg anthropicMsg
		toolMsg.Role = "user"
		for i, c := range rr.Calls {
			toolMsg.Content = append(toolMsg.Content, anthropicBlock{
				Type: "tool_result", ToolUseID: c.ID, Content: results[i].Text,
			})
		}
		messages = append(messages, toolMsg)
	}
	// Unreachable in practice: round==maxToolRounds sends no tools, so the
	// model cannot ask for one and rr.Calls is always empty by then.
	return callResult{InTokens: totalIn, OutTokens: totalOut, ActualMicros: totalActual},
		fmt.Errorf("the tool loop ran past its round cap without an answer")
}

// ------------------------------------------------------------- openrouter

// openAIToolCall is one entry of a `tool_calls` array, in the shape both
// the assistant message that ASKS for one and this file's own bookkeeping
// use identically.
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIMsg is one message in an OpenAI-shaped conversation. A "tool" role
// message needs ToolCallID; an "assistant" message that asked for tools
// carries ToolCalls and usually no Content; everything else uses Content
// alone.
type openAIMsg struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openRouterEndpoint is a var, not a literal, only so a test can point the
// loop at a fake server: the Anthropic route already has an override for
// exactly this (gatewayHeaders.URL, which is what -gateway and B6's whole
// suite of tests use), and OpenRouter has no equivalent one. callOpenRouter
// in live.go keeps its own literal, unchanged; this var exists for the
// loop's own round function alone.
var openRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"

func openRouterRoundBody(model string, messages []openAIMsg, tools []map[string]any, maxTok int) ([]byte, error) {
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"messages":   messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

func openRouterRound(ctx context.Context, model string, messages []openAIMsg,
	tools []map[string]any, maxTok int) (roundResult, openAIMsg, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		return roundResult{}, openAIMsg{}, fmt.Errorf("OPENROUTER_API_KEY is not set in this process")
	}
	body, err := openRouterRoundBody(model, messages, tools, maxTok)
	if err != nil {
		return roundResult{}, openAIMsg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		openRouterEndpoint, bytes.NewReader(body))
	if err != nil {
		return roundResult{}, openAIMsg{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return roundResult{}, openAIMsg{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return roundResult{}, openAIMsg{}, fmt.Errorf("the router answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return roundResult{}, openAIMsg{}, fmt.Errorf("the router answered 200 with an empty body")
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content   string           `json:"content"`
				ToolCalls []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return roundResult{}, openAIMsg{}, fmt.Errorf("the router's answer did not parse: %w", err)
	}
	if len(out.Choices) == 0 {
		return roundResult{}, openAIMsg{}, fmt.Errorf("the router returned no answer")
	}
	msg := out.Choices[0].Message

	rr := roundResult{InTokens: out.Usage.PromptTokens, OutTokens: out.Usage.CompletionTokens}
	assistant := openAIMsg{Role: "assistant", Content: msg.Content}
	if len(msg.ToolCalls) > 0 {
		assistant.ToolCalls = msg.ToolCalls
		for _, tc := range msg.ToolCalls {
			rr.Calls = append(rr.Calls, requestedCall{
				ID: tc.ID, Name: tc.Function.Name, Args: json.RawMessage(tc.Function.Arguments)})
		}
	}
	rr.Text = msg.Content
	if len(rr.Calls) == 0 && strings.TrimSpace(rr.Text) == "" {
		return rr, assistant, fmt.Errorf(
			"the router returned no text and asked for no tool (finish_reason %q)",
			out.Choices[0].FinishReason)
	}
	return rr, assistant, nil
}

func openRouterToolLoop(ctx context.Context, db, roDB *sql.DB, e estimate, prompt string,
	maxTok int, a crew.Analyst, b bus) (callResult, error) {
	messages := []openAIMsg{{Role: "user", Content: prompt}}
	var totalIn, totalOut int
	var totalActual int64

	for round := 1; round <= maxToolRounds; round++ {
		var tools []map[string]any
		if round < maxToolRounds {
			tools = openAITools()
		}
		rr, assistant, err := openRouterRound(ctx, e.Model, messages, tools, maxTok)
		totalIn += rr.InTokens
		totalOut += rr.OutTokens
		totalActual += roundCostMicros(rr.InTokens, rr.OutTokens, e.Price)
		res := callResult{InTokens: totalIn, OutTokens: totalOut, ActualMicros: totalActual}
		if err != nil {
			return res, err
		}
		if len(rr.Calls) == 0 {
			res.Text = rr.Text
			return res, nil
		}

		messages = append(messages, assistant)
		results := dispatchAll(ctx, db, roDB, a, rr.Calls, b)
		for i, c := range rr.Calls {
			messages = append(messages, openAIMsg{
				Role: "tool", ToolCallID: c.ID, Content: results[i].Text,
			})
		}
	}
	return callResult{InTokens: totalIn, OutTokens: totalOut, ActualMicros: totalActual},
		fmt.Errorf("the tool loop ran past its round cap without an answer")
}
