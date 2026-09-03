package main

// The runner's -supervise mode: B3-SPEC.md section 4's deterministic pass,
// run over the command line rather than the console's button
// (internal/web/decisions.go), for a person who scripts a sprint boundary
// rather than clicking through it. Both call the one function,
// finops.Supervise; nothing about the pass itself is duplicated here.

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func superviseRun(db *sql.DB, sprintID int, b bus) error {
	pass, err := finops.Supervise(db, sprintID, b.rec)
	if err != nil {
		return err
	}
	fmt.Printf("SUPERVISE. sprint %d: %d applied, %d carried, %d stale halt(s), %d decision request(s). Nothing is dropped.\n\n",
		sprintID, len(pass.Applied), len(pass.Carried), len(pass.StaleHalts), len(pass.Requests))
	for _, o := range pass.Applied {
		fmt.Printf("  applied  %-22s artifact %d option %d\n", o.Class, o.Artifact, o.Ordinal)
	}
	for _, h := range pass.StaleHalts {
		fmt.Printf("  stale halt  %-10s halted since %s, owner %s: %s\n", h.Desk, h.Started, h.Owner, h.Reason)
	}
	for _, r := range pass.Requests {
		fmt.Printf("  decision request for %-14s %d option(s)\n", r.Owner, r.Options)
		if err := b.decisionRequested(sprintID, r.Owner, r.Options); err != nil {
			fmt.Fprintf(os.Stderr, "  the bus refused this decision request's event: %v\n", err)
		}
	}
	if len(pass.Applied)+len(pass.Carried) == 0 {
		fmt.Println("  nothing to review: no posted deliverable in this sprint carries an open option.")
	}
	return nil
}
