package main

// The half that can spend.
//
// It is a separate file on purpose. main.go holds the estimator and holds no
// way to call anything, and TestThisBinaryCannotSpend reads that file to keep
// it so. Everything that can put a charge on somebody's account lives here,
// where it can be read in one sitting.
//
// Four things bound it, and every one of them refuses BEFORE a call rather
// than reporting after:
//
//  1. -live must be passed. Without it nothing here runs at all.
//  2. -ceiling must be passed with it. A run with no ceiling is refused, not
//     defaulted: a default ceiling is a number nobody chose.
//  3. The worst case of the whole run is checked against that ceiling before
//     the first call.
//  4. Each call is checked against what is left of its task's guard AND
//     against what is left of the run's ceiling, using the same worst-case
//     arithmetic the dry run prints.
//
// The credential is read from the environment and never written anywhere: not
// to the database, not to the journal, not into an error message.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// callResult is what came back, and what it actually cost.
type callResult struct {
	Text         string
	InTokens     int
	OutTokens    int
	ActualMicros int64
}

// call routes to the engine the analyst was HIRED with.
//
// Which engine an analyst runs on is a decision recorded at hire time and
// visible on its card, and this is where that decision finally does
// something. A router that ignored it would make the field decoration.
func call(ctx context.Context, engine, model, prompt string, maxTok int) (callResult, error) {
	switch engine {
	case "openrouter":
		return callOpenRouter(ctx, model, prompt, maxTok)
	case "anthropic":
		return callAnthropic(ctx, model, prompt, maxTok)
	}
	return callResult{}, fmt.Errorf("no caller is written for engine %q", engine)
}

// callAnthropic is the second of the two functions here that spend money.
//
// A different wire from the router's: the key travels in x-api-key rather
// than a bearer token, the version header is required, and usage comes back
// as input_tokens and output_tokens rather than prompt and completion. Nothing
// about that is guessable, which is why it is written out rather than shared
// with a "compatible" abstraction that would be wrong in one of them.
// anthropicBody is the request, separate so the one thing that is easy to get
// silently wrong can be tested without spending anything.
//
// Thinking is turned OFF explicitly. Four tasks on a full run came back with
// "no text", and the reason, once the error said it, was exact:
//
//	stop_reason "max_tokens", blocks: thinking, 1200 output tokens
//
// The model spent the entire budget reasoning and never reached the answer.
// What this runner wants is the deliverable, not the reasoning, so the fix is
// to ask for the deliverable. It also makes the calls CHEAPER, which is the
// unusual direction for a fix.
func anthropicBody(model, prompt string, maxTok int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
}

func callAnthropic(ctx context.Context, model, prompt string, maxTok int) (callResult, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return callResult{}, fmt.Errorf("ANTHROPIC_API_KEY is not set in this process")
	}
	body, _ := anthropicBody(model, prompt, maxTok)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return callResult{}, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return callResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return callResult{}, fmt.Errorf("anthropic answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return callResult{}, fmt.Errorf("anthropic answered 200 with an empty body")
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return callResult{}, fmt.Errorf("anthropic's answer did not parse: %w", err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		// Say WHY. "returned no text" blocked four tasks on a full run and
		// named nothing a person could act on: a refusal, a turn that stopped
		// on max_tokens before writing a word, and an empty content array are
		// three different problems wearing one message.
		kinds := make([]string, 0, len(out.Content))
		for _, c := range out.Content {
			kinds = append(kinds, c.Type)
		}
		where := "no content blocks at all"
		if len(kinds) > 0 {
			where = "blocks: " + strings.Join(kinds, ", ")
		}
		return callResult{}, fmt.Errorf(
			"anthropic returned no text (stop_reason %q, %s, %d output tokens)",
			out.StopReason, where, out.Usage.OutputTokens)
	}
	return callResult{
		Text:      text.String(),
		InTokens:  out.Usage.InputTokens,
		OutTokens: out.Usage.OutputTokens,
	}, nil
}

// callOpenRouter is the first.
func callOpenRouter(ctx context.Context, model, prompt string, maxTok int) (callResult, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		return callResult{}, fmt.Errorf("OPENROUTER_API_KEY is not set in this process")
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return callResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return callResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// The body can echo a request, so only the status and a short prefix
		// travel: a key does not end up in a log through an error message.
		return callResult{}, fmt.Errorf("the router answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return callResult{}, fmt.Errorf("the router answered 200 with an empty body")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return callResult{}, fmt.Errorf("the router's answer did not parse: %w", err)
	}
	if len(out.Choices) == 0 {
		return callResult{}, fmt.Errorf("the router returned no answer")
	}
	return callResult{
		Text:      out.Choices[0].Message.Content,
		InTokens:  out.Usage.PromptTokens,
		OutTokens: out.Usage.CompletionTokens,
	}, nil
}

