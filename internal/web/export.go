package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// writeCSV sends a table as a download.
//
// UseCRLF is not a preference. Python's csv.writer terminates every line with
// \r\n by default, the original relies on that default, and the golden master
// is 65 lines each ending in one. A file with the other ending is a different
// file to every byte-level check, so this is the one place the port has no
// freedom at all.
func writeCSV(w http.ResponseWriter, filename string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	_ = cw.Write(header)
	_ = cw.WriteAll(rows)
}

func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func (s *Server) exportBudget(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "aws"
	}
	rows, err := estate.BudgetVsActual(s.db, source, 20, true)
	if err != nil {
		// A source with no charges is a real answer, not a failure: the header
		// alone is what the original returns, and it is what the golden master
		// records for the ai and saas sources.
		writeCSV(w, "budget-vs-actual-"+source+".csv", budgetHeader, nil)
		return
	}
	out := make([][]string, 0, len(rows))
	for _, b := range rows {
		pct := ""
		if b.HasPct {
			pct = strconv.FormatFloat(b.VarPct, 'f', 1, 64)
		}
		out = append(out, []string{
			b.Month, source, b.Team, money(b.Budget), money(b.Actual), money(b.Var), pct,
		})
	}
	writeCSV(w, "budget-vs-actual-"+source+".csv", budgetHeader, out)
}

var budgetHeader = []string{
	"month", "source", "team", "budget_usd", "actual_usd", "variance_usd", "variance_pct",
}

func (s *Server) exportRequests(w http.ResponseWriter, r *http.Request) {
	reqs, err := estate.Requests(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	out := make([][]string, 0, len(reqs))
	for _, q := range reqs {
		out = append(out, []string{
			strconv.Itoa(q.ID), q.Source, q.Team, q.Title, q.Kind,
			money(q.Est), q.Status, q.TargetMonth, q.Note,
		})
	}
	writeCSV(w, "requests.csv", []string{
		"id", "source", "team", "title", "kind", "est_monthly_usd",
		"status", "target_month", "note",
	}, out)
}

// dumpFigures is the machine-readable side of the parity oracle: every number
// the console computes, with nothing of the page around it.
//
// It exists because the HTML is now free to differ. Holding two
// implementations to identical markup costs a great deal and proves little,
// while holding them to identical ARITHMETIC costs almost nothing and is the
// only thing a FinOps console must never get wrong. This endpoint is what the
// gate compares once the pages stop being byte-identical.
func (s *Server) dumpFigures(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "aws"
	}
	rows, err := estate.BudgetVsActual(s.db, source, 20, true)
	if err != nil {
		http.Error(w, fmt.Sprintf("no such source %q", source), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"source":%q,"budget_rows":%d}`, source, len(rows))
}
