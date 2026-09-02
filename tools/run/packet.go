package main

// The TASK PACKET: the figures the task is about.
//
// B2-SPEC.md section 2. Until this file existed, execute() sent one message
// per task -- persona, mission, title, goal, date, and "do not invent a
// number you were not given" -- and handed the model no number at all, which
// is a strange thing to tell a model not to invent while giving it nothing
// to check against. This is what hands it the actual figures.
//
// The builder itself now lives in internal/deliver (B7-SPEC.md section 2):
// tools/bench needs to build the identical packet, in a mode that hides the
// driver label and its kind, and Go will not let a second "package main"
// import this one to reach it. This file keeps the old unexported name and
// three-argument call sites working, one line deep, so nothing else in this
// package or its tests had to change.
import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
)

// packet is production's own call: hideDriver is always false here. See
// internal/deliver.Packet for the bench's hiding mode and everything this
// used to say about packetMaxBytes, boundBytes and each section.
func packet(db *sql.DB, t crew.Task, a crew.Analyst) string {
	return deliver.Packet(db, t, a, false)
}

// The three thin wrappers below are the same move: dispatch.go's tool gate
// (hasString, on an analyst's rights) and its result cap (boundBytes), and
// tools.go's own "anomaly" tool (anomalySection, always with the driver
// showing -- it is a production tool call, never a bench one) all called
// these directly before this file's builder moved to internal/deliver.
// Keeping the old unexported names here means neither of those files, nor
// their tests, needed to change at all.
func hasString(list []string, want ...string) bool { return deliver.HasString(list, want...) }
func boundBytes(s string, max int) string          { return deliver.BoundBytes(s, max) }
func anomalySection(an anomaly.Anomaly) string     { return deliver.AnomalySection(an, false) }
