package finops_test

// ANNOUNCED-DRIVER-SPEC.md section 11: an owner's answer on a decision that names a
// team writes that team into the drivers row, and the detector then moves that
// team's series alone. Through ApplyAs, the path the owner's stamp takes.
//
// Red against the code before this step, shown by stubbing the team out of the
// written row: the row names no team, the step is split, and the other teams
// take shares of it.

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// teamRec is the recorder the detection pass writes to; the test does not read it.
type teamRec struct{}

func (teamRec) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	return nil
}

func TestAnOwnersAnswerNamingATeamWritesThatTeamAndMovesOnlyIt(t *testing.T) {
	db := applyTestDB(t)
	// svc-g2 on aws: data-eng spends 400.00 a day until 31 July and 100.00 a day
	// from 1 August, the whole 300.00 the decision names. The other three teams
	// stay where they were.
	annSpendTeam(t, db, "aws", "svc-g2", "data-eng", "2026-07-01", 31, 400_00)
	annSpendTeam(t, db, "aws", "svc-g2", "data-eng", "2026-08-01", 30, 100_00)
	annSpendTeam(t, db, "aws", "svc-g2", "product-web", "2026-07-01", 61, 200_00)
	annSpendTeam(t, db, "aws", "svc-g2", "sre-platform", "2026-07-01", 61, 300_00)
	annSpendTeam(t, db, "aws", "svc-g2", "ml-platform", "2026-07-01", 61, 100_00)

	target := `{"scope":"svc-g2","start":"2026-08-01","end":"2026-08-30","daily_cents":-30000,"team":"data-eng"}`
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.announced", "data-eng trims 300", target)
	if err := finops.ApplyAs(db, opt, crew.OwnerAnswer("y.mercer"), nil); err != nil {
		t.Fatal(err)
	}

	var team sql.NullString
	if err := db.QueryRow(`SELECT team FROM drivers WHERE kind='announced' AND scope='svc-g2'`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if team.String != "data-eng" {
		t.Fatalf("the drivers row names team %q (valid %v), want data-eng: a named decision was written as a split",
			team.String, team.Valid)
	}

	if _, _, err := anomaly.Run(db, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), detect.Default(), teamRec{}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT team, day FROM anomalies WHERE service='svc-g2' ORDER BY team, day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var raised []string
	for rows.Next() {
		var team, day string
		if err := rows.Scan(&team, &day); err != nil {
			t.Fatal(err)
		}
		raised = append(raised, fmt.Sprintf("%s %s", team, day))
	}
	if len(raised) != 0 {
		t.Fatalf("the named team landed its whole step and the others took none, yet the detector raised: %s",
			strings.Join(raised, "; "))
	}
}
