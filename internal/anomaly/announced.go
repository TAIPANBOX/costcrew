package anomaly

// Which drivers a series judges its days against (ANNOUNCED-DRIVER-SPEC.md
// section 11). The detector judges each (desk, team, service) series on its own,
// so an announced decision must reach each series in the part of the step that
// belongs to it, and not the whole step again on every team.

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// splitKey identifies one announced decision's split across teams.
type splitKey struct {
	desk, service, start string
	daily                int64
}

// splitCache holds each decision's split, so one detection pass reads each
// decision's medians once however many series the decision reaches.
type splitCache map[splitKey]map[string]money.Cents

// sharesOf is the step each team on the decision's desk takes, from the medians
// of the days before its start.
func (c splitCache) sharesOf(db *sql.DB, d world.Driver) (map[string]money.Cents, error) {
	key := splitKey{desk: d.Source, service: d.Scope, start: d.Start, daily: *d.Expected}
	if s, ok := c[key]; ok {
		return s, nil
	}
	medians, err := estate.TeamSpendMedians(db, d.Source, d.Scope, d.Start)
	if err != nil {
		return nil, err
	}
	s := estate.SplitByMedian(money.Cents(*d.Expected), medians)
	c[key] = s
	return s, nil
}

// seriesDrivers is the list one series judges its days against. Every driver
// that is not announced is exactly as it was. An announced decision takes part
// only on its own desk. A decision that names a team takes its whole step on
// that team's series and no other. An unnamed one takes each team's own share,
// and a series whose share is zero takes no part at all.
func seriesDrivers(db *sql.DB, k estate.SeriesKey, all []world.Driver, splits splitCache) ([]detect.Driver, error) {
	out := make([]detect.Driver, 0, len(all))
	for _, d := range all {
		dd := detect.Driver{Start: d.Start, End: d.End, Scope: d.Scope, Label: d.Label, Kind: d.Kind}
		if d.Expected != nil {
			c := money.Cents(*d.Expected)
			dd.Expected = &c
		}
		if d.Kind == "announced" && d.Expected != nil {
			if d.Source != k.Source {
				continue
			}
			if d.Team != "" {
				if d.Team != k.Team {
					continue
				}
			} else {
				shares, err := splits.sharesOf(db, d)
				if err != nil {
					return nil, err
				}
				share := shares[k.Team]
				if share == 0 {
					continue
				}
				dd.Expected = &share
			}
		}
		out = append(out, dd)
	}
	return out, nil
}
