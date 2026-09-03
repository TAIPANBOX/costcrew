package main

import (
	"context"
	"strings"
	"testing"
)

// TestBedrockAsksWithTheSharedConverseShape moved to internal/deliver's own
// bedrock_test.go (found by CI's staticcheck U1000, after B6B-SPEC.md's
// move: bedrockRequest and callBedrock moved to internal/deliver/bedrock.go
// with call(), and this file's own duplicate copy of both was left behind,
// unreachable -- callBedrock was flagged unused; bedrockRequest was not,
// only because this file's own test still called it directly, which is
// exactly the coupling that made the leftover invisible to go build/vet/test
// and visible only to staticcheck, which cannot run locally on this Go
// version). This file keeps TestBedrockHasACaller, which tests call() -- the
// wrapper over internal/deliver.Call, unaffected by where the engine bodies
// physically live.

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
