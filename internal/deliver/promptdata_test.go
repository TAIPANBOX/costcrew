package deliver

// The policy's own behaviour, below the gate: the closed vocabulary, the key,
// the pseudonyms (stable, readable, reversible, collision-free), what the
// scrub does and does not touch, the prompt's one-line statement of the mode,
// the plan packet, and the re-identification of an answer.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/promptfixture"
)

// ------------------------------------------------------ the closed vocabulary

func TestPromptDataIsAClosedVocabulary(t *testing.T) {
	for _, ok := range []string{"full", "masked", "aggregates"} {
		got, err := ParsePromptData(ok)
		if err != nil || string(got) != ok {
			t.Errorf("ParsePromptData(%q) = %q, %v; want it accepted as itself", ok, got, err)
		}
	}
	// A typo must refuse to start, never fall back to full: a data-minimising
	// setting that quietly turns itself off on a misspelling is worse than none.
	for _, bad := range []string{
		"", " ", "mask", "maskd", "Masked", "MASKED", " masked", "masked ", "masked\n", "aggregate",
		"aggregates,full", "off", "none", "true", "1", "full\x00", "ful", "fulll", "masked;full",
	} {
		got, err := ParsePromptData(bad)
		if err == nil {
			t.Errorf("ParsePromptData(%q) = %q, nil; want a refusal", bad, got)
			continue
		}
		if !strings.Contains(err.Error(), "full") || !strings.Contains(err.Error(), "masked") ||
			!strings.Contains(err.Error(), "aggregates") {
			t.Errorf("the refusal for %q does not name the three modes it would have accepted: %v", bad, err)
		}
	}
}

func TestThePromptDataEnvironmentVariableBacksTheFlagDefault(t *testing.T) {
	t.Setenv(PromptDataEnv, "")
	if got := PromptDataEnvDefault(); got != "full" {
		t.Errorf("with nothing set the default is %q, want full", got)
	}
	t.Setenv(PromptDataEnv, "masked")
	if got := PromptDataEnvDefault(); got != "masked" {
		t.Errorf("COSTCREW_PROMPT_DATA=masked gave default %q", got)
	}
	// The environment is read as written: a typo in it reaches the same parser
	// as a typo on the command line and is refused there.
	t.Setenv(PromptDataEnv, "maskd")
	if _, err := ParsePromptData(PromptDataEnvDefault()); err == nil {
		t.Error("a typo in COSTCREW_PROMPT_DATA was accepted")
	}
}

func TestConfiguringATypoLeavesTheActivePolicyAlone(t *testing.T) {
	before := ActivePolicy()
	if _, err := ConfigurePromptData("maskd", t.TempDir()); err == nil {
		t.Fatal("a typo configured a policy")
	}
	if ActivePolicy() != before {
		t.Error("a refused configuration still replaced the active policy")
	}
}

// -------------------------------------------------------------------- the key

func TestTheKeyLivesInTheDataDirWithMode0600(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewPolicy(PromptMasked, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, KeyFileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no key file at %s: %v", path, err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("the key file is mode %v, want 0600", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if len(strings.TrimSpace(string(raw))) != 64 {
		t.Errorf("the key file holds %d characters, want 64 hex (32 random bytes)", len(strings.TrimSpace(string(raw))))
	}
}

func TestFullModeNeedsNoKeyAndWritesNone(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewPolicy(PromptFull, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFileName)); err == nil {
		t.Error("full mode wrote a key file it has no use for")
	}
}

func TestTheSameKeyGivesTheSameTokensAcrossRunsAndAnotherKeyDoesNot(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	es := []entry{{kindTeam, "ml-platform"}, {kindService, "Amazon EC2"}}
	tok := func(dir string) string {
		p, err := NewPolicy(PromptMasked, dir)
		if err != nil {
			t.Fatal(err)
		}
		return p.maskWith("ml-platform / Amazon EC2", es, nil)
	}
	first, second, other := tok(dirA), tok(dirA), tok(dirB)
	if first != second {
		t.Errorf("one installation's key gave two different pseudonyms across runs: %q then %q", first, second)
	}
	if first == "ml-platform / Amazon EC2" {
		t.Fatalf("nothing was masked: %q", first)
	}
	if first == other {
		t.Errorf("two installations with different keys gave the same pseudonyms: %q", first)
	}
}

