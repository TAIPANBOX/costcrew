package anomaly_test

// ANNOUNCED-DRIVER-SPEC.md section 6: the detection pass never deletes a raised
// anomaly. An announcement made after the fact stops new findings only, and
// the decisions page shows that the answer came after the start. This guard
// reads the module's own non-test Go source and fails when any line of code,
// outside a comment, deletes from an anomalies table.
//
// The red check plants a delete in a temporary directory, never in the tree.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var anomalyDelete = regexp.MustCompile(`(?i)delete\s+from\s+anomal`)

// deletesAnomalies returns file:line for every non-test Go file under root
// whose code, not a comment line, deletes from an anomalies table.
func deletesAnomalies(t *testing.T, root string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if anomalyDelete.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d", path, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestNothingDeletesARaisedAnomaly(t *testing.T) {
	if hits := deletesAnomalies(t, filepath.Join("..", "..")); len(hits) != 0 {
		t.Fatalf("a raised anomaly is deleted in code, which the announced-decision rule forbids: %v", hits)
	}
}

// TestTheDeleteGuardCatchesAPlantedDelete is the guard's own red check: a
// delete in code is found, and the same words in a comment are not.
func TestTheDeleteGuardCatchesAPlantedDelete(t *testing.T) {
	dir := t.TempDir()
	planted := "package x\n\nfunc prune(db *sql.DB) error {\n\t_, err := db.Exec(`DELETE FROM anomalies WHERE state = 'open'`)\n\treturn err\n}\n"
	comment := "package x\n\n// DELETE FROM anomalies is what this file must never do.\n"
	if err := os.WriteFile(filepath.Join(dir, "prune.go"), []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comment.go"), []byte(comment), 0o600); err != nil {
		t.Fatal(err)
	}
	hits := deletesAnomalies(t, dir)
	if len(hits) != 1 || !strings.HasSuffix(hits[0], "prune.go:4") {
		t.Fatalf("the guard reported %v, want exactly the planted delete at prune.go:4", hits)
	}
}
