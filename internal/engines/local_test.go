package engines

import (
	"math"
	"strings"
	"testing"
)

// The local engine is an engine the console knows, and it reads as METERED
// although no vendor bills anything.
//
// Red first: before this engine existed there was no "local" in the catalogue,
// so the estimator refused every task on it as "not an engine this console
// knows". The direction that would spend is the other one: Metered false reads
// as "already paid for, nothing new billed", and prices.go's own header records
// what that reading does to a bound.
func TestTheLocalEngineIsKnownAndReadsAsMetered(t *testing.T) {
	metered, known := Metered(LocalID)
	if !known {
		t.Fatal("local is not in the catalogue: the estimator refuses a task on an engine it does not know")
	}
	if !metered {
		t.Error("local reads as unmetered: the estimator would wave it through with no bound at " +
			"all, on an engine whose whole point is that the operator sets its price and its ceiling")
	}
	var found Engine
	for _, e := range Catalogue {
		if e.ID == LocalID {
			found = e
		}
	}
	if found.Family != SelfHosted {
		t.Errorf("family %q, want %q", found.Family, SelfHosted)
	}
	if len(found.Models) != 0 {
		t.Errorf("the local engine lists models %v: the operator names what their own server serves, "+
			"and a list here would be a claim about somebody else's machine", found.Models)
	}
	if found.EndpointEnv != "COSTCREW_MODEL_URL" {
		t.Errorf("EndpointEnv %q, want COSTCREW_MODEL_URL", found.EndpointEnv)
	}
	for _, say := range []string{found.When, found.Cost, found.How} {
		if strings.TrimSpace(say) == "" {
			t.Error("the local engine does not answer all three of when, cost and how: an option " +
				"that does not say what it costs is one somebody picks by accident")
		}
	}
}

// Unconfigured is UNKNOWN, never free. The console never configures this, so
// the console must show the engine as unpriced rather than as costing nothing.
func TestAnUnconfiguredLocalEngineHasNoPrice(t *testing.T) {
	ResetLocal()
	if p, ok := PriceFor(LocalID, "llama3.1:8b"); ok {
		t.Errorf("an unconfigured local engine is priced at %+v: unknown must not read as free", p)
	}
	if m := DefaultModel(LocalID); m != "" {
		t.Errorf("DefaultModel(local) = %q with nothing configured, want empty", m)
	}
}