func TestACorruptKeyFileRefusesRatherThanRotating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyFileName)
	for _, bad := range []string{"", "not hex at all", strings.Repeat("a", 63), strings.Repeat("g", 64), strings.Repeat("a", 65)} {
		if err := os.WriteFile(path, []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPolicy(PromptMasked, dir); err == nil {
			t.Errorf("a key file holding %q was accepted; regenerating it silently would change every pseudonym", bad)
		}
		if got, _ := os.ReadFile(path); string(got) != bad+"\n" {
			t.Errorf("a refused key file was overwritten (%q)", got)
		}
	}
}

func TestAKeyFileOthersCanReadIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewPolicy(PromptMasked, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, KeyFileName)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicy(PromptMasked, dir); err == nil {
		t.Error("a world-readable key file was accepted: whoever read it can test any name against the tokens")
	}
}

func TestTwoStartsAtOnceShareOneKey(t *testing.T) {
	dir := t.TempDir()
	done := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			p, err := NewPolicy(PromptMasked, dir)
			if err != nil {
				done <- "error: " + err.Error()
				return
			}
			done <- p.maskWith("ml-platform", []entry{{kindTeam, "ml-platform"}}, nil)
		}()
	}
	var got []string
	for i := 0; i < 8; i++ {
		got = append(got, <-done)
	}
	for _, g := range got {
		if g != got[0] || strings.HasPrefix(g, "error") {
			t.Fatalf("concurrent first starts disagreed about the key: %q", got)
		}
	}
}

// ------------------------------------------------------------- the pseudonyms

var tokenShape = regexp.MustCompile(`^(team|desk|svc|agent|user|inv|vendor|product|cmt|res|model|run|host)-[0-9a-f]{4,}$`)

func TestATokenIsReadableAndShaped(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	for _, c := range []struct {
		k    kindName
		want string
	}{
		{kindTeam, "team-"}, {kindDesk, "desk-"}, {kindService, "svc-"}, {kindAgent, "agent-"},
		{kindUser, "user-"}, {kindInvoice, "inv-"}, {kindVendor, "vendor-"}, {kindProduct, "product-"},
		{kindCommitment, "cmt-"}, {kindResource, "res-"}, {kindModel, "model-"}, {kindRun, "run-"},
	} {
		name := "zzname " + string(c.k)
		got := p.maskWith(name, []entry{{c.k, name}}, nil)
		if !tokenShape.MatchString(got) || !strings.HasPrefix(got, c.want) {
			t.Errorf("kind %s masked to %q; want %s followed by 4 or more hex digits", c.k, got, c.want)
		}
		if strings.Contains(got, "zzname") {
			t.Errorf("the token %q still contains the name", got)
		}
		// a name is one token whichever kind asks: the first kind to claim it names it

	}
}

func TestTheSameNameIsTheSameTokenInEveryRoundAndEveryText(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindTeam, "ml-platform"}, {kindTeam, "research"}, {kindService, "GKE"}}
	a := p.maskWith("ml-platform spent more", es, nil)
	b := p.maskWith("round two: GKE, ml-platform, research", es, nil)
	tokA := strings.Fields(a)[0]
	if !strings.Contains(b, tokA) {
		t.Errorf("ml-platform was %q in one round and not in the next: %q", tokA, b)
	}
	// And it holds when the dictionary has changed in between: the pseudonym
	// handed out is not given up because a later read of the store is smaller.
	c := p.maskWith("ml-platform", es[:1], nil)
	if c != tokA {
		t.Errorf("a smaller dictionary gave ml-platform %q instead of %q", c, tokA)
	}
}

