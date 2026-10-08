package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

const testKey = "sk-ant-admin01-TESTKEY-never-anywhere"

var fixedNow = func() time.Time { return time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC) }

func anthropicBucket(day string, results string) string {
	t, _ := time.Parse("2006-01-02", day)
	return fmt.Sprintf(`{"starting_at":"%s","ending_at":"%s","results":[%s]}`,
		t.Format(time.RFC3339), t.AddDate(0, 0, 1).Format(time.RFC3339), results)
}

// fakeAnthropic serves the two reports, the usage one over two pages, and
// records every request it sees.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req.Clone(context.Background()))
}

func fakeAnthropic(t *testing.T, rec *recorder) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.Header.Get("x-api-key") != testKey {
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key %s"}}`, r.Header.Get("x-api-key"))
			return
		}
		switch r.URL.Path {
		case "/v1/organizations/usage_report/messages":
			if r.URL.Query().Get("page") == "" {
				fmt.Fprintf(w, `{"data":[%s],"has_more":true,"next_page":"page_2"}`,
					anthropicBucket("2026-10-05", `{"model":"claude-haiku-4-5","api_key_id":"apikey_A","workspace_id":null,"uncached_input_tokens":10,"cache_read_input_tokens":0,"output_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0}}`))
				return
			}
			fmt.Fprintf(w, `{"data":[%s],"has_more":false,"next_page":null}`, anthropicBucket("2026-10-06", ""))
		case "/v1/organizations/cost_report":
			fmt.Fprintf(w, `{"data":[%s,%s],"has_more":false,"next_page":null}`,
				anthropicBucket("2026-10-05", `{"amount":"12.5","currency":"USD","model":"claude-haiku-4-5","token_type":"output_tokens","cost_type":"tokens","workspace_id":null,"description":"x"}`),
				anthropicBucket("2026-10-06", ""))
		default:
			w.WriteHeader(404)
		}
	}))
}

func runTool(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &out, &errb, fixedNow)
	return code, out.String(), errb.String()
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAnthropicFetchWritesWhatTheConsoleReads(t *testing.T) {
	rec := &recorder{}
	srv := fakeAnthropic(t, rec)
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "usage")
	code, out, errs := runTool(t, []string{"-vendor", "anthropic", "-out", dir, "-from", "2026-10-05",
		"-to", "2026-10-06", "-base-url", srv.URL}, map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	names := dirFiles(t, dir)
	want := []string{"cost-20261007T093000Z-2026-10-05-2026-10-06.json", "usage-20261007T093000Z-2026-10-05-2026-10-06.json"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("files %v, want %v", names, want)
	}
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600: a usage report names the organisation's keys", n, fi.Mode().Perm())
		}
	}
	if !strings.Contains(out, "2 pages, 2 days (2026-10-05 to 2026-10-06)") {
		t.Errorf("the usage line does not say both pages were followed: %s", out)
	}
	// Every request: a GET, with the admin key and the version header, daily
	// buckets, the window asked for, and the groupings the reconciliation needs.
	if len(rec.reqs) != 3 {
		t.Fatalf("%d requests, want 3 (two usage pages and one cost page)", len(rec.reqs))
	}
	for _, r := range rec.reqs {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("anthropic-version") != "2023-06-01" ||
			q.Get("bucket_width") != "1d" || q.Get("starting_at") != "2026-10-05T00:00:00Z" ||
			q.Get("ending_at") != "2026-10-07T00:00:00Z" {
			t.Errorf("request %s %s headers %v", r.Method, r.URL, r.Header)
		}
		groups := strings.Join(q["group_by[]"], ",")
		if r.URL.Path == "/v1/organizations/usage_report/messages" && groups != "model,api_key_id,workspace_id" {
			t.Errorf("usage grouped by %q", groups)
		}
		if r.URL.Path == "/v1/organizations/cost_report" && groups != "description,workspace_id" {
			t.Errorf("cost grouped by %q", groups)
		}
	}
	if rec.reqs[1].URL.Query().Get("page") != "page_2" {
		t.Errorf("the second usage request did not pass next_page: %s", rec.reqs[1].URL)
	}

	// The console's own reader reads what was written.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	if err := connectors.Save(db, "anthropic-usage", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	msg, err := connectors.Import(db, "anthropic-usage", false, connectors.ImportOptions{})
	if err != nil {
		t.Fatalf("the console refused what the tool wrote: %v", err)
	}
	if !strings.Contains(msg, "USD 0.125000") {
		t.Errorf("the console read %q, want the 12.5 cents as USD 0.125000", msg)
	}
	assertKeyNowhere(t, db, out+errs, dir)
}

