package engines

import "testing"

// An engine that spends money must READ as one, whether or not it holds a key.
//
// Red first: Metered inferred "does this cost new money" from whether the
// engine carries an environment variable for a credential. That was
// accidentally right for every engine in the catalogue until Bedrock, which
// bills the AWS account and takes its credentials from the cloud's own chain
// rather than from a variable somebody pastes.
//
// The direction of the failure is what makes it worth a test. An engine read
// as unmetered is read as "on a subscription, nothing new billed", so the
// estimator waves it through with no bound at all. That is the reading that
// spends money, and prices.go's own header says so about the previous version
// of this same function.
func TestAnEngineWithNoEnvVarCanStillBeMetered(t *testing.T) {
	metered, known := Metered("bedrock")
	if !known {
		t.Fatal("bedrock is not in the catalogue at all")
	}
	if !metered {
		t.Error("bedrock reads as unmetered because it carries no EnvVar: the " +
			"estimator would treat every call as already paid for and bound " +
			"nothing, on an engine that bills somebody's AWS account per token")
	}
}

// And the subscriptions must still read as subscriptions.
func TestASubscriptionStillReadsAsOne(t *testing.T) {
	for _, id := range []string{"claude-cli", "local-cli"} {
		metered, known := Metered(id)
		if !known {
			t.Fatalf("%s is not in the catalogue", id)
		}
		if metered {
			t.Errorf("%s reads as metered: a bound that refuses a subscription "+
				"call for costing 0.00 refuses the engine most people use", id)
		}
	}
}

// A Bedrock model needs a price, or the estimator refuses it.
func TestBedrockModelsCarryPrices(t *testing.T) {
	for _, e := range Catalogue {
		if e.ID != "bedrock" {
			continue
		}
		if len(e.Models) == 0 {
			t.Fatal("the bedrock engine offers no model, so nothing can be hired onto it")
		}
		for _, m := range e.Models {
			if _, ok := PriceFor("bedrock", m); !ok {
				t.Errorf("no price for bedrock/%s: the estimator refuses a model "+
					"it cannot price, so an engine whose models have no prices is "+
					"an engine nobody can run", m)
			}
		}
	}
}