// The operator's model, the operator's price, whatever the model is called.
func TestTheLocalEngineIsPricedByTheOperatorForAnyModelName(t *testing.T) {
	t.Cleanup(ResetLocal)
	if err := ConfigureLocal(LocalSetting{Model: "qwen2.5-coder:7b", InPerM: 0.12, OutPerM: 0.34}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"qwen2.5-coder:7b", "something-else", "deepseek/deepseek-chat"} {
		p, ok := PriceFor(LocalID, model)
		if !ok {
			t.Fatalf("no price for local/%s: any model the operator names must be priced", model)
		}
		if p.InPerM != 0.12 || p.OutPerM != 0.34 {
			t.Errorf("local/%s priced %+v, want in 0.12 out 0.34: a vendor row must never leak "+
				"into the operator's engine", model, p)
		}
		if !strings.Contains(p.Source, "operator") {
			t.Errorf("the price's source %q does not say the operator set it", p.Source)
		}
	}
	if m := DefaultModel(LocalID); m != "qwen2.5-coder:7b" {
		t.Errorf("DefaultModel(local) = %q, want the operator's model", m)
	}
	// And a price of exactly zero is a legal setting, distinguishable from none.
	if err := ConfigureLocal(LocalSetting{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	p, ok := PriceFor(LocalID, "m")
	if !ok || p.InPerM != 0 || p.OutPerM != 0 {
		t.Errorf("a configured price of 0 reads as %+v, ok=%v", p, ok)
	}
}

// A price that is not a price makes a reservation negative and every check
// against it pass.
func TestConfigureLocalRefusesAPriceThatIsNotAPrice(t *testing.T) {
	t.Cleanup(ResetLocal)
	for name, s := range map[string]LocalSetting{
		"negative in":    {Model: "m", InPerM: -1},
		"negative out":   {Model: "m", OutPerM: -0.0001},
		"NaN in":         {Model: "m", InPerM: math.NaN()},
		"NaN out":        {Model: "m", OutPerM: math.NaN()},
		"infinite in":    {Model: "m", InPerM: math.Inf(1)},
		"infinite out":   {Model: "m", OutPerM: math.Inf(1)},
		"minus infinity": {Model: "m", InPerM: math.Inf(-1)},
		"no model":       {InPerM: 1, OutPerM: 1},
		"blank model":    {Model: "   "},
	} {
		ResetLocal()
		if err := ConfigureLocal(s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
		if _, ok := Local(); ok {
			t.Errorf("%s: a refused setting was recorded anyway", name)
		}
	}
	if err := CheckLocalPrices(0, 0); err != nil {
		t.Errorf("zero and zero is a legal price: %v", err)
	}
	if err := CheckLocalPrices(1e12, 1e12); err != nil {
		t.Errorf("a large finite price is the operator's to choose: %v", err)
	}
}

// ResetLocal is what keeps a second run in one process from reading the first
// run's price.
func TestResetLocalForgetsThePreviousRunsSetting(t *testing.T) {
	t.Cleanup(ResetLocal)
	if err := ConfigureLocal(LocalSetting{Model: "m", InPerM: 9, OutPerM: 9}); err != nil {
		t.Fatal(err)
	}
	ResetLocal()
	if _, ok := PriceFor(LocalID, "m"); ok {
		t.Error("the price survived ResetLocal")
	}
}

// Check reads the endpoint's variable and calls nothing.
func TestCheckReadsTheLocalEndpointFromTheEnvironment(t *testing.T) {
	ready := func(env map[string]string) Availability {
		av := Check(func(k string) string { return env[k] }, func(string) (string, error) {
			return "", errNotOnPath
		})
		for _, a := range av {
			if a.ID == LocalID {
				return a
			}
		}
		t.Fatal("local is not in Check's answer")
		return Availability{}
	}
	if a := ready(map[string]string{"COSTCREW_MODEL_URL": "http://127.0.0.1:11434/v1"}); !a.Ready {
		t.Errorf("a set COSTCREW_MODEL_URL is not ready: %q", a.Reason)
	}
	if a := ready(map[string]string{"COSTCREW_MODEL_URL": "   "}); a.Ready {
		t.Error("a blank COSTCREW_MODEL_URL reads as ready")
	}
	if a := ready(nil); a.Ready || !strings.Contains(a.Reason, "COSTCREW_MODEL_URL") {
		t.Errorf("an unset endpoint: ready=%v reason=%q", a.Ready, a.Reason)
	}
}

type notOnPath struct{}

func (notOnPath) Error() string { return "not on path" }

var errNotOnPath error = notOnPath{}

// The price table the runner prints names the operator's price and who set it.
func TestThePriceTableNamesTheOperatorsLocalPrice(t *testing.T) {
	t.Cleanup(ResetLocal)
	ResetLocal()
	if strings.Contains(PriceTable(), "local/") {
		t.Error("the price table lists a local row with nothing configured")
	}
	if err := ConfigureLocal(LocalSetting{Model: "llama3.1:8b", InPerM: 0.05, OutPerM: 0.1}); err != nil {
		t.Fatal(err)
	}
	table := PriceTable()
	if !strings.Contains(table, "local/llama3.1:8b") || !strings.Contains(table, "operator-set") {
		t.Errorf("the price table does not name the operator's row and its source:\n%s", table)
	}
}

func TestSelfHostedFamilyHasATitleAndANote(t *testing.T) {
	if FamilyTitle(SelfHosted) == string(SelfHosted) || FamilyNote(SelfHosted) == "" {
		t.Error("the self-hosted family renders as its raw id with no note")
	}
}
