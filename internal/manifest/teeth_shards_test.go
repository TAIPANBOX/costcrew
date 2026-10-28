package manifest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The teeth shards partition the teeth cases, checked from the test suite as
// well as from scripts/teeth-shards-cover-every-case.sh.
//
// The script is the gate a person runs. This is the same check reachable from
// `go test ./...`, so a split that drops or repeats a case is caught by the
// ordinary suite, and so that gates-have-teeth.sh has a Go test to plant a
// broken filter against.
//
// With no shard count the script reads the one CI runs from the workflow, so
// this is also the test that fails when the matrix and the totals disagree, when
// the plan is for another count, and when a plan row names no case.
func TestTeethShardsCoverEveryCase(t *testing.T) {
	out, err := exec.Command("../../scripts/teeth-shards-cover-every-case.sh").CombinedOutput()
	text := string(out)
	if err != nil {
		t.Errorf("the teeth shards do not cover every case:\n%s", strings.TrimSpace(text))
		return
	}
	if !strings.Contains(text, "every case in exactly one shard") {
		t.Errorf("the gate reported success without saying the shards partition the cases:\n%s", text)
	}
	t.Log("  " + strings.TrimSpace(text))
}

// The same check for a shard count the plan is not for. The plan is read only for
// the count it was made for, so five shards fall back to counting cases, and that
// rule has to be a partition too: it is what a person gets on a laptop with
// TEETH_SHARD=1/3, and what every shard gets if the plan is ever deleted.
func TestTeethShardsCoverEveryCaseWithoutThePlan(t *testing.T) {
	out, err := exec.Command("../../scripts/teeth-shards-cover-every-case.sh", "5").CombinedOutput()
	text := string(out)
	if err != nil {
		t.Errorf("five teeth shards do not cover every case:\n%s", strings.TrimSpace(text))
		return
	}
	if !strings.Contains(text, "5 shards of the") || !strings.Contains(text, "every case in exactly one shard") {
		t.Errorf("the gate did not say it checked five shards and found a partition:\n%s", text)
	}
	if strings.Contains(text, "placed by the plan") {
		t.Errorf("a plan made for another count was read for five shards:\n%s", text)
	}
}

// scripts/teeth-plan.sh makes a plan in which every case has one shard, the cases
// that plant a fault against the same test stay together (the harness runs that
// test once on the clean tree per shard they are in), and no shard is more than a
// tenth over the mean by the seconds the plan itself records.
func TestTheTeethPlanScriptBalancesTheShardsAndKeepsAGateTogether(t *testing.T) {
	const shards = 7
	planPath := filepath.Join(t.TempDir(), "plan.tsv")
	cmd := exec.Command("../../scripts/teeth-plan.sh", strconv.Itoa(shards))
	cmd.Env = append(os.Environ(), "TEETH_PLAN_FILE="+planPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/teeth-plan.sh %d failed: %v\n%s", shards, err, out)
	}

	list := func(mode string) []string {
		c := exec.Command("../../scripts/gates-have-teeth.sh")
		c.Env = append(os.Environ(), "TEETH_LIST="+mode, "TEETH_SHARD=")
		out, err := c.Output()
		if err != nil {
			t.Fatalf("listing the cases (TEETH_LIST=%s): %v", mode, err)
		}
		return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	}
	names := list("1")
	gates := list("gates")
	if len(names) == 0 || len(names) != len(gates) {
		t.Fatalf("%d case names and %d case rows with a package and a pattern", len(names), len(gates))
	}

	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\n# shards: 7\n") {
		t.Errorf("the plan does not say it is for 7 shards:\n%.300s", raw)
	}
	shardOf := map[string]int{}
	load := make([]float64, shards+1)
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			t.Fatalf("a plan row that is not shard, seconds, name: %q", line)
		}
		shard, err := strconv.Atoi(f[0])
		if err != nil || shard < 1 || shard > shards {
			t.Fatalf("a plan row with shard %q, want 1 to %d: %q", f[0], shards, line)
		}
		secs, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			t.Fatalf("a plan row with seconds %q: %q", f[1], line)
		}
		if _, dup := shardOf[f[2]]; dup {
			t.Errorf("the plan names %q twice", f[2])
		}
		shardOf[f[2]] = shard
		load[shard] += secs
	}

	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var planned []string
	for n := range shardOf {
		planned = append(planned, n)
	}
	sort.Strings(planned)
	if strings.Join(sorted, "\n") != strings.Join(planned, "\n") {
		t.Fatalf("the plan names %d cases and the harness has %d, and they are not the same set", len(planned), len(names))
	}

	// A gate is a package and a -run pattern, and its cases share one shard.
	gateShard := map[string]int{}
	for _, row := range gates {
		f := strings.Split(row, "\t")
		key := f[1] + "\t" + f[2]
		s := shardOf[f[0]]
		if prev, seen := gateShard[key]; seen && prev != s {
			t.Errorf("cases that plant a fault against %s %s are in shards %d and %d, so each pays for the clean run of it", f[1], f[2], prev, s)
		}
		gateShard[key] = s
	}

	var total float64
	for s := 1; s <= shards; s++ {
		if load[s] == 0 {
			t.Errorf("shard %d of %d holds no seconds of work", s, shards)
		}
		total += load[s]
	}
	mean := total / shards
	for s := 1; s <= shards; s++ {
		if load[s] > mean*1.10 {
			t.Errorf("shard %d is %.0fs against a mean of %.0fs, more than a tenth over", s, load[s], mean)
		}
	}
}
