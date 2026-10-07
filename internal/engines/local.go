package engines

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

// LocalSetting is what the operator told this process about the model they
// host themselves. It is the one engine whose model and price are not a fact
// about a vendor's list but a decision of the person running it.
//
// A process-wide setting rather than a parameter, because the price is read by
// four callers that cannot be handed it (the runner's estimator, the console's
// cadence preview, the planning estimate, the bench) and each of them already
// asks this package for "the price of engine/model". It is set once, at the
// start of a run, by the binary that parsed the flags, and read by everybody.
// Nothing here is secret: the endpoint and the key are not part of it.
type LocalSetting struct {
	// Model is the name the operator's server serves ("llama3.1:8b",
	// "qwen2.5-coder"). Not validated against anything: nobody here can see the
	// server's own list.
	Model string
	// InPerM and OutPerM are USD per million tokens, what the operator charges
	// themselves for their own hardware. 0 and 0 is a legal setting and means
	// money cannot bound a run on this engine; the runner then requires a token
	// ceiling instead.
	InPerM, OutPerM float64
}

var local struct {
	mu  sync.RWMutex
	s   LocalSetting
	set bool
}

// ConfigureLocal records the operator's setting. A price that is negative,
// NaN or infinite is refused: a bound computed from one is not a bound, and
// "-1 dollars per million tokens" would make a reservation negative and every
// check against it pass. An empty model name is refused for the same reason
// the catalogue has no model list: with no name there is nothing to send.
func ConfigureLocal(s LocalSetting) error {
	s.Model = strings.TrimSpace(s.Model)
	if s.Model == "" {
		return fmt.Errorf("the local engine needs a model name (-model-name): the operator " +
			"names what their own server serves")
	}
	if err := CheckLocalPrices(s.InPerM, s.OutPerM); err != nil {
		return err
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	local.s, local.set = s, true
	return nil
}

// CheckLocalPrices is the refusal for a price that is not a price: negative,
// NaN or infinite. Exported so a caller can refuse a bad -local-price-* flag
// even when no model name was given and ConfigureLocal is never reached.
func CheckLocalPrices(inPerM, outPerM float64) error {
	for _, v := range []struct {
		flag string
		p    float64
	}{{"-local-price-in", inPerM}, {"-local-price-out", outPerM}} {
		if math.IsNaN(v.p) || math.IsInf(v.p, 0) || v.p < 0 {
			return fmt.Errorf("%s must be a number of USD per million tokens, zero or more", v.flag)
		}
	}
	return nil
}

// ResetLocal forgets the setting, so a process that configures it twice (a
// test, a second run in one process) never reads the first run's price.
func ResetLocal() {
	local.mu.Lock()
	defer local.mu.Unlock()
	local.s, local.set = LocalSetting{}, false
}

// Local is the current setting and whether there is one.
func Local() (LocalSetting, bool) {
	local.mu.RLock()
	defer local.mu.RUnlock()
	return local.s, local.set
}

// localPrice is the local engine's price as a Price, and false when nothing was
// configured. Unconfigured is "unknown", never "free": the console, which never
// configures this, must show the engine as unpriced rather than as costing 0.
func localPrice() (Price, bool) {
	s, ok := Local()
	if !ok {
		return Price{}, false
	}
	return Price{
		InPerM: s.InPerM, OutPerM: s.OutPerM,
		Recorded: "set by the operator for this run",
		Source:   "operator-set (-local-price-in, -local-price-out), what their own hardware costs; no vendor price",
	}, true
}
