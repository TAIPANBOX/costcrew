package main

// How many tasks a live run keeps in flight at once, per engine, and what a
// run that names one task with -only says when that task cannot run.
//
// Measured on 2026-10-08 (an 8-vCPU VM, Ollama on CPU answering one request
// at a time): the runner kept four local tasks in flight, each round has a
// five-minute client timeout, and 17 of 19 tasks were blocked while they
// waited in the server's own queue. One at a time, all 19 finished. These
// tests replay that in miniature against a server on loopback.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// widthServer is a server on loopback that records how many requests it was
// handed at the same moment. hold is how long it keeps each one; serial makes
// it answer one at a time, the way Ollama on a CPU with one slot does, so a
// request that arrives while another is being answered WAITS in the server.
type widthServer struct {
	*httptest.Server
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	order    []string // "start:<path>" and "end:<path>" in the order they happened
	at       []time.Time
	work     sync.Mutex
}

func newWidthServer(t *testing.T, hold time.Duration, serial bool, body string) *widthServer {
	t.Helper()
	w := &widthServer{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			// The preflight probe's GET /models: there, and not a round.
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		w.mu.Lock()
		w.inFlight++
		w.maxSeen = max(w.maxSeen, w.inFlight)
		w.order = append(w.order, "start:"+r.URL.Path)
		w.at = append(w.at, time.Now())
		w.mu.Unlock()
		if serial {
			w.work.Lock()
		}
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
		}
		if serial {
			w.work.Unlock()
		}
		w.mu.Lock()
		w.inFlight--
		w.order = append(w.order, "end:"+r.URL.Path)
		w.at = append(w.at, time.Now())
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, body)
	}))
	t.Cleanup(w.Close)
	return w
}

func (w *widthServer) widest() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxSeen
}

func (w *widthServer) events() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.order...)
}

// first is when the first event with this prefix ("start:" or "end:")
// happened, and whether there was one.
func (w *widthServer) first(prefix string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, ev := range w.order {
		if strings.HasPrefix(ev, prefix) {
			return w.at[i], true
		}
	}
	return time.Time{}, false
}

// vendorStartedBefore is whether the vendor server saw its first request
// before the local server finished its first answer.
func vendorStartedBefore(vendor, local *widthServer) bool {
	v, ok := vendor.first("start:")
	if !ok {
		return false
	}
	l, ok := local.first("end:")
	return ok && v.Before(l)
}

// shortLocalRound shortens the per-round timeout on the local engine for one
// test, so a queue the server keeps can be seen to pass it in milliseconds
// rather than five minutes.
func shortLocalRound(t *testing.T, d time.Duration) {
	t.Helper()
	old := localRoundTimeout
	localRoundTimeout = d
	t.Cleanup(func() { localRoundTimeout = old })
}

