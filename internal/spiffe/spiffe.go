// Package spiffe holds the identity this console was ISSUED, if it was issued
// one.
//
// # What this is, and what it is not
//
// A SPIFFE SVID is a certificate a workload API hands to a process after
// attesting it: on this machine, by the user it runs as, the path of the
// binary, and that binary's SHA-256. So the identity is bound to something
// that can be checked, which is the whole difference between an attestation
// and a word in a form.
//
// It attests THE CONSOLE, one process, and this package says so rather than
// letting the number of agents suggest otherwise. There are thirty-nine
// analysts on the roster and one binary on the disk: a workload attestor
// cannot tell triage-aws from forecaster, because at the level it looks at,
// they are the same process. Anything claiming thirty-nine distinct SVIDs
// would be back to inventing identities, which is what
// internal/crew/attestation.go exists to stop.
//
// What it therefore means on a passport is: this agent runs inside a workload
// whose identity was attested, and here is that workload's SPIFFE ID. That is
// true, it is checkable by anybody holding the trust bundle, and it is more
// than "none" while being less than "each agent proved itself".
package spiffe

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// Identity is what this process was issued.
type Identity struct {
	ID      string    // spiffe://<trust domain>/<path>
	Expires time.Time // when the SVID stops being valid
	Serial  string    // of the leaf certificate, so two runs can be told apart
}

// Source holds the current SVID and keeps it fresh.
//
// An SVID expires, typically within the hour, and a process that fetched one
// at startup and never looked again is one that will one day present something
// nobody accepts. The workload API pushes a new one before the old expires and
// this follows it, so what a passport carries is what the process actually
// holds right now.
type Source struct {
	mu     sync.RWMutex
	id     Identity
	src    *workloadapi.X509Source
	closed bool
}

// Open connects to the workload API socket and waits for the first SVID.
//
// It BLOCKS for that first one, deliberately, with a short deadline: an
// installation started with a socket path is one whose operator expects to be
// attested, and starting anyway with no identity would leave every passport
// quietly saying "none" while the operator believed otherwise. Failing loudly
// at startup is the honest outcome.
func Open(ctx context.Context, socket string) (*Source, error) {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return nil, nil
	}
	if !strings.HasPrefix(socket, "unix://") && !strings.HasPrefix(socket, "tcp://") {
		socket = "unix://" + socket
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	src, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(socket)))
	if err != nil {
		return nil, fmt.Errorf("no SVID from the workload API at %s: %w. "+
			"Either nothing is listening there, or this binary matches no "+
			"registration entry, which the agent's own log will say", socket, err)
	}
	svid, err := src.GetX509SVID()
	if err != nil {
		src.Close()
		return nil, fmt.Errorf("the workload API answered but issued no SVID: %w", err)
	}
	s := &Source{src: src}
	s.set(svid.ID.String(), svid.Certificates[0].NotAfter,
		svid.Certificates[0].SerialNumber.Text(16))
	return s, nil
}

func (s *Source) set(id string, exp time.Time, serial string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = Identity{ID: id, Expires: exp, Serial: serial}
}

// Identity is what this process holds now, re-read from the source so a
// rotation is picked up rather than remembered from startup.
func (s *Source) Identity() Identity {
	if s == nil {
		return Identity{}
	}
	if svid, err := s.src.GetX509SVID(); err == nil && len(svid.Certificates) > 0 {
		s.set(svid.ID.String(), svid.Certificates[0].NotAfter,
			svid.Certificates[0].SerialNumber.Text(16))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// On reports whether this console holds an attested identity.
func (s *Source) On() bool { return s != nil && s.Identity().ID != "" }

func (s *Source) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	return s.src.Close()
}
