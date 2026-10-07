// costcrew-usage fetches a model provider's own usage and cost reports into a
// folder the console's anthropic-usage or openai-usage reader reads.
//
// It exists as a separate binary because the console makes no outbound call
// of its own (invariant 64): the console reads the folder, and this is the
// one thing that talks to the provider. It only ever sends GET requests to
// the four read-only report endpoints named below, and it never calls a
// model.
//
//	costcrew-usage -vendor anthropic -out ./usage/anthropic -from 2026-09-01 -to 2026-09-30
//	costcrew-usage -vendor openai    -out ./usage/openai    -dry-run
//
// The key comes from the environment, ANTHROPIC_ADMIN_KEY or
// OPENAI_ADMIN_KEY, and goes nowhere else: it is never written to the
// folder, never printed, and never sent to a host other than the one the
// request was made to (redirects are refused, because Go's client forwards a
// custom header such as x-api-key to wherever a redirect points).
//
// What it writes is the providers' own pages, verbatim, one JSON array per
// report, named <report>-<fetched at>-<from>-<to>.json. Before a byte is
// written the pages are read by the SAME parser the console uses
// (connectors.ParseProviderUsageFile), so a response the console would
// refuse is refused here, and nothing half-checked lands in the folder.
//
// Endpoints, pinned 2026-10-07 (see internal/connectors/providerusage.go for
// the fields read and where each was read from):
//
//	anthropic  GET https://api.anthropic.com/v1/organizations/usage_report/messages
//	           GET https://api.anthropic.com/v1/organizations/cost_report
//	           headers x-api-key, anthropic-version: 2023-06-01
//	           arrays as group_by[]=..., times as RFC 3339
//	openai     GET https://api.openai.com/v1/organization/usage/completions
//	           GET https://api.openai.com/v1/organization/costs
//	           header Authorization: Bearer
//	           arrays as group_by=... repeated (OpenAPI's default form/explode),
//	           times as Unix seconds
//
// Neither provider's documentation names a charge for these endpoints.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

// maxPageBytes bounds one response. A variable only so a test can lower it.
var maxPageBytes int64 = 32 << 20

const (
	maxPages      = 400 // one report; 31 daily buckets a page, so years of data
	maxWindowDays = 366
	bucketsPage   = 31
	userAgent     = "costcrew-usage (https://github.com/TAIPANBOX/costcrew)"
)

type vendor struct {
	name, connector, envKey, base string
	reports                       map[string]string // report -> path
}

var vendors = map[string]vendor{
	"anthropic": {
		name: "anthropic", connector: "anthropic-usage", envKey: "ANTHROPIC_ADMIN_KEY",
		base: "https://api.anthropic.com",
		reports: map[string]string{
			"usage": "/v1/organizations/usage_report/messages",
			"cost":  "/v1/organizations/cost_report",
		},
	},
	"openai": {
		name: "openai", connector: "openai-usage", envKey: "OPENAI_ADMIN_KEY",
		base: "https://api.openai.com",
		reports: map[string]string{
			"usage": "/v1/organization/usage/completions",
			"cost":  "/v1/organization/costs",
		},
	},
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr, time.Now))
}

