package web_test

// An admin answering a decision addressed to somebody else. `@decided
// 2026-10-04`: that stays possible as an emergency path (an owner on leave,
// an owner who has left), but it is never silent: the admin gives a reason,
// and the option, the decision card, the journal and the bus all say the
// answer was given on behalf of the owner, by whom, and why. An owner
// answering their own decision needs no reason and is marked as the owner.

import (
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

const behalfReason = "owner1 is on leave until the 20th and the lapse date is tomorrow"

// behalfFixture is a console with an admin (boss), an owner (owner1), an
// operator who is neither (rando), and one carried option of a class nobody's
// side effect interferes with (budget.set is recorded only, so applying it
// changes nothing but the option's own state).
type behalfFixture struct {
	h            *harness
	artID, ord   int
	sprint       int
	apply, deny  string
	boss, owner1 *harness
	rando        *harness
}

func newBehalfFixture(t *testing.T, h *harness) behalfFixture {
	t.Helper()
	h.signUp(t, "boss", "boss-password-2026") // first account: admin
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.au.Create("rando", "rando-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	art, ord, sprint := plantCarriedOptionOfClass(t, h, "owner1", "budget.set", "raise the ml-platform budget")
	base := "/option/" + strconv.Itoa(art) + "/" + strconv.Itoa(ord)
	return behalfFixture{
		h: h, artID: art, ord: ord, sprint: sprint,
		apply: base + "/apply", deny: base + "/refuse",
		boss:   h.as(t, "boss", "boss-password-2026"),
		owner1: h.as(t, "owner1", "owner1-password-2026"),
		rando:  h.as(t, "rando", "rando-password-2026"),
	}
}

func (f behalfFixture) option(t *testing.T) crew.Option {
	t.Helper()
	o, err := crew.GetOption(f.h.st.DB(), f.artID, f.ord)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// journalOf is the newest journal entry of kind naming this artifact.
func journalOf(t *testing.T, h *harness, kind string, artifact int) map[string]any {
	t.Helper()
	tail, err := h.st.JournalTail(300)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range tail {
		if rec.Event != kind {
			continue
		}
		if a, _ := rec.Data["artifact"].(float64); int(a) == artifact {
			return rec.Data
		}
	}
	return nil
}

// Red first: today an admin applies another owner's option with no reason and
// nothing marks it.
func TestAnAdminAnsweringForAnotherOwnerMustGiveAReason(t *testing.T) {
	for _, reason := range []string{"", "   ", "\n\t "} {
		f := newBehalfFixture(t, start(t))
		code, loc := f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {reason}})
		if code != 303 {
			t.Fatalf("answered %d, want a redirect", code)
		}
		if got := f.option(t).State; got != crew.OptionCarried {
			t.Fatalf("an admin applied another owner's option with reason %q: state %q (redirected to %s)", reason, got, loc)
		}
		if e := journalOf(t, f.h, "option_applied", f.artID); e != nil {
			t.Errorf("an option_applied entry was journaled for an answer that was refused: %v", e)
		}
		if !strings.Contains(loc, "reason") {
			t.Errorf("the refusal does not say a reason is needed: %s", loc)
		}
	}
}

// Red first: the same for a refusal, which already needs its own reason; the
// on-behalf reason is a second thing, why somebody else is answering.
func TestAnAdminRefusingForAnotherOwnerMustGiveAReasonToo(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, _ := f.boss.post(t, f.deny, url.Values{
		"csrf": {f.boss.csrf(t, "/board")}, "reason": {"the budget stands"}, // no behalf_reason
	})
	if code != 303 {
		t.Fatalf("answered %d, want a redirect", code)
	}
	if got := f.option(t).State; got != crew.OptionCarried {
		t.Fatalf("an admin refused another owner's option with no reason for answering: state %q", got)
	}
}

// Red first: with a reason, the option, the journal and the event name the
// owner it was addressed to, the admin who answered, and why.
func TestAnAdminAnswerWithAReasonIsMarkedOnBehalfOfTheOwner(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, loc := f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {behalfReason}})
	if code != 303 {
		t.Fatalf("answered %d, want a redirect", code)
	}
	o := f.option(t)
	if o.State != crew.OptionApplied {
		t.Fatalf("state %q, want applied (redirected to %s)", o.State, loc)
	}
	if o.DecidedBy != "boss" || o.OnBehalfOf != "owner1" || o.BehalfReason != behalfReason {
		t.Errorf("option marks decided_by %q on_behalf_of %q reason %q, want boss, owner1 and the reason",
			o.DecidedBy, o.OnBehalfOf, o.BehalfReason)
	}
	e := journalOf(t, f.h, "option_applied", f.artID)
	if e == nil {
		t.Fatal("no option_applied entry in the journal")
	}
	if e["on_behalf_of"] != "owner1" || e["on_behalf_reason"] != behalfReason ||
		e["answered_by"] != "boss" || e["answered_as"] != "admin_on_behalf_of_owner" {
		t.Errorf("the journal entry reads %v, want on_behalf_of owner1, the reason, answered_by boss and answered_as admin_on_behalf_of_owner", e)
	}
	if e["actor"] != "boss" {
		t.Errorf("the entry's actor is %v, want boss: the admin is who stamped it", e["actor"])
	}
}

