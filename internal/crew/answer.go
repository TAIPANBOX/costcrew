package crew

// A person's answer to a carried option, and who it was given for.
//
// `@decided 2026-10-04`: an admin may still answer a decision request that is
// addressed to somebody else, as an emergency path (an owner on leave, an
// owner who has left), but never silently. Before this, mayAnswerFor in
// internal/web let any admin apply or refuse any owner's decision and nothing
// recorded that the stamp was not the owner's: the option said "decided_by
// boss" and read the same as an answer from the person it was asked of.
//
// Now an Answer says whose stamp it is. When it is somebody's own, nothing
// more is recorded. When it is given for somebody else, the answer names that
// owner and carries a reason, and that is checked here, below the handler, so
// no caller can leave it out: Apply and RefuseOption both refuse an answer on
// behalf of an owner with no valid reason before they change anything.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What an answer was given as, in the journal and on the bus (answered_as).
const (
	AnsweredAsOwner         = "owner"
	AnsweredAsAdminForOwner = "admin_on_behalf_of_owner"
)

// BehalfReasonMaxBytes caps the reason an admin gives for answering for an
// owner. It is a sentence or two: the reason lands in the journal, on the bus
// and on two pages, and a cap is what keeps a hostile megabyte out of all
// four. Over the cap is refused, never truncated, because a reason cut in the
// middle would say something its author did not.
const BehalfReasonMaxBytes = 500

// ErrNeedBehalfReason is returned when somebody answers for an owner without
// a usable reason.
var ErrNeedBehalfReason = errors.New("answering for somebody else needs a reason")

// Answer is one stamp on a carried option.
type Answer struct {
	// Actor is the account that stamped it.
	Actor string
	// As is AnsweredAsOwner or AnsweredAsAdminForOwner when a person
	// answered through the console, and "" for the supervisor's own act,
	// which is not an answer to anybody's request.
	As string
	// OnBehalfOf is the owner the request was addressed to, set exactly when
	// As is AnsweredAsAdminForOwner.
	OnBehalfOf string
	// Reason is why Actor answered for OnBehalfOf. Required then, empty
	// otherwise.
	Reason string
}

// OwnerAnswer is an owner's own answer to a request addressed to them.
func OwnerAnswer(owner string) Answer { return Answer{Actor: owner, As: AnsweredAsOwner} }

// AdminAnswerFor is an admin's answer to a request addressed to owner, with
// the reason they give for answering it.
func AdminAnswerFor(admin, owner, reason string) Answer {
	return Answer{Actor: admin, As: AnsweredAsAdminForOwner, OnBehalfOf: owner, Reason: reason}
}

// OnBehalf says whether the answer was given for somebody else.
func (a Answer) OnBehalf() bool { return a.OnBehalfOf != "" }

// Validate refuses an answer that claims to be on behalf of an owner without a
// reason this package accepts, and a reason where none belongs. It is the one
// check every path that records an answer runs first.
func (a Answer) Validate() error {
	if !a.OnBehalf() {
		if a.Reason != "" {
			return fmt.Errorf("a reason was given for answering on behalf of nobody")
		}
		return nil
	}
	if a.As != AnsweredAsAdminForOwner {
		return fmt.Errorf("an answer on behalf of %q must be marked %q, not %q", a.OnBehalfOf, AnsweredAsAdminForOwner, a.As)
	}
	_, err := ValidBehalfReason(a.Reason)
	return err
}

// ValidBehalfReason returns the reason as it will be stored (surrounding
// whitespace trimmed) or says why it is not acceptable: empty, over
// BehalfReasonMaxBytes, not valid UTF-8, or carrying a character that would
// let one reason pass for two lines or hide its own text (a control character
// other than a newline or a tab, a line or paragraph separator, a bidi
// override, a zero-width space). Markup is not refused: it is stored as given
// and every page that shows it escapes it.
func ValidBehalfReason(s string) (string, error) {
	r := strings.TrimSpace(s)
	if r == "" {
		return "", ErrNeedBehalfReason
	}
	if len(r) > BehalfReasonMaxBytes {
		return "", fmt.Errorf("the reason is %d bytes, over the %d-byte limit: say it shorter", len(r), BehalfReasonMaxBytes)
	}
	if !utf8.ValidString(r) {
		return "", errors.New("the reason is not valid text")
	}
	visible := false
	for _, c := range r {
		switch {
		case c == '\n' || c == '\t':
		case unicode.IsControl(c):
			return "", errors.New("the reason carries a control character")
		case c == '\u2028' || c == '\u2029':
			return "", errors.New("the reason carries a line separator")
		case c >= '\u202a' && c <= '\u202e', c >= '\u2066' && c <= '\u2069':
			return "", errors.New("the reason carries a text-direction override")
		case c == '\u200b' || c == '\u200c' || c == '\u200d' || c == '\ufeff':
			continue // zero-width: allowed inside text, never counted as text
		case !unicode.IsSpace(c):
			visible = true
		}
	}
	if !visible {
		return "", ErrNeedBehalfReason
	}
	return r, nil
}

