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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
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
	def, ok := toolByName(name)
	if !ok {
		r := dispatchResult{
			Text:    fmt.Sprintf("there is no tool named %q", name),
			Outcome: outcomeUnknownTool,
		}
		b.toolDispatch(a.Name, name, "", string(r.Outcome), len(r.Text))
		return r
	}

	rights := crew.RightsFor(a.Skills, a.State)
	if !hasString(rights, def.Right) {
		r := dispatchResult{
			Text:    fmt.Sprintf("you do not hold %s; ask the supervisor", def.Right),
			Outcome: outcomeRefused,
			Right:   def.Right,
		}
		fmt.Printf("  tool refused: %s called %s, needs %s\n", a.Name, def.Name, def.Right)
		b.toolDispatch(a.Name, def.Name, def.Right, string(r.Outcome), len(r.Text))
		return r
	}

	if err := validateArgs(def.Schema, args); err != nil {
		r := dispatchResult{
			Text:    fmt.Sprintf("bad arguments for %s: %v", def.Name, err),
			Outcome: outcomeInvalidArgs,
			Right:   def.Right,
		}
		b.toolDispatch(a.Name, def.Name, def.Right, string(r.Outcome), len(r.Text))
		return r
	}

	rctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, err := def.Run(rctx, db, roDB, args)
	if err != nil {
		r := dispatchResult{
			Text:    fmt.Sprintf("%s failed: %v", def.Name, err),
			Outcome: outcomeError,
			Right:   def.Right,
		}
		b.toolDispatch(a.Name, def.Name, def.Right, string(r.Outcome), len(r.Text))
		return r
	}

	r := dispatchResult{
		Text:    boundBytes(out, toolResultMaxBytes),
		Outcome: outcomeOK,
		Right:   def.Right,
	}
	b.toolDispatch(a.Name, def.Name, def.Right, string(r.Outcome), len(r.Text))
	return r
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
	}, nil)
}