func TestAnAdminRefusalWithAReasonIsMarkedOnBehalfOfTheOwner(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, loc := f.boss.post(t, f.deny, url.Values{
		"csrf": {f.boss.csrf(t, "/board")}, "reason": {"the budget stands"}, "behalf_reason": {behalfReason},
	})
	if code != 303 {
		t.Fatalf("answered %d, want a redirect", code)
	}
	o := f.option(t)
	if o.State != crew.OptionRefused || o.Reason != "the budget stands" {
		t.Fatalf("state %q reason %q, want refused with the refusal's own reason (redirected to %s)", o.State, o.Reason, loc)
	}
	if o.DecidedBy != "boss" || o.OnBehalfOf != "owner1" || o.BehalfReason != behalfReason {
		t.Errorf("option marks decided_by %q on_behalf_of %q reason %q", o.DecidedBy, o.OnBehalfOf, o.BehalfReason)
	}
	e := journalOf(t, f.h, "option_refused", f.artID)
	if e == nil {
		t.Fatal("no option_refused entry in the journal for a refusal a person made")
	}
	if e["on_behalf_of"] != "owner1" || e["on_behalf_reason"] != behalfReason || e["answered_by"] != "boss" ||
		e["reason"] != "the budget stands" || e["answered_as"] != "admin_on_behalf_of_owner" {
		t.Errorf("the journal entry reads %v", e)
	}
}

// Red first: an owner's own answer carries no on_behalf_of, needs no reason,
// and is marked as the owner's. Today nothing is journaled for a refusal and
// nothing marks either.
func TestAnOwnersOwnAnswerCarriesNoOnBehalfOf(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, loc := f.owner1.post(t, f.apply, url.Values{"csrf": {f.owner1.csrf(t, "/board")}})
	if code != 303 {
		t.Fatalf("answered %d, want a redirect", code)
	}
	o := f.option(t)
	if o.State != crew.OptionApplied {
		t.Fatalf("an owner's own answer needed a reason or was refused: state %q (redirected to %s)", o.State, loc)
	}
	if o.OnBehalfOf != "" || o.BehalfReason != "" {
		t.Errorf("the owner's own answer carries on_behalf_of %q reason %q, want neither", o.OnBehalfOf, o.BehalfReason)
	}
	e := journalOf(t, f.h, "option_applied", f.artID)
	if e == nil {
		t.Fatal("no option_applied entry")
	}
	if _, has := e["on_behalf_of"]; has {
		t.Errorf("the owner's own answer journals on_behalf_of: %v", e)
	}
	if e["answered_as"] != "owner" || e["answered_by"] != "owner1" {
		t.Errorf("the entry reads %v, want answered_as owner and answered_by owner1", e)
	}
}

func TestAnOwnersOwnRefusalIsJournaledAsTheOwnersAndCarriesNoOnBehalfOf(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, _ := f.owner1.post(t, f.deny, url.Values{"csrf": {f.owner1.csrf(t, "/board")}, "reason": {"not this month"}})
	if code != 303 {
		t.Fatalf("answered %d, want a redirect", code)
	}
	if got := f.option(t).State; got != crew.OptionRefused {
		t.Fatalf("state %q, want refused", got)
	}
	e := journalOf(t, f.h, "option_refused", f.artID)
	if e == nil {
		t.Fatal("no option_refused entry in the journal for the owner's own refusal")
	}
	if _, has := e["on_behalf_of"]; has {
		t.Errorf("the owner's own refusal journals on_behalf_of: %v", e)
	}
	if e["answered_as"] != "owner" || e["reason"] != "not this month" {
		t.Errorf("the entry reads %v, want answered_as owner and the refusal's reason", e)
	}
}

