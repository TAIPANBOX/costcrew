package crew

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// An attestation is a claim about how this agent's identity is bound to a
// workload, and this console is not allowed to invent one.
//
// # What was here before, and why it was worse than nothing
//
// Until now the seeding DERIVED an attestation from an agent's permissions:
// hold `close-covered` and you were issued `spiffe-svid`, hold `channel-post`
// and you were issued `oidc`. Twelve of thirty-nine agents carried a method
// that way. Nothing had attested anything; a permission list had been read and
// a security claim written down.
//
// That is worse than `none`, and measurably so. idryx's `bom_incomplete`
// detector treats `none` as missing and flags it, which is the correct
// treatment of an identity bound to nothing. Those twelve were NOT flagged:
// the console had told the identity graph they were bound, so the graph
// stopped asking. Silence bought by a false claim is the exact failure this
// estate's contracts exist to prevent, and it is the one nobody notices,
// because a missing alert looks like a healthy system.
//
// # What replaces it
//
// Nothing is derived. A method is recorded when somebody records it, and it
// must carry the evidence that makes it checkable: an issuer for OIDC, a
// SPIFFE ID, a certificate fingerprint, an enclave measurement. A method with
// no detail is refused rather than stored, because "oidc" on its own names no
// issuer and can be neither verified nor disproved.
//
// The consequence is that this console now reports MORE unattested agents than
// it did this morning, and that is the direction the number was always
// supposed to move in.

// Attestations is what the Agent Passport spec allows.
var Attestations = []string{"none", "oidc", "spiffe-svid", "enclave-key", "mtls-cert"}

// AttestationNeeds says what evidence a method has to carry, in the words a
// person filling in the form needs rather than the spec's.
var AttestationNeeds = map[string]string{
	"none":        "",
	"oidc":        "the issuer URL that mints the token, e.g. https://login.example.com",
	"spiffe-svid": "the SPIFFE ID, e.g. spiffe://example.com/ns/finops/sa/triage",
	"enclave-key": "the measurement or key id the enclave publishes",
	"mtls-cert":   "the certificate's SHA-256 fingerprint, or its subject DN",
}

// ValidAttestation checks a method and its evidence together.
//
// Together, because neither half means anything alone: a method with no detail
// is a word, and a detail with no method says nothing about how it was
// obtained.
func ValidAttestation(method, detail string) error {
	method = strings.TrimSpace(method)
	detail = strings.TrimSpace(detail)

	known := false
	for _, m := range Attestations {
		if m == method {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%q is not an attestation method this contract allows: %s",
			method, strings.Join(Attestations, ", "))
	}
	if method == "none" {
		if detail != "" {
			return fmt.Errorf("an attestation of 'none' carries no detail: " +
				"nothing attested it, so there is nothing to point at")
		}
		return nil
	}
	if detail == "" {
		return fmt.Errorf("%s needs %s. A method on its own is a word, not an "+
			"attestation: it can be neither checked nor disproved, and an identity "+
			"graph that believes it will stop asking about an agent bound to nothing",
			method, AttestationNeeds[method])
	}

	// Per-method shape. Not proof - this console cannot verify an issuer from
	// here - but enough that a typo is caught where it is made rather than
	// travelling into a passport somebody else reads.
	switch method {
	case "oidc":
		u, err := url.Parse(detail)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("an OIDC issuer is an https URL; %q is not one", detail)
		}
	case "spiffe-svid":
		if !strings.HasPrefix(detail, "spiffe://") || len(detail) < len("spiffe://a/b") {
			return fmt.Errorf("a SPIFFE ID starts with spiffe:// and names a trust "+
				"domain and a path; %q does not", detail)
		}
	case "mtls-cert":
		d := strings.ReplaceAll(strings.ToLower(detail), ":", "")
		hex := len(d) == 64 && strings.Trim(d, "0123456789abcdef") == ""
		if !hex && !strings.Contains(detail, "=") {
			return fmt.Errorf("give either a SHA-256 fingerprint (64 hex characters) " +
				"or a subject DN with at least one attribute; that is neither")
		}
	}
	return nil
}

// Unattested counts the agents whose identity is bound to nothing.
//
// The console reports this itself rather than waiting for the identity graph
// to say it, because an operator reading this page should not learn it from
// somewhere else.
func Unattested(roster []Analyst) (n int, of int) {
	for _, a := range roster {
		if a.State == "suspended" {
			continue
		}
		of++
		if a.Attestation == "" || a.Attestation == "none" {
			n++
		}
	}
	return n, of
}

// ClearFabricated removes attestations this console invented.
//
// A migration and an apology. Twelve agents on an installation seeded before
// today carry `oidc` or `spiffe-svid` chosen from their permission list, with
// no issuer, no SPIFFE ID and nothing that attested anything. Leaving them
// would leave an identity graph believing them.
//
// It only clears a method with NO detail, so an attestation somebody actually
// recorded, with its evidence, survives untouched. That is the whole test for
// whether a claim was made by a person or by the code that used to guess.
func ClearFabricated(db *sql.DB) (int, error) {
	if err := ensureRoster(db); err != nil {
		return 0, err
	}
	res, err := db.Exec(`UPDATE analysts
		SET attestation = 'none'
		WHERE COALESCE(attestation,'none') <> 'none'
		  AND COALESCE(attestation_detail,'') = ''`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
