package main

import (
	"context"
	"strings"
	"testing"
)

// An analyst hired onto Bedrock must have a caller.
//
// Red first: call() answered `no caller is written for engine "bedrock"`, so
// an analyst hired onto it was blocked at the moment of the call rather than
// at the moment of hiring. The estimator priced the task, the ceiling reserved
// its worst case, and only then did the run find out there was no way to make
// it.
//
// This asserts the ROUTE, not the answer. A test that reached Bedrock would
// need credentials and would spend money to prove a switch statement.
func TestBedrockHasACaller(t *testing.T) {
	_, err := call(context.Background(), "bedrock", "eu.amazon.nova-micro-v1:0", "hello", 16, gatewayHeaders{})
	if err != nil && strings.Contains(err.Error(), "no caller is written for engine") {
		t.Fatalf("an analyst hired onto bedrock cannot be run at all: %v", err)
	}
}

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
