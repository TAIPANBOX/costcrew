package connectors

// The gateway's export folders as a launcher sets them (invariant 104).
//
// Until this, the tokenfuse-focus connector was configured only by a person
// on its page, and read only when that person clicked Import. A launcher
// could mount the gateway's export folder into the console and the console
// would still read nothing: on forge (2026-10-14) the console ran beside two
// gateways for a whole day of agent runs and its AI pages never saw a call.
// -focus-dir and -focus-dir-openai name the folders on the command line, and
// the console reads them at start and again every -focus-every.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// FocusInput is the tokenfuse-focus connector's folders as the command line
// sets them.
type FocusInput struct {
	// Main is the main gateway's export folder (the connector's path), and
	// OpenAI the OpenAI-wire door's (OpenAIPathInput, invariant 102).
	Main, OpenAI string
	// Every is how often the folders are read again; zero reads them once, at
	// start.
	Every time.Duration
	// ReplaceGenerated is the launcher's yes to replacing the generated
	// estate on the first import that finds real calls (refusal 1 of the
	// reader). Without it an installation that still holds the generated
	// estate imports nothing and the connector's page says why.
	ReplaceGenerated bool
}

// The environment twins of -focus-dir and -focus-dir-openai.
const (
	FocusDirEnv       = "COSTCREW_FOCUS_DIR"
	FocusDirOpenAIEnv = "COSTCREW_FOCUS_DIR_OPENAI"
)

// DefaultFocusEvery is -focus-every's default, and MinFocusEvery the
// shortest it accepts: every import reads the whole folder, so a few seconds
// would keep the store busy reading the same files.
const (
	DefaultFocusEvery = 15 * time.Minute
	MinFocusEvery     = time.Minute
)

// FocusActor is who the journal credits with an import the command line asked
// for.
const FocusActor = "costcrew -focus-dir"

// FocusDirEnvDefault is -focus-dir's default: COSTCREW_FOCUS_DIR or "".
func FocusDirEnvDefault() string { return strings.TrimSpace(os.Getenv(FocusDirEnv)) }

// FocusDirOpenAIEnvDefault is -focus-dir-openai's default.
func FocusDirOpenAIEnvDefault() string { return strings.TrimSpace(os.Getenv(FocusDirOpenAIEnv)) }

// On says whether the command line set the input at all.
func (f FocusInput) On() bool { return f.Main != "" }

// CheckFocusInput is called before the store is opened, so a mistake in the
// launcher's configuration stops the start rather than an import an hour
// later. A folder must exist and be a directory (an empty one is fine: the
// gateway may not have exported yet); the OpenAI-wire door's folder needs the
// main one and must not be it; the interval is zero or at least a minute.
func CheckFocusInput(f FocusInput) (FocusInput, error) {
	f.Main, f.OpenAI = strings.TrimSpace(f.Main), strings.TrimSpace(f.OpenAI)
	if f.Main == "" {
		if f.OpenAI != "" {
			return f, fmt.Errorf("-focus-dir-openai names the OpenAI-wire door's export folder and needs " +
				"-focus-dir, the main gateway's, beside it")
		}
		return f, nil
	}
	for _, d := range []struct{ flag, dir string }{{"-focus-dir", f.Main}, {"-focus-dir-openai", f.OpenAI}} {
		flag, dir := d.flag, d.dir
		if dir == "" {
			continue
		}
		fi, err := os.Stat(dir)
		if err != nil {
			return f, fmt.Errorf("%s %s: %w", flag, dir, err)
		}
		if !fi.IsDir() {
			return f, fmt.Errorf("%s %s is not a folder", flag, dir)
		}
	}
	if f.OpenAI != "" && sameFolder(f.Main, f.OpenAI) {
		return f, fmt.Errorf("-focus-dir-openai is the same folder as -focus-dir; each door writes its " +
			"own export, and one folder read as both would name every call twice")
	}
	if f.Every < 0 || (f.Every > 0 && f.Every < MinFocusEvery) {
		return f, fmt.Errorf("-focus-every is %s; it is 0 (read once, at start) or at least %s, "+
			"because every import reads the whole folder", f.Every, MinFocusEvery)
	}
	return f, nil
}

// ApplyFocusInput saves the command line's folders on the connector, in
// place of whatever was saved on its page: the launcher's configuration is
// the one a restart comes back to.
func ApplyFocusInput(db *sql.DB, f FocusInput) error {
	return Save(db, "tokenfuse-focus", map[string]string{"path": f.Main, OpenAIPathInput: f.OpenAI})
}

// ImportFocus reads the folders once, as the command line's import, and
// records the outcome on the connector either way, so its page says what the
// last automatic import read or why it read nothing.
func ImportFocus(db *sql.DB, f FocusInput, rec Recorder) (string, error) {
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{
		ReplaceGenerated: f.ReplaceGenerated, Actor: FocusActor, Rec: rec,
	})
	if err != nil {
		note := "Not imported (" + FocusActor + "): " + err.Error()
		if _, werr := db.Exec(`INSERT INTO connections(id, last_import, last_import_result)
			VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET
			last_import=excluded.last_import, last_import_result=excluded.last_import_result`,
			"tokenfuse-focus", time.Now().UTC().Format(time.RFC3339), note); werr != nil {
			return msg, fmt.Errorf("%w; and recording that failed: %v", err, werr)
		}
	}
	return msg, err
}

// WatchFocus imports at once and then every f.Every until ctx ends, and says
// each outcome through logf. It returns when ctx ends, or after the one
// import when f.Every is zero.
func WatchFocus(ctx context.Context, db *sql.DB, f FocusInput, rec Recorder, logf func(string, ...any)) {
	once := func() {
		if msg, err := ImportFocus(db, f, rec); err != nil {
			logf("CostCrew: the gateway's export was not imported: %v", err)
		} else {
			logf("CostCrew: imported the gateway's export: %s", msg)
		}
	}
	once()
	if f.Every <= 0 {
		return
	}
	t := time.NewTicker(f.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			once()
		}
	}
}
