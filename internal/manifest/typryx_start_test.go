package manifest_test

// Invariants 76 and 77 against the RUNNING console: typryx is asked after
// detection, beside the listener and never in front of it; with -typryx-url
// unset, nothing about the store changes.

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func buildConsole(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a process")
	}
	_, r := load(t)
	bin := filepath.Join(t.TempDir(), "console")
	build := exec.Command("go", "build", "-o", bin, "./cmd/costcrew")
	build.Dir = r
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the console: %v\n%s", err, out)
	}
	return bin
}

// startConsole runs the console on a free port over data, with extra flags,
// and waits until it listens. The environment is PATH and HOME only, so no
// COSTCREW_TYPRYX_* variable of the machine running the test leaks in.
func startConsole(t *testing.T, bin, data string, extra ...string) (*exec.Cmd, string) {
	t.Helper()
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + itoa(port)
	cmd := exec.Command(bin, append([]string{"-addr", addr, "-data", data}, extra...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if !waitFor(addr, 90*time.Second) {
		t.Fatalf("the console never listened on %s", addr)
	}
	return cmd, addr
}

func hintColumns(t *testing.T, data string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "app.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('anomalies') WHERE name LIKE 'hint_%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWithoutTypryxTheConsoleAddsNoHintColumn(t *testing.T) {
	bin := buildConsole(t)
	data := t.TempDir()
	_, addr := startConsole(t, bin, data)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if n := hintColumns(t, data); n != 0 {
		t.Errorf("a console started without -typryx-url added %d hint columns", n)
	}
}

// A typryx that never answers: the console listens anyway, while the pass
// is still waiting on it.
func TestATypryxThatHangsNeverHoldsTheConsolesStart(t *testing.T) {
	bin := buildConsole(t)
	var reached int32
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)

	data := t.TempDir()
	_, addr := startConsole(t, bin, data, "-typryx-url", hang.URL)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("the console does not answer while typryx hangs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz answered %d while typryx hangs", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&reached) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if atomic.LoadInt32(&reached) == 0 {
		t.Error("the console never asked typryx: this test measured nothing")
	}
}

// A typryx that answers: hints land on the anomalies and anomaly_hinted on
// the bus, and the bus line carries no field value.
func TestAStartWithTypryxStoresHintsAndReportsTheBackend(t *testing.T) {
	bin := buildConsole(t)
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/templates":
			_, _ = io.WriteString(w, `{"templates":[{"id":"triage.anomaly_class","version":"v","type":"choice",
				"fields":["anomaly","recent_changes"],
				"options":["expected_growth","misconfiguration","price_change","runaway_agent","unknown"]}]}`)
		case "/v1/ask":
			atomic.AddInt32(&asked, 1)
			_, _ = io.WriteString(w, `{"answer_id":"ans-start","template":"triage.anomaly_class","type":"choice",
				"answer":"unknown","backend":"stub","model":"stub-0",
				"probabilities":{"expected_growth":0.1,"misconfiguration":0.1,"price_change":0.1,"runaway_agent":0.1,"unknown":0.6}}`)
		}
	}))
	defer srv.Close()

	data := t.TempDir()
	events := filepath.Join(t.TempDir(), "costcrew.ndjson")
	cmd, _ := startConsole(t, bin, data, "-typryx-url", srv.URL, "-typryx-max-asks", "4",
		"-stack-events", events, "-stack-host", "costcrew.test")
	deadline := time.Now().Add(30 * time.Second)
	for atomic.LoadInt32(&asked) < 4 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	_ = cmd.Process.Signal(os.Interrupt)
	_, _ = cmd.Process.Wait()

	if got := atomic.LoadInt32(&asked); got != 4 {
		t.Errorf("typryx was asked %d times, want -typryx-max-asks 4", got)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "app.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var hinted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM anomalies WHERE hint_class='unknown' AND hint_backend='off'`).Scan(&hinted); err != nil {
		t.Fatal(err)
	}
	if hinted != 4 {
		t.Errorf("%d anomalies carry the hint, want 4", hinted)
	}
	b, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, `"type":"anomaly_hinted"`) {
			continue
		}
		n++
		if !strings.Contains(line, `"backend":"off"`) || strings.Contains(line, "robust deviations") {
			t.Errorf("an anomaly_hinted line does not name the backend, or carries a field value:\n%s", line)
		}
	}
	if n != 4 {
		t.Errorf("%d anomaly_hinted lines on the bus, want 4", n)
	}
}
