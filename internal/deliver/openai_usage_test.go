package deliver

import (
	"math"
	"testing"
)

// The rule itself, on the numbers a JSON decoder can hand it, extremes
// included: it never subtracts from completion_tokens and never wraps.
func TestOpenAIUsageOutputTokens(t *testing.T) {
	for _, c := range []struct {
		name                    string
		prompt, completion, tot int
		want                    int
	}{
		{"vertex", 14, 59, 633, 619},
		{"openai with reasoning inside", 14, 619, 633, 619},
		{"no total", 14, 59, 0, 59},
		{"total equal to the parts", 14, 59, 73, 59},
		{"total one more than the parts", 14, 59, 74, 60},
		{"total smaller than the parts", 14, 59, 20, 59},
		{"total smaller than the prompt", 14, 59, 5, 59},
		{"negative total", 14, 59, -1, 59},
		{"negative prompt is not a reason to grow the output", -1000, 59, 100, 59},
		{"negative completion is passed through as before", 14, -5, 0, -5},
		{"the largest total does not wrap", 14, 59, math.MaxInt, math.MaxInt - 14},
		{"the largest prompt does not wrap", math.MaxInt, 59, math.MaxInt, 59},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := OpenAIUsage{PromptTokens: c.prompt, CompletionTokens: c.completion, TotalTokens: c.tot}
			if got := u.OutputTokens(); got != c.want {
				t.Errorf("OutputTokens() = %d, want %d", got, c.want)
			}
		})
	}
}
