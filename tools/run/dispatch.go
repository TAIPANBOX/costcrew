package main

// The dispatcher: a skill is a tool it can call, and a right it does not
// hold is refused.
//
// B2-SPEC.md section 3.2. Before this step an analyst's rights bounded
// nothing it could actually reach through a model call, because there was
// no call for a model to make in the first place -- the enforcement
// invariant 8 names ("a right this console can grant has an explanation")
// held for the CARD and never for an action. This is what makes the right
// check reachable from a real request.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
)

// toolResultMaxBytes bounds what a tool hands back to the model, the same
// way packetMaxBytes bounds the packet: a number the actual output can be
// checked against.
const toolResultMaxBytes = 16 * 1024

const toolTimeout = 5 * time.Second

// dispatchOutcome is what the bus event and the console line read; the
// model only ever sees dispatchResult.Text.
type dispatchOutcome string

const (
	outcomeOK          dispatchOutcome = "ok"
	outcomeUnknownTool dispatchOutcome = "tool_unknown"
	outcomeRefused     dispatchOutcome = "tool_refused"
	outcomeInvalidArgs dispatchOutcome = "invalid_args"
	outcomeError       dispatchOutcome = "error"
	// outcomeWithheld is a tool this installation's -prompt-data setting does
	// not offer: the model asked for it anyway (a model can name a tool it was
	// not offered), and it was told so rather than answered.
	outcomeWithheld dispatchOutcome = "tool_withheld"
)

type dispatchResult struct {
	Text    string
	Outcome dispatchOutcome
	Right   string
}

// dispatch is section 3.2's three steps, in order: look the tool up, check
// the right, validate and run. Every path returns a Text a model can read
// as the tool_result content -- never a bare Go error -- because a model
// mid-conversation has no other channel to be told anything on.
func dispatch(ctx context.Context, db, roDB *sql.DB, a crew.Analyst, name string, args json.RawMessage, b bus) dispatchResult {
	pol := deliver.ActivePolicy()
	// finish is every return below. The text the model reads is masked first
	// (a tool's own error can repeat a name the model passed in) and bounded
	// after, so the bound is a bound on what is sent and a token longer than
	// the name it replaced cannot push a result past it (invariant 70).
	finish := func(r dispatchResult, tool string) dispatchResult {
		if !pol.Full() {
			r.Text = pol.MaskText(r.Text, a.Name)
		}
		r.Text = boundBytes(r.Text, toolResultMaxBytes)
		b.toolDispatch(a.Name, tool, r.Right, string(r.Outcome), len(r.Text))
		return r
	}

	def, ok := toolByName(name)
	if !ok {
		return finish(dispatchResult{
			Text:    fmt.Sprintf("there is no tool named %q", name),
			Outcome: outcomeUnknownTool,
		}, name)
	}

	// A tool the policy does not offer is not run, whatever rights the analyst
	// holds. It is checked here as well as when the catalogue is rendered: the
	// model chooses what to call, and nothing says it only calls what it was
	// shown.
	if !pol.ToolOffered(def.Name) {
		return finish(dispatchResult{
			Text: fmt.Sprintf("%s is not available: this installation does not send that kind of data "+
				"to a model (-prompt-data %s)", def.Name, pol.Mode()),
			Outcome: outcomeWithheld,
			Right:   def.Right,
		}, def.Name)
	}

	rights := crew.RightsFor(a.Skills, a.State)
	if !hasString(rights, def.Right) {
		fmt.Printf("  tool refused: %s called %s, needs %s\n", a.Name, def.Name, def.Right)
		return finish(dispatchResult{
			Text:    fmt.Sprintf("you do not hold %s; ask the supervisor", def.Right),
			Outcome: outcomeRefused,
			Right:   def.Right,
		}, def.Name)
	}

	// The model reads tokens, so it writes tokens: the arguments it passes
	// name a team as team-7f3a, and the tool needs the team. Put the names
	// back before anything validates or runs them. A token it invented stays
	// as written and finds nothing.
	args = reidentifyArgs(pol, args)

	if err := validateArgs(def.Schema, args); err != nil {
		return finish(dispatchResult{
			Text:    fmt.Sprintf("bad arguments for %s: %v", def.Name, err),
			Outcome: outcomeInvalidArgs,
			Right:   def.Right,
		}, def.Name)
	}

	rctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, err := def.Run(rctx, db, roDB, args)
	if err != nil {
		return finish(dispatchResult{
			Text:    fmt.Sprintf("%s failed: %v", def.Name, err),
			Outcome: outcomeError,
			Right:   def.Right,
		}, def.Name)
	}

	return finish(dispatchResult{
		Text:    out,
		Outcome: outcomeOK,
		Right:   def.Right,
	}, def.Name)
}

// reidentifyArgs puts real names back into the string values of a tool call's
// arguments. It decodes the JSON, rewrites the strings and encodes it again,
// and does NOT replace inside the raw text: a real name is data, and one that
// carries a quote, a brace or a comma ("x\",\"period\":\"y") spliced into
// the text of the arguments would close the string it was in and write
// arguments of its own. Arguments that are not JSON are returned as they are,
// for validateArgs to refuse.
func reidentifyArgs(pol *deliver.Policy, args json.RawMessage) json.RawMessage {
	if pol.Full() || len(args) == 0 {
		return args
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return args
	}
	out, err := json.Marshal(mapStrings(v, pol.Reidentify))
	if err != nil {
		return args
	}
	return out
}

func mapStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case string:
		return f(x)
	case []any:
		for i := range x {
			x[i] = mapStrings(x[i], f)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = mapStrings(e, f)
		}
		return x
	}
	return v
}

// validateArgs is a small, hand-written JSON Schema subset: object, string
// and integer properties, required by name. It exists to catch a malformed
// call before a Run function has to, not to be a general schema engine --
// the catalogue's own schemas are simple by construction (tools.go), and a
// dependency for validating eleven small objects would cost more surface
// than it buys.
func validateArgs(schema map[string]any, args json.RawMessage) error {
	parsed := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return fmt.Errorf("arguments are not valid JSON: %v", err)
		}
	}
	required, _ := schema["required"].([]string)
	props, _ := schema["properties"].(map[string]any)
	for _, name := range required {
		v, ok := parsed[name]
		if !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
		propSchema, _ := props[name].(map[string]any)
		wantType, _ := propSchema["type"].(string)
		switch wantType {
		case "string":
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("argument %q must be a string", name)
			}
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("argument %q must not be empty", name)
			}
		case "integer":
			if _, ok := v.(float64); !ok {
				return fmt.Errorf("argument %q must be a number", name)
			}
		}
	}
	return nil
}

// toolDispatch is the bus event section 3.2 asks for: every dispatch,
// allowed or refused, reaches the shared bus as a tool_call event carrying
// the tool name, the right, the outcome and the bytes returned -- the same
// event NAME saveDraft already emits for a model call (bus.go's toolCall),
// reused rather than invented a second time, with its own data shape for
// what a TOOL dispatch actually is.
func (b bus) toolDispatch(analyst, tool, right, outcome string, bytesReturned int) error {
	if b.em == nil || !b.em.On() {
		return nil
	}
	return b.em.Emit("tool_call", analyst, "info", map[string]any{
		"run":     b.run,
		"tool":    tool,
		"right":   right,
		"outcome": outcome,
		"bytes":   bytesReturned,
		// Which policy this call's text was built under (invariant 70): a
		// reader of a run's evidence can tell what could have left.
		"prompt_data": b.mode(),
	}, nil)
}
