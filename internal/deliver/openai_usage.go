package deliver

// OpenAIUsage is the usage block of an OpenAI-shaped chat completion, the one
// type every reader of that wire decodes into: the single-shot openrouter and
// local calls here, and the tool loop's round in tools/run.
//
// Why it is more than two fields: measured 2026-10-07 against Vertex AI's
// OpenAI-compatible endpoint (google/gemini-2.5-flash) through the local
// engine, a non-streamed answer reported completion_tokens 59, reasoning 560,
// prompt 14, total 633. Google's completion_tokens EXCLUDES the reasoning
// (633 = 14 + 59 + 560); OpenAI's INCLUDES it (total = prompt + completion).
// Reading completion_tokens alone counted 59 of 619 generated tokens, so the
// token ceiling, the runner's own price and the tool_call event all
// under-counted a thinking model by nine tenths.
//
// completion_tokens_details.reasoning_tokens is deliberately not read: it is
// inside completion_tokens on one vendor and outside on the other, and adding
// it would double-count OpenAI. The total is the one figure both agree on.
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OutputTokens is what the model generated: completion_tokens, or
// total_tokens minus prompt_tokens when that is larger.
//
// Equal to completion_tokens for OpenAI-shaped usage (the total is exactly the
// two parts), when the total is absent (decoded as 0), and when a server sends
// a total smaller than its own parts: a hostile or broken total never REDUCES
// the count and never underflows it. A negative prompt is not believed as a
// reason to grow the output (a minus sign would otherwise turn into tokens),
// and the subtraction cannot wrap: both sides are non-negative before it.
func (u OpenAIUsage) OutputTokens() int {
	out := u.CompletionTokens
	if u.PromptTokens < 0 || u.TotalTokens <= 0 {
		return out
	}
	if beyondPrompt := u.TotalTokens - u.PromptTokens; beyondPrompt > out {
		return beyondPrompt
	}
	return out
}
