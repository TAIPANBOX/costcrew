package web

import (
	"net/http"
	"strconv"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

var budgetHeader = []csvCol{
	textCol("month"), textCol("source"), textCol("team"), numberCol("budget_usd"),
	numberCol("actual_usd"), numberCol("variance_usd"), numberCol("variance_pct"),
	textCol("month_state"),
}

func (s *Server) exportBudget(w http.ResponseWriter, r *http.Request) {
	// A download is a page. This one served every team's budget and actual
	// spend to anyone who could reach the port, because it returns a file
	// rather than HTML and so did not look like something behind a login.
	// Its eight neighbours in this package all guard; this one did not.
	if s.guard(w, r) == nil {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "aws"
	}
	rows, err := estate.BudgetVsActual(s.db, source)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	out := make([][]string, 0, len(rows))
	for _, b := range rows {
		// An empty cell, not "0.0" and not "inf". A variance against a budget
		// of nothing is not a percentage, and printing one invites somebody to
		// average a column of lies.
		pct := ""
		if b.HasPct {
			pct = strconv.FormatFloat(b.VariancePct, 'f', 1, 64)
		}
		state := "closed"
		if b.Open {
			state = "open, month to date"
		}
		out = append(out, []string{
			b.Month, b.Source, b.Team,
			b.Budget.String(), b.Actual.String(), b.Variance.String(), pct, state,
		})
	}
	writeCSV(w, "budget-vs-actual-"+source+".csv", budgetHeader, out)
}
