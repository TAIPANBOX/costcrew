package main

import (
	"encoding/json"
	"testing"
)

// The request asks for a deliverable, not for reasoning.
//
// Red first: without the field, this read back nothing, which is what four
// tasks on a full run hit. They came back "no text", and once the error named
// its reason it was exact:
//
//	stop_reason "max_tokens", blocks: thinking, 1200 output tokens
//
// The model spent all 1200 tokens reasoning and never reached an answer. The
// task blocked, the person got no deliverable, and the call was billed in full.
func TestAnthropicIsAskedForAnAnswerRatherThanReasoning(t *testing.T) {
	raw, err := anthropicBody("anthropic/claude-sonnet-5", "write it", 1200)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Thinking.Type != "disabled" {
		t.Errorf("thinking is %q, want disabled: the model can spend the whole "+
			"token budget reasoning and never write the deliverable, and the "+
			"call is billed either way", body.Thinking.Type)
	}
	if body.MaxTokens != 1200 {
		t.Errorf("max_tokens %d, want 1200: it is what bounds the worst case",
			body.MaxTokens)
	}
}
