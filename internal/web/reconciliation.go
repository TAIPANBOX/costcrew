package web

// The reconciliation page and its CSV: the provider's own cost per model per
// day beside the sum of the gateway's rows for the same model and day, the
// gap as its own column, and a status. Read-only: it has no form, and it
// reads the store without creating a table, so a GET changes nothing.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

var tplReconciliation = page("reconciliation.html")

// reconView is one row as the page prints it: every amount with all six
// decimals, because a reconciliation rounded to cents hides the drift it
// exists to show.
type reconView struct {
	Day, Model, Provider, Gateway, Gap, Status, Chip string
	ProviderIn, GatewayIn, ProviderOut, GatewayOut   string
}

func statusChip(s string) string {
	switch s {
	case connectors.StatusMatched:
		return "explained"
	case connectors.StatusProviderMissing:
		return "triaged"
	}
	return "open"
}

func (s *Server) reconcileFor(w http.ResponseWriter, r *http.Request) (connectors.Reconciliation, connectors.Connector, bool) {
	id := r.URL.Query().Get("connector")
	if id == "" {
		id = "anthropic-usage"
	}
	var c connectors.Connector
	found := false
	for _, rc := range connectors.ReconcileConnectors() {
		if rc.ID == id {
			c, found = rc, true
		}
	}
	if !found {
		http.Error(w, "no reconciliation for that connector", http.StatusNotFound)
		return connectors.Reconciliation{}, c, false
	}
	rec, err := connectors.Reconcile(s.db, id, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		http.Error(w, "cannot reconcile: "+err.Error(), http.StatusBadRequest)
		return rec, c, false
	}
	return rec, c, true
}

func tokens(n int64, covered bool) string {
	if !covered && n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func (s *Server) reconciliation(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	rec, c, ok := s.reconcileFor(w, r)
	if !ok {
		return
	}
	rows := make([]reconView, 0, len(rec.Rows))
	for _, x := range rec.Rows {
		rows = append(rows, reconView{
			Day: x.Day, Model: modelLabel(x.Model),
			Provider: connectors.MicrosExact(x.ProviderMicros), Gateway: connectors.MicrosExact(x.GatewayMicros),
			Gap: connectors.MicrosExact(x.GapMicros), Status: x.Status, Chip: statusChip(x.Status),
			ProviderIn: tokens(x.ProviderIn, x.UsageCovered), GatewayIn: strconv.FormatInt(x.GatewayIn, 10),
			ProviderOut: tokens(x.ProviderOut, x.UsageCovered), GatewayOut: strconv.FormatInt(x.GatewayOut, 10),
		})
	}
	s.render(w, tplReconciliation, struct {
		shell
		C                             connectors.Connector
		All                           []connectors.Connector
		Rec                           connectors.Reconciliation
		Rows                          []reconView
		Provider, Gateway, Gap        string
		Matched, Under, Over, Missing int
		CSV                           string
	}{s.shellFor(r, "Reconciliation", "ai"), c, connectors.ReconcileConnectors(), rec, rows,
		connectors.MicrosExact(rec.ProviderMicros), connectors.MicrosExact(rec.GatewayMicros),
		connectors.MicrosExact(rec.GapMicros),
		rec.Counts[connectors.StatusMatched], rec.Counts[connectors.StatusGatewayUnder],
		rec.Counts[connectors.StatusGatewayOver], rec.Counts[connectors.StatusProviderMissing],
		"/export/reconciliation.csv?connector=" + c.ID + "&from=" + rec.From + "&to=" + rec.To})
}

func modelLabel(m string) string {
	if m == "" {
		return "(no model: not a token cost, or not grouped by model)"
	}
	return m
}

// csvCell keeps a value a spreadsheet would run as a formula from being one.
// A model name comes from a provider's JSON, which this console does not
// control, and "=HYPERLINK(...)" is a valid string there.
func csvCell(v string) string {
	if v != "" && strings.ContainsAny(v[:1], "=+-@\t\r") {
		return "'" + v
	}
	return v
}

func (s *Server) exportReconciliation(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	rec, c, ok := s.reconcileFor(w, r)
	if !ok {
		return
	}
	out := make([][]string, 0, len(rec.Rows)+1)
	for _, x := range rec.Rows {
		out = append(out, []string{x.Day, csvCell(x.Model),
			connectors.MicrosExact(x.ProviderMicros), connectors.MicrosExact(x.GatewayMicros),
			connectors.MicrosExact(x.GapMicros), x.Status,
			tokens(x.ProviderIn, x.UsageCovered), strconv.FormatInt(x.GatewayIn, 10),
			tokens(x.ProviderOut, x.UsageCovered), strconv.FormatInt(x.GatewayOut, 10)})
	}
	// The gap over the whole window is a ROW, not a footnote: a file whose
	// lines do not add up to the provider's bill is one somebody reconciles
	// by hand, and the difference has to be there to be found.
	out = append(out, []string{"total", rec.From + " to " + rec.To,
		connectors.MicrosExact(rec.ProviderMicros), connectors.MicrosExact(rec.GatewayMicros),
		connectors.MicrosExact(rec.GapMicros), "", "", "", "", ""})
	writeCSV(w, "reconciliation-"+c.ID+"-"+rec.From+"-"+rec.To+".csv", []string{
		"day", "model", "provider_usd", "gateway_usd", "gap_usd", "status",
		"provider_tokens_in", "gateway_tokens_in", "provider_tokens_out", "gateway_tokens_out"}, out)
}
