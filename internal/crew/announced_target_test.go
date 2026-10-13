package crew

// ANNOUNCED-DRIVER-SPEC.md section 2: driver.announced's own target, decoded
// strictly and refused at save time for every malformed shape the spec names.
// A class the owner alone may close is also held here: no role may decide it
// alone, and exactly the four analyst families that may propose it do.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestAnnouncedTargetAcceptsAFutureDatedSaving(t *testing.T) {
	raw := json.RawMessage(`{"scope":"svc-a","start":"2026-11-01","end":"2027-04-30","daily_cents":-30000}`)
	if r := validateAnnouncedTarget(raw); r != "" {
		t.Fatalf("a well-formed target was refused: %s", r)
	}
}

func TestAnnouncedTargetRefusesEveryMalformedShape(t *testing.T) {
	cases := map[string]string{
		"absent":        ``,
		"no scope":      `{"start":"2026-11-01","end":"2026-11-30","daily_cents":-1}`,
		"wide scope":    `{"scope":"*","start":"2026-11-01","end":"2026-11-30","daily_cents":-1}`,
		"end first":     `{"scope":"s","start":"2026-11-30","end":"2026-11-01","daily_cents":-1}`,
		"over 366 days": `{"scope":"s","start":"2026-01-01","end":"2027-01-03","daily_cents":-1}`,
		"zero":          `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":0}`,
		"not integer":   `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":1.5}`,
		"string amount": `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":"-1"}`,
		"past int64":    `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":99999999999999999999}`,
		"unknown key":   `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":-1,"x":1}`,
		"bad date":      `{"scope":"s","start":"2026-13-01","end":"2026-11-30","daily_cents":-1}`,
		"trailing data": `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":-1} x`,
	}
	for name, body := range cases {
		if r := validateAnnouncedTarget(json.RawMessage(body)); r == "" {
			t.Errorf("%s: accepted %s", name, body)
		} else if !strings.Contains(r, "driver.announced") {
			t.Errorf("%s: the reason does not name the class: %s", name, r)
		}
	}
}

func TestNoRoleDecidesAnAnnouncedDriverAlone(t *testing.T) {
	proposers := 0
	for _, r := range AllRoles() {
		for _, c := range r.DecidesAlone {
			if c == "driver.announced" {
				t.Fatalf("%s decides driver.announced alone; only the owner may close it", r.Family)
			}
		}
		for _, c := range r.HandsUp {
			if c == "driver.announced" {
				proposers++
			}
		}
	}
	if proposers != 4 {
		t.Fatalf("driver.announced is handed up by %d roles, want 4 (optimizer, optimizer-onprem, ai-spend-analyst, forecaster)", proposers)
	}
}

