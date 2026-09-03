package crew

// The options block: an analyst's deliverable ends in a machine-readable
// list of OPTIONS, never an action. B3-SPEC.md sections 1 and 2.
//
// `@yurii 2026-09-02`: "він має давати на вибір якісь певні рішення, які він
// вважає за потрібне спочатку супервайзеру, тобто головному агенту, а вже
// той має запитувати юзера, користувача, власника цих агентів, що робити
// далі."
//
// An analyst's Post has never applied anything (crew.go's own comment on
// Post says so). What changes here is that the deliverable's PROSE now also
// carries a closed, checked list of what a person could do about it: each
// option names a decision class from internal/crew/roles.yaml, and the class
// has to be one the writing role's own job description lists under
// decides_alone or hands_up -- the same closed vocabulary
// tools/run/mandate.go's jobDescriptionBlock already shows the model, so
// there is one source for what a role may say and what this file will
// accept. A class outside that list is refused whole: nothing is written to
// artifact_options, and the caller (tools/run/live.go's saveDraft) returns
// the task with the reason.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// OptionState is where one option of a deliverable has got to.
type OptionState string

const (
	OptionOpen      OptionState = "open"       // saved, nobody has looked
	OptionCarried   OptionState = "carried"    // handed to an owner in a decision request
	OptionApplied   OptionState = "applied"    // a stamp (the supervisor's or an owner's) applied it
	OptionRefused   OptionState = "refused"    // a stamp refused it, with a reason
	OptionNotChosen OptionState = "not_chosen" // its deliverable's choice went the other way
)

// Option is one row of artifact_options: one course of action an analyst's
// deliverable named, never one it took.
type Option struct {
	Artifact    int
	Ordinal     int
	Class       string
	Summary     string
	FigureCents int64
	SavingCents int64
	Risk        string
	Needs       string
	Evidence    []string
	// Target is a class-specific structured target, carried verbatim as the
	// raw JSON object the deliverable's options block named -- empty for
	// every class but allocation.rule (C2-SPEC.md section 2) and
	// driver.recurring and driver.one-time (DRIVER-WINDOW-SPEC.md section 2;
	// shape and name reused from allocation.rule's own target so the
	// classes' own validators sit beside each other rather than duplicate
	// this field). Kept generic here rather than typed ({rule_id, method,
	// share} or {start, end}) because those types belong to internal/finops,
	// which already imports this package: the reverse import would cycle.
	// internal/finops.applySideEffect decodes each with its own local type
	// when it actually calls SetRule or applies a driver.
	Target    json.RawMessage
	State     OptionState
	DecidedBy string
	DecidedAt string
	Reason    string
}

// Recorder used below is guard.go's: this package already declares it, the
// same interface anomaly.Recorder and store.Recorder are, restated in each
// package rather than imported so the dependency graph stays what it is.

// optionsBlockMaxBytes bounds the fenced block itself, the same way
// tools/run/packet.go bounds the packet: a number a hostile 1 MB block is
// checked against before anything tries to parse it.
const optionsBlockMaxBytes = 64 * 1024

// optionsMax is "one to three options" (B3-SPEC.md section 2).
const optionsMax = 3

// optionsFence finds the LAST ```options ... ``` block in a deliverable: the
// spec places it "at the end of the deliverable", and taking the last match
// rather than the first means a model that echoes the shape earlier in its
// prose (explaining the format to itself) does not get read as the block.
var optionsFence = regexp.MustCompile("(?s)```options[ \\t]*\\r?\\n(.*?)```")

// rawOption is the wire shape a model writes. FigureCents and SavingCents are
// json.Number rather than int64 or float64: encoding/json refuses a JSON
// string into a json.Number field (catching "a string where an integer
// goes") and Number.Int64() refuses anything with a decimal point or
// exponent (catching a non-integer figure), where int64 would have accepted
// a JSON string that merely looked numeric and float64 would have silently
// rounded 123.6 instead of refusing it.
type rawOption struct {
	Class       string          `json:"class"`
	Summary     string          `json:"summary"`
	FigureCents json.Number     `json:"figure_cents"`
	SavingCents json.Number     `json:"saving_cents"`
	Risk        string          `json:"risk"`
	Needs       string          `json:"needs"`
	Evidence    []string        `json:"evidence"`
	Target      json.RawMessage `json:"target"`
}

