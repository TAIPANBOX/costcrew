package typryx

// Invariant 70 at the typryx door: what leaves for typryx, which may hand it
// to a hosted model, is governed by -prompt-data exactly as a packet is.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/promptfixture"
)

func TestWhatTypryxIsSentFollowsThePromptDataSetting(t *testing.T) {
	st, err := promptfixture.Build(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	check, err := promptfixture.NewChecker(db)
	if err != nil {
		t.Fatal(err)
	}
	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil || len(list) == 0 {
		t.Fatalf("the fixture has no anomaly (%v): this test measures nothing", err)
	}
	sent := func(mode deliver.PromptData) (string, []map[string]string) {
		p, err := deliver.NewPolicy(mode, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p.Bind(db)
		defer deliver.SetActivePolicy(p)()
		var b strings.Builder
		var states []map[string]string
		for _, a := range list {
			s := State(db, a)
			states = append(states, s)
			for _, k := range Offered {
				b.WriteString(s[k])
				b.WriteString("\n")
			}
		}
		return b.String(), states
	}

	full, _ := sent(deliver.PromptFull)
	if len(check.Leaks(full, "")) == 0 {
		t.Fatal("the checker finds nothing in full mode, so it would find nothing anywhere")
	}
	for _, mode := range []deliver.PromptData{deliver.PromptMasked, deliver.PromptAggregates} {
		text, states := sent(mode)
		if leaks := check.Leaks(text, ""); len(leaks) > 0 {
			t.Errorf("%s: what typryx would be sent carries %v", mode, leaks)
		}
		for _, s := range states {
			if mode == deliver.PromptAggregates {
				if _, ok := s["service"]; ok {
					t.Errorf("aggregates offers the service to typryx")
				}
				if strings.Contains(s["recent_changes"], " to ") {
					t.Errorf("aggregates sends registered changes: %q", s["recent_changes"])
				}
			}
		}
		if mode == deliver.PromptMasked && !strings.Contains(text, deliver.WithheldLabel) {
			t.Errorf("masked sent no withheld driver label: the fixture's drivers never reached the state")
		}
	}
}