func assertKeyNowhere(t *testing.T, db *sql.DB, printed, dir string) {
	t.Helper()
	if strings.Contains(printed, "TESTKEY") {
		t.Error("the admin key was printed")
	}
	for _, n := range dirFiles(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("TESTKEY")) {
			t.Errorf("the admin key was written into %s", n)
		}
	}
	if db != nil {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM connections WHERE config LIKE '%TESTKEY%' OR last_result LIKE '%TESTKEY%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Error("the admin key reached the console's store")
		}
	}
}

func TestOpenAIFetchSendsABearerAndRepeatedGroupBy(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		bucket := func(results string) string {
			return fmt.Sprintf(`{"object":"bucket","start_time":%d,"end_time":%d,"results":[%s]}`, start, start+86400, results)
		}
		switch r.URL.Path {
		case "/v1/organization/usage/completions":
			fmt.Fprintf(w, `{"object":"page","data":[%s],"has_more":false,"next_page":null}`,
				bucket(`{"object":"organization.usage.completions.result","input_tokens":10,"output_tokens":2,"num_model_requests":1,"model":"gpt-5-mini","api_key_id":"key_A","project_id":"proj_1"}`))
		case "/v1/organization/costs":
			fmt.Fprintf(w, `{"object":"page","data":[%s],"has_more":false,"next_page":null}`,
				bucket(`{"object":"organization.costs.result","amount":{"value":0.25,"currency":"usd"},"line_item":"gpt-5-mini, input","project_id":"proj_1","api_key_id":"key_A"}`))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	code, out, errs := runTool(t, []string{"-vendor", "openai", "-out", dir, "-from", "2026-10-05", "-to", "2026-10-05",
		"-base-url", srv.URL}, map[string]string{"OPENAI_ADMIN_KEY": testKey})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	for _, r := range rec.reqs {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer "+testKey || r.Header.Get("x-api-key") != "" {
			t.Errorf("%s: the key is not sent as a bearer token alone", r.URL.Path)
		}
		if q.Get("start_time") != fmt.Sprint(start) || q.Get("end_time") != fmt.Sprint(start+86400) {
			t.Errorf("%s: window %s to %s", r.URL.Path, q.Get("start_time"), q.Get("end_time"))
		}
		want := "model,api_key_id,project_id"
		if r.URL.Path == "/v1/organization/costs" {
			want = "line_item,api_key_id,project_id"
		}
		if got := strings.Join(q["group_by"], ","); got != want {
			t.Errorf("%s grouped by %q, want %q", r.URL.Path, got, want)
		}
	}
	assertKeyNowhere(t, nil, out+errs, dir)
}

func TestAPaginationLoopIsStoppedAndNothingIsWritten(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every page claims more and hands back the same cursor.
		fmt.Fprintf(w, `{"data":[%s],"has_more":true,"next_page":"page_again"}`, anthropicBucket("2026-10-05", ""))
	}))
	defer srv.Close()
	dir := t.TempDir()
	code, _, errs := runTool(t, []string{"-vendor", "anthropic", "-out", dir, "-from", "2026-10-05", "-to", "2026-10-05",
		"-base-url", srv.URL}, map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 1 || !strings.Contains(errs, "pagination loop") {
		t.Errorf("exit %d, %q; want 1 naming the pagination loop", code, errs)
	}
	if n := dirFiles(t, dir); len(n) != 0 {
		t.Errorf("files written after a pagination loop: %v", n)
	}
}

func TestAPageCapStopsAProviderThatNeverEnds(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprintf(w, `{"data":[],"has_more":true,"next_page":"p%d"}`, n)
	}))
	defer srv.Close()
	code, _, errs := runTool(t, []string{"-vendor", "anthropic", "-dry-run", "-from", "2026-10-05", "-to", "2026-10-05",
		"-base-url", srv.URL}, map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 1 || !strings.Contains(errs, "more than 400 pages") || n != maxPages {
		t.Errorf("exit %d after %d requests, %q", code, n, errs)
	}
}

func TestARefusedKeyIsReportedForScopeAndNeverEchoed(t *testing.T) {
	srv := fakeAnthropic(t, &recorder{})
	defer srv.Close()
	dir := t.TempDir()
	wrong := "sk-ant-api03-TESTKEY-an-ordinary-key"
	code, out, errs := runTool(t, []string{"-vendor", "anthropic", "-out", dir, "-base-url", srv.URL},
		map[string]string{"ANTHROPIC_ADMIN_KEY": wrong})
	if code != 1 || !strings.Contains(errs, "HTTP 401") || !strings.Contains(errs, "may not have admin scope") {
		t.Errorf("exit %d, %q; want the refusal named as a scope problem", code, errs)
	}
	// The fake echoes the key it was given in its error body.
	if strings.Contains(out+errs, wrong) || !strings.Contains(errs, "[the admin key]") {
		t.Errorf("the key the provider echoed back was printed: %q", errs)
	}
	if n := dirFiles(t, dir); len(n) != 0 {
		t.Errorf("files written after a refusal: %v", n)
	}
}