func TestTokensNeverCollideEvenWhenThereAreMoreNamesThanFourHexDigitsHold(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	var es []entry
	var names []string
	for i := 0; i < 20000; i++ {
		n := "team-name-" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + strings.Repeat("7", i/26%9) + string(rune('A'+i/700%26)) + itoa(i)
		es = append(es, entry{kindTeam, n})
		names = append(names, n)
	}
	// Mask every name on its own line and require 20000 different tokens, each
	// of which comes back as exactly its own name.
	text := strings.Join(names, "\n")
	masked := p.maskWith(text, es, nil)
	lines := strings.Split(masked, "\n")
	if len(lines) != len(names) {
		t.Fatalf("masking changed the number of lines: %d -> %d", len(names), len(lines))
	}
	seen := map[string]int{}
	for i, l := range lines {
		if !tokenShape.MatchString(l) {
			t.Fatalf("name %d masked to %q, which is not a token", i, l)
		}
		if j, dup := seen[l]; dup {
			t.Fatalf("names %q and %q were given the same token %s", names[j], names[i], l)
		}
		seen[l] = i
	}
	if back := p.Reidentify(masked); back != text {
		t.Error("20000 masked names did not come back as themselves")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// ---------------------------------------------------------- what the scrub touches

func TestMaskingMatchesWholeNamesOnly(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindDesk, "aws"}, {kindTeam, "ml-platform"}, {kindService, "Amazon EC2"}, {kindService, "S3"}}
	in := "aws laws; ml-platform-prod, ml-platform_x; Amazon EC2s and Amazon EC2; S3 and S3x; AWS; aws—ml-platform."
	got := p.maskWith(in, es, nil)
	for _, want := range []string{"laws;", "Amazon EC2s", "S3x", "AWS;"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was changed though it only contains a name as part of a longer word: %q", want, got)
		}
	}
	for _, gone := range []string{"aws laws", "ml-platform-prod", "ml-platform_x", "and Amazon EC2;", "S3 and", "aws—ml-platform"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived masking: %q", gone, got)
		}
	}
	if strings.Count(got, "desk-") != 2 || strings.Count(got, "team-") != 3 {
		t.Errorf("expected 2 desk tokens and 3 team tokens (a name beside - _ or a dash is still the name): %q", got)
	}
}

func TestTheLongestNameWins(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindService, "Amazon"}, {kindService, "Amazon EC2"}, {kindService, "Amazon EC2 Container"}}
	got := p.maskWith("Amazon EC2 Container and Amazon EC2 and Amazon", es, nil)
	if n := len(tokenRe.FindAllString(got, -1)); n != 3 {
		t.Errorf("want three tokens for three names, got %d: %q", n, got)
	}
	if strings.Contains(got, "Amazon") {
		t.Errorf("a prefix name was masked on its own and left the rest of the longer name behind: %q", got)
	}
}

func TestAnInvoiceNumberDoesNotEatAnAmount(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindInvoice, "1042"}, {kindInvoice, "INV-2026-0007"}}
	got := p.maskWith("billed 1,042.50 and 1042.50 on invoice 1042. See INV-2026-0007, 21042.", es, nil)
	for _, keep := range []string{"1,042.50", "1042.50", "21042"} {
		if !strings.Contains(got, keep) {
			t.Errorf("the amount %q was damaged by masking an invoice number: %q", keep, got)
		}
	}
	if strings.Contains(got, "invoice 1042.") || strings.Contains(got, "INV-2026-0007") {
		t.Errorf("an invoice number survived: %q", got)
	}
}

func TestTheWorkingAnalystKeepsItsOwnNameAndNothingInsideItIsMasked(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindDesk, "aws"}, {kindAgent, "investigator-aws"}, {kindAgent, "triage-aws"}}
	got := p.maskWith("You are investigator-aws on the aws desk; triage-aws also.", es, []string{"investigator-aws"})
	if !strings.Contains(got, "You are investigator-aws on") {
		t.Errorf("the working analyst lost its own name: %q", got)
	}
	if strings.Contains(got, "triage-aws") || strings.Contains(got, "the aws desk") {
		t.Errorf("another analyst or the desk survived: %q", got)
	}
}

