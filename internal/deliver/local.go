package deliver

// The local engine: a model the organisation hosts itself, reached over the
// OpenAI chat-completions wire (Ollama, vLLM, LM Studio, the llama.cpp server).
//
// Why this exists, and what it must never do: an organisation that may not
// send billing data outside its perimeter has to be able to run the crew with
// nothing leaving it. So the one question this file answers is WHERE A CALL
// GOES, and the answer has exactly two shapes and no third:
//
//   - no gateway configured: straight to -model-url, the operator's own server;
//   - -gateway-openai configured: to that gateway, whose upstream is then the
//     operator's server, metered like any other call (invariant 54);
//   - any gateway configured and none that fronts the OpenAI wire: refused,
//     before any request, by Gateway.RouteFor. Never sent direct to
//     -model-url behind a gateway's back, for the reason invariant 54 gives.
//
// There is no vendor host anywhere in this file. A test holds that: the only
// address a local call can name is one the operator typed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// LocalRoundTimeout bounds one round to the operator's server. Longer than the
// 90 seconds the vendor routes get, deliberately: a model on the
// organisation's own CPU can spend minutes loading and prefilling a 12 KiB
// packet, and a timeout that fires on a slow-but-working server would block
// every task for a reason that is the hardware's and not the crew's. Each task
// still has its own deadline in spend().
const LocalRoundTimeout = 5 * time.Minute

// NormalizeModelURL validates -model-url: the base URL of an OpenAI-compatible
// server, e.g. http://127.0.0.1:11434/v1. The empty string means "not
// configured". Everything the gateway flags refuse is refused here too (it IS
// that validator, NormalizeGatewayFlag), and four more things that matter for
// an address a call is built from:
//
//   - credentials in the URL (http://user:pass@host/v1). A key belongs in
//     COSTCREW_MODEL_KEY, where it is sent as a bearer token and logged
//     nowhere; a password in a URL ends up in every error message that names
//     the URL, in the process list, and in shell history. The refusal does not
//     echo the value it refused.
//   - a query string or a fragment, because the route is appended to the path
//     and "…/v1?x=1/chat/completions" is a different, broken address.
//
// The trailing slash is stripped so "<base>/chat/completions" never doubles it.
func NormalizeModelURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(raw)
	if u, err := url.Parse(trimmed); err == nil {
		if u.User != nil {
			return "", fmt.Errorf("-model-url must not carry credentials (user:password@host): " +
				"put a key in COSTCREW_MODEL_KEY, which is sent as a bearer token and never logged")
		}
		if u.RawQuery != "" || u.Fragment != "" || strings.Contains(trimmed, "?") || strings.Contains(trimmed, "#") {
			return "", fmt.Errorf("-model-url must be a base URL with no query string or fragment: " +
				"the route /chat/completions is appended to its path")
		}
	}
	out, err := NormalizeGatewayFlag("-model-url", raw)
	if err != nil {
		if strings.Contains(raw, "@") {
			// A value that does not parse may still hold a password. Say what is
			// wrong without repeating it.
			return "", fmt.Errorf("-model-url is not an http(s) URL")
		}
		return "", err
	}
	return out, nil
}

// ModelURLEnvDefault backs -model-url's default with COSTCREW_MODEL_URL, the
// same way GatewayEnvDefault backs -gateway: read here and not in tools/run's
// main.go, which TestThisBinaryCannotSpend keeps free of os.Getenv.
func ModelURLEnvDefault() string { return strings.TrimSpace(os.Getenv("COSTCREW_MODEL_URL")) }

// ModelNameEnvDefault backs -model-name with COSTCREW_MODEL_NAME.
func ModelNameEnvDefault() string { return strings.TrimSpace(os.Getenv("COSTCREW_MODEL_NAME")) }

// modelKey is the optional bearer token some servers want. Read at the moment
// of use and never stored, never put in an error, never printed.
func modelKey() string { return strings.TrimSpace(os.Getenv("COSTCREW_MODEL_KEY")) }

// SetLocalAuth puts COSTCREW_MODEL_KEY on a request as a bearer token when the
// operator set one, and does nothing when they did not: most self-hosted
// servers want no key at all, and sending "Bearer " with nothing after it is a
// request some of them refuse.
func SetLocalAuth(req *http.Request) {
	if k := modelKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
}

// OpenAIEndpoint is the URL one OpenAI-shaped round for engine goes to, and
// whether it goes through a gateway. The two engines that speak this wire,
// openrouter and local, ask the same question and get their answers from
// Gateway.RouteFor, so neither can be sent direct by a path the other does not
// have.
//
// For the local engine the direct address is <ModelURL>/chat/completions
// (ModelURL already carries /v1, as Ollama's and vLLM's documented base URLs
// do), and the gateway address is <OpenAIURL>/v1/chat/completions, the route
// TokenFuse serves, exactly as for openrouter.
func OpenAIEndpoint(engine string, gw Gateway) (endpoint string, routed bool, err error) {
	switch engine {
	case "openrouter":
		return OpenRouterEndpoint(gw)
	case engines.LocalID:
		base, err := gw.RouteFor(engines.LocalID)
		if err != nil {
			return "", false, err
		}
		if base != "" {
			return base + OpenAICompletionsPath, true, nil
		}
		if gw.ModelURL == "" {
			return "", false, fmt.Errorf("the local engine has no server to call: set -model-url " +
				"(COSTCREW_MODEL_URL) to the base URL of your OpenAI-compatible server, or " +
				"-gateway-openai to a gateway whose upstream it is")
		}
		return gw.ModelURL + "/chat/completions", false, nil
	}
	return "", false, fmt.Errorf("no OpenAI-shaped route is defined for engine %q", engine)
}

