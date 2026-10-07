package deliver

// The runner works four tasks at once, and every one of them masks a packet,
// masks tool results and puts names back, through the one policy the process
// has. This holds the policy under that, and is the test to run with -race.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAPolicyIsSafeAndConsistentWhenManyTasksUseItAtOnce(t *testing.T) {
	in := newInstallation(t)
	p := in.policy(t, PromptMasked)
	names := []string{"ml-platform", "data-eng", "research", "Amazon EC2", "BigQuery", "GKE"}

	// the tokens every goroutine must agree on
	want := map[string]string{}
	for _, n := range names {
		want[n] = p.MaskText(n)
		if want[n] == n {
			t.Fatalf("%q was not masked", n)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				n := names[(g+i)%len(names)]
				text := fmt.Sprintf("round %d: %s spent more than %s", i, n, names[(g+i+1)%len(names)])
				masked := p.MaskText(text)
				if strings.Contains(masked, n) {
					errs <- fmt.Sprintf("goroutine %d: %q survived in %q", g, n, masked)
					return
				}
				if !strings.Contains(masked, want[n]) {
					errs <- fmt.Sprintf("goroutine %d: %q was not the token %s in %q", g, n, want[n], masked)
					return
				}
				if back := p.Reidentify(masked); back != text {
					errs <- fmt.Sprintf("goroutine %d: round trip gave %q, want %q", g, back, text)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