func TestARedirectIsNotFollowedSoTheKeyGoesNowhereElse(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	code, _, errs := runTool(t, []string{"-vendor", "anthropic", "-dry-run", "-base-url", srv.URL},
		map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 1 || !strings.Contains(errs, "redirect") || elsewhere != 0 {
		t.Errorf("exit %d, %d requests reached the redirect's target, %q", code, elsewhere, errs)
	}
}

func TestAResponseTheConsoleWouldRefuseIsNotWritten(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A cost amount as a number: the console refuses it, so the tool must too.
		fmt.Fprintf(w, `{"data":[%s],"has_more":false,"next_page":null}`,
			anthropicBucket("2026-10-05", `{"amount":12.5,"currency":"USD","model":"m","uncached_input_tokens":1,"cache_read_input_tokens":0,"output_tokens":1}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	code, _, errs := runTool(t, []string{"-vendor", "anthropic", "-out", dir, "-from", "2026-10-05", "-to", "2026-10-05",
		"-base-url", srv.URL}, map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 1 || !strings.Contains(errs, "does not pass the checks the console reads with") {
		t.Errorf("exit %d, %q", code, errs)
	}
	// usage passes (it ignores amount), cost does not: the usage file may
	// have landed, the cost file must not.
	for _, n := range dirFiles(t, dir) {
		if strings.HasPrefix(n, "cost-") {
			t.Errorf("a refused cost report was written: %s", n)
		}
	}
}

func TestAnOversizedResponseIsRefused(t *testing.T) {
	old := maxPageBytes
	maxPageBytes = 1024
	defer func() { maxPageBytes = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[],"has_more":false,"pad":"%s"}`, strings.Repeat("x", 4096))
	}))
	defer srv.Close()
	code, _, errs := runTool(t, []string{"-vendor", "anthropic", "-dry-run", "-base-url", srv.URL},
		map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 1 || !strings.Contains(errs, "over 0 MB") && !strings.Contains(errs, "one response is over") {
		t.Errorf("exit %d, %q", code, errs)
	}
}

func TestDryRunFetchesChecksAndWritesNothing(t *testing.T) {
	srv := fakeAnthropic(t, &recorder{})
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "never")
	code, out, errs := runTool(t, []string{"-vendor", "anthropic", "-out", dir, "-dry-run", "-from", "2026-10-05",
		"-to", "2026-10-06", "-base-url", srv.URL}, map[string]string{"ANTHROPIC_ADMIN_KEY": testKey})
	if code != 0 || !strings.Contains(out, "Would write usage-") || !strings.Contains(out, "nothing was written") {
		t.Errorf("exit %d\n%s\n%s", code, out, errs)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a dry run created the folder: %v", err)
	}
}

func TestTheFlagsAreCheckedBeforeAnyRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	key := map[string]string{"ANTHROPIC_ADMIN_KEY": testKey}
	for _, c := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{[]string{"-vendor", "anthropic", "-dry-run", "-base-url", srv.URL}, nil, "ANTHROPIC_ADMIN_KEY is not set"},
		{[]string{"-vendor", "gemini", "-dry-run"}, key, "anthropic or openai"},
		{[]string{"-vendor", "anthropic", "-base-url", srv.URL}, key, "-out is required"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-base-url", "http://api.example.com"}, key, "plain http to another machine"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-base-url", "https://x.example/v1?a=b"}, key, "must be an origin"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-base-url", "ftp://x.example"}, key, "must be https"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-from", "2024-01-01", "-to", "2026-01-01"}, key, "longer than 366 days"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-to", "2026-12-01"}, key, "in the future"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-from", "2026-10-06", "-to", "2026-10-05"}, key, "after -to"},
		{[]string{"-vendor", "anthropic", "-dry-run", "-from", "last week"}, key, "not a day"},
	} {
		code, _, errs := runTool(t, c.args, c.env)
		if code != 2 || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d %q, want 2 and %q", c.args, code, errs, c.want)
		}
	}
	if hits != 0 {
		t.Errorf("%d requests were made before the flags were checked", hits)
	}
}