// ParseOptions extracts and structurally validates the trailing options
// block. found is false when no fenced ```options block exists at all, which
// is not itself a refusal: whether a deliverable may end in zero options
// depends on the writing role (AllowsNoOptions) and is the caller's call,
// not this function's. reason is non-empty exactly when the block that WAS
// found is malformed in some way this function can catch on its own, before
// any class is checked against a role: not valid JSON, too large, too many
// options, or a figure that is not a whole number of cents.
//
// It does not check classes against a role at all -- ValidateAndSaveOptions
// does that once it knows which role wrote the deliverable, because this
// function has no role to check against and must not guess one.
func ParseOptions(body string) (opts []Option, found bool, reason string) {
	m := optionsFence.FindStringSubmatch(body)
	if m == nil {
		return nil, false, ""
	}
	found = true
	raw := m[1]
	if len(raw) > optionsBlockMaxBytes {
		return nil, found, fmt.Sprintf(
			"the options block is %d bytes, over the %d byte limit", len(raw), optionsBlockMaxBytes)
	}

	var block struct {
		Options []rawOption `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		return nil, found, "the options block is not valid JSON: " + err.Error()
	}
	if len(block.Options) > optionsMax {
		return nil, found, fmt.Sprintf(
			"%d options, at most %d allowed", len(block.Options), optionsMax)
	}

	out := make([]Option, 0, len(block.Options))
	for i, r := range block.Options {
		if strings.TrimSpace(r.Class) == "" {
			return nil, found, fmt.Sprintf("option %d names no class", i+1)
		}
		figure, err := centsOf(r.FigureCents)
		if err != nil {
			return nil, found, fmt.Sprintf("option %d's figure_cents %v", i+1, err)
		}
		if figure < 0 {
			return nil, found, fmt.Sprintf("option %d's figure_cents is negative", i+1)
		}
		saving, err := centsOf(r.SavingCents)
		if err != nil {
			return nil, found, fmt.Sprintf("option %d's saving_cents %v", i+1, err)
		}
		out = append(out, Option{
			Ordinal:     i + 1,
			Class:       strings.TrimSpace(r.Class),
			Summary:     r.Summary,
			FigureCents: figure,
			SavingCents: saving,
			Risk:        r.Risk,
			Needs:       r.Needs,
			Evidence:    append([]string(nil), r.Evidence...),
			Target:      normalizedTarget(r.Target),
			State:       OptionOpen,
		})
	}
	return out, found, ""
}

// centsOf reads a json.Number as a whole number of cents. An absent field
// decodes as the empty json.Number and is zero, which is a legal value
// ("saving_cents": 0 is the spec's own example for an option with no
// saving).
func centsOf(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("must be a whole number of cents, not %q", n.String())
	}
	return v, nil
}

// normalizedTarget treats an absent field and a literal JSON null the same
// way: both mean "no target", and a caller checking len(Target) == 0 should
// not have to also know that json.RawMessage("null") is non-empty bytes.
func normalizedTarget(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	return raw
}

// allocationRuleTarget is allocation.rule's own structured target
// (C2-SPEC.md section 2), decoded generically here rather than with
// internal/finops's Method type: internal/finops already imports this
// package (apply.go), so the reverse import would cycle. Method is checked
// here only for being present at all; the specific set of valid method
// strings is finops.SetRule's own switch, checked when the option is
// actually applied, the same way an unknown rule id is (see this file's
// validateAllocationRuleTarget comment).
type allocationRuleTarget struct {
	RuleID json.Number `json:"rule_id"`
	Method string      `json:"method"`
	Share  *float64    `json:"share"`
}

// targetMaxBytes bounds a class-specific target sub-object on its own terms,
// the same way optionsBlockMaxBytes bounds the whole block: a 1 MB target is
// already refused by that outer cap before it ever reaches a class's own
// validator (a target cannot exceed the block it sits inside), but this
// gives the reason its own name rather than a generic "block too large" when
// the target itself is what bloated it.
const targetMaxBytes = 4 * 1024

// validateAllocationRuleTarget is C2-SPEC.md section 2's save-time gate for
// allocation.rule alone: "absent target refused with the reason". Only
// structural and range checks live here -- whether rule_id actually names a
// rule this practice has, and whether method is one finops.SetRule accepts,
// both need internal/finops and are refused there instead, when the option
// is actually applied (the class already degrades gracefully to a no-op
// when it has nothing to act on, the same "recorded only" shape every other
// unwired class has, so a target this function passed but finops later
// cannot use fails loudly at apply time rather than silently at save time).
func validateAllocationRuleTarget(raw json.RawMessage) (reason string) {
	if len(raw) > targetMaxBytes {
		return fmt.Sprintf("allocation.rule's target is %d bytes, over the %d byte limit",
			len(raw), targetMaxBytes)
	}
	if len(raw) == 0 {
		return "allocation.rule needs a target naming the rule and the method " +
			`("target": {"rule_id": ..., "method": ..., "share": ...}); none was given`
	}
	var tgt allocationRuleTarget
	if err := json.Unmarshal(raw, &tgt); err != nil {
		return "allocation.rule's target is not a valid JSON object: " + err.Error()
	}
	id, err := tgt.RuleID.Int64()
	if err != nil || id <= 0 {
		return fmt.Sprintf("allocation.rule's target.rule_id %q is not a positive whole number",
			tgt.RuleID.String())
	}
	if strings.TrimSpace(tgt.Method) == "" {
		return "allocation.rule's target names no method"
	}
	if tgt.Share != nil && (*tgt.Share < 0 || *tgt.Share > 1) {
		return fmt.Sprintf("allocation.rule's target.share %v is not between 0 and 1", *tgt.Share)
	}
	return ""
}

// driverTarget is driver.recurring's and driver.one-time's own structured
// target (DRIVER-WINDOW-SPEC.md section 2): the window during which a
// recurring rhythm is expected, or the day a one-time event covers when its
// own task carries no anomaly to take the day from instead.
//
// internal/detect.Driver.Covers has no periodicity column anywhere -- the
// window IS the extent of the rhythm -- so a fact this shape was not given
// is refused rather than guessed, the same rule allocationRuleTarget above
// already holds for a rule id this store does not have.
type driverTarget struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// driverTargetMaxWindowDays is DRIVER-WINDOW-SPEC.md section 2's own bound:
// "start at most 366 days before end". A recurring driver with no end at all
// would hide a service from the detector for good, so an open-ended window is
// refused rather than defaulted; the day bound is what keeps "not literally
// open-ended" from being satisfied by a window a thousand years long, which
// would be the same invented number by another shape.
const driverTargetMaxWindowDays = 366

// validateDriverTarget is DRIVER-WINDOW-SPEC.md section 2's save-time gate
// for driver.recurring and driver.one-time, beside validateAllocationRuleTarget
// above and following its shape: absent target refused with the reason,
// present and malformed refused with the reason.
//
// hasAnomaly is whether the task this option's own artifact belongs to came
// from an anomaly: driver.one-time on such a task takes the anomaly's own
// day (section 2, "that day IS the driver, nothing to ask") and may omit the
// target entirely. driver.recurring always needs one; there is no anomaly
// day for a rhythm to borrow.
//
// `@claude` 2026-09-03: the spec's own hostile-input list names one case as
// "a target on a class that takes none" without saying which of the two
// classes that is. Read here as the one state within this function's own
// scope that never needs a target -- driver.one-time WITH an anomaly -- so a
// target volunteered there is refused rather than silently dropped: silently
// accepting JSON this console will never read is the same shape of mistake
// as inventing a number nobody gave, just in the other direction, and
// applyDriver (internal/finops/apply.go) enforces the same rule again at
// apply time for an option that reached it by bypassing this gate.
func validateDriverTarget(class string, raw json.RawMessage, hasAnomaly bool) (reason string) {
	if len(raw) > targetMaxBytes {
		return fmt.Sprintf("%s's target is %d bytes, over the %d byte limit",
			class, len(raw), targetMaxBytes)
	}
	optional := class == "driver.one-time" && hasAnomaly
	if len(raw) == 0 {
		if optional {
			return ""
		}
		return fmt.Sprintf(`%s needs a target naming the window `+
			`("target": {"start": "YYYY-MM-DD", "end": "YYYY-MM-DD"}); none was given`, class)
	}
	if optional {
		return fmt.Sprintf("%s on a task with its own anomaly takes the anomaly's own day; "+
			"a target here is not needed and is refused rather than silently ignored", class)
	}
	var tgt driverTarget
	if err := json.Unmarshal(raw, &tgt); err != nil {
		return fmt.Sprintf("%s's target is not a valid JSON object: %s", class, err.Error())
	}
	start, err := time.Parse("2006-01-02", tgt.Start)
	if err != nil {
		return fmt.Sprintf("%s's target.start %q does not parse as YYYY-MM-DD", class, tgt.Start)
	}
	end, err := time.Parse("2006-01-02", tgt.End)
	if err != nil {
		return fmt.Sprintf("%s's target.end %q does not parse as YYYY-MM-DD", class, tgt.End)
	}
	if end.Before(start) {
		return fmt.Sprintf("%s's target.end %s is before target.start %s", class, tgt.End, tgt.Start)
	}
	if end.Sub(start) > driverTargetMaxWindowDays*24*time.Hour {
		return fmt.Sprintf("%s's target spans more than %d days (%s to %s)",
			class, driverTargetMaxWindowDays, tgt.Start, tgt.End)
	}
	return ""
}

// ValidClassesFor is the class vocabulary one role's deliverable may name:
// the union of its decides_alone and hands_up lists. The same closed
// vocabulary the prompt packet already shows the model
// (tools/run/mandate.go's jobDescriptionBlock), so there is one source for
// what a role may say and what this file will accept.
func ValidClassesFor(role JobDescription) map[string]bool {
	out := make(map[string]bool, len(role.DecidesAlone)+len(role.HandsUp))
	for _, c := range role.DecidesAlone {
		out[c] = true
	}
	for _, c := range role.HandsUp {
		out[c] = true
	}
	return out
}

// AllowsNoOptions is true for a role that may end a deliverable in zero
// options: B3-SPEC.md section 2's own words, "zero is allowed only for a
// commentary.* or forecast.project deliverable, which produce prose". Read
// as: every class the role's own job description names AT ALL -- decides
// alone or hands up, ValidClassesFor's union -- is one of those two kinds,
// or the union is empty (the role decides no closed class alone and hands
// none up either: nothing here is a machine-checked class such a role could
// attach an option to in the first place).
//
// Checked against the union rather than DecidesAlone alone: a role with an
// empty decides_alone but a real hands_up list (an investigator's own
// hands_up carries purchase, infra.change, message.team) still owes options
// naming those classes, and reading DecidesAlone alone let such a role skip
// the block entirely, vacuously true on nothing.
func AllowsNoOptions(role JobDescription) bool {
	for c := range ValidClassesFor(role) {
		switch c {
		case "commentary.variance", "commentary.showback", "forecast.project":
		// data.halt is CONDITIONAL prose, unlike the three above, which are
		// always prose: the data-quality analyst's own owes line names a
		// halt request only "when a threshold (T.stale, T.untagged) is
		// crossed" (C9-SPEC.md section 1), and most days nothing is. Its
		// whole vocabulary is this one hands_up class, so on an ordinary
		// day the deliverable is the freshness-and-coverage report alone,
		// naming no options at all -- the same shape the three prose
		// classes above already establish is allowed to skip the block.
		case "data.halt":
		default:
			return false
		}
	}
	return true
}

// ValidateAndSaveOptions is section 2's save-time gate. It parses the
// deliverable's trailing options block and, if every option's class is one
// roleName's own job description lists (under decides_alone or hands_up),
// stores the options as open rows and returns refused=false. Otherwise
// NOTHING is written to artifact_options -- "the deliverable is saved
// WITHOUT its options" -- refused is true, and reason names why; the
// refusal is journaled here as option_refused, with the actor named as the
// role that wrote it, so the audit page carries it whether or not the
// caller does anything else with the reason.
//
// It does not touch artifacts or tasks at all: what a refusal means for the
// task (tools/run/live.go returns it to the analyst) is the caller's
// decision, because this function's only job is the options block itself.
func ValidateAndSaveOptions(db *sql.DB, artifactID int, roleName, body string, rec Recorder) (refused bool, reason string, err error) {
	role, roleOK := RoleFor(roleName)

	opts, found, parseReason := ParseOptions(body)
	if parseReason != "" {
		journalOptionRefused(rec, roleName, artifactID, parseReason)
		return true, parseReason, nil
	}
	// No block at all is additive, not a refusal, when the writer matches no
	// role family: the same rule jobDescriptionBlock and packet() already
	// hold for a name roles.yaml has never heard of (a hire made by hand, a
	// synthetic name a test fixture uses for something unrelated). It is
	// only a refusal once the writer IS a known role that owes a real
	// decision class, because that role's own vocabulary says a block was
	// owed and none arrived.
	if !found {
		if roleOK && !AllowsNoOptions(role) {
			reason := fmt.Sprintf("this deliverable must end in one to three options "+
				"naming a class %s's job description lists under decides_alone or "+
				"hands_up; none was found", roleName)
			journalOptionRefused(rec, roleName, artifactID, reason)
			return true, reason, nil
		}
		return false, "", nil
	}
	// A block WAS found, even if it names zero options: that is a deliberate
	// signal (a model that writes `{"options": []}` is saying it considered
	// the question), so from here on an unrecognized role is refused rather
	// than trusted, because nothing exists to check its classes against.
	if !roleOK {
		reason := fmt.Sprintf(
			"no job description is on file for %q, so no class it names can be checked", roleName)
		journalOptionRefused(rec, roleName, artifactID, reason)
		return true, reason, nil
	}
	if len(opts) == 0 {
		if !AllowsNoOptions(role) {
			reason := fmt.Sprintf("this deliverable must end in one to three options "+
				"naming a class %s's job description lists under decides_alone or "+
				"hands_up; the block named zero", roleName)
			journalOptionRefused(rec, roleName, artifactID, reason)
			return true, reason, nil
		}
		return false, "", nil // zero options, and this role's own classes are all prose
	}

	legal := ValidClassesFor(role)
	var hasAnomaly bool
	var hasAnomalyChecked bool
	for _, o := range opts {
		if !legal[o.Class] {
			reason := fmt.Sprintf("%q is not a class %s's job description lists under "+
				"decides_alone or hands_up", o.Class, roleName)
			journalOptionRefused(rec, roleName, artifactID, reason)
			return true, reason, nil
		}
		// A second, independent check against the vocabulary itself, not only
		// against the role's own list: this reads what a MODEL wrote, and
		// mustLoadRoles (roles.go) already refuses a role naming an undefined
		// class at package init, so legal[o.Class] being true here should
		// already imply ClassFor succeeds -- but that implication runs through
		// roles.yaml being well-formed, not through anything this function
		// re-checks, and the model's text is untrusted input like any other.
		if _, ok := ClassFor(o.Class); !ok {
			reason := fmt.Sprintf("%q is not a decision class this practice defines", o.Class)
			journalOptionRefused(rec, roleName, artifactID, reason)
			return true, reason, nil
		}
		// allocation.rule alone carries a structured target (C2-SPEC.md
		// section 2): "the one class the B3 review left recorded-only for
		// lack of a target gains one". Checked here, inside the same
		// whole-deliverable validation pass, so a bad target refuses the
		// deliverable's options exactly the way an out-of-vocabulary class
		// already does -- nothing is written, the reason is journaled once.
		if o.Class == "allocation.rule" {
			if reason := validateAllocationRuleTarget(o.Target); reason != "" {
				journalOptionRefused(rec, roleName, artifactID, reason)
				return true, reason, nil
			}
		}
		// driver.recurring and driver.one-time alone carry a structured
		// target (DRIVER-WINDOW-SPEC.md section 2), the same
		// "checked here, inside the same whole-deliverable validation pass"
		// shape allocation.rule's own target check above uses: nothing is
		// written, the reason is journaled once. hasAnomaly is looked up at
		// most once per artifact, not once per option, since every option in
		// this deliverable belongs to the same task.
		if o.Class == "driver.recurring" || o.Class == "driver.one-time" {
			if !hasAnomalyChecked {
				var err error
				hasAnomaly, err = artifactHasAnomaly(db, artifactID)
				if err != nil {
					return false, "", err
				}
				hasAnomalyChecked = true
			}
			if reason := validateDriverTarget(o.Class, o.Target, hasAnomaly); reason != "" {
				journalOptionRefused(rec, roleName, artifactID, reason)
				return true, reason, nil
			}
		}
	}

	for _, o := range opts {
		ev, _ := json.Marshal(o.Evidence)
		var target any
		if len(o.Target) > 0 {
			target = string(o.Target)
		}
		if _, err := db.Exec(`INSERT INTO artifact_options
			(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, target, state)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			artifactID, o.Ordinal, o.Class, o.Summary, o.FigureCents, o.SavingCents,
			o.Risk, o.Needs, string(ev), target, string(OptionOpen)); err != nil {
			return false, "", err
		}
	}
	return false, "", nil
}

