package web

import (
	"encoding/csv"
	"net/http"
	"strconv"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// writeCSV sends a table as a download.
//
// CRLF because that is what every spreadsheet on every platform opens without
// asking a question, which is the only audience a CSV export has.
func writeCSV(w http.ResponseWriter, filename string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	_ = cw.Write(header)
	_ = cw.WriteAll(rows)
}

var budgetHeader = []string{
	"month", "source", "team", "budget_usd", "actual_usd", "variance_usd",
	"variance_pct", "month_state",
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