// LocalTarget names where a local call is going, for the one-line messages: the
// gateway when the call is routed through one, the operator's server
// otherwise.
func LocalTarget(gw Gateway) string {
	if gw.OpenAIURL != "" {
		return gw.OpenAIURL
	}
	return gw.ModelURL
}

// ReachError turns a transport failure into the one line a person acts on:
// what could not be reached, at which address, and the reason in a few words.
// The raw error is `Post "http://…/v1/chat/completions": dial tcp …: connect:
// connection refused`, which is a stack of wrappers around the fact that the
// server is not there. The URL is the operator's own base URL, which
// NormalizeModelURL has already stripped of credentials, so naming it is safe;
// the key is never in a URL and so is never in this line.
func ReachError(what, base string, err error) error {
	reason := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		reason = ue.Err.Error()
	}
	var to interface{ Timeout() bool }
	if errors.As(err, &to) && to.Timeout() {
		reason = "no answer in time (" + reason + ")"
	}
	reason = strings.Join(strings.Fields(reason), " ")
	return fmt.Errorf("%s at %s did not answer: %s", what, base, trim(reason, 160))
}

// ProbeModelServer asks the operator's server whether anybody is there, once,
// before a run starts, so that a server that is down stops the run with one
// line instead of blocking every task one by one. ANY HTTP answer counts as
// "there": a 404 from a server with no /models route, or a 401 from one that
// wants a key, is a server that is up. Only a transport failure is a refusal.
// It sends no key and no prompt: it names nothing but an address.
func ProbeModelServer(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return fmt.Errorf("the local model server address %q is not usable: %w", base, err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return ReachError("the local model server", base, err)
	}
	_ = resp.Body.Close()
	return nil
}

// CountLocalUsage is what one local round is counted as. The server's own
// numbers when it reported any; otherwise the WORST CASE for the round: every
// byte of the request counted as a prompt token (no tokeniser splits below a
// byte, the rule Tokens already uses) and the whole output cap as output.
//
// Zero and zero is read as "did not report", not as "used nothing": a request
// with a prompt in it used at least one token, and a server that omits its
// usage block would otherwise make every round free, which on an engine whose
// price may be 0 is the difference between a bounded run and none. Counting
// the worst case can only over-count, and over-counting is the direction a
// ceiling is allowed to be wrong in.
func CountLocalUsage(reportedIn, reportedOut, requestBytes, maxTok int) (in, out int, estimated bool) {
	// A server cannot have used a negative number of tokens. Whatever it sent
	// is believed only when positive, so a hostile or broken minus sign cannot
	// subtract from a ceiling.
	reportedIn, reportedOut = max(reportedIn, 0), max(reportedOut, 0)
	if reportedIn > 0 || reportedOut > 0 {
		return reportedIn, reportedOut, false
	}
	return requestBytes, maxTok, true
}

// callLocal is the single-shot call on the local engine: the same OpenAI
// request the openrouter route sends (openRouterBody, SetFuseHeaders), to the
// address OpenAIEndpoint names. The tool loop in tools/run does not come
// through here; it shares the SAME endpoint function and the same body shape.
func callLocal(ctx context.Context, model, prompt string, maxTok int, gw Gateway) (Result, error) {
	if strings.TrimSpace(model) == "" {
		return Result{}, fmt.Errorf("the local engine has no model name: set -model-name " +
			"(COSTCREW_MODEL_NAME) to the model your server serves")
	}
	endpoint, routed, err := OpenAIEndpoint(engines.LocalID, gw)
	if err != nil {
		return Result{}, err
	}
	if err := RequireIdentity(gw, routed); err != nil {
		return Result{}, err
	}
	body, err := openRouterBody(model, prompt, maxTok)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	SetLocalAuth(req)
	if routed {
		SetFuseHeaders(req, gw)
	}

	resp, err := (&http.Client{Timeout: LocalRoundTimeout}).Do(req)
	if err != nil {
		what := "the local model server"
		if routed {
			what = "the gateway in front of the local model"
		}
		return Result{}, ReachError(what, LocalTarget(gw), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusPaymentRequired && routed {
		return Result{}, GatewayRefusal{ParseGatewayRefusal(raw)}
	}
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("the local model server answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Result{}, fmt.Errorf("the local model server answered 200 with an empty body")
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("the local model server's answer did not parse: %w", err)
	}
	var st Settlement
	if routed {
		st = ParseSettlement(resp.Header)
	}
	if len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("the local model server returned no answer")
	}
	in, outTok, _ := CountLocalUsage(out.Usage.PromptTokens, out.Usage.CompletionTokens, len(body), maxTok)
	return Result{
		Text:       out.Choices[0].Message.Content,
		InTokens:   in,
		OutTokens:  outTok,
		Settlement: st,
	}, nil
}
