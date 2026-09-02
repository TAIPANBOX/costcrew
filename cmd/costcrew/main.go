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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/history"
	"github.com/TAIPANBOX/costcrew/internal/spiffe"
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
	// Where the SPIFFE workload API listens. Empty means this console holds no
	// attested identity and says so on every passport, which is the default
	// and the honest one.
	spiffeSock := flag.String("spiffe-socket", "",
		"the SPIFFE Workload API socket; empty means this console is not attested")

	setPw := flag.String("set-password", "", "create or reset an account as NAME:PASSWORD, then exit")
	setRole := flag.String("set-role", "admin", "the role a new -set-password account gets")
	weak := flag.Bool("allow-weak-password", false, "let -set-password set a password below the minimum, for a local demo account")
	rebuild := flag.Bool("rebuild-fixture", false, "drop the seeded estate, crew and history and build them again, then serve")
	flag.Parse()

	if *setPw != "" {
		if err := setPassword(*dir, *setPw, *setRole, *weak); err != nil {
			log.Fatalf("costcrew: %v", err)
		}
		return
	}

	cfg := stack.Config{
		EventsPath: *events, PassportDir: *passports,
		Host: *host, Owner: *owner, Attestation: *attest,
		SpiffeSocket: *spiffeSock,
	}
	if *rebuild {
		if err := rebuildFixture(*dir); err != nil {
			log.Fatalf("costcrew: %v", err)
		}
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
func setPassword(dir, spec, role string, weak bool) error {
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
	created, err := au.SetPassword(name, pw, role, weak)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created %s as %s\n", name, role)
	} else {
		fmt.Printf("reset the password for %s; any session it had is now signed out\n", name)
	}
	if len(pw) < auth.MinPassword {
		fmt.Printf("WARNING: %q is %d characters, below the %d this console asks for.\n"+
			"Serve this installation on loopback only.\n", name, len(pw), auth.MinPassword)
	}
	return nil
}

// rebuildFixture empties everything this binary seeds, so the next start
// builds it again from the current fixture.
//
// Accounts, sessions and the journal are NOT touched. The journal is a hash
// chain and deleting from it would break the chain the audit page verifies;
// the accounts are the one thing in the database a person put there.
//
// It exists because the fixture is code: when the crew grows or a plane starts
// being derived from the ledger, an installation seeded last week keeps the
// old one, and the seeders correctly refuse to overwrite what is already
// there. This is the way to ask for the new one on purpose.
func rebuildFixture(dir string) error {
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("opening the store in %s: %w", dir, err)
	}
	defer st.Close()
	seeded := []string{
		"comments", "artifacts", "tasks", "sprints",
		"attribution", "anomalies", "drivers",
		"explainers", "forecasts", "chargeback",
		"budgets", "allocation_rules", "charges", "analysts",
	}
	for _, t := range seeded {
		// A table this build does not know about is not an error: an older
		// database simply does not have it.
		if _, err := st.DB().Exec("DELETE FROM " + t); err != nil &&
			!strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("emptying %s: %w", t, err)
		}
	}
	if _, err := st.Journal("fixture_rebuilt", 0, map[string]any{
		"tables": strings.Join(seeded, ","),
	}); err != nil {
		return err
	}
	log.Printf("CostCrew: emptied the seeded tables; accounts and the journal are untouched")
	return nil
}

