package deliver

// Moved from tools/run/bedrock_test.go (found by CI's staticcheck U1000):
// bedrockRequest and callBedrock moved to bedrock.go in this package with
// Call() (B6B-SPEC.md), and tools/run's own duplicate copy of both, left
// behind rather than removed or wrapped, was flagged unused (callBedrock)
// or kept alive only by this very test calling bedrockRequest directly
// (which is why go build/vet/test never caught the leftover; only
// staticcheck's whole-module unused-code analysis did). Content unchanged.

import "testing"

// The request Bedrock is sent is the same shape for every vendor on it.
//
// Converse and not InvokeModel, and that is the whole design decision. On
// InvokeModel every vendor has its own body: Anthropic wants
// anthropic_version and messages, Nova wants messages plus inferenceConfig,
// Llama wants a prompt string. Measured 2026-09-01 against nova-lite,
// nova-micro, nova-pro and pixtral-large: through Converse one request shape
// and one response shape answered for all four, usage counters included.
//
// So adding a vendor to this engine costs nothing, and Claude joins it the day
// an account's use-case form is accepted rather than the day somebody writes a
// second body builder.
func TestBedrockAsksWithTheSharedConverseShape(t *testing.T) {
	in := bedrockRequest("eu.amazon.nova-micro-v1:0", "write the thing", 400)
	if in.ModelId == nil || *in.ModelId != "eu.amazon.nova-micro-v1:0" {
		t.Error("the model id is not the one it was asked for")
	}
	if len(in.Messages) != 1 {
		t.Fatalf("sent %d messages, want exactly the one prompt", len(in.Messages))
	}
	if in.InferenceConfig == nil || in.InferenceConfig.MaxTokens == nil {
		t.Fatal("no output cap was sent: the worst case this run reserved is " +
			"max-tokens at the output price, and a request that does not carry " +
			"it is a request the bound does not describe")
	}
	if got := *in.InferenceConfig.MaxTokens; got != 400 {
		t.Errorf("cap sent as %d, reserved as 400: the ceiling bounds a number "+
			"the request has to actually carry", got)
	}
}
