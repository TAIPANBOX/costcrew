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
	"syscall"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8321", "listen address; loopback by default, put a proxy in front for TLS")
	dir := flag.String("data", ".", "directory for the database, the journal and the signing key")
	flag.Parse()

	if err := run(*addr, *dir); err != nil {
		log.Fatalf("costcrew: %v", err)
	}
}

func run(addr, dir string) error {
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
	// Detection runs on start and reconciles rather than replaces, so a
	// restart never disturbs a decision somebody already made.
	found, added, err := anomaly.Run(st.DB(), time.Now(), detect.Default())
	if err != nil {
		return fmt.Errorf("running detection: %w", err)
	}
	log.Printf("CostCrew: %d anomalies, %d of them new", found, added)

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
		Addr:              addr,
		Handler:           web.New(st, au),
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
