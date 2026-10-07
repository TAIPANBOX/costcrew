package spiffe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// issued is one SVID the fake agent hands out.
type issued struct {
	id       string
	serial   int64
	notAfter time.Time
}

// fakeAgent is a SPIFFE Workload API served over a real socket, speaking the
// real protocol, so Open is judged against the wire go-spiffe speaks and not
// against a stub of the library. It issues from one CA. The go-spiffe fake of
// this is under its internal/ and cannot be imported; this is the part of it
// this package needs.
type fakeAgent struct {
	workload.UnimplementedSpiffeWorkloadAPIServer
	t      *testing.T
	caKey  *ecdsa.PrivateKey
	ca     *x509.Certificate
	caDER  []byte
	mu     sync.Mutex
	cur    *workload.X509SVIDResponse
	subs   map[chan *workload.X509SVIDResponse]struct{}
	srv    *grpc.Server
	Addr   string // as Open is told it: unix path or tcp://host:port
	noAuth bool   // answer PermissionDenied, like an agent with no entry for this binary
}

func newCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert, der
}

func (f *fakeAgent) response(s issued) *workload.X509SVIDResponse {
	f.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	uri, err := url.Parse(s.id)
	if err != nil {
		f.t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(s.serial),
		Subject:      pkix.Name{CommonName: "costcrew"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     s.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.ca, &key.PublicKey, f.caKey)
	if err != nil {
		f.t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		f.t.Fatal(err)
	}
	return &workload.X509SVIDResponse{Svids: []*workload.X509SVID{{
		SpiffeId: s.id, X509Svid: der, X509SvidKey: keyDER, Bundle: f.caDER,
	}}}
}

// FetchX509SVID is the one call a Source makes: send what is current, then
// send every replacement until the client goes away.
func (f *fakeAgent) FetchX509SVID(_ *workload.X509SVIDRequest, stream workload.SpiffeWorkloadAPI_FetchX509SVIDServer) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	if v := md.Get("workload.spiffe.io"); len(v) != 1 || v[0] != "true" {
		return status.Error(codes.InvalidArgument, "missing the workload.spiffe.io header")
	}
	if f.noAuth {
		return status.Error(codes.PermissionDenied, "no identity issued")
	}
	ch := make(chan *workload.X509SVIDResponse, 4)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	cur := f.cur
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}()
	if err := stream.Send(cur); err != nil {
		return err
	}
	for {
		select {
		case r := <-ch:
			if err := stream.Send(r); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// rotate replaces the identity, as an agent does before the old one expires.
func (f *fakeAgent) rotate(s issued) {
	r := f.response(s)
	f.mu.Lock()
	f.cur = r
	for ch := range f.subs {
		ch <- r
	}
	f.mu.Unlock()
}

func startAgent(t *testing.T, first issued, network string, noAuth bool) *fakeAgent {
	t.Helper()
	f := &fakeAgent{t: t, subs: map[chan *workload.X509SVIDResponse]struct{}{}, noAuth: noAuth}
	f.caKey, f.ca, f.caDER = newCA(t)
	f.cur = f.response(first)

	var l net.Listener
	var err error
	if network == "unix" {
		// A socket path has a short limit on this platform; the test's own
		// directory name is too long, so a short one is made.
		dir, derr := os.MkdirTemp("", "sp")
		if derr != nil {
			t.Fatal(derr)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		path := filepath.Join(dir, "agent.sock")
		l, err = net.Listen("unix", path)
		f.Addr = path // deliberately bare: Open must add the scheme itself
	} else {
		l, err = net.Listen("tcp", "127.0.0.1:0")
		f.Addr = "tcp://" + l.Addr().String()
	}
	if err != nil {
		t.Fatal(err)
	}
	f.srv = grpc.NewServer()
	workload.RegisterSpiffeWorkloadAPIServer(f.srv, f)
	go func() { _ = f.srv.Serve(l) }()
	t.Cleanup(f.srv.Stop)
	return f
}

func open(t *testing.T, addr string) *Source {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Open(ctx, addr)
	if err != nil {
		t.Fatalf("Open(%q): %v", addr, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var firstSVID = func() issued {
	return issued{
		id:       "spiffe://example.test/ns/costcrew/sa/console",
		serial:   0xABCDEF123, // not a number whose decimal and hex spellings agree
		notAfter: time.Now().Add(time.Hour).Truncate(time.Second),
	}
}

// Asked for nothing, Open attests nothing, and every method on the result is
// safe to call: an installation that never passed -spiffe-socket must not
// have to guard each call site.
func TestNoSocketMeansNoIdentityAndANilSafeSource(t *testing.T) {
	for _, socket := range []string{"", "   ", "\t\n"} {
		s, err := Open(context.Background(), socket)
		if err != nil || s != nil {
			t.Fatalf("Open(%q) = %v, %v; want nil, nil", socket, s, err)
		}
		if s.On() {
			t.Errorf("a nil source reports an attested identity")
		}
		if id := s.Identity(); id != (Identity{}) {
			t.Errorf("Identity() of a nil source = %+v", id)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close() of a nil source = %v", err)
		}
	}
}

// The identity a passport will carry is the one the agent issued: the SPIFFE
// ID, the leaf's expiry, and the leaf's serial in hex.
func TestOpenReadsTheIssuedIdentity(t *testing.T) {
	want := firstSVID()
	agent := startAgent(t, want, "tcp", false)
	s := open(t, agent.Addr)

	if !s.On() {
		t.Fatal("a source that was issued an identity says it holds none")
	}
	got := s.Identity()
	if got.ID != want.id {
		t.Errorf("ID = %q, want %q", got.ID, want.id)
	}
	if !got.Expires.Equal(want.notAfter) {
		t.Errorf("Expires = %v, want %v", got.Expires, want.notAfter)
	}
	if got.Serial != "abcdef123" {
		t.Errorf("Serial = %q, want the leaf serial in hex, abcdef123", got.Serial)
	}
}

// An address with no scheme is a unix socket path, which is how an operator
// writes it; the scheme is added for them.
func TestABarePathIsAUnixSocket(t *testing.T) {
	agent := startAgent(t, firstSVID(), "unix", false)
	if strings.Contains(agent.Addr, "://") {
		t.Fatalf("the test must hand Open a bare path, got %q", agent.Addr)
	}
	s := open(t, "  "+agent.Addr+"  ") // and surrounding space is not part of the path
	if got := s.Identity().ID; got != firstSVID().id {
		t.Errorf("ID = %q", got)
	}
}

// A rotation is picked up rather than remembered from startup: the SVID
// expires within the hour and a passport must say what the process holds now.
func TestARotatedIdentityIsPickedUp(t *testing.T) {
	agent := startAgent(t, firstSVID(), "tcp", false)
	s := open(t, agent.Addr)
	before := s.Identity()

	next := issued{
		id:       "spiffe://example.test/ns/costcrew/sa/console",
		serial:   0x777,
		notAfter: before.Expires.Add(30 * time.Minute),
	}
	agent.rotate(next)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.Identity(); got.Serial == "777" {
			if !got.Expires.Equal(next.notAfter) {
				t.Errorf("Expires = %v, want %v", got.Expires, next.notAfter)
			}
			if got.Serial == before.Serial {
				t.Errorf("the serial did not change")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("still presenting %+v after the agent rotated to serial 777", s.Identity())
}

// Nothing listening there: Open fails loudly, naming the address and what to
// look at, and does not hand back a source that would quietly say "none".
func TestOpenFailsLoudlyWhenNothingIsListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := "tcp://" + l.Addr().String()
	l.Close() // the port is now closed

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	s, err := Open(ctx, addr)
	if err == nil || s != nil {
		t.Fatalf("Open = %v, %v; want an error", s, err)
	}
	for _, want := range []string{"no SVID from the workload API at " + addr, "nothing is listening there", "registration entry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Open took %v with a 400ms deadline; the caller's deadline must bound it", took)
	}
}

// An agent that answers but has no entry for this binary issues nothing; that
// is the same loud failure, not an empty identity.
func TestOpenFailsWhenTheAgentIssuesNothingToThisBinary(t *testing.T) {
	agent := startAgent(t, firstSVID(), "tcp", true)
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	s, err := Open(ctx, agent.Addr)
	if err == nil || s != nil {
		t.Fatalf("Open = %v, %v; want an error", s, err)
	}
	if !strings.Contains(err.Error(), "no SVID from the workload API at "+agent.Addr) {
		t.Errorf("error = %v", err)
	}
}

// Closing is idempotent, and a second Close does not touch the source again.
func TestCloseIsSafeToCallTwice(t *testing.T) {
	agent := startAgent(t, firstSVID(), "tcp", false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Open(ctx, agent.Addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("first Close = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if !s.closed {
		t.Errorf("Close did not mark the source closed")
	}
}
