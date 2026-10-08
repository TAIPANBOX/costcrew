package main

// The runner's half of the local engine: a model the organisation hosts
// itself. What the flags mean, what a run on it is bounded by, and the
// preflight that refuses before the first call.
//
// The shape of the problem. Every guard in this binary is in money: a task's
// guard, the run's ceiling, the reservation taken before a call. A model on the
// organisation's own hardware has no vendor price, so its money is whatever the
// operator says it is (-local-price-in, -local-price-out, USD per million
// tokens) and 0 by default. A reservation of 0 never refuses anything, so a
// run on a zero-priced engine would be bounded by nothing at all. The answer is
// a second unit: tokens. -max-run-tokens is a ceiling on the tokens a live run
// may use, reserved before each task exactly as money is, and a run that
// includes the local engine at a price of 0 is refused at start without one.
//
// What this file does not touch: which engine a task is on (the analyst's
// hire-time engine), the packet, the prompt, or how a settled call is charged
// (invariant 51). A call through the gateway is still charged what the
// gateway settled it at.

import (
	"context"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// localOptions is what the flags -model-url, -model-name, -local-price-in,
// -local-price-out, -max-run-tokens and -local-parallel carry, unvalidated.
type localOptions struct {
	ModelURL     string
	ModelName    string
	PriceIn      float64 // USD per million input tokens
	PriceOut     float64 // USD per million output tokens
	MaxRunTokens int
	// Parallel is -local-parallel: how many local-engine tasks run at once.
	// 0 means the default, 1.
	Parallel int
}

// apply validates the options and publishes them to the engines package, which
// the estimator reads the local engine's price and model from. It returns the
// normalised -model-url. Called before the store or the bus open, with the
// gateway flags' own validation, so a bad value is reported before anything
// has happened.
//
// It resets first: a second run in one process (a test) must never read the
// first run's price. The setting is published only when a model name was given;
// without one the engine stays unconfigured, and price() says "name the model"
// for a task on it instead of pricing a call with nothing to send.
func (o localOptions) apply() (string, error) {
	engines.ResetLocal()
	modelURL, err := deliver.NormalizeModelURL(o.ModelURL)
	if err != nil {
		return "", err
	}
	if err := engines.CheckLocalPrices(o.PriceIn, o.PriceOut); err != nil {
		return "", err
	}
	if o.MaxRunTokens < 0 {
		return "", fmt.Errorf("-max-run-tokens must be zero or more; zero means no token ceiling")
	}
	if o.Parallel < 0 || o.Parallel > localParallelMax {
		return "", fmt.Errorf("-local-parallel must be between 1 and %d: how many local-engine tasks run "+
			"at once, which should not exceed the requests your server answers at once", localParallelMax)
	}
	if o.ModelName != "" {
		if err := engines.ConfigureLocal(engines.LocalSetting{
			Model: o.ModelName, InPerM: o.PriceIn, OutPerM: o.PriceOut}); err != nil {
			return "", err
		}
	}
	return modelURL, nil
}

// modelURLEnvDefault and modelNameEnvDefault back -model-url and -model-name
// with COSTCREW_MODEL_URL and COSTCREW_MODEL_NAME, read through
// internal/deliver for the reason gatewayEnvDefault is: main.go itself must
// stay provably unable to read the environment.
func modelURLEnvDefault() string  { return deliver.ModelURLEnvDefault() }
func modelNameEnvDefault() string { return deliver.ModelNameEnvDefault() }

// reservedWorstTokens is the most tokens one execute() of this task can use,
// the unit -max-run-tokens is held in: the loop's rounds times one round's
// bound, which is the prompt, the tool catalogue the loop sends on every round
// but the last, and the whole output cap. The same arithmetic as the money
// bound (reservedWorstCase), in the other unit, so the two cannot describe
// different worst cases.
//
// What is still NOT covered: the conversation grows from round to round (each
// round re-sends every prior message and tool result), so a task's real
// count can pass this. Settling books the real count, above the reservation if
// it came to that, and the next task's reservation is checked against it, the
// same property the money ceiling has.
func reservedWorstTokens(e estimate, maxTok int) int64 {
	return int64(loopsFor(e.Engine)) * int64(e.PromptTokens+e.CatalogueTokens+maxTok)
}

// priceIsZero is whether money can bound a call on this estimate at all: a
// price of 0 in and 0 out reserves 0 and refuses nothing.
func priceIsZero(p engines.Price) bool { return p.InPerM == 0 && p.OutPerM == 0 }

// localPreflight is the refusal, before the first call, of a run the local
// engine cannot be run safely in. nil means every check passed. In order:
//
//  1. a task on the local engine with nowhere to go: neither -model-url nor
//     -gateway-openai is set. (With a gateway set and none that fronts the
//     OpenAI wire, noRouteRefusal has already refused, naming the engine.)
//  2. a task on the local engine priced at 0 with no -max-run-tokens: money
//     cannot bound it, so the run would have no bound at all.
//  3. the worst case of the whole run, in tokens, over -max-run-tokens, when
//     one is set: refused before it starts, as the money ceiling is.
//
// None of these touches the network. The fourth refusal, a direct server that
// does not answer, is localReachRefusal, run after the money preflight so that
// a run the numbers already refuse never knocks on a server.
func localPreflight(gw gatewayConfig, todo []estimate, maxTok int) error {
	var local, zero int
	var tokens int64
	for _, e := range todo {
		tokens += reservedWorstTokens(e, maxTok)
		if e.Engine != engines.LocalID {
			continue
		}
		local++
		if priceIsZero(e.Price) {
			zero++
		}
	}
	if local > 0 && gw.ModelURL == "" && gw.OpenAIURL == "" {
		return fmt.Errorf("%d task(s) are on the local engine and there is no server to call: "+
			"set -model-url (COSTCREW_MODEL_URL) to the base URL of your OpenAI-compatible server, "+
			"or -gateway-openai to a gateway whose upstream it is", local)
	}
	if zero > 0 && gw.MaxRunTokens <= 0 {
		return fmt.Errorf("%d task(s) are on the local engine at a price of 0, so money cannot bound "+
			"them and the run would have no limit at all: set -max-run-tokens to a ceiling on the "+
			"tokens this run may use, or price your hardware with -local-price-in and -local-price-out",
			zero)
	}
	if gw.MaxRunTokens > 0 && tokens > int64(gw.MaxRunTokens) {
		return fmt.Errorf("the worst case of the whole run is %d tokens and -max-run-tokens is %d: "+
			"refused before the first call", tokens, gw.MaxRunTokens)
	}
	return nil
}

// localReachRefusal asks the operator's server once whether anybody is there,
// when a task in the run goes to it directly: one line naming its URL, rather
// than every task blocked one by one with the same message. A run through the
// gateway does not probe: the gateway is the thing in front of it, and an
// unreachable gateway is the same condition for every engine.
func localReachRefusal(gw gatewayConfig, todo []estimate) error {
	if gw.OpenAIURL != "" || gw.ModelURL == "" {
		return nil
	}
	for _, e := range todo {
		if e.Engine == engines.LocalID {
			return deliver.ProbeModelServer(context.Background(), gw.ModelURL)
		}
	}
	return nil
}

// chargeBasisFor is chargeBasis with the local engine's own wording: a
// local call the gateway did not settle is priced at the operator's rate, not
// "by the runner" as if a vendor table were involved.
func chargeBasisFor(engine string, s deliver.Settlement) string {
	if engine == engines.LocalID && !s.Settled {
		return "priced at your own local rate (-local-price-in, -local-price-out); no vendor involved"
	}
	return chargeBasis(s)
}

// priceBasis is the price_basis the bus carries for a call. An engine=local run
// is "local" whatever else is true of the call, so the evidence says no vendor
// was involved; any other engine carries what the gateway said ("known",
// "fallback") or nothing.
func priceBasis(engine string, s deliver.Settlement) string {
	if engine == engines.LocalID {
		return "local"
	}
	return s.PriceBasis
}