func run(args []string, env func(string) string, stdout, stderr io.Writer, now func() time.Time) int {
	// flag.CommandLine is replaced on every call, the way tools/bench does,
	// so the flags are defined with flag.String (which internal/manifest
	// reads to hold components.json to them) and a test can call run twice.
	flag.CommandLine = flag.NewFlagSet("costcrew-usage", flag.ContinueOnError)
	flag.CommandLine.SetOutput(stderr)
	vendorName := flag.String("vendor", "", "anthropic or openai")
	out := flag.String("out", "", "the folder to write into: the path saved on the console's connector")
	from := flag.String("from", "", "first day, 2006-01-02 (default: 30 days before -to)")
	to := flag.String("to", "", "last day, inclusive, 2006-01-02 (default: yesterday, UTC)")
	dry := flag.Bool("dry-run", false, "fetch and check, print what would be written, write nothing")
	base := flag.String("base-url", "", "the provider's API origin (default: the provider's own); https only, except a loopback address")
	if err := flag.CommandLine.Parse(args); err != nil {
		return 2
	}
	v, ok := vendors[*vendorName]
	if !ok {
		fmt.Fprintln(stderr, "-vendor must be anthropic or openai")
		return 2
	}
	if !*dry && strings.TrimSpace(*out) == "" {
		fmt.Fprintln(stderr, "-out is required unless -dry-run is given")
		return 2
	}
	origin := v.base
	if *base != "" {
		o, err := checkBase(*base)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		origin = o
	}
	start, endExcl, err := window(*from, *to, now().UTC())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	key := strings.TrimSpace(env(v.envKey))
	if key == "" {
		fmt.Fprintf(stderr, "%s is not set: an admin key is read from the environment and nowhere else\n", v.envKey)
		return 2
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
		// Never follow a redirect: the key travels in a header Go would copy
		// to the redirect's target, whatever host that is.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	fetchedAt := now().UTC().Format("20060102T150405Z")
	lastDay := endExcl.AddDate(0, 0, -1).Format("2006-01-02")
	for _, report := range []string{"usage", "cost"} {
		pages, err := fetch(context.Background(), client, v, origin, key, report, start, endExcl)
		if err != nil {
			fmt.Fprintf(stderr, "%s %s report: %s. Nothing was written for it.\n", v.name, report, redact(err.Error(), key))
			return 1
		}
		body := joinPages(pages)
		rows, days, err := connectors.ParseProviderUsageFile(v.connector, report, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			fmt.Fprintf(stderr, "%s %s report: the response does not pass the checks the console reads with: %s. "+
				"Nothing was written for it.\n", v.name, report, redact(err.Error(), key))
			return 1
		}
		name := fmt.Sprintf("%s-%s-%s-%s.json", report, fetchedAt, start.Format("2006-01-02"), lastDay)
		if *dry {
			fmt.Fprintf(stdout, "Would write %s: %d page%s, %d day%s (%s to %s), %d row%s.\n",
				name, len(pages), s(len(pages)), len(days), s(len(days)), days[0], days[len(days)-1], rows, s(rows))
			continue
		}
		if err := writeFile(*out, name, body); err != nil {
			fmt.Fprintf(stderr, "%s %s report: %v\n", v.name, report, err)
			return 1
		}
		fmt.Fprintf(stdout, "Wrote %s: %d page%s, %d day%s (%s to %s), %d row%s.\n",
			filepath.Join(*out, name), len(pages), s(len(pages)), len(days), s(len(days)), days[0], days[len(days)-1], rows, s(rows))
	}
	if *dry {
		fmt.Fprintln(stdout, "Dry run: nothing was written.")
	}
	return 0
}

func s(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// checkBase allows the provider's origin to be replaced, for a proxy or a
// test, and refuses anything that would send an admin key in the clear to
// another machine.
func checkBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("-base-url %q must be an origin such as https://api.example.com", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("-base-url %q is plain http to another machine; an admin key is only sent over https", raw)
		}
	default:
		return "", fmt.Errorf("-base-url %q must be https", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func window(from, to string, now time.Time) (time.Time, time.Time, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	last := today.AddDate(0, 0, -1)
	if to != "" {
		t, err := time.Parse("2006-01-02", to)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("-to %q is not a day written 2006-01-02", to)
		}
		if t.After(today) {
			return time.Time{}, time.Time{}, fmt.Errorf("-to %s is in the future", to)
		}
		last = t
	}
	first := last.AddDate(0, 0, -30)
	if from != "" {
		f, err := time.Parse("2006-01-02", from)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("-from %q is not a day written 2006-01-02", from)
		}
		first = f
	}
	if first.After(last) {
		return time.Time{}, time.Time{}, errors.New("-from is after -to")
	}
	if last.Sub(first) >= maxWindowDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("the window is longer than %d days", maxWindowDays)
	}
	return first, last.AddDate(0, 0, 1), nil
}