// artifactHasAnomaly is whether the task an artifact belongs to came from an
// anomaly, looked up through TaskOfArtifact and GetTask exactly as
// DRIVER-WINDOW-SPEC.md section 3 directs: this function already knows the
// artifact id, so there is no reason to make a caller pass the task in.
func artifactHasAnomaly(db *sql.DB, artifactID int) (bool, error) {
	taskID, err := TaskOfArtifact(db, artifactID)
	if err != nil {
		return false, err
	}
	t, err := GetTask(db, taskID)
	if err != nil {
		return false, err
	}
	return t.Anomaly != "", nil
}

// EnsureOptionTarget adds artifact_options.target for an installation
// migrated from before allocation.rule (C2-SPEC.md section 2) and
// driver.recurring/driver.one-time (DRIVER-WINDOW-SPEC.md section 2) carried
// a structured target. CREATE TABLE IF NOT EXISTS does nothing to a table
// that already exists (Schema's own header comment, roster.go's
// ensureRoster), so a console started before this column existed needs the
// ALTER too; the duplicate-column error is the normal path on every start
// after the first, the same convention EnsureOwnershipHistory and
// connectors.EnsureFocusSchema already hold for their own added columns.
// Every option saved before this reads back with an absent Target, exactly
// as before: the column is nullable and the zero value is what "no target"
// already means.
func EnsureOptionTarget(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	if _, err := db.Exec("ALTER TABLE artifact_options ADD COLUMN target TEXT"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("adding artifact_options.target: %w", err)
	}
	return nil
}