// The incident, in miniature: a server that answers one request at a time,
// four local tasks, and a round timeout shorter than three answers. With four
// in flight the last ones wait in the server's queue past their timeout and are
// blocked; one at a time, which is the default on the local engine, every one
// of them finishes.
func TestOnAServerThatAnswersOneAtATimeNoLocalTaskIsBlockedWaiting(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 0, 0)
	shortLocalRound(t, 500*time.Millisecond)
	srv := newWidthServer(t, 200*time.Millisecond, true, localAnswer)
	db, tasks, analyst := runnerTasks(t, 4)
	ests := make([]estimate, 0, len(tasks))
	for _, tk := range tasks {
		ests = append(ests, localEstimate(tk, analyst, 0, 0))
	}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(500), MaxRunTokens: 10_000_000}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v\n%s", err, out)
	}
	var blocked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE state='blocked'`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 0 || !strings.Contains(out, "4 of 4 done, 0 blocked") {
		t.Errorf("%d of 4 local tasks were blocked waiting in a server that answers one at a time "+
			"(the server saw %d at once):\n%s", blocked, srv.widest(), out)
	}
	if w := srv.widest(); w != 1 {
		t.Errorf("the server was handed %d local requests at once, want 1: the default on the local "+
			"engine is one task at a time", w)
	}
}

// -local-parallel raises it, for a server that has the slots: three at once,
// and never a fourth.
func TestLocalParallelSetsHowManyLocalTasksRunAtOnce(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 0, 0)
	srv := newWidthServer(t, 300*time.Millisecond, false, localAnswer)
	db, tasks, analyst := runnerTasks(t, 5)
	ests := make([]estimate, 0, len(tasks))
	for _, tk := range tasks {
		ests = append(ests, localEstimate(tk, analyst, 0, 0))
	}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(500), MaxRunTokens: 10_000_000, LocalParallel: 3}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v\n%s", err, out)
	}
	if w := srv.widest(); w != 3 {
		t.Errorf("the server was handed %d local requests at once with -local-parallel 3, want 3", w)
	}
	if !strings.Contains(out, "5 of 5 done") {
		t.Errorf("not every task finished:\n%s", out)
	}
	if !strings.Contains(out, "3 at a time (-local-parallel 3)") {
		t.Errorf("the run does not say how many local tasks it runs at once:\n%s", out)
	}
}

// The vendor engines keep four: their far side has many slots and rate-limits
// rather than queues, and one at a time would make a sixty-task sprint an hour
// of somebody watching a terminal.
func TestVendorEnginesStillRunFourAtOnce(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newWidthServer(t, 300*time.Millisecond, false, anthropicAnswer)
	db, tasks, analyst := runnerTasks(t, 6)
	ests := make([]estimate, 0, len(tasks))
	for _, tk := range tasks {
		ests = append(ests, estimate{Task: tk, Analyst: analyst, Engine: "anthropic",
			Model: "m", WorstMicros: 1_000, Priced: true})
	}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 100, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v\n%s", err, out)
	}
	if w := srv.widest(); w != 4 {
		t.Errorf("the gateway was handed %d vendor requests at once, want 4", w)
	}
}

// A run with tasks on both: the vendor task is not held behind the local
// queue. It reaches its server while the first local task is still being
// answered, rather than waiting for a local slot it does not need.
func TestAVendorTaskIsNotHeldBehindTheLocalQueue(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 0, 0)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	local := newWidthServer(t, 400*time.Millisecond, true, localAnswer)
	vendor := newWidthServer(t, 10*time.Millisecond, false, openAIAnswer)
	old := openRouterEndpoint
	openRouterEndpoint = vendor.URL + "/direct"
	t.Cleanup(func() { openRouterEndpoint = old })

	db, tasks, analyst := runnerTasks(t, 3)
	// Two local tasks first and the vendor task last: an order the sort by
	// worst case can produce.
	ests := []estimate{
		localEstimate(tasks[0], analyst, 0, 0),
		localEstimate(tasks[1], analyst, 0, 0),
		{Task: tasks[2], Analyst: analyst, Engine: "openrouter", Model: "m", WorstMicros: 1_000, Priced: true},
	}
	// No gateway at all: both go direct, the local task to the operator's
	// server and the openrouter one to the trapped endpoint on loopback.
	gw := gatewayConfig{ModelURL: local.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(500), MaxRunTokens: 10_000_000}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 of 3 done") {
		t.Fatalf("not every task finished:\n%s", out)
	}
	// Both servers append to their own log; the question is whether the
	// vendor request started before the first local answer ended. Each
	// server's events carry no clock, so the local server is asked instead:
	// the vendor request must have been seen while the local server was
	// still holding its first request.
	if !vendorStartedBefore(vendor, local) {
		t.Errorf("the vendor task waited for the local queue: vendor %v, local %v",
			vendor.events(), local.events())
	}
}

// -only names one task, and when that task cannot run the run says why: the
// task's own refusal, not a sentence that lists every reason a task might
// have had.
func TestOnlyATaskThatWasRefusedSaysItsOwnReason(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 2)
	refused := estimate{Task: tasks[0], Analyst: analyst, Engine: "anthropic", Model: "m",
		Verdict: "worst case 0.2312 is past what is left of its guard, 0.0500", Refused: true, Priced: true}
	other := estimate{Task: tasks[1], Analyst: analyst, Engine: "anthropic", Model: "m",
		WorstMicros: 1_000, Priced: true}

	err := spend(db, nil, []estimate{refused, other}, 100, money.Cents(500), tasks[0].ID, bus{run: "crew-1"},
		gatewayConfig{})
	if err == nil {
		t.Fatal("a refused task named with -only was run")
	}
	want := fmt.Sprintf("task %d was refused: worst case 0.2312 is past what is left of its guard, 0.0500",
		tasks[0].ID)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q\nwant it to name the task's own refusal: %q", err, want)
	}
}

// A task -only names that is not among the open tasks this run priced (done,
// blocked, in another sprint, on another engine) is said to be that, not
// "refused".
func TestOnlyATaskThatIsNotOpenSaysSo(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 1)
	e := estimate{Task: tasks[0], Analyst: analyst, Engine: "anthropic", Model: "m",
		WorstMicros: 1_000, Priced: true}
	err := spend(db, nil, []estimate{e}, 100, money.Cents(500), 9999, bus{run: "crew-1"}, gatewayConfig{})
	if err == nil {
		t.Fatal("a task id that is not open ran something")
	}
	if !strings.Contains(err.Error(), "task 9999 is not among the open tasks this run priced") {
		t.Errorf("err = %q, want it to say task 9999 is not among the open tasks", err)
	}
	if strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %q calls a task that was never priced refused", err)
	}
}

// The per-task deadline starts when the task gets its slot, not when the run
// queued it: a task that waited behind others is given its whole deadline.
func TestWaitingForALocalSlotIsNotCountedAgainstTheTasksDeadline(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 0, 0)
	old := taskDeadline
	taskDeadline = func(string) time.Duration { return 400 * time.Millisecond }
	t.Cleanup(func() { taskDeadline = old })
	srv := newWidthServer(t, 250*time.Millisecond, false, localAnswer)
	db, tasks, analyst := runnerTasks(t, 3)
	ests := make([]estimate, 0, len(tasks))
	for _, tk := range tasks {
		ests = append(ests, localEstimate(tk, analyst, 0, 0))
	}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(500), MaxRunTokens: 10_000_000}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	// The third task waited about 500ms for its slot, past a 400ms deadline
	// counted from the start of the run; from its own slot it needs 250ms.
	if !strings.Contains(out, "3 of 3 done, 0 blocked") {
		t.Errorf("a task that waited for its slot was blocked by a deadline that counted the wait:\n%s", out)
	}
}

// -local-parallel is checked with the other local flags, before the store
// opens: a width below one or above what any server here has is a typo.
func TestLocalParallelIsValidatedBeforeTheStoreOpens(t *testing.T) {
	t.Cleanup(engines.ResetLocal)
	for _, n := range []int{-1, localParallelMax + 1, 400} {
		if _, err := (localOptions{Parallel: n}).apply(); err == nil ||
			!strings.Contains(err.Error(), "-local-parallel") {
			t.Errorf("-local-parallel %d: err = %v, want a refusal naming the flag", n, err)
		}
	}
	for _, n := range []int{0, 1, localParallelMax} {
		if _, err := (localOptions{Parallel: n}).apply(); err != nil {
			t.Errorf("-local-parallel %d was refused: %v", n, err)
		}
	}
	dir := t.TempDir()
	err := run(dir, "", 2000, 0, false, false, false, 0, "", "", "", "", "",
		localOptions{Parallel: -1}, "")
	if err == nil || !strings.Contains(err.Error(), "-local-parallel") {
		t.Fatalf("err = %v, want the -local-parallel refusal", err)
	}
	if entries, _ := readDirNames(dir); len(entries) != 0 {
		t.Errorf("the data directory holds %v: the store was opened before the flag was checked", entries)
	}
	// Unset is one, never the vendor width.
	if got := localParallel(gatewayConfig{}); got != 1 {
		t.Errorf("localParallel with -local-parallel unset = %d, want 1", got)
	}
	if got := localParallel(gatewayConfig{LocalParallel: 6}); got != 6 {
		t.Errorf("localParallel with -local-parallel 6 = %d, want 6", got)
	}
}