// prompt is what the analyst is asked, built only from what the console holds.
//
// The task, and the brief the analyst was hired with. Nothing else: an analyst
// without figures-read is not handed figures, and this is where that rule is
// kept rather than hoped for.
func prompt(t crew.Task, a crew.Analyst) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, %s on the %s desk of a FinOps practice.\n", a.Name, a.Role, a.Desk)
	if a.Mission != "" {
		fmt.Fprintf(&b, "Your brief: %s\n", a.Mission)
	}
	fmt.Fprintf(&b, "\nThe task on your desk is %q.\n", t.Title)
	if t.Goal != "" {
		fmt.Fprintf(&b, "What it asks for: %s\n", t.Goal)
	}
	b.WriteString("\nWrite the deliverable. Be specific, say what you do not know, " +
		"and do not invent a number you were not given.\n")
	return b.String()
}

// execute runs ONE task and records what it produced and what it cost.
//
// The artifact is a draft, never a post. Only a person's stamp publishes, and
// that invariant is older than this file.
func execute(ctx context.Context, db *sql.DB, e estimate, maxTok int, run *runBudget) error {
	if e.Refused {
		return fmt.Errorf("refused before the call: %s", e.Verdict)
	}
	if err := run.reserve(e.WorstMicros); err != nil {
		return refusal{err}
	}

	res, err := call(ctx, e.Engine, e.Model, prompt(e.Task, e.Analyst), maxTok)
	if err != nil {
		run.settle(e.WorstMicros, 0)
		return err
	}

	// What it ACTUALLY cost, from the usage the router reports, not from the
	// worst case. The worst case is what bounds the decision; this is what
	// goes in the ledger.
	in := float64(res.InTokens) / 1e6 * e.Price.InPerM
	out := float64(res.OutTokens) / 1e6 * e.Price.OutPerM
	res.ActualMicros = int64((in + out) * 1e6)
	run.settle(e.WorstMicros, res.ActualMicros)

	if err := saveDraft(db, e, res); err != nil {
		return err
	}

	fmt.Printf("  %-22s %-14s %-10s in %5d out %5d  cost %s  (worst %s)\n",
		trim(e.Task.Title, 22), e.Analyst.Name, trim(e.Engine, 10),
		res.InTokens, res.OutTokens, usd(res.ActualMicros), usd(e.WorstMicros))
	return nil
}

