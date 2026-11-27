package deliver

// callBedrock against a local stand-in for the Converse endpoint, so what the
// engine does with each kind of answer is proven without an AWS account and
// without spending. The endpoint is redirected with the SDK's own
// AWS_ENDPOINT_URL_BEDROCK_RUNTIME, and the credential chain is given fixed
// test values, so nothing here can reach a real account.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeConverse answers every Converse call with status and body, and records
// the request body it was sent.
func fakeConverse(t *testing.T, status int, body string, errType string) (got *[]byte) {
	t.Helper()
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = b
		w.Header().Set("Content-Type", "application/json")
		if errType != "" {
			w.Header().Set("X-Amzn-ErrorType", errType)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "no-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "no-credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	return &seen
}

func TestCallBedrockRefusesWithNoRegion(t *testing.T) {
	fakeConverse(t, 200, `{}`, "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", " ")
	_, err := callBedrock(context.Background(), "eu.amazon.nova-lite-v1:0", "hello", 64)
	if err == nil || !strings.Contains(err.Error(), "AWS_REGION is not set") {
		t.Fatalf("no region: %v, want the AWS_REGION refusal", err)
	}
}

func TestCallBedrockReadsTextAndUsage(t *testing.T) {
	seen := fakeConverse(t, 200, `{
		"output": {"message": {"role": "assistant", "content": [{"text": "the cause "}, {"text": "is a batch job"}]}},
		"usage": {"inputTokens": 120, "outputTokens": 34, "totalTokens": 154},
		"stopReason": "end_turn", "metrics": {"latencyMs": 5}}`, "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "eu-central-1") // the fallback is honoured

	res, err := callBedrock(context.Background(), "eu.amazon.nova-lite-v1:0", "what happened", 77)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the cause is a batch job" || res.InTokens != 120 || res.OutTokens != 34 {
		t.Errorf("result %+v, want both text blocks joined and the counters read", res)
	}
	var req struct {
		InferenceConfig struct {
			MaxTokens int `json:"maxTokens"`
		} `json:"inferenceConfig"`
	}
	if err := json.Unmarshal(*seen, &req); err != nil {
		t.Fatalf("request body %q: %v", *seen, err)
	}
	if req.InferenceConfig.MaxTokens != 77 {
		t.Errorf("the request carried maxTokens %d, want the 77 the run reserved for", req.InferenceConfig.MaxTokens)
	}
}

func TestCallBedrockRefusesWhatItCannotRecord(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"an empty message",
			`{"output": {"message": {"role": "assistant", "content": [{"text": "  "}]}},
			  "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}}`,
			"with an empty message"},
		{"no usage counters",
			`{"output": {"message": {"role": "assistant", "content": [{"text": "an answer"}]}}}`,
			"without usage counters"},
		{"no message at all",
			`{"usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}}`,
			"with no message"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeConverse(t, 200, c.body, "")
			t.Setenv("AWS_REGION", "eu-central-1")
			res, err := callBedrock(context.Background(), "eu.amazon.nova-lite-v1:0", "x", 16)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s: %+v, %v; want an error containing %q", c.name, res, err, c.want)
			}
		})
	}
}

func TestCallBedrockNamesTheModelARefusalWasFor(t *testing.T) {
	fakeConverse(t, 400, `{"message": "The provided model identifier is invalid."}`, "ValidationException")
	t.Setenv("AWS_REGION", "eu-central-1")
	_, err := callBedrock(context.Background(), "eu.no-such-model", "x", 16)
	if err == nil || !strings.Contains(err.Error(), "bedrock refused eu.no-such-model") {
		t.Fatalf("a refused call: %v, want it to name the model", err)
	}
}