// wellFormedAnnounced is the spec's own rule, restated independently of
// validateAnnouncedTarget so the sweep below can hold the one against the
// other rather than against itself.
func wellFormedAnnounced(raw json.RawMessage) bool {
	var tgt struct {
		Scope      string  `json:"scope"`
		Start      string  `json:"start"`
		End        string  `json:"end"`
		DailyCents *int64  `json:"daily_cents"`
		Team       *string `json:"team"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tgt); err != nil {
		return false
	}
	if strings.TrimSpace(tgt.Scope) == "" || tgt.Scope == "*" {
		return false
	}
	if tgt.DailyCents == nil || *tgt.DailyCents == 0 {
		return false
	}
	if tgt.Team != nil && (strings.TrimSpace(*tgt.Team) == "" || strings.TrimSpace(*tgt.Team) != *tgt.Team) {
		return false
	}
	s, err := time.Parse("2006-01-02", tgt.Start)
	if err != nil {
		return false
	}
	e, err := time.Parse("2006-01-02", tgt.End)
	if err != nil {
		return false
	}
	return !e.Before(s) && e.Sub(s) <= 366*24*time.Hour
}

// TestAnnouncedTargetSurvivesHostileBytesAcrossSeeds is the repo's own sweep
// convention for hostile targets: 200 seeds, each a random object drawn from
// the four keys with values weighted toward valid ones, plus a noise key now
// and then. None may panic; every refusal must name the class; every
// acceptance must be well-formed by the independent rule above.
func TestAnnouncedTargetSurvivesHostileBytesAcrossSeeds(t *testing.T) {
	// Each key draws a valid value 85% of the time, so that a fixed 200 seeds
	// reach accepted targets, and a hostile one the rest: the sweep is about the
	// hostile values, and it must also reach the accepting path.
	valid := map[string][]string{
		"scope":       {`"svc-a"`},
		"start":       {`"2026-11-01"`},
		"end":         {`"2026-11-30"`},
		"daily_cents": {`-30000`, `30000`},
		"team":        {`"data-eng"`, `null`},
	}
	hostile := map[string][]string{
		"scope":       {`"*"`, `""`, `"   "`, `1`, `null`},
		"start":       {`"2026-10-01"`, `"2026-13-01"`, `"2026-02-30"`, `"nope"`, `1`, `null`},
		"end":         {`"2026-10-01"`, `"2027-12-31"`, `""`, `"2026-11-1"`, `1`},
		"daily_cents": {`0`, `1.5`, `"-1"`, `99999999999999999999`, `-9223372036854775808`, `null`, `1e3`, `true`},
		"team":        {`""`, `" data-eng "`, `7`, `"a\nb"`},
	}
	keys := []string{"scope", "start", "end", "daily_cents", "team"}
	accepted := 0
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var b strings.Builder
		b.WriteString("{")
		n := 0
		for _, k := range keys {
			if rng.Intn(6) == 0 {
				continue
			}
			pool := valid[k]
			if rng.Intn(100) >= 85 {
				pool = hostile[k]
			}
			if n > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `%q:%s`, k, pool[rng.Intn(len(pool))])
			n++
		}
		if rng.Intn(4) == 0 {
			if n > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"noise%d":%d`, rng.Intn(3), rng.Intn(10))
		}
		b.WriteString("}")
		raw := json.RawMessage(b.String())

		reason := validateAnnouncedTarget(raw)
		if reason != "" {
			if !strings.Contains(reason, "driver.announced") {
				t.Errorf("seed %d: refused %s without naming the class: %s", seed, raw, reason)
			}
			continue
		}
		accepted++
		if !wellFormedAnnounced(raw) {
			t.Errorf("seed %d: accepted %s, which is not well-formed by the spec's rule", seed, raw)
		}
	}
	if accepted == 0 {
		t.Fatal("no seed produced a valid target: the sweep measured nothing")
	}
}

// ANNOUNCED-DRIVER-SPEC.md section 11: the target may name the one team a
// decision moves. Absent or null means the step is split across the desk's
// teams; a named team must be a real plain name.
func TestAnnouncedTargetMayNameATeam(t *testing.T) {
	raw := json.RawMessage(`{"scope":"Amazon EC2","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000,"team":"data-eng"}`)
	if r := validateAnnouncedTarget(raw); r != "" {
		t.Fatalf("a target naming a team was refused: %s", r)
	}
	unnamed := json.RawMessage(`{"scope":"Amazon EC2","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000,"team":null}`)
	if r := validateAnnouncedTarget(unnamed); r != "" {
		t.Fatalf("a null team was refused, but null means the split: %s", r)
	}
}

func TestAnnouncedTargetRefusesAnEmptyOrTypedTeam(t *testing.T) {
	for name, body := range map[string]string{
		"empty team":   `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":-1,"team":""}`,
		"spaced team":  `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":-1,"team":"   "}`,
		"number team":  `{"scope":"s","start":"2026-11-01","end":"2026-11-30","daily_cents":-1,"team":7}`,
		"control team": "{\"scope\":\"s\",\"start\":\"2026-11-01\",\"end\":\"2026-11-30\",\"daily_cents\":-1,\"team\":\"a\\nb\"}",
	} {
		r := validateAnnouncedTarget(json.RawMessage(body))
		if r == "" {
			t.Errorf("%s: accepted %s", name, body)
		} else if !strings.Contains(r, "driver.announced") {
			t.Errorf("%s: the reason does not name the class: %s", name, r)
		}
	}
}