// An admin who IS the owner of the request is the owner: no reason, no mark.
func TestAnAdminAnsweringTheirOwnDecisionIsTheOwner(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	art, ord, _ := plantCarriedOptionOfClass(t, h, "boss", "budget.set", "raise the budget")
	boss := h.as(t, "boss", "boss-password-2026")
	path := "/option/" + strconv.Itoa(art) + "/" + strconv.Itoa(ord) + "/apply"
	if code, _ := boss.post(t, path, url.Values{"csrf": {boss.csrf(t, "/board")}}); code != 303 {
		t.Fatalf("answered %d", code)
	}
	o, err := crew.GetOption(h.st.DB(), art, ord)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != crew.OptionApplied || o.OnBehalfOf != "" {
		t.Errorf("state %q on_behalf_of %q: an admin answering their own decision is the owner", o.State, o.OnBehalfOf)
	}
}

// Somebody who is neither the owner nor an admin is refused, reason or not.
func TestAnOperatorWhoIsNotTheOwnerIsStillRefusedWhateverReasonTheyGive(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, _ := f.rando.post(t, f.apply, url.Values{"csrf": {f.rando.csrf(t, "/board")}, "behalf_reason": {behalfReason}})
	if code != 303 {
		t.Fatalf("answered %d", code)
	}
	if got := f.option(t).State; got != crew.OptionCarried {
		t.Fatalf("an operator who is not the owner applied it with a reason: state %q", got)
	}
}

// Hostile reasons: over the cap, control characters, markup, and a megabyte.
func TestAnOnBehalfReasonIsCappedPlainAndEscaped(t *testing.T) {
	refused := map[string]string{
		"one byte over the cap":   strings.Repeat("a", 501),
		"a megabyte":              strings.Repeat("x", 1<<20),
		"a NUL byte":              "owner1 is away\x00 and so on",
		"an escape character":     "owner1 is away \x1b[2J",
		"invalid UTF-8":           "owner1 is away \xff\xfe",
		"a carriage return":       "owner1 is away\rboss approved it",
		"a line separator":        "owner1 is away\u2028and a second line",
		"a bidi override":         "owner1 is away \u202e",
		"only a zero-width space": "\u200b\u200b",
	}
	for name, reason := range refused {
		t.Run(name, func(t *testing.T) {
			f := newBehalfFixture(t, start(t))
			code, _ := f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {reason}})
			if code != 303 {
				t.Fatalf("answered %d", code)
			}
			if got := f.option(t).State; got != crew.OptionCarried {
				t.Fatalf("a reason with %s was accepted: state %q", name, got)
			}
		})
	}
	t.Run("exactly the cap is accepted", func(t *testing.T) {
		f := newBehalfFixture(t, start(t))
		reason := strings.Repeat("a", 500)
		f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {reason}})
		if got := f.option(t); got.State != crew.OptionApplied || got.BehalfReason != reason {
			t.Fatalf("a 500-byte reason was not accepted as given: state %q", got.State)
		}
	})
	t.Run("markup is stored as text and rendered as text", func(t *testing.T) {
		f := newBehalfFixture(t, start(t))
		hostile := `<script>alert(1)</script> & "quotes" 'single'`
		f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {hostile}})
		got := f.option(t)
		if got.State != crew.OptionApplied || got.BehalfReason != hostile {
			t.Fatalf("a reason with markup was altered or refused: %q, state %q", got.BehalfReason, got.State)
		}
		taskID, _ := crew.TaskOfArtifact(f.h.st.DB(), f.artID)
		for _, page := range []string{"/task/" + strconv.Itoa(taskID),
			"/sprint/" + strconv.Itoa(f.sprint) + "/decisions/owner1"} {
			code, body, _ := f.boss.get(t, page)
			if code != 200 {
				t.Fatalf("GET %s answered %d", page, code)
			}
			if strings.Contains(body, "<script>alert(1)</script>") {
				t.Errorf("GET %s renders the reason's markup unescaped", page)
			}
			if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
				t.Errorf("GET %s does not show the reason, escaped", page)
			}
		}
	})
}

