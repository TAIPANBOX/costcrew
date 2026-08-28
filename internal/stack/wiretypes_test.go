package stack_test

// The declaration in types.go is only worth having if something holds it to the
// code. This is that something: it reads every call site in the repository,
// applies the same translation the emitter applies, and requires the result to
// be exactly WireTypes.
//
// It exists because estate-gates C4 reads that list to check agent-passport's
// 6.2 registry, and a hand-written list nothing verifies is how the registry
// came to reserve seven names for idryx that were wrong in both directions.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// Kinds handed to an emitter as a literal first argument, in either shape this
// repository uses: `rec.Emit("kind", ...)` and the anomaly package's
// `emit(rec, "kind", ...)`.
var kindCall = regexp.MustCompile(`(?:\bEmit\(|emit\(rec,\s*)"([a-z][a-z0-9_]*)"`)

// The one kind built rather than written, `"anomaly_" + string(to)`. Its
// reachable values are the four transitions the package exports; `Open` is
// written only by the insert and never transitioned to.
var dynamicKind = regexp.MustCompile(`emit\(rec,\s*"([a-z_]+)"\s*\+\s*string\(`)

func TestWireTypesIsExactlyWhatTheCallSitesProduce(t *testing.T) {
	root := repoRoot(t)
	found := map[string]bool{}
	dynamicPrefixes := map[string]bool{}

	err := filepath.Walk(filepath.Join(root, "internal"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		src := string(b)
		for _, m := range kindCall.FindAllStringSubmatch(src, -1) {
			found[m[1]] = true
		}
		for _, m := range dynamicKind.FindAllStringSubmatch(src, -1) {
			dynamicPrefixes[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("no emit call site was read, so this test measured nothing")
	}

	// A dynamic kind this test does not know how to resolve is a FAILURE, not a
	// silent omission: the whole point of the list is that nothing reaches the
	// bus unlisted.
	for prefix := range dynamicPrefixes {
		// The literal reader saw the prefix as a whole kind, because
		// `emit(rec, "anomaly_" + ...)` starts with a string literal. It is a
		// fragment and not a type, so it is removed rather than resolved.
		delete(found, prefix)
		if prefix != "anomaly_" {
			t.Fatalf("a kind is built from the prefix %q and this test cannot "+
				"resolve its values; either resolve them here or stop building "+
				"the kind, because an unresolved one reaches the bus unlisted",
				prefix)
		}
		for _, state := range []string{"triaged", "explained", "accepted", "dismissed"} {
			found[prefix+state] = true
		}
	}

	// The emitter renames on the way out, so compare what goes on the WIRE.
	wire := map[string]bool{}
	for kind := range found {
		wire[stack.WireTypeOf(kind)] = true
	}

	got := make([]string, 0, len(wire))
	for k := range wire {
		got = append(got, k)
	}
	sort.Strings(got)

	want := stack.WireTypes()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the call sites produce\n  %v\nand WireTypes declares\n  %v\n"+
			"estate-gates C4 reads that declaration to check agent-passport's "+
			"6.2 registry, so a difference here is a registry that claims the "+
			"wrong set", got, want)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.mod above the test's working directory")
	return ""
}
