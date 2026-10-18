package deliver

import (
	"reflect"
	"testing"
)

// The gateway's on-behalf-of chain names the analyst under the costcrew path,
// the one place a gateway key's prefix binding is matched against. A chain
// that names the analyst without it would be refused by a key bound to
// agent://<host>/costcrew/*, and the spend would reach the gateway unattributed.
//
// @decided 2026-10-18 (paraphrased): every agent this console mints sits under
// its own path, so one gateway key covers the whole crew and nothing else.
func TestTheOnBehalfOfChainNamesTheAnalystUnderTheCostcrewPath(t *testing.T) {
	got, err := OnBehalfOfChain("taipanbox.dev", "olena", "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user://taipanbox.dev/olena", "agent://taipanbox.dev/costcrew/triage-aws"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chain = %q, want %q", got, want)
	}
}