func journalOptionRefused(rec Recorder, roleName string, artifactID int, reason string) {
	if rec == nil {
		return
	}
	_ = rec.Emit("option_refused", roleName, "warn", map[string]any{
		"artifact": artifactID, "reason": reason,
	}, nil)
}

// ------------------------------------------------------------------ reads

// Options is one artifact's options, in ordinal order.
func Options(db *sql.DB, artifactID int) ([]Option, error) {
	rows, err := db.Query(`SELECT artifact, ordinal, class, COALESCE(summary,''),
		figure_cents, saving_cents, COALESCE(risk,''), COALESCE(needs,''),
		COALESCE(evidence,''), COALESCE(target,''), state, COALESCE(decided_by,''),
		COALESCE(decided_at,''), COALESCE(reason,'')
		FROM artifact_options WHERE artifact=? ORDER BY ordinal`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOptions(rows)
}

// GetOption reads one option by its primary key.
func GetOption(db *sql.DB, artifactID, ordinal int) (Option, error) {
	opts, err := Options(db, artifactID)
	if err != nil {
		return Option{}, err
	}
	for _, o := range opts {
		if o.Ordinal == ordinal {
			return o, nil
		}
	}
	return Option{}, ErrNotFound
}

// OpenOptionsForSprint is every option in state "open" belonging to a POSTED
// deliverable of the sprint: what the supervisor's pass collects
// (B3-SPEC.md section 4, step 1). A deliverable that has not been posted is
// not yet the crew's answer, so its options are not collected either.
func OpenOptionsForSprint(db *sql.DB, sprintID int) ([]Option, error) {
	rows, err := db.Query(`SELECT o.artifact, o.ordinal, o.class, COALESCE(o.summary,''),
		o.figure_cents, o.saving_cents, COALESCE(o.risk,''), COALESCE(o.needs,''),
		COALESCE(o.evidence,''), COALESCE(o.target,''), o.state, COALESCE(o.decided_by,''),
		COALESCE(o.decided_at,''), COALESCE(o.reason,'')
		FROM artifact_options o
		JOIN artifacts a ON a.id = o.artifact
		JOIN tasks t ON t.id = a.task
		WHERE t.sprint = ? AND a.state = 'posted' AND o.state = 'open'
		ORDER BY o.artifact, o.ordinal`, sprintID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOptions(rows)
}

// CarriedOptionsFor is the live list of options carried to one owner in one
// sprint: what a decision request's page shows, and what
// PostDecisionRequestIfComplete counts down to zero.
func CarriedOptionsFor(db *sql.DB, sprintID int, owner string) ([]Option, error) {
	rows, err := db.Query(`SELECT o.artifact, o.ordinal, o.class, COALESCE(o.summary,''),
		o.figure_cents, o.saving_cents, COALESCE(o.risk,''), COALESCE(o.needs,''),
		COALESCE(o.evidence,''), COALESCE(o.target,''), o.state, COALESCE(o.decided_by,''),
		COALESCE(o.decided_at,''), COALESCE(o.reason,'')
		FROM artifact_options o
		JOIN artifacts a ON a.id = o.artifact
		JOIN tasks t ON t.id = a.task
		WHERE t.sprint = ? AND COALESCE(t.owner,'') = ? AND o.state = 'carried'
		ORDER BY o.artifact, o.ordinal`, sprintID, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOptions(rows)
}

func scanOptions(rows *sql.Rows) ([]Option, error) {
	var out []Option
	for rows.Next() {
		var o Option
		var evidence, target string
		var state string
		if err := rows.Scan(&o.Artifact, &o.Ordinal, &o.Class, &o.Summary,
			&o.FigureCents, &o.SavingCents, &o.Risk, &o.Needs, &evidence, &target, &state,
			&o.DecidedBy, &o.DecidedAt, &o.Reason); err != nil {
			return nil, err
		}
		o.State = OptionState(state)
		if evidence != "" {
			_ = json.Unmarshal([]byte(evidence), &o.Evidence)
		}
		if target != "" {
			o.Target = json.RawMessage(target)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ writes

// setOptionState is the one place every option transition writes through,
// so decided_by and decided_at are never set by one path and forgotten by
// another.
func setOptionState(db *sql.DB, artifactID, ordinal int, state OptionState, by, reason string) error {
	res, err := db.Exec(`UPDATE artifact_options
		SET state=?, decided_by=?, decided_at=datetime('now'), reason=?
		WHERE artifact=? AND ordinal=?`,
		string(state), nullIf(by), nullIf(reason), artifactID, ordinal)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOptionApplied records that actor applied this option. Called by
// internal/finops.Apply once the side effect (if this class has one) has
// already succeeded, never before.
func MarkOptionApplied(db *sql.DB, artifactID, ordinal int, actor string) error {
	return setOptionState(db, artifactID, ordinal, OptionApplied, actor, "")
}

// MarkOptionRefused records that actor refused this option, and insists on
// why: a refusal without a reason is indistinguishable from nobody having
// looked, the same argument Return and anomaly.Dismiss already make.
func MarkOptionRefused(db *sql.DB, artifactID, ordinal int, actor, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	return setOptionState(db, artifactID, ordinal, OptionRefused, actor, reason)
}

// MarkOptionCarried records that the supervisor's pass carried this option
// into a decision request, because its class is not one the supervisor's own
// job description lets it decide alone.
func MarkOptionCarried(db *sql.DB, artifactID, ordinal int) error {
	return setOptionState(db, artifactID, ordinal, OptionCarried, "supervisor", "")
}

// MarkOptionNotChosen records that this option was not the one a deliverable's
// choice applied. `@yurii 2026-09-02`: "давати на вибір якісь певні рішення"
// is offering a CHOICE, and roles.yaml's own option.select ("which of an
// analyst's options is carried forward") is the supervisor's word for
// making it -- an owner's stamp makes the same kind of choice. actor is
// whoever applied the option THIS one lost to; reason names it.
func MarkOptionNotChosen(db *sql.DB, artifactID, ordinal int, actor, reason string) error {
	return setOptionState(db, artifactID, ordinal, OptionNotChosen, actor, reason)
}

// LiveRivalsOf is every other option that applying opt resolves: every other
// still-live (open or carried) option of opt's OWN deliverable -- options in
// one deliverable are alternatives, decided together, never independent
// actions -- plus, when opt is anomaly.explain, every other still-live
// anomaly.explain option on the SAME anomaly from a DIFFERENT deliverable
// whose (trimmed) summary differs from opt's. That second case is
// roles.yaml's own hands_to_owner_conditions, "any question two analysts
// answer differently on the same evidence": applying one side of that
// question is what answers it, even though the two options live on two
// different artifacts.
func LiveRivalsOf(db *sql.DB, opt Option) ([]Option, error) {
	same, err := Options(db, opt.Artifact)
	if err != nil {
		return nil, err
	}
	var out []Option
	for _, o := range same {
		if o.Ordinal == opt.Ordinal {
			continue
		}
		if o.State != OptionOpen && o.State != OptionCarried {
			continue
		}
		out = append(out, o)
	}

	if opt.Class != "anomaly.explain" {
		return out, nil
	}
	taskID, err := TaskOfArtifact(db, opt.Artifact)
	if err != nil {
		return out, nil // best-effort: same-artifact rivals still resolve
	}
	t, err := GetTask(db, taskID)
	if err != nil || t.Anomaly == "" {
		return out, nil
	}
	rows, err := db.Query(`SELECT o.artifact, o.ordinal, o.class, COALESCE(o.summary,''),
		o.figure_cents, o.saving_cents, COALESCE(o.risk,''), COALESCE(o.needs,''),
		COALESCE(o.evidence,''), COALESCE(o.target,''), o.state, COALESCE(o.decided_by,''),
		COALESCE(o.decided_at,''), COALESCE(o.reason,'')
		FROM artifact_options o
		JOIN artifacts a ON a.id = o.artifact
		JOIN tasks t2 ON t2.id = a.task
		WHERE t2.anomaly = ? AND o.class = 'anomaly.explain' AND o.artifact <> ?
		  AND o.state IN ('open','carried')`, t.Anomaly, opt.Artifact)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	rivals, err := scanOptions(rows)
	if err != nil {
		return out, err
	}
	trimmed := strings.TrimSpace(opt.Summary)
	for _, r := range rivals {
		if strings.TrimSpace(r.Summary) != trimmed {
			out = append(out, r)
		}
	}
	return out, nil
}
