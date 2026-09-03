package main

// Case selection: which known anomalies the bench scores, and who answers
// for each one. B7-SPEC.md section 2, steps 1 and "-skill".

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// knownCase is one anomaly the estate's own registry has a driver label for,
// paired with the active analyst who would answer it under the requested
// skill.
type knownCase struct {
	Anomaly anomaly.Anomaly
	Analyst crew.Analyst
}

// rolePrefixForSkill is the roster's own naming convention -- confirmed
// against the seeded fixture, not assumed: every triage analyst is named
// "triage-<desk>" (internal/world/world.go's buildCrew, e.g. "triage-gcp"),
// every investigator "investigator-<desk>" ("investigator-onprem" included:
// a separate role FAMILY in roles.yaml from the cloud "investigator" one,
// but the same roster NAME shape). Not every desk has both: the roster has
// no triage-onprem, no triage-saas, no investigator-ai, no investigator-saas
// at all, which is exactly why selection below skips a case rather than
// guessing an analyst for it.
func rolePrefixForSkill(skill string) (string, error) {
	switch skill {
	case "triage":
		return "triage", nil
	case "investigate":
		return "investigator", nil
	default:
		return "", fmt.Errorf("-skill must be \"triage\" or \"investigate\", got %q", skill)
	}
}

// selectKnownCases gathers every anomaly with a driver, shuffles them
// deterministically by seed, and keeps the first n that have an ACTIVE
// analyst on the roster for the requested skill and that anomaly's own
// desk. total is how many anomalies carry a driver at all; eligible is how
// many of those have such an analyst -- both named in the header when n
// clamps down to less than what was asked for.
func selectKnownCases(db *sql.DB, skill string, n int, seed int64) (cases []knownCase, total, eligible int, err error) {
	prefix, err := rolePrefixForSkill(skill)
	if err != nil {
		return nil, 0, 0, err
	}

	all, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		return nil, 0, 0, err
	}
	var withDriver []anomaly.Anomaly
	for _, a := range all {
		if a.Driver != "" {
			withDriver = append(withDriver, a)
		}
	}
	total = len(withDriver)
	// Sorted by id BEFORE the shuffle, so the shuffle's own result depends
	// only on the seed and not on whatever order SQLite happened to return
	// rows in (anomaly.List already orders by money, which is itself stable,
	// but this makes the input to rand.Shuffle independent of that too).
	sort.Slice(withDriver, func(i, j int) bool { return withDriver[i].ID < withDriver[j].ID })

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(withDriver), func(i, j int) {
		withDriver[i], withDriver[j] = withDriver[j], withDriver[i]
	})

	for _, an := range withDriver {
		name := prefix + "-" + an.Source
		a, err := crew.GetAnalyst(db, name)
		if err != nil || a.State != "active" {
			continue // no eligible analyst for this desk and skill: not a case
		}
		eligible++
		if len(cases) < n {
			cases = append(cases, knownCase{Anomaly: an, Analyst: a})
		}
	}
	return cases, total, eligible, nil
}

// hasAnyDriver says whether this store's own anomalies carry a driver at
// all: the switch B7-SPEC.md section 2 names between scoring against the
// known cause (a generated fixture) and scoring against a stamp (imported
// data, which has never had world.Drivers() seeded into it).
func hasAnyDriver(db *sql.DB) (bool, error) {
	all, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		return false, err
	}
	for _, a := range all {
		if a.Driver != "" {
			return true, nil
		}
	}
	return false, nil
}
