package crew

// The cadence switch, in the store. B5-SPEC.md section 2.
//
// Nothing runs on its own, by design (stack-k8s's CronJob and stack-single's
// routine both stay suspended, a platform act and a separate decision). This
// is the SECOND, inner switch: `tools/run -due` refuses to spend anything
// unless a person has turned this on in the console, so a routine that fires
// while the console says "off" spends nothing.
//
// No key-value table existed in this store before this file (internal/store
// itself holds only users and sessions, and every other setting this console
// has -- budgets, allocation rules, forecasts -- has its own typed table).
// `settings` is a plain key-value table rather than a fifth typed one,
// because "one switch and one number" does not earn a schema of its own, and
// a future setting of the same shape can reuse it rather than growing a sixth
// ad hoc table.

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

const settingsSchema = `
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT);
`

func ensureSettings(db *sql.DB) error {
	_, err := db.Exec(settingsSchema)
	return err
}

const (
	keyCadenceEnabled = "cadence.enabled"
	keyCadenceCeiling = "cadence.ceiling_cents"
	keyCadenceBy      = "cadence.changed_by"
	keyCadenceAt      = "cadence.changed_at"
)

// CadenceSettings reads the switch: whether a clock-driven `-due` run may
// spend anything at all, the most one such run may spend, and who set it
// last. A store that has never had the table, or never had a row, reads
// exactly as the documented default: off, ceiling zero.
//
// A row this console cannot parse reads as the SAFE default rather than
// erroring the whole call or being read as an unbounded allowance: garbage
// in `cadence.enabled` is "off", and garbage or a negative number in
// `cadence.ceiling_cents` is 0, which is "off" by another name. This is the
// reader's own defence, independent of SetCadence's own refusal of a
// negative ceiling on the way IN: the two are different hostile-input paths
// (a bad write, and a store edited by something other than this console).
func CadenceSettings(db *sql.DB) (enabled bool, ceilingCents money.Cents, changedBy, changedAt string, err error) {
	if err := ensureSettings(db); err != nil {
		return false, 0, "", "", err
	}
	rows, err := db.Query(`SELECT key, value FROM settings WHERE key IN (?,?,?,?)`,
		keyCadenceEnabled, keyCadenceCeiling, keyCadenceBy, keyCadenceAt)
	if err != nil {
		return false, 0, "", "", err
	}
	defer rows.Close()
	vals := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return false, 0, "", "", err
		}
		vals[k] = v
	}
	if err := rows.Err(); err != nil {
		return false, 0, "", "", err
	}

	enabled = vals[keyCadenceEnabled] == "on"
	if n, perr := strconv.ParseInt(strings.TrimSpace(vals[keyCadenceCeiling]), 10, 64); perr == nil && n > 0 {
		ceilingCents = money.Cents(n)
	}
	return enabled, ceilingCents, vals[keyCadenceBy], vals[keyCadenceAt], nil
}

// SetCadence is the ONLY writer of the switch (B5-SPEC.md section 2: "the
// console page /cadence is the only writer; the runner is a reader"). It
// refuses a negative ceiling before anything is written -- a ceiling of -1
// is nonsense a person mistyped, not a smaller-than-zero allowance -- and
// records who changed it and when, in one transaction so a reader never sees
// three of the four keys updated and one stale.
func SetCadence(db *sql.DB, enabled bool, ceilingCents money.Cents, changedBy string) error {
	if ceilingCents < 0 {
		return fmt.Errorf("the ceiling cannot be negative")
	}
	if err := ensureSettings(db); err != nil {
		return err
	}
	state := "off"
	if enabled {
		state = "on"
	}
	now := time.Now().UTC().Format("2006-01-02")

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{
		keyCadenceEnabled: state,
		keyCadenceCeiling: strconv.FormatInt(int64(ceilingCents), 10),
		keyCadenceBy:      changedBy,
		keyCadenceAt:      now,
	} {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
