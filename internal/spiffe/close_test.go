package spiffe

import (
	"crypto/x509"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// fakeSource stands in for the workload API: it counts what is asked of it
// and, when it is closed, records whether the Source's lock was held.
type fakeSource struct {
	owner         *Source
	gets, closes  atomic.Int32
	getsAfter     atomic.Int32
	closed        atomic.Bool
	lockedOnClose atomic.Bool
}

func (f *fakeSource) GetX509SVID() (*x509svid.SVID, error) {
	f.gets.Add(1)
	if f.closed.Load() {
		f.getsAfter.Add(1)
	}
	return &x509svid.SVID{
		ID: spiffeid.RequireFromString("spiffe://example.test/costcrew"),
		Certificates: []*x509.Certificate{{
			NotAfter: time.Unix(2_000_000_000, 0), SerialNumber: big.NewInt(0xbeef),
		}},
	}, nil
}

func (f *fakeSource) Close() error {
	f.closes.Add(1)
	f.closed.Store(true)
	// TryLock fails while somebody else holds the write lock: the Close that
	// called this one, if it took the lock as it should.
	if f.owner.mu.TryLock() {
		f.owner.mu.Unlock()
	} else {
		f.lockedOnClose.Store(true)
	}
	return nil
}

func newFakeSource() (*Source, *fakeSource) {
	f := &fakeSource{}
	s := &Source{src: f}
	f.owner = s
	return s, f
}

// Close used to read and write s.closed with no lock while Identity, on
// another goroutine (every passport render), took the same mutex to read the
// identity. Close now decides under the lock, so the workload source is closed
// once and the flag is never read half written.
func TestCloseDecidesUnderTheLockAndClosesOnce(t *testing.T) {
	s, f := newFakeSource()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
	if n := f.closes.Load(); n != 1 {
		t.Errorf("the workload source was closed %d times, want once", n)
	}
	if !f.lockedOnClose.Load() {
		t.Errorf("the workload source was closed without the Source's lock held, " +
			"so the closed flag was read and written unguarded")
	}
}

// After Close, Identity answers with what the source last held and does not
// go back to a source it has closed.
func TestIdentityAfterCloseKeepsTheLastIdentityAndAsksNothing(t *testing.T) {
	s, f := newFakeSource()
	before := s.Identity()
	if before.ID != "spiffe://example.test/costcrew" || before.Serial != "beef" {
		t.Fatalf("identity before close: %+v", before)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after := s.Identity()
	if after != before {
		t.Errorf("identity after close %+v, want the last one %+v", after, before)
	}
	if n := f.getsAfter.Load(); n != 0 {
		t.Errorf("Identity asked a closed source %d time(s)", n)
	}
}

// Run under -race (CI does, for this package): Close and Identity on
// different goroutines, as the console does at shutdown while a page renders.
func TestCloseAndIdentityDoNotRace(t *testing.T) {
	s, _ := newFakeSource()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.On() }()
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
}