func TestTinyAndEmptyValuesAreNeverMasked(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{{kindTeam, ""}, {kindTeam, " "}, {kindTeam, "a"}, {kindService, "-"}, {kindUser, "supervisor"}}
	in := "a - supervisor a-b"
	if got := p.maskWith(in, es, nil); got != in {
		t.Errorf("values that name nothing changed the text: %q", got)
	}
}

// ------------------------------------------------------------ re-identification

func TestReidentifyBringsBackExactlyTheNamesThatWereMasked(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	es := []entry{
		{kindTeam, "ml-platform"}, {kindService, "Amazon EC2"}, {kindUser, "alice"},
		{kindInvoice, "INV-2026-07-00417"}, {kindAgent, "agent://taipanbox.dev/costcrew/triage-aws"},
	}
	in := "Alice's team ml-platform spent on Amazon EC2 (INV-2026-07-00417), as alice said via agent://taipanbox.dev/costcrew/triage-aws."
	masked := p.maskWith(in, es, nil)
	if masked == in {
		t.Fatal("nothing was masked")
	}
	if back := p.Reidentify(masked); back != in {
		t.Errorf("the round trip changed the text.\n in: %q\nout: %q\n(masked: %q)", in, back, masked)
	}
}

func TestATokenTheModelInventedStaysAsWritten(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	_ = p.maskWith("ml-platform", []entry{{kindTeam, "ml-platform"}}, nil)
	for _, in := range []string{
		"team-0000 and svc-ffff and agent-1234", "team-7f3a1", "xteam-7f3a", "team-", "team-zzzz", "user-abcd",
	} {
		if got := p.Reidentify(in); got != in {
			// the one case that may legitimately change is a token that happens
			// to be real; none of these is one
			t.Errorf("Reidentify(%q) = %q; a token that maps to nothing must stay as written", in, got)
		}
	}
}

