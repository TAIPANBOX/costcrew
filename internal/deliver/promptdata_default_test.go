package deliver

import "testing"

// Invariant 110: nothing configured means masked. A flag that is left off must
// send less, not everything; full is still one explicit word away.
func TestThePromptDataDefaultIsMasked(t *testing.T) {
	t.Setenv(PromptDataEnv, "")
	if got := PromptDataEnvDefault(); got != "masked" {
		t.Errorf("with %s unset, the default is %q, want masked", PromptDataEnv, got)
	}
	if _, err := ParsePromptData(PromptDataEnvDefault()); err != nil {
		t.Errorf("the default does not parse: %v", err)
	}
}

func TestTheEnvironmentStillChoosesFull(t *testing.T) {
	t.Setenv(PromptDataEnv, "full")
	if got := PromptDataEnvDefault(); got != "full" {
		t.Errorf("with %s=full, the default is %q, want full", PromptDataEnv, got)
	}
}