// query is the request each report needs, in the form each provider reads.
func query(v vendor, report string, start, endExcl time.Time, page string) url.Values {
	q := url.Values{}
	q.Set("bucket_width", "1d")
	q.Set("limit", fmt.Sprint(bucketsPage))
	var group []string
	if v.name == "anthropic" {
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("ending_at", endExcl.Format(time.RFC3339))
		if report == "usage" {
			group = []string{"model", "api_key_id", "workspace_id"}
		} else {
			// The cost report groups by description and workspace only; the
			// description is what carries the model and the token type.
			group = []string{"description", "workspace_id"}
		}
		for _, g := range group {
			q.Add("group_by[]", g)
		}
	} else {
		q.Set("start_time", fmt.Sprint(start.Unix()))
		q.Set("end_time", fmt.Sprint(endExcl.Unix()))
		if report == "usage" {
			group = []string{"model", "api_key_id", "project_id"}
		} else {
			group = []string{"line_item", "api_key_id", "project_id"}
		}
		for _, g := range group {
			q.Add("group_by", g)
		}
	}
	if page != "" {
		q.Set("page", page)
	}
	return q
}

func fetch(ctx context.Context, client *http.Client, v vendor, origin, key, report string, start, endExcl time.Time) ([]json.RawMessage, error) {
	var pages []json.RawMessage
	seen := map[string]bool{}
	page := ""
	for {
		if len(pages) >= maxPages {
			return nil, fmt.Errorf("more than %d pages: stopped rather than follow the provider indefinitely", maxPages)
		}
		u := origin + v.reports[report] + "?" + query(v, report, start, endExcl, page).Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		if v.name == "anthropic" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("the request failed: %v", err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("reading the response: %v", rerr)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("the provider refused the key (HTTP %d): it may not have admin scope; %s",
				resp.StatusCode, snippet(body))
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			return nil, fmt.Errorf("the provider answered with a redirect (HTTP %d), which is not followed "+
				"because the key would travel with it", resp.StatusCode)
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("the provider answered HTTP %d; %s", resp.StatusCode, snippet(body))
		}
		if int64(len(body)) > maxPageBytes {
			return nil, fmt.Errorf("one response is over %d MB", maxPageBytes>>20)
		}
		more, next, err := pageCursor(body)
		if err != nil {
			return nil, err
		}
		pages = append(pages, json.RawMessage(body))
		if !more {
			return pages, nil
		}
		if next == "" {
			return nil, errors.New("a page says has_more but gives no next_page")
		}
		if seen[next] {
			return nil, errors.New("the provider handed back a next_page it had already given: a pagination loop, stopped")
		}
		seen[next] = true
		page = next
	}
}

// pageCursor reads only the two fields that steer paging, by exact key.
func pageCursor(body []byte) (bool, string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return false, "", fmt.Errorf("the response is not a JSON object: %v", err)
	}
	var more bool
	raw, ok := top["has_more"]
	if !ok {
		return false, "", errors.New("the response has no has_more")
	}
	if err := json.Unmarshal(raw, &more); err != nil {
		return false, "", errors.New("has_more is not true or false")
	}
	var next *string
	if raw, ok := top["next_page"]; ok {
		if err := json.Unmarshal(raw, &next); err != nil {
			return false, "", errors.New("next_page is not a string or null")
		}
	}
	if next == nil {
		return more, "", nil
	}
	if len(*next) > 4096 {
		return false, "", errors.New("next_page is over 4096 bytes")
	}
	return more, *next, nil
}

func snippet(body []byte) string {
	const max = 300
	s := strings.ToValidUTF8(string(body), "?")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max] + "..."
	}
	if s == "" {
		return "no body"
	}
	return "the provider said: " + s
}

// redact removes the key from anything printed, in case a provider ever
// echoes it back in an error body.
func redact(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "[the admin key]")
}

func joinPages(pages []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, p := range pages {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(bytes.TrimSpace(p))
	}
	b.WriteByte(']')
	return b.Bytes()
}

// writeFile writes atomically and privately: a half-written file must never
// be one the console can read, and a usage report names the organisation's
// keys and workspaces.
func writeFile(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".costcrew-usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