func TestReidentifyFindsATokenInsideMarkdownAndPunctuation(t *testing.T) {
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	masked := p.maskWith("ml-platform", []entry{{kindTeam, "ml-platform"}}, nil)
	for _, wrap := range []string{"**%s**", "`%s`", "(%s)", "%s's", "## %s", "- %s,", "\"%s\"", "%s."} {
		in := strings.Replace(wrap, "%s", masked, 1)
		want := strings.Replace(wrap, "%s", "ml-platform", 1)
		if got := p.Reidentify(in); got != want {
			t.Errorf("Reidentify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := p.Reidentify(strings.ToUpper(masked)); got != "ml-platform" {
		t.Errorf("a token the model upper-cased was not recognised: %q", got)
	}
}

func TestFullModeReidentifiesNothing(t *testing.T) {
	p, _ := NewPolicy(PromptFull, t.TempDir())
	for _, s := range []string{"team-7f3a", "anything at all", ""} {
		if got := p.Reidentify(s); got != s {
			t.Errorf("full mode changed %q to %q", s, got)
		}
		if got := p.MaskText(s); got != s {
			t.Errorf("full mode masked %q to %q", s, got)
		}
	}
}

// ---------------------------------------------------------- the prompt and the plan

func TestThePromptSaysWhichModeItWasBuiltUnder(t *testing.T) {
	task := crew.Task{Title: "Explain it", Goal: "say what happened"}
	a := crew.Analyst{Name: "investigator-aws", Role: "Investigator", Desk: "aws", State: "active"}
	for _, mode := range []PromptData{PromptFull, PromptMasked, PromptAggregates} {
		p, _ := NewPolicy(mode, t.TempDir())
		restore := SetActivePolicy(p)
		got := Prompt(task, a, "2026-09-07", "")
		restore()
		line := "Prompt data policy: " + string(mode) + "."
		if strings.Count(got, "Prompt data policy:") != 1 || !strings.Contains(got, line) {
			t.Errorf("the %s prompt does not state its mode exactly once as %q:\n%s", mode, line, got)
		}
	}
}

func TestTheOperatorsGoalAndATaskGoalAreWithheldUnderMaskedAndAggregates(t *testing.T) {
	in := newInstallation(t)
	task := crew.Task{Title: "Explain the move", Goal: promptfixture.OperatorGoalText + " for the ml-platform team"}
	a := crew.Analyst{Name: "investigator-aws", Role: "Investigator", Desk: "aws", State: "active"}

	in.policy(t, PromptFull)
	if full := Prompt(task, a, "2026-09-07", ""); !strings.Contains(full, promptfixture.OperatorGoalText) {
		t.Fatal("full mode no longer sends the task's goal")
	}
	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		in.policy(t, mode)
		got := Prompt(task, a, "2026-09-07", "")
		if strings.Contains(got, promptfixture.OperatorGoalText) || strings.Contains(got, "ml-platform") {
			t.Errorf("%s prompt sent the typed goal:\n%s", mode, got)
		}
		if !strings.Contains(got, WithheldFreeText) {
			t.Errorf("%s prompt withheld the goal without saying so:\n%s", mode, got)
		}
	}
}

func TestThePromptMasksTheTaskTitleAndKeepsTheDateAndTheAnalystsOwnPersona(t *testing.T) {
	in := newInstallation(t)
	in.policy(t, PromptMasked)
	task := crew.Task{Title: "Explain the Amazon EC2 move on 2026-07-14"}
	a := crew.Analyst{Name: "reporter-aws", Role: "Reporter (aws desk)", Desk: "aws", State: "active"}
	got := Prompt(task, a, "2026-09-07", "")
	if strings.Contains(got, "Amazon EC2") {
		t.Errorf("the service in the task title was sent:\n%s", got)
	}
	if !strings.Contains(got, "2026-07-14") || !strings.Contains(got, "Today is 2026-09-07.") {
		t.Errorf("a date was lost:\n%s", got)
	}
	if !strings.Contains(got, "You are reporter-aws, ") {
		t.Errorf("the working analyst lost its own name:\n%s", got)
	}
	// The analyst is told who it is: name, role and desk, as it was hired.
	if !strings.Contains(got, "You are reporter-aws, Reporter (aws desk) on the aws desk of a FinOps practice.") {
		t.Errorf("the working analyst's own persona was changed:\n%s", got)
	}
}

func TestThePlanPacketLeaksNoIdentifierOrGoalUnderMaskedAndAggregates(t *testing.T) {
	in := newInstallation(t)
	det, err := crew.Propose(in.db, "Sprint 2026-09-07", "2026-09-07", "2026-09-20",
		promptfixture.OperatorGoalText+" for the ml-platform team")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Items) < 5 {
		t.Fatalf("the plan has only %d items; the gate would measure too little", len(det.Items))
	}
	spent, _ := crew.SpendInMonth(in.db, "2026-09")
	var sup crew.Analyst
	for _, a := range in.roster {
		if a.Name == "supervisor" {
			sup = a
		}
	}

	in.policy(t, PromptFull)
	full := PlanPacket(in.db, det, in.roster, spent)
	if !strings.Contains(full, promptfixture.OperatorGoalText) || len(in.leaks(full, "supervisor")) == 0 {
		t.Fatal("full mode's plan packet carries neither the goal nor any identifier; the test cannot tell")
	}

	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		in.policy(t, mode)
		packet := PlanPacket(in.db, det, in.roster, spent)
		prompt := PlanPrompt(sup, packet)
		// The supervisor's job description is the same words in every
		// installation (and uses "renewals" and "commitments", which are also
		// the names of two analysts); it is not data and is not looked at here.
		prompt = strings.Replace(prompt, JobDescriptionBlock("supervisor", "management"), "", 1)
		if l := in.leaks(prompt, "supervisor"); len(l) > 0 {
			t.Errorf("%s: the plan prompt leaks %s\n%s", mode, strings.Join(first(l, 6), "; "), prompt)
		}
		if !strings.Contains(prompt, WithheldFreeText) {
			t.Errorf("%s: the goal was withheld without saying so", mode)
		}
		if len(packet) > packetMaxBytes {
			t.Errorf("%s: the plan packet is %d bytes, over the cap", mode, len(packet))
		}
		if !strings.Contains(prompt, "Prompt data policy: "+string(mode)+".") {
			t.Errorf("%s: the plan prompt does not state its mode", mode)
		}
	}

	// aggregates names no agent and no service at all
	in.policy(t, PromptAggregates)
	agg := PlanPacket(in.db, det, in.roster, spent)
	if tok := regexp.MustCompile(`\b(svc|agent|user)-[0-9a-f]{4,}\b`).FindString(agg); tok != "" {
		t.Errorf("the aggregates plan packet carries the token %q:\n%s", tok, agg)
	}
}

