package web

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Sorting happens on the SERVER, driven by the query string.
//
// No JavaScript. A sorted table is then a real URL somebody can bookmark, send
// to a colleague, or open twice and compare, and it keeps working with
// scripting off, in a text browser, and in whatever renders an emailed link.
// Client-side sorting buys a fractionally faster click and loses all of that.
type sortSpec struct {
	Col   string
	Desc  bool
	base  string // the path plus any filters, so sorting does not drop them
	param string // the query parameter this table listens to, "sort" by default
}

// key is the query parameter carrying the column, so two tables on one page do
// not fight over the same name.
func (s sortSpec) key() string {
	if s.param == "" {
		return "sort"
	}
	return s.param
}

// dirKey is the matching direction parameter.
func (s sortSpec) dirKey() string {
	if k := s.key(); k != "sort" {
		return k + "dir"
	}
	return "dir"
}

// readSort takes the column and direction off the query, keeping every other
// parameter, so sorting a filtered table does not silently unfilter it.
func readSort(r *http.Request, def string, defDesc bool) sortSpec {
	return readSortNamed(r, "sort", def, defDesc)
}

// Header renders one clickable column heading.
//
// Clicking the current column flips the direction; clicking another starts it
// in the direction that column is usually read in, which is descending for
// money and counts and ascending for names and dates. A table that always
// starts ascending makes the first click on "money" show the smallest number,
// which is never what anybody wanted.
func (s sortSpec) Header(col, label, class string, moneyish bool) template.HTML {
	desc := moneyish
	arrow := ""
	if s.Col == col {
		desc = !s.Desc
		if s.Desc {
			arrow = ` <span aria-hidden="true">▼</span>`
		} else {
			arrow = ` <span aria-hidden="true">▲</span>`
		}
	}
	dir := "asc"
	if desc {
		dir = "desc"
	}
	aria := ""
	if s.Col == col {
		if s.Desc {
			aria = ` aria-sort="descending"`
		} else {
			aria = ` aria-sort="ascending"`
		}
	}
	href := s.base + s.key() + "=" + url.QueryEscape(col) + "&" + s.dirKey() + "=" + dir
	cls := ""
	if class != "" {
		cls = ` class="` + class + `"`
	}
	return template.HTML(`<th` + cls + aria + `><a class="sortcol" href="` + href + `">` +
		template.HTMLEscapeString(label) + arrow + `</a></th>`)
}

// apply sorts a slice using the comparators a page declares for its columns.
//
// An unknown column falls back to the default rather than leaving the rows in
// whatever order the database happened to return, because "the sort did
// nothing" and "the sort put them in a different arbitrary order" look the
// same to a reader and only one of them is a bug they will report.
func applySort[T any](rows []T, s sortSpec, cmps map[string]func(a, b T) int, def string) {
	cmp, ok := cmps[s.Col]
	if !ok {
		cmp, ok = cmps[def]
		if !ok {
			return
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := cmp(rows[i], rows[j])
		if s.Desc {
			return c > 0
		}
		return c < 0
	})
}

func cmpString(a, b string) int { return strings.Compare(a, b) }

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int { return cmpInt64(int64(a), int64(b)) }

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// readSortNamed is readSort for a page with more than one sortable table.
//
// Two tables sharing one "sort" parameter is a trap: clicking a column on the
// second table silently reorders the first by a column it does not have, which
// falls back to its default and looks like the click did nothing.
func readSortNamed(r *http.Request, param, def string, defDesc bool) sortSpec {
	q := r.URL.Query()
	dirParam := param + "dir"
	if param == "sort" {
		dirParam = "dir" // the plain form, which is what a single-table page uses
	}
	s := sortSpec{Col: q.Get(param), Desc: q.Get(dirParam) == "desc", param: param}
	if s.Col == "" {
		s.Col, s.Desc = def, defDesc
	}
	q.Del(param)
	q.Del(dirParam)
	base := r.URL.Path
	if enc := q.Encode(); enc != "" {
		base += "?" + enc + "&"
	} else {
		base += "?"
	}
	s.base = base
	return s
}

// copyOf takes a copy before sorting.
//
// world.UtilisationRows and its siblings are package-level slices shared by
// every request. Sorting one in place would reorder what the next reader sees,
// and the fixture's own order is what several tests pin.
func copyOf[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// abs64 sorts a signed column by how big the move was rather than which way it
// went, which is what somebody scanning a true-up actually wants to see first.
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
