package web_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// Invariant 93: the engines page and the forms say what the local engine is,
// and a local run's task and card show the tokens it used.

func TestTheEnginesPageCountsItsCatalogueAndLeavesLocalToTheRunner(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_URL", "http://127.0.0.1:11434/v1")
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, body, _ := h.get(t, "/engines")
	want := strconv.Itoa(engines.FamilyCount()) + ` answers to "what do I need", covering ` +
		strconv.Itoa(engines.EngineCount()) + " engines"
	if !strings.Contains(htmlUnescape(body), want) {
		t.Errorf("the engines page does not count its own catalogue: want %q", want)
	}
	if strings.Contains(body, "twelve providers") || strings.Contains(body, "Three answers") {
		t.Error("the engines page still says three answers and twelve providers")
	}
	i := strings.Index(body, "<strong>A model you host yourself</strong>")
	if i < 0 {
		t.Fatal("no local engine entry")
	}
	local := body[i : i+strings.Index(body[i:], "</dl>")]
	if strings.Contains(local, `<span class="chip accepted">ready</span>`) {
		t.Error("the local engine reads as ready because this console's own environment names a server")
	}
	for _, w := range []string{"set up where costcrew-run runs", "decided by costcrew-run", "-local-parallel"} {
		if !strings.Contains(local, w) {
			t.Errorf("the local engine's entry does not say %q", w)
		}
	}
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&#34;", `"`, "&quot;", `"`, "&#39;", "'").Replace(s)
}

func TestTheHireAndRebriefFormsNameTheEngines(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, hire, _ := h.get(t, "/staff/new")
	if !strings.Contains(hire, `<option value="local">A model you host yourself (local)</option>`) {
		t.Error("the hire form does not name the local engine")
	}
	var name string
	if err := h.st.DB().QueryRow(`SELECT name FROM analysts ORDER BY name LIMIT 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	_, re, _ := h.get(t, "/staff/"+name+"/edit")
	if !strings.Contains(re, ">Anthropic API (anthropic)</option>") {
		t.Error("the re-brief form does not name the engines")
	}
}

// localTask plants a local-engine analyst with one task that used tokens.
func localTask(t *testing.T, h *harness, tokens, ceiling int64) string {
	t.Helper()
	db := h.st.DB()
	if _, err := db.Exec(`UPDATE analysts SET engine='local' WHERE name='ai-spend'`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents,
		created, updated, live_tokens, live_token_ceiling)
		VALUES ('local work', 'g', 'ai-spend', 'ai', 'active', 500, 0, datetime('now'), datetime('now'), ?, ?)`,
		tokens, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created, source)
		VALUES (?, 'ai-spend', 'Deliverable', 'written on our own server', 'draft', datetime('now'), 'live')`, id); err != nil {
		t.Fatal(err)
	}
	return "/task/" + strconv.FormatInt(id, 10)
}

func TestALocalTaskShowsTokensAgainstTheRunCeiling(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, body, _ := h.get(t, localTask(t, h, 12345, 50000))
	for _, w := range []string{"<strong>12 345 tokens</strong> on the organisation's own model, in a run bounded at 50 000 tokens",
		"No vendor bills this engine", "own server wrote this"} {
		if !strings.Contains(body, w) {
			t.Errorf("the local task page does not show %q:\n%s", w, fragment(body, "What it cost", 700))
		}
	}
	if strings.Contains(body, "against a per-task guard") {
		t.Error("a local task at no price shows 0.00 against a per-task guard")
	}
	if strings.Contains(body, "against a real key") {
		t.Error("a deliverable written on the organisation's own model is marked as written against a real key")
	}
}

func TestTheLocalAgentCardShowsTheTokensItsWorkUsed(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	localTask(t, h, 12345, 50000)
	_, body, _ := h.get(t, "/staff/ai-spend")
	if !strings.Contains(body, "12 345 tokens on the organisation's own model across 1 task") {
		t.Errorf("the local agent's card does not show the tokens its live work used:\n%s", fragment(body, "Its work cost", 900))
	}
}