// What the console does with a masked plan answer: it asks crew.ValidatePlanAnswer
// about the assignee. The answer must be re-identified before then, by the one
// door every answer comes back through.
func TestAPlanAnswerNamingTokensIsAcceptedOnlyOnceItIsReidentified(t *testing.T) {
	in := newInstallation(t)
	det, err := crew.Propose(in.db, "Sprint 2026-09-07", "2026-09-07", "2026-09-20", "")
	if err != nil {
		t.Fatal(err)
	}
	spent, _ := crew.SpendInMonth(in.db, "2026-09")
	pol := in.policy(t, PromptMasked)

	// an item routed by skill, and another holder of that skill on its desk
	var item crew.PlanItem
	var ref int
	for i, it := range det.Items {
		if it.Skill != "" && it.Assignee != "" {
			item, ref = it, i+1
			break
		}
	}
	if ref == 0 {
		t.Skip("no skill-routed item in the plan")
	}
	var other string
	for _, a := range in.roster {
		if a.Name != item.Assignee && a.Desk == item.Desk && a.State == "active" && containsStr(a.Skills, item.Skill) {
			other = a.Name
		}
	}
	if other == "" {
		t.Skip("no second holder of the skill on the item's desk")
	}
	token := pol.MaskText(other)
	if token == other {
		t.Fatalf("%q was not masked", other)
	}
	answer := "```plan\n{\"items\": [{\"ref\": " + itoa(ref) + ", \"assignee\": \"" + token +
		"\", \"budget_cents\": 100, \"why\": \"a lighter hand\"}]}\n```"

	if _, found, reason := crew.ValidatePlanAnswer(answer, det, in.roster, spent); !found || reason == "" {
		t.Errorf("the console accepted a token as an analyst's name (found=%v, reason=%q): the test's premise is wrong", found, reason)
	}
	if _, found, reason := crew.ValidatePlanAnswer(pol.Reidentify(answer), det, in.roster, spent); !found || reason != "" {
		t.Errorf("the re-identified answer was refused: found=%v reason=%q", found, reason)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Every answer comes back through Call; Call hands the caller real names.
func TestCallHandsBackTheAnswerWithItsRealNames(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	in := newInstallation(t)
	pol := in.policy(t, PromptMasked)
	token := pol.MaskText("ml-platform")
	if token == "ml-platform" {
		t.Fatal("ml-platform was not masked")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"## ` + token + ` overspent\nask ` + token + `"}],` +
			`"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	res, err := Call(context.Background(), "anthropic", "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "r1", AgentID: "agent://x/y", OnBehalfOf: testChain, BudgetUSD: "1.00"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "## ml-platform overspent\nask ml-platform" {
		t.Errorf("Call returned %q; the caller must see the real name", res.Text)
	}
}

func TestCallInFullModeReturnsTheAnswerUntouched(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	p, _ := NewPolicy(PromptFull, t.TempDir())
	defer SetActivePolicy(p)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"team-7f3a is a name"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	res, err := Call(context.Background(), "anthropic", "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "r1", AgentID: "agent://x/y", OnBehalfOf: testChain, BudgetUSD: "1.00"})
	if err != nil || res.Text != "team-7f3a is a name" {
		t.Errorf("got %q, %v", res.Text, err)
	}
}
