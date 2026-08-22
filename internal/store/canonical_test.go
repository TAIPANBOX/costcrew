package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// Vectors produced by CPython 3.14.7 on 2026-08-22 with the original's own
// call, json.dumps(rec, sort_keys=True, ensure_ascii=False), and its hash,
// sha256(body)[:16].
//
// This is a cross-language byte contract: the audit page renders these hashes
// and a chain the two implementations disagree about is a chain nobody can
// verify. It is pinned rather than reasoned about, because every difference
// here is invisible until it is a broken chain.
var vectors = []struct {
	name string
	rec  map[string]any
	body string
	hash string
}{
	{
		"empty",
		map[string]any{"ts": 1780300800.0, "event": "task_created", "data": map[string]any{}, "prev": "genesis"},
		`{"data": {}, "event": "task_created", "prev": "genesis", "ts": 1780300800.0}`,
		"6f7d65c3c648fd57",
	},
	{
		"typical",
		map[string]any{"ts": 1780301400.5, "event": "task_started",
			"data": map[string]any{"assignee": "supervisor", "task": 1},
			"prev": "46e22ccd9e77d49f"},
		`{"data": {"assignee": "supervisor", "task": 1}, "event": "task_started", "prev": "46e22ccd9e77d49f", "ts": 1780301400.5}`,
		"6c5348157acc6599",
	},
	{
		// Two traps in one line. Python leaves non-ASCII alone under
		// ensure_ascii=False, and it does NOT escape '>' the way Go's
		// encoding/json does by default.
		"unicode and angle bracket",
		map[string]any{"ts": 1780301400.0, "event": "note",
			"data": map[string]any{"text": `Аномалія "ml" > 5`, "ok": true, "none": nil},
			"prev": "x"},
		`{"data": {"none": null, "ok": true, "text": "Аномалія \"ml\" > 5"}, "event": "note", "prev": "x", "ts": 1780301400.0}`,
		"7fa15a5a04c4f332",
	},
	{
		// 1780301400.123 stays positional in Python. Go's %g would render it
		// as 1.780301400123e+09, and an integral 3.0 must keep its ".0",
		// which Go's shortest form drops.
		"floats",
		map[string]any{"ts": 1780301400.123, "event": "run_cost",
			"data": map[string]any{"usd": 2.81, "whole": 3.0, "neg": -0.5, "big": 1234567.0},
			"prev": "y"},
		`{"data": {"big": 1234567.0, "neg": -0.5, "usd": 2.81, "whole": 3.0}, "event": "run_cost", "prev": "y", "ts": 1780301400.123}`,
		"1ccda28dbfc06cee",
	},
	{
		"nested",
		map[string]any{"ts": 1.0, "event": "e",
			"data": map[string]any{
				"list": []any{1, "a", 2.5, true, nil},
				"map":  map[string]any{"b": 1, "a": 2}},
			"prev": "z"},
		`{"data": {"list": [1, "a", 2.5, true, null], "map": {"a": 2, "b": 1}}, "event": "e", "prev": "z", "ts": 1.0}`,
		"2b7f565269849cd5",
	},
}

func TestCanonicalMatchesCPython(t *testing.T) {
	for _, v := range vectors {
		got, err := canonical(v.rec)
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		if string(got) != v.body {
			t.Errorf("%s bytes:\n got %s\nwant %s", v.name, got, v.body)
			continue
		}
		sum := sha256.Sum256(got)
		if h := hex.EncodeToString(sum[:])[:16]; h != v.hash {
			t.Errorf("%s hash: got %s, want %s", v.name, h, v.hash)
		}
	}
}

// Without this, a canonicaliser that emitted a constant string would pass
// every vector above by accident of the vectors all being different.
func TestDifferentRecordsDifferentHashes(t *testing.T) {
	seen := map[string]string{}
	for _, v := range vectors {
		got, err := canonical(v.rec)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(got)
		h := hex.EncodeToString(sum[:])[:16]
		if prev, dup := seen[h]; dup {
			t.Fatalf("%s and %s hash to the same value %s", prev, v.name, h)
		}
		seen[h] = v.name
	}
}

// A value the canonicaliser has no rule for must fail loudly. Emitting
// something plausible instead would put a hash on a record whose bytes the
// other implementation would never produce.
func TestUnknownTypeIsRefused(t *testing.T) {
	_, err := canonical(map[string]any{"x": struct{ A int }{1}})
	if err == nil {
		t.Fatal("a struct was accepted; it should have been refused")
	}
}