// saveDraft writes what the model produced and what it cost.
//
// It is separate from execute so that it can be tested without a network call,
// which is the only way to hold the property that matters here: a deliverable
// a model actually wrote is MARKED as one.
//
// The estate ships 279 generated drafts. A live run adds real ones to the same
// table, with the same author and the same state, and for one run 63 real
// deliverables sat indistinguishable among 342. Two kinds of thing under one
// heading is the fault this console exists to catch in other people's data.
func saveDraft(db *sql.DB, e estimate, res callResult) error {
	title := "Deliverable for " + e.Task.Title
	if _, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created, source)
		VALUES (?,?,?,?, 'draft', datetime('now'), 'live')`,
		e.Task.ID, e.Analyst.Name, trim(title, 120), res.Text); err != nil {
		return err
	}
	// The charge lands on the task in cents, which is the ledger's unit, and
	// rounds UP: a call that cost a fraction of a cent still cost something,
	// and rounding it to nothing is how a bill grows out of a column of zeroes.
	cents := money.Cents((res.ActualMicros + 9_999) / 10_000)
	if _, err := db.Exec(
		`UPDATE tasks SET spent_cents = spent_cents + ?, updated = datetime('now') WHERE id = ?`,
		int64(cents), e.Task.ID); err != nil {
		return err
	}
	return nil
}

// runBudget is the ceiling, held for the whole run.
//
// It RESERVES the worst case before a call and settles the difference after,
// which is what makes running several at once safe rather than hopeful. With
// a plain running total, four calls in flight could each pass a check against
// the same unspent balance and collectively walk past the ceiling; every one
// of them would have been individually correct.
//
// Reserved money is spent money until proven otherwise. That is the direction
// to be wrong in.
type runBudget struct {
	mu            sync.Mutex
	ceilingMicros int64
	reserved      int64 // in flight, at worst case
	spent         int64 // settled, at what it actually cost
}

// reserve takes the worst case out of the ceiling before the call is made.
func (r *runBudget) reserve(worst int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spent+r.reserved+worst > r.ceilingMicros {
		return fmt.Errorf("the run's ceiling is %s, %s is spent and %s is in "+
			"flight, and this call could cost %s: refused before making it",
			usd(r.ceilingMicros), usd(r.spent), usd(r.reserved), usd(worst))
	}
	r.reserved += worst
	return nil
}

// settle puts back what the call did not use. actual is 0 when it failed,
// which returns the whole reservation: a call that produced nothing cost
// nothing.
func (r *runBudget) settle(worst, actual int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved -= worst
	r.spent += actual
}

func (r *runBudget) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// spend runs the live half: it checks the whole run against the ceiling
// before the first call, then executes task by task, stopping the moment
// anything refuses.
//
// Stopping rather than continuing is the point. A run that skips a refusal
// and carries on is a run whose ceiling is advisory, and the next call is
// exactly as likely to be the expensive one.
// refusal is a budget decision: it stops everything. A call that simply
// failed is not one.
//
// The first version returned both as a plain error and stopped the run on
// either, so one empty response from the router aborted a sprint that was two
// cents into a fifty cent ceiling. Stopping on a refusal is the point, because
// a ceiling somebody carries on past is advisory. Stopping on a flaky response
// is just losing the rest of the work.
//
// A failed call becomes what the console already has a word for: the task is
// blocked, with the reason, which the board renders and the agent card shows
// under "Where it stopped".
type refusal struct{ error }

func spend(db *sql.DB, ests []estimate, maxTok int, cap money.Cents, only int) error {
	run := &runBudget{ceilingMicros: int64(cap) * 10_000}

	todo := make([]estimate, 0, len(ests))
	for _, e := range ests {
		if only != 0 && e.Task.ID != only {
			continue
		}
		if e.Refused || !e.Priced {
			continue
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		return fmt.Errorf("nothing to run: every open task was refused, is on a " +
			"subscription, or does not match -only")
	}

	var worst int64
	for _, e := range todo {
		worst += e.WorstMicros
	}
	fmt.Printf("LIVE. %d task(s), worst case %s, ceiling %s.\n", len(todo), usd(worst), cap)
	if worst > run.ceilingMicros {
		return fmt.Errorf("the worst case is %s and the ceiling is %s: refused "+
			"before the first call", usd(worst), cap)
	}
	fmt.Println()

	// A deadline PER CALL, not one for the whole run.
	//
	// This was a single ten-minute context shared by every task, so a run long
	// enough to matter guaranteed its own tail failed: forty-two calls at
	// twenty seconds each exhausted it, and the last fourteen were blocked
	// with "context deadline exceeded" having never been attempted. A bound on
	// one call is a timeout; a bound on all of them is an egg timer.
	// A few at a time. Sixty-three calls at twenty seconds each is twenty
	// minutes of somebody watching a terminal, and the wait is entirely the
	// model's: nothing here is CPU-bound.
	//
	// Four rather than as many as possible, because the far side rate-limits
	// and a run that trips that turns into a page of blocked tasks. Safe at
	// any width, because the ceiling is RESERVED before each call rather than
	// checked against a balance several calls are racing.
	const atOnce = 4

	var wg sync.WaitGroup
	var mu sync.Mutex
	var done, blocked int
	var stop bool

	sem := make(chan struct{}, atOnce)
	for _, e := range todo {
		mu.Lock()
		halted := stop
		mu.Unlock()
		if halted {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(e estimate) {
			defer wg.Done()
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := execute(ctx, db, e, maxTok, run)
			cancel()

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				done++
				return
			}
			var r refusal
			if errors.As(err, &r) {
				// A refusal stops the run. Nothing new starts; what is already
				// in flight finishes, and every one of those has its worst
				// case reserved, so the ceiling holds.
				fmt.Printf("\nstopped at %q: %v\n", trim(e.Task.Title, 40), err)
				stop = true
				return
			}
			if _, e2 := db.Exec(
				`UPDATE tasks SET state='blocked', reason=?, updated=datetime('now') WHERE id=?`,
				"the engine did not answer: "+trim(err.Error(), 160), e.Task.ID); e2 != nil {
				fmt.Printf("  could not record the block: %v\n", e2)
			}
			blocked++
			fmt.Printf("  %-22s %-14s BLOCKED: %v\n", trim(e.Task.Title, 22), e.Analyst.Name, err)
		}(e)
	}
	wg.Wait()
	fmt.Printf("\n%d of %d done, %d blocked. Spent %s of a %s ceiling.\n",
		done, len(todo), blocked, usd(run.total()), cap)
	fmt.Printf("Every deliverable is a DRAFT. Nothing is published until a person stamps it.\n")
	return nil
}
