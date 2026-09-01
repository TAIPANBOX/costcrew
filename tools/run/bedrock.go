package main

// Amazon Bedrock, through Converse.
//
// WHY CONVERSE AND NOT InvokeModel
//
// On InvokeModel every vendor on Bedrock has its own body. Anthropic wants
// anthropic_version and a messages array; Nova wants messages plus an
// inferenceConfig; Llama wants a prompt string. Writing that engine means
// writing one body builder and one response parser per vendor, and adding a
// vendor later means writing another pair.
//
// Converse is Bedrock's own answer to that: one request shape, one response
// shape, and the usage counters in the same place for everything. Measured
// 2026-09-01 on a real account in eu-central-1, the identical call answered
// from nova-lite, nova-micro, nova-pro and pixtral-large, each returning
// usage.inputTokens and usage.outputTokens where this reads them.
//
// So this file is the whole engine, and Claude joins it the day an account's
// Anthropic use-case form is accepted rather than the day somebody writes a
// second body builder.
//
// WHY THERE IS NO KEY IN HERE
//
// Every other engine in this runner reads a credential out of one environment
// variable. This one reads none. The request is signed with whatever the AWS
// credential chain resolves for the workload: a profile on a laptop, an
// instance role on EC2, IRSA in EKS. That is the reason it exists: an agent
// already running in somebody's AWS account can be given a model bill without
// a key being created, pasted, or rotated anywhere.
//
// The cost of that is a real one and it is named rather than hidden: this
// process can spend money with no secret in its environment, so what bounds it
// is the run's ceiling and the task's guard, which is what bounds every other
// engine here too.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// bedrockRequest builds the call, separately from making it.
//
// Separate so the shape can be tested without credentials and without
// spending: what the request carries is a property worth holding, and the
// output cap especially. The run reserves max-tokens at the output price
// before the call, and a request that does not actually carry that cap is a
// request the reservation does not describe.
func bedrockRequest(model, prompt string, maxTok int) *bedrockruntime.ConverseInput {
	cap32 := int32(maxTok)
	return &bedrockruntime.ConverseInput{
		ModelId: aws.String(model),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: prompt}},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(cap32)},
	}
}

func callBedrock(ctx context.Context, model, prompt string, maxTok int) (callResult, error) {
	// The region is not defaulted. Bedrock prices differ by region and the
	// price table in internal/engines is keyed to eu-central-1, so a call that
	// silently landed somewhere else would be bounded by the wrong number.
	// Refusing here is the same discipline as refusing an unpriced model.
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = strings.TrimSpace(os.Getenv("AWS_DEFAULT_REGION"))
	}
	if region == "" {
		return callResult{}, fmt.Errorf(
			"AWS_REGION is not set in this process: Bedrock prices differ by region " +
				"and the bound this run reserved is for the region the price table names")
	}

	cfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if err != nil {
		return callResult{}, fmt.Errorf("the AWS credential chain did not resolve: %w", err)
	}
	out, err := bedrockruntime.NewFromConfig(cfg).Converse(ctx, bedrockRequest(model, prompt, maxTok))
	if err != nil {
		return callResult{}, fmt.Errorf("bedrock refused %s: %w", model, err)
	}

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return callResult{}, fmt.Errorf("bedrock answered with no message for %s", model)
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		if t, ok := block.(*types.ContentBlockMemberText); ok {
			b.WriteString(t.Value)
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return callResult{}, fmt.Errorf("bedrock answered %s with an empty message", model)
	}
	// Usage is what the ledger records. Missing counters are an error rather
	// than a zero: a call recorded as costing nothing is how a bill grows out
	// of a column of zeroes, which this console exists to catch elsewhere.
	if out.Usage == nil || out.Usage.InputTokens == nil || out.Usage.OutputTokens == nil {
		return callResult{}, fmt.Errorf(
			"bedrock answered %s without usage counters, so what the call cost "+
				"cannot be recorded and recording it as free would be a lie", model)
	}
	return callResult{
		Text:      b.String(),
		InTokens:  int(*out.Usage.InputTokens),
		OutTokens: int(*out.Usage.OutputTokens),
	}, nil
}
