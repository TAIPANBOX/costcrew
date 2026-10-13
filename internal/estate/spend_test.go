package estate_test

// ANNOUNCED-DRIVER-SPEC.md section 11, finding (a): a service-level step is
// split across the teams on a desk in proportion to each team's median daily
// spend, in integer cents, summing exactly to the step. The remainder goes to
// the largest share. A team with no spend before the start gets nothing.
//
// Red against the stub SplitByMedian (attaches nothing): the sum test fails.
// The round trip of a named team is red against an InsertDriver that does not
// yet write the team.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Three equal teams and an amount not divisible by three: the shares must still
// sum to the step exactly, and the remainder goes to the first of the equal
// largest shares in name order.
func TestTheSplitSumsExactlyToTheStep(t *testing.T) {
	medians := map[string]money.Cents{"a": 5_00, "b": 5_00, "c": 5_00, "d": 0}
	shares := estate.SplitByMedian(-10_001, medians)
	var sum money.Cents
	for _, v := range shares {
		sum += v
	}
	if sum != -10_001 {
		t.Fatalf("shares %v sum to %s, want exactly -10001", shares, sum)
	}
	if shares["a"] != -3_335 || shares["b"] != -3_333 || shares["c"] != -3_333 {
		t.Errorf("shares %v, want a -3335 (the remainder), b and c -3333", shares)
	}
	if shares["d"] != 0 {
		t.Errorf("a team with no spend took a share of %s", shares["d"])
	}
	for _, daily := range []money.Cents{-1, 1, -7, 10_001, -9_999_999} {
		s := estate.SplitByMedian(daily, medians)
		var total money.Cents
		for _, v := range s {
			total += v
		}
		if total != daily {
			t.Errorf("step %s: shares %v sum to %s", daily, s, total)
		}
	}
}

// Shares are proportional: a team with twice another's median takes twice its
// share, to the cent, when the step divides evenly.
func TestTheSplitIsProportionalToEachTeamsSpend(t *testing.T) {
	shares := estate.SplitByMedian(-300_00, map[string]money.Cents{
		"data-eng": 100_00, "product-web": 200_00, "sre-platform": 300_00, "ml-platform": 400_00})
	want := map[string]money.Cents{"data-eng": -30_00, "product-web": -60_00, "sre-platform": -90_00, "ml-platform": -120_00}
	for team, w := range want {
		if shares[team] != w {
			t.Errorf("%s takes %s, want %s", team, shares[team], w)
		}
	}
}

// With no spend to split against, nothing is attached: there is no team whose
// share could be named.
func TestASplitAgainstNoSpendAttachesNothing(t *testing.T) {
	if s := estate.SplitByMedian(-300_00, map[string]money.Cents{"a": 0, "b": 0}); len(s) != 0 {
		t.Fatalf("a split with no spend attached %v", s)
	}
}

// A named team is stored and read back, and an unnamed split reads back empty.
func TestAnAnnouncedDriverKeepsItsTeam(t *testing.T) {
	db := seededTemp(t)
	v := int64(-30_000)
	for _, team := range []string{"data-eng", ""} {
		if err := estate.InsertDriver(db, world.Driver{Start: "2026-11-01", End: "2026-11-30", Scope: "svc",
			Label: "swap " + team, Kind: "announced", Source: "aws", Expected: &v, Team: team}); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := estate.Drivers(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, d := range ds {
		if d.Kind == "announced" {
			seen[d.Label] = d.Team
		}
	}
	if seen["swap data-eng"] != "data-eng" {
		t.Errorf("a named team read back as %q, want data-eng", seen["swap data-eng"])
	}
	if team, ok := seen["swap "]; !ok || team != "" {
		t.Errorf("an unnamed split read back team %q (found %v), want empty", team, ok)
	}
}