// The form carries the reason field only when the viewer is answering for
// somebody else.
func TestTheReasonFieldAppearsOnlyWhenAnsweringForSomeoneElse(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	page := "/sprint/" + strconv.Itoa(f.sprint) + "/decisions/owner1"

	_, forAdmin, _ := f.boss.get(t, page)
	if !strings.Contains(forAdmin, `name="behalf_reason"`) {
		t.Error("an admin answering for owner1 is not offered the reason field")
	}
	if !strings.Contains(forAdmin, "on behalf of owner1") {
		t.Error("an admin answering for owner1 is not told the answer will be on owner1's behalf")
	}
	_, forOwner, _ := f.owner1.get(t, page)
	if strings.Contains(forOwner, `name="behalf_reason"`) {
		t.Error("the owner is offered a reason field for answering their own decision")
	}
	if !strings.Contains(forOwner, `name="csrf"`) {
		t.Error("the owner's own form lost its CSRF field")
	}
}

// The decision card says, once an answer is given, who gave it and for whom.
func TestTheDecisionCardNamesWhoAnsweredAndForWhom(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {behalfReason}})
	_, body, _ := f.boss.get(t, "/sprint/"+strconv.Itoa(f.sprint)+"/decisions/owner1")
	for _, want := range []string{"on behalf of owner owner1", "boss", behalfReason} {
		if !strings.Contains(body, want) {
			t.Errorf("the decision card does not say %q after an admin answered for the owner", want)
		}
	}

	g := newBehalfFixture(t, start(t))
	g.owner1.post(t, g.apply, url.Values{"csrf": {g.owner1.csrf(t, "/board")}})
	_, own, _ := g.owner1.get(t, "/sprint/"+strconv.Itoa(g.sprint)+"/decisions/owner1")
	if strings.Contains(own, "on behalf of owner") {
		t.Error("the card says an owner's own answer was on behalf of somebody")
	}
	if !strings.Contains(own, "answered by the owner") {
		t.Error("the card does not mark the owner's own answer as the owner's")
	}
}

// CSRF is still checked on an on-behalf answer.
func TestAnOnBehalfAnswerStillNeedsTheCSRFToken(t *testing.T) {
	f := newBehalfFixture(t, start(t))
	code, _ := f.boss.post(t, f.apply, url.Values{"csrf": {"not-the-token"}, "behalf_reason": {behalfReason}})
	if code != 303 {
		t.Fatalf("answered %d", code)
	}
	if got := f.option(t).State; got != crew.OptionCarried {
		t.Fatalf("an on-behalf answer with a wrong CSRF token was accepted: state %q", got)
	}
	code, _ = f.boss.post(t, f.apply, url.Values{"behalf_reason": {behalfReason}})
	if got := f.option(t).State; got != crew.OptionCarried {
		t.Fatalf("an on-behalf answer with no CSRF token was accepted (%d): state %q", code, got)
	}
}

// On a console teed onto the bus, both events reach the bus: the emitter
// refuses a severity outside its enum, so a line in the file is proof that the
// severity was valid, and the payload carries the on-behalf marking.
func TestBothAnswerEventsReachTheBusWithTheirMarking(t *testing.T) {
	h, events := startOnTheBus(t)
	f := newBehalfFixture(t, h)
	f.boss.post(t, f.apply, url.Values{"csrf": {f.boss.csrf(t, "/board")}, "behalf_reason": {behalfReason}})

	art2, ord2, _ := plantCarriedOptionOfClass(t, h, "owner1", "purchase", "buy the reservation")
	path := "/option/" + strconv.Itoa(art2) + "/" + strconv.Itoa(ord2) + "/refuse"
	f.boss.post(t, path, url.Values{
		"csrf": {f.boss.csrf(t, "/board")}, "reason": {"not now"}, "behalf_reason": {behalfReason},
	})

	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev struct {
			Type     string         `json:"type"`
			Severity string         `json:"severity"`
			Data     map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "option_applied" || ev.Type == "option_refused" {
			ev.Data["_severity"] = ev.Severity
			seen[ev.Type] = ev.Data
		}
	}
	for kind, sev := range map[string]string{"option_applied": "info", "option_refused": "low"} {
		d, ok := seen[kind]
		if !ok {
			t.Errorf("no %s event reached the bus", kind)
			continue
		}
		if d["_severity"] != sev {
			t.Errorf("%s reached the bus with severity %v, want %s", kind, d["_severity"], sev)
		}
		if d["on_behalf_of"] != "owner1" || d["on_behalf_reason"] != behalfReason || d["answered_by"] != "boss" {
			t.Errorf("%s on the bus reads %v, want on_behalf_of owner1, the reason and answered_by boss", kind, d)
		}
	}
}