// abs is filepath.Abs with the error folded away: a path that cannot be
// resolved is compared as it was given, which is the safe direction because it
// can only make the comparison miss, never make it wrongly match.
func abs(p string) string {
	if p == "" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
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
	// An installation seeded before the mandate existed gets it now, without
	// its roster being replaced: only blank columns are written.
	if n, err := crew.BackfillMandate(st.DB(), scfg.Owner); err != nil {
		return fmt.Errorf("filling in the roster's mandate: %w", err)
	} else if n > 0 {
		log.Printf("CostCrew: filled in the mandate for %d analysts", n)
	}
	// The column first: SeedOwners moves charges between people, and it cannot
	// do that before there is a column recording who a charge belonged to.
	// Who answered for a charge, recorded ON the charge.
	if err := crew.EnsureOwnershipHistory(st.DB()); err != nil {
		return fmt.Errorf("ownership history: %w", err)
	}

	// More than one person to answer for the estate.
	// The SAME name SeedRoster stamped, which is "unclaimed" when no -stack-owner
	// was given. Passing scfg.Owner here matched nothing on a fresh install with
	// no flag, so five owners were created and none of them was given an agent.
	if acc, moved, err := crew.SeedOwners(st.DB(), au, crew.SeededOwner(scfg.Owner)); err != nil {
		return fmt.Errorf("seeding owners: %w", err)
	} else if acc > 0 || moved > 0 {
		log.Printf("CostCrew: %d owner account(s) created with no usable password "+
			"(set one with -set-password), %d agent(s) placed with them", acc, moved)
	}

	// Rights naming something this console no longer has.
	if n, err := crew.DropRetiredRights(st.DB()); err != nil {
		return fmt.Errorf("dropping retired rights: %w", err)
	} else if n > 0 {
		log.Printf("CostCrew: dropped a retired right from %d analyst(s); it named an "+
			"intake queue that was never built", n)
	}

	// Skill names this console has renamed, so an installation seeded before
	// the rename does not sit forever on a skill rightsForSkill no longer
	// recognises.
	if n, err := crew.RenameRetiredSkills(st.DB()); err != nil {
		return fmt.Errorf("renaming retired skill names: %w", err)
	} else if n > 0 {
		log.Printf("CostCrew: renamed a retired skill name on %d analyst(s)", n)
	}

	// Attestations this console invented before it knew better. Only those
	// with no evidence behind them; a recorded one survives.
	if n, err := crew.ClearFabricated(st.DB()); err != nil {
		return fmt.Errorf("clearing fabricated attestations: %w", err)
	} else if n > 0 {
		log.Printf("CostCrew: cleared %d attestation(s) this console had invented "+
			"from permission lists; those agents now report as unattested, which they are", n)
	}

	// The ROSTER, not the fixture: an analyst hired through the console after
	// the first start exists only here, and a passport run that read the
	// fixture would quietly publish yesterday's crew.
	roster, err := crew.Roster(st.DB())
	if err != nil {
		return fmt.Errorf("reading the roster: %w", err)
	}
	// Two writers, one file, and one of them is a hash chain.
	//
	// The store's journal and the agent-event stream are both append-only
	// NDJSON, and nothing about either name stops somebody pointing
	// -stack-events at the journal. They interleave, the chain no longer
	// verifies, and the audit page reports a break that nobody caused. I did
	// exactly this while testing the trailryx integration, which is how it is
	// here rather than in a list of things that could go wrong.
	if abs(scfg.EventsPath) == abs(st.JournalPath()) {
		return fmt.Errorf("-stack-events points at %s, which is the store's own "+
			"hash-chained journal. Two writers appending to one chain breaks it, "+
			"and the break looks like tampering. Use a different file",
			st.JournalPath())
	}
	em, err := stack.Open(scfg)
	if err != nil {
		return fmt.Errorf("opening the event stream: %w", err)
	}
	defer em.Close()

	// The identity this process was issued, if it was issued one.
	//
	// A failure here STOPS the start. An operator who passed a socket path
	// expects to be attested, and carrying on unattested would leave every
	// passport saying "none" while they believed otherwise, which is the
	// quietest way to be wrong about a security property.
	if scfg.SpiffeSocket != "" {
		src, err := spiffe.Open(context.Background(), scfg.SpiffeSocket)
		if err != nil {
			return fmt.Errorf("fetching this console's own SVID: %w", err)
		}
		defer src.Close()
		id := src.Identity()
		em.SetRuntimeIdentity(id.ID)
		log.Printf("CostCrew: attested as %s, valid until %s (serial %s)",
			id.ID, id.Expires.UTC().Format(time.RFC3339), id.Serial)
	}
	// The hash chain always records the work; the agent-event stream is added
	// when the governance plane is switched on. The chain is the one that has
	// to be complete, so it does not depend on a flag.
	var rec anomaly.Recorder = st.AsRecorder()
	if em.On() {
		rec = store.Tee(st.AsRecorder(), em)
		log.Printf("CostCrew: emitting agent-events to %s", scfg.EventsPath)
		if n, err := em.WritePassports(roster); err != nil {
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

		// Who went past their guard this month, said out loud rather than only
		// drawn on a page. The console has always been able to show it; nothing
		// told anybody who was not looking.
		if past, by, err := crew.CheckGuards(st.DB(), world.LastDay[:7], rec); err != nil {
			return fmt.Errorf("checking the guards: %w", err)
		} else if past > 0 {
			log.Printf("CostCrew: %d %s past their guard this month, by %s in total",
				past, map[bool]string{true: "analyst is", false: "analysts are"}[past == 1], by)
		}

		// A fresh installation gets a past: findings that were worked, forecasts
		// that were frozen and later scored, explainers in review, and a
		// conversation on the board. Every part of it checks first whether it has
		// already run, so a restart never moves a decision somebody made.
		if hc, err := history.Seed(st.DB(), rec); err != nil {
			return fmt.Errorf("seeding the history: %w", err)
		} else if hc.Triaged+hc.Forecasts+hc.Explainers+hc.Comments > 0 {
			log.Printf("CostCrew: history: %d findings taken, %d answered, %d accepted, %d dismissed; "+
				"%d forecasts frozen, %d explainers, %d comments",
				hc.Triaged, hc.Explained, hc.Accepted, hc.Dismissed,
				hc.Forecasts, hc.Explainers, hc.Comments)
		}
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
			Passports: em.WritePassports, PassportFor: em.PassportFor,
			Delegation: em.Delegation,
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