// EnsureOptionBehalf adds artifact_options.on_behalf_of and behalf_reason for
// an installation from before an admin's answer for an owner was marked. The
// same convention EnsureOptionTarget holds: CREATE TABLE IF NOT EXISTS does
// nothing to a table that exists, the duplicate-column error is the normal
// path on every start after the first, and every option answered before this
// reads back with both empty, which is what "answered by the owner" already
// meant.
func EnsureOptionBehalf(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	for _, col := range []string{"on_behalf_of", "behalf_reason"} {
		if _, err := db.Exec("ALTER TABLE artifact_options ADD COLUMN " + col + " TEXT"); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("adding artifact_options.%s: %w", col, err)
		}
	}
	return nil
}

// MarkOptionAppliedAs records that an answer applied this option. Called by
// internal/finops.ApplyAs once the side effect (if this class has one) has
// already succeeded. An answer on behalf of an owner that fails Validate
// changes nothing.
func MarkOptionAppliedAs(db *sql.DB, artifactID, ordinal int, ans Answer) error {
	if err := ans.Validate(); err != nil {
		return err
	}
	return setOptionStateAs(db, artifactID, ordinal, OptionApplied, ans.Actor, "", ans.OnBehalfOf, trimmedReason(ans))
}

func trimmedReason(a Answer) string {
	if !a.OnBehalf() {
		return ""
	}
	r, _ := ValidBehalfReason(a.Reason)
	return r
}

// RefuseOption records that an answer refused this option, for reason (which
// it insists on, as MarkOptionRefused does), and journals option_refused with
// who answered and for whom: the event had no producer for a person's refusal
// before, only for the save-time gate, so a refusal an owner made was in the
// chain only as a changed row. An answer on behalf of an owner that fails
// Validate changes nothing and journals nothing.
func RefuseOption(db *sql.DB, artifactID, ordinal int, ans Answer, reason string, rec Recorder) error {
	if err := ans.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	opt, err := GetOption(db, artifactID, ordinal)
	if err != nil {
		return err
	}
	if err := setOptionStateAs(db, artifactID, ordinal, OptionRefused, ans.Actor, reason, ans.OnBehalfOf, trimmedReason(ans)); err != nil {
		return err
	}
	if rec != nil {
		data := map[string]any{
			"artifact": artifactID,
			"ordinal":  ordinal,
			"class":    opt.Class,
			"reason":   reason,
		}
		AnswerData(data, ans)
		// "low", the same level the save-time refusal carries: nothing was
		// spent and nothing was changed (journalOptionRefused's own comment).
		_ = rec.Emit("option_refused", ans.Actor, "low", data, nil)
	}
	return nil
}

// AnswerData adds to an event's data how the answer was given: answered_by,
// answered_as and, only when it was given for somebody else, on_behalf_of and
// on_behalf_reason. It adds nothing for the supervisor's own act (As empty).
// The keys live in the payload and not in the envelope's on_behalf_of chain,
// whose entries must be agent:// or user:// URIs (agent-passport SPEC 5.1) and
// are a delegation between identities, which a console username is not.
func AnswerData(data map[string]any, ans Answer) {
	if ans.As == "" {
		return
	}
	data["answered_by"] = ans.Actor
	data["answered_as"] = ans.As
	if ans.OnBehalf() {
		data["on_behalf_of"] = ans.OnBehalfOf
		data["on_behalf_reason"] = trimmedReason(ans)
	}
}

// AnsweredOptionsFor is the options of one owner's request in one sprint that
// a person has already answered (applied or refused through the console, not
// the supervisor's own selection), oldest first, so the decision card can say
// who answered each one and for whom.
func AnsweredOptionsFor(db *sql.DB, sprintID int, owner string) ([]Option, error) {
	rows, err := db.Query(`SELECT o.artifact, o.ordinal, o.class, COALESCE(o.summary,''),
		o.figure_cents, o.saving_cents, COALESCE(o.risk,''), COALESCE(o.needs,''),
		COALESCE(o.evidence,''), COALESCE(o.target,''), o.state, COALESCE(o.decided_by,''),
		COALESCE(o.decided_at,''), COALESCE(o.reason,''),
		COALESCE(o.on_behalf_of,''), COALESCE(o.behalf_reason,'')
		FROM artifact_options o
		JOIN artifacts a ON a.id = o.artifact
		JOIN tasks t ON t.id = a.task
		WHERE t.sprint = ? AND COALESCE(t.owner,'') = ?
		  AND o.state IN ('applied','refused') AND COALESCE(o.decided_by,'') <> 'supervisor'
		ORDER BY o.decided_at, o.artifact, o.ordinal`, sprintID, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOptions(rows)
}
