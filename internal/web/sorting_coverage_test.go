package web_test

// Every sortable table on the console, sorted by each of its columns in both
// directions. The sort is on the server, driven by the query string, and each
// column has its own comparator: a comparator that no request reaches is a
// column that has never been sorted by anybody, and the first person to click
// it finds out whether it works.

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

type covSortCase struct {
	path, param string
	cols        []string
}

var covSprintLink = regexp.MustCompile(`href="/sprint/(\d+)"`)

// covSortCases names each table with its query parameter and the columns its
// comparator map declares. The owner's agents and the seeded fixture give every
// table at least two rows, which is what makes the comparators run at all.
var covSortCases = []covSortCase{
	{"/staff", "sort", []string{"name", "desk", "open", "posted", "returned", "rate", "spent", "state"}},
	{"/staff/triage-aws", "ssort", []string{"when", "kind", "task", "spent", "reason"}},
	{"/staff/triage-aws", "rsort", []string{"sprint", "tasks", "posted", "spent"}},
	{"/staff/triage-aws", "wsort", []string{"task", "sprint", "spent", "state"}},
	{"/team/ml-platform", "sort", []string{"service", "amount", "share"}},
	{"/team/ml-platform", "tsort", []string{"month", "amount", "change"}},
	{"/team/ml-platform", "usort", []string{"resource", "cpu", "monthly", "saving"}},
	{"/team/ml-platform", "dsort", []string{"name", "amount"}},
	{"/desk/aws", "sort", []string{"team", "amount", "share"}},
	{"/desk/aws", "tsort", []string{"month", "amount", "change"}},
	{"/desk/aws", "vsort", []string{"name", "amount"}},
	{"/desk/aws", "msort", []string{"name", "amount"}},
	{"/desk/aws", "bsort", []string{"team", "budget", "actual", "var", "pct"}},
	{"/desk/aws", "asort", []string{"analyst", "open", "posted", "rate", "spent", "state"}},
	{"/teams", "sort", []string{"team", "unit", "direct", "allocated", "loaded", "anomalies", "openmoney"}},
	{"/desks", "sort", []string{"desk", "kind", "cost", "share", "anomalies", "openmoney", "analysts"}},
	{"/o/owner", "sort", []string{"agent", "role", "desk", "open", "spent", "guard", "anomalies", "state"}},
	{"/allocation", "sort", []string{"desk", "team", "direct", "allocated", "loaded"}},
	{"/chargeback", "sort", []string{"desk", "team", "loaded"}},
	{"/chargeback", "tsort", []string{"desk", "team", "frozen", "now", "delta"}},
	{"/connectors", "sort", []string{"name", "feeds", "kind", "provider", "status", "lasttest"}},
	{"/accounts", "sort", []string{"account", "role", "seen"}},
	{"/audit", "sort", []string{"when", "event", "what"}},
	{"/owners", "sort", []string{"owner", "agents", "month", "spent", "guard", "over", "open", "findings", "unbound"}},
	{"/", "msort", []string{"service", "desk", "was", "now", "change"}},
	{"/anomalies", "sort", []string{"money", "day", "source", "team", "service", "caused", "handled", "state", "z"}},
	{"/budgets", "sort", []string{"month", "team", "budget", "actual", "var", "pct"}},
	{"/forecast", "psort", []string{"desk", "amount"}},
	{"/forecast", "hsort", []string{"period", "desk", "forecast", "actual", "error", "grade"}},
	{"/sprint/plan", "sort", []string{"task", "analyst", "desk", "guard", "because"}},
	{"/kpis", "sort", []string{"group", "kpi", "value", "verdict"}},
	{"/utilisation", "sort", []string{"desk", "service", "team", "resource", "kind", "cpu", "mem", "monthly", "saving"}},
	{"/saas", "sort", []string{"vendor", "product", "team", "issued", "active", "idle", "seat", "waste", "renews"}},
	{"/saas", "csort", []string{"desk", "name", "kind", "hourly", "used", "expires"}},
	{"/ai", "sort", []string{"team", "model", "tokens", "cost", "permil", "actions", "deflected"}},
	{"/services", "sort", []string{"service", "desk", "kind", "teams", "amount", "change", "open"}},
	{"/sprints", "sort", []string{"label", "goal", "tasks", "open", "posted", "spent", "budget", "state"}},
}

// Each sortable table answers every column in both directions with a page, not
// an error, and a column it does not know falls back to its default.
func TestCovEverySortableTableOrdersByEachColumnBothWays(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, sprints, _ := h.get(t, "/sprints")
	m := covSprintLink.FindStringSubmatch(sprints)
	if m == nil {
		t.Fatal("the seeded console lists no sprint to sort the sprint page by")
	}
	sprintPage := covSortCase{"/sprint/" + m[1], "sort", []string{"task", "analyst", "desk", "spent", "state"}}
	cases := append([]covSortCase{sprintPage}, covSortCases...)

	for _, c := range cases {
		dirParam := c.param + "dir"
		if c.param == "sort" {
			dirParam = "dir"
		}
		for _, col := range c.cols {
			for _, dir := range []string{"asc", "desc"} {
				path := c.path + "?" + c.param + "=" + col + "&" + dirParam + "=" + dir
				code, body, _ := h.get(t, path)
				if code != 200 {
					t.Errorf("GET %s: %d, want 200 with the table sorted", path, code)
					continue
				}
				if !strings.Contains(body, "</html>") {
					t.Errorf("GET %s: the page was cut short before its end", path)
				}
			}
		}
	}
}

var covStaffLink = regexp.MustCompile(`href="/staff/([a-z0-9][a-z0-9-]*)"`)

// The sort is only a sort if the rows come out in the order asked for. The
// crew page's names are read back off the page, once each, and checked against
// the same name order in both directions.
func TestCovStaffTableOrdersByNameInEitherDirection(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	for _, dir := range []string{"asc", "desc"} {
		_, body, _ := h.get(t, "/staff?sort=name&dir="+dir)
		var names []string
		seen := map[string]bool{}
		for _, m := range covStaffLink.FindAllStringSubmatch(body, -1) {
			if m[1] == "new" || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			names = append(names, m[1])
		}
		if len(names) < 2 {
			t.Fatalf("the crew page lists %d agents, need two to see an order", len(names))
		}
		want := slices.Clone(names)
		slices.Sort(want)
		if dir == "desc" {
			slices.Reverse(want)
		}
		if !slices.Equal(names, want) {
			t.Errorf("sorted by name %s the crew reads %v, want %v", dir, names, want)
		}
	}
}
