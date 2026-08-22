// Command costcrew serves the FinOps analyst console.
//
// One static binary: no interpreter, no virtual environment, no second
// database engine in the process. Everything it needs at runtime is a
// directory to keep its state in.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/stack"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8321", "listen address; loopback by default, put a proxy in front for TLS")
	dir := flag.String("data", ".", "directory for the database, the journal and the signing key")

	// The governance stack is off until somebody points it somewhere. Nothing
	// is emitted by default, because a product that starts writing into a
	// shared event stream without being asked is one nobody trusts to install.
	events := flag.String("stack-events", "", "append agent-events to this NDJSON file; empty means off")
	passports := flag.String("stack-passports", "", "write Agent Passport documents here")
	host := flag.String("stack-host", "costcrew.local", "the agent:// authority for this installation")
	owner := flag.String("stack-owner", "", "the owning team or human on every passport")
	attest := flag.String("stack-attestation", "none", "none|oidc|spiffe-svid|enclave-key|mtls-cert")

	// The way back in. Nothing about this runs a server.
	setPw := flag.String("set-password", "", "create or reset an account as NAME:PASSWORD, then exit")
	setRole := flag.String("set-role", "admin", "the role a new -set-password account gets")
	flag.Parse()

	if *setPw != "" {
		if err := setPassword(*dir, *setPw, *setRole); err != nil {
			log.Fatalf("costcrew: %v", err)
		}
		return
	}

	cfg := stack.Config{
		EventsPath: *events, PassportDir: *passports,
		Host: *host, Owner: *owner, Attestation: *attest,
	}
	if err := run(*addr, *dir, cfg); err != nil {
		log.Fatalf("costcrew: %v", err)
	}
}

// setPassword opens the store, changes one account and exits.
//
// It never starts the listener, so it is safe to run against a directory a
// server is already serving: SQLite takes the write, and the running process
// reads the new hash on the next sign-in.
func setPassword(dir, spec, role string) error {
	name, pw, ok := strings.Cut(spec, ":")
	if !ok {
		return fmt.Errorf("-set-password wants NAME:PASSWORD, got %q", spec)
	}
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("opening the store in %s: %w", dir, err)
	}
	defer st.Close()
	au, err := auth.New(st, dir)
	if err != nil {
		return err
	}
	created, err := au.SetPassword(name, pw, role)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created %s as %s\n", name, role)
	} else {
		fmt.Printf("reset the password for %s; any session it had is now signed out\n", name)
	}
	return nil
}

func run(addr, dir string, scfg stack.Config) error {
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("opening the store in %s: %w", dir, err)
	}
	defer st.Close()

	au, err := auth.New(st, dir)
	if err != nil {
		return fmt.Errorf("loading the signing key: %w", err)
	}

	// Seeded once, never rebuilt: an existing estate is somebody's work, and a
	// start-up that quietly regenerates it destroys whatever was recorded
	// against those numbers.
	rows, err := estate.Seed(st.DB())
	if err != nil {
		return fmt.Errorf("building the estate: %w", err)
	}
	if rows > 0 {
		log.Printf("CostCrew: built the estate, %d charge rows", rows)
	}
	if err := estate.SeedBudgets(st.DB()); err != nil {
		return fmt.Errorf("setting budgets: %w", err)
	}
	if err := finops.SeedRules(st.DB()); err != nil {
		return fmt.Errorf("setting allocation rules: %w", err)
	}
	if n, err := crew.SeedRoster(st.DB(), scfg.Owner); err != nil {
		return fmt.Errorf("seeding the roster: %w", err)
	} else if n > 0 {
		log.Printf("CostCrew: %d analysts on the roster", n)
	}
	em, err := stack.Open(scfg)
	if err != nil {
		return fmt.Errorf("opening the event stream: %w", err)
	}
	defer em.Close()
	var rec anomaly.Recorder
	if em.On() {
		rec = em
		log.Printf("CostCrew: emitting agent-events to %s", scfg.EventsPath)
		if n, err := em.WritePassports(world.Crew); err != nil {
			return fmt.Errorf("writing passports: %w", err)
		} else if n > 0 {
			log.Printf("CostCrew: published %d Agent Passports to %s", n, scfg.PassportDir)
		}
	}

	// Detection runs on start and reconciles rather than replaces, so a
	// restart never disturbs a decision somebody already made.
	found, added, err := anomaly.Run(st.DB(), time.Now(), detect.Default(), rec)
	if err != nil {
		return fmt.Errorf("running detection: %w", err)
	}
	log.Printf("CostCrew: %d anomalies, %d of them new", found, added)

	// The board is seeded from the anomalies, so every finding arrives with
	// its investigation already open rather than as a row nobody owns.
	var seeds []crew.AnomalySeed
	if list, err := anomaly.List(st.DB(), anomaly.Filter{}); err == nil {
		for _, a := range list {
			seeds = append(seeds, crew.AnomalySeed{
				ID: a.ID, Source: a.Source, Service: a.Service,
				Day: a.Day, Direction: a.Direction, Excess: a.Excess,
			})
		}
	}
	sp, tk, ar, err := crew.Seed(st.DB(), seeds)
	if err != nil {
		return fmt.Errorf("seeding the board: %w", err)
	}
	if sp > 0 {
		log.Printf("CostCrew: %d sprints, %d tasks, %d deliverables", sp, tk, ar)
	}

	n, err := au.Count()
	if err != nil {
		return err
	}
	if n == 0 {
		log.Print("CostCrew: no accounts yet. The first account created at /signup")
		log.Print("          becomes the admin of this installation. Do that before")
		log.Print("          you hand the address to anyone.")
	}
	if auth.DemoMode() {
		log.Print("CostCrew: demo mode, nobody can spend the owner's model budget")
	}

	srv := &http.Server{
		Addr: addr,
		Handler: web.New(st, au, web.Stack{
			Recorder: rec, Host: scfg.Host, EventsPath: scfg.EventsPath,
			Passports: em.WritePassports, Delegation: em.Delegation,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down on a signal rather than being killed mid-write: the journal is
	// a hash chain, and a half-written line is a break a verifier has to
	// report.
	idle := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("costcrew: shutdown: %v", err)
		}
		close(idle)
	}()

	log.Printf("CostCrew listening on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-idle
	return nil
}
