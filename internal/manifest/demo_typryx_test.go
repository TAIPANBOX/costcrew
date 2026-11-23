package manifest_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestDemoModeAsksTypryxNothing: a hosted typryx backend bills per answer, so
// a console started in demo mode with -typryx-url set asks it nothing.
func TestDemoModeAsksTypryxNothing(t *testing.T) {
	bin := buildConsole(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "a demo console must not ask", http.StatusTeapot)
	}))
	defer srv.Close()
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + itoa(port)
	cmd := exec.Command(bin, "-addr", addr, "-data", t.TempDir(), "-typryx-url", srv.URL)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "COSTCREW_DEMO=1"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if !waitFor(addr, 90*time.Second) {
		t.Fatalf("the console never listened on %s", addr)
	}
	time.Sleep(2 * time.Second) // the hint pass runs beside the listener
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("a demo console asked typryx %d time(s)", n)
	}
}
