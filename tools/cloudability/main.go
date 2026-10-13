// costcrew-cloudability turns the TokenFuse gateway's FOCUS exports into a
// drop for Apptio Cloudability's Custom Data FOCUS ingress, so a FinOps team
// sees agent-level AI spend inside Cloudability, by agent, by the gateway's
// unit, by model and by credential.
//
//	costcrew-cloudability -in ./tokenfuse-exports -out ./cloudability \
//	    -root-dir byod-input-prod -billing-account-id acme-tokenfuse -month 2026-10
//
// It reads the folder the console's tokenfuse-focus connector reads, with that
// connector's own reader (connectors.LoadTokenFuseFocus, into a scratch store
// that is removed when it exits), so the columns, the refusals, the exact
// micro-dollars and the rule that a call exported twice is counted once are
// the console's, not a second parser's. It writes a FOCUS 1.1 drop, gzipped
// CSV with its manifest, into a LOCAL folder laid out the way the ingress
// reads a bucket:
//
//	<out>/<sub dirs>/<vendor>/<YYYYMMDD-YYYYMMDD>/<epoch>/<prefix>-00001.csv.gz
//	<out>/<sub dirs>/<vendor>/<YYYYMMDD-YYYYMMDD>/<epoch>/<prefix>-Manifest.json
//
// Copying <out> to the bucket is the operator's step (aws s3 sync, gsutil
// rsync, az storage blob sync; README.md). This binary makes no network call,
// holds no credential and carries no cloud SDK.
//
// The ingress, as IBM documents it (read 2026-10-13): FOCUS 1.0 or 1.1, the
// manifest's focus_version matched exactly; CSV gzipped or Parquet; every drop
// carries the ENTIRE month to date; the epoch folder marks the latest drop of
// a month. So each run writes the whole month again into a new epoch folder,
// and the next run supersedes it.
//
// A drop is all or nothing. A row the reader refused, a file it did not read,
// a value that would print as a spreadsheet formula, a sum that overflows: any
// of them and no folder is written, because a drop missing money it was given
// would read in Cloudability as the whole month. Exit 0 wrote a drop, 1 refused
// the data or failed writing it, 2 refused a flag.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/plainname"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

// segment is one folder or file-name part the drop's path is built from: it
// starts with a letter or digit (so never "." or ".." and never hidden), and
// holds nothing that could leave the folder it names.
var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// bucketName is root_dir: a bucket or container name, written into the
// manifest and never used as a local path.
var bucketName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

const accountMaxBytes = 256

type config struct {
	in          string
	l           layout
	month       time.Time
	accountID   string
	accountName string
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	// flag.CommandLine is replaced on every call, the way tools/usage does, so
	// the flags are defined with flag.String (which internal/manifest reads to
	// hold components.json to them) and a test can call run many times.
	flag.CommandLine = flag.NewFlagSet("costcrew-cloudability", flag.ContinueOnError)
	flag.CommandLine.SetOutput(stderr)
	in := flag.String("in", "", "the folder of TokenFuse FOCUS exports (tokenfuse focus-export), the one the tokenfuse-focus connector reads")
	out := flag.String("out", "", "the local folder that stands for the bucket's root; the drop is written under it")
	rootDir := flag.String("root-dir", "", "the bucket or container name the folder is synced to: the manifest's root_dir")
	subDir := flag.String("sub-dir", "", "optional folders between the bucket's root and the vendor, slash-separated")
	vendor := flag.String("vendor", "TokenFuse", "the vendor folder Cloudability reads this source from")
	prefix := flag.String("prefix", "costcrew", "the file prefix: <prefix>-Manifest.json and <prefix>-00001.csv.gz")
	month := flag.String("month", "", "the month to drop, YYYY-MM, UTC (default: the current month)")
	accountID := flag.String("billing-account-id", "", "FOCUS BillingAccountId: the account the gateway's spend is billed under; required, the gateway has none of its own")
	accountName := flag.String("billing-account-name", "", "FOCUS BillingAccountName; left null when not given")
	if err := flag.CommandLine.Parse(args); err != nil {
		return 2
	}
	t := now().UTC()
	cfg, err := checkFlags(*in, *out, *rootDir, *subDir, *vendor, *prefix, *month, *accountID, *accountName, t)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := makeDrop(cfg, t, stdout); err != nil {
		fmt.Fprintf(stderr, "No drop was written for %s. %v\n", cfg.month.Format("2006-01"), err)
		return 1
	}
	return 0
}

func checkFlags(in, out, rootDir, subDir, vendor, prefix, month, accountID, accountName string, now time.Time) (config, error) {
	var c config
	if strings.TrimSpace(in) == "" {
		return c, fmt.Errorf("-in is required: the folder of TokenFuse FOCUS exports")
	}
	if strings.TrimSpace(out) == "" {
		return c, fmt.Errorf("-out is required: the local folder the drop is written under")
	}
	if !bucketName.MatchString(rootDir) {
		return c, fmt.Errorf("-root-dir %q must be the bucket or container name alone (letters, digits, '.', '_', '-'), "+
			"never a path: Cloudability's keys are paths inside it", rootDir)
	}
	for _, f := range [][2]string{{"-vendor", vendor}, {"-prefix", prefix}} {
		if !segment.MatchString(f[1]) {
			return c, fmt.Errorf("%s %q must be one folder or file name: letters, digits, '.', '_' and '-', "+
				"starting with a letter or digit, at most 64 bytes", f[0], f[1])
		}
	}
	var subs []string
	if subDir != "" {
		subs = strings.Split(subDir, "/")
		if len(subs) > 8 {
			return c, fmt.Errorf("-sub-dir %q is more than 8 folders deep", subDir)
		}
		for _, s := range subs {
			if !segment.MatchString(s) {
				return c, fmt.Errorf("-sub-dir %q must be relative folder names separated by '/', each starting with "+
					"a letter or digit; %q is not one, so the drop could land outside -out", subDir, s)
			}
		}
	}
	m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if month != "" {
		p, err := time.Parse("2006-01", month)
		if err != nil || p.Format("2006-01") != month {
			return c, fmt.Errorf("-month %q must be a month as YYYY-MM", month)
		}
		m = p
	}
	if accountID == "" || strings.TrimSpace(accountID) != accountID {
		return c, fmt.Errorf("-billing-account-id is required, without padding: FOCUS 1.1 makes BillingAccountId " +
			"mandatory and never null, and the gateway has no billing account of its own to give it")
	}
	if reason := plainname.Check("-billing-account-id", accountID, accountMaxBytes); reason != "" {
		return c, fmt.Errorf("%s", reason)
	}
	if accountName != "" {
		if reason := plainname.Check("-billing-account-name", accountName, accountMaxBytes); reason != "" {
			return c, fmt.Errorf("%s", reason)
		}
	}
	c.in = in
	c.l = layout{out: out, rootDir: rootDir, subDirs: subs, vendor: vendor, prefix: prefix}
	c.month, c.accountID, c.accountName = m, accountID, accountName
	return c, nil
}

func makeDrop(cfg config, now time.Time, stdout io.Writer) error {
	scratch, err := os.MkdirTemp("", "costcrew-cloudability-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	st, err := store.Open(scratch)
	if err != nil {
		return fmt.Errorf("opening the scratch store: %w", err)
	}
	defer st.Close()

	load, err := connectors.LoadTokenFuseFocus(st.DB(), cfg.in)
	if err != nil {
		return fmt.Errorf("The tokenfuse-focus reader refused the folder: %w", err)
	}
	if load.RefusedRows > 0 || load.FilesNotRead > 0 {
		return fmt.Errorf("The reader refused %d row%s and did not read %d file%s or link%s, and a drop that leaves "+
			"out money it was given reads in Cloudability as the whole month. Fix or move them and run again. "+
			"The reader said: %s", load.RefusedRows, plural(int64(load.RefusedRows)), load.FilesNotRead,
			plural(int64(load.FilesNotRead)), plural(int64(load.FilesNotRead)), load.Sentence)
	}
	if load.CommitmentRows > 0 {
		return fmt.Errorf("The folder holds %d ChargeCategory Purchase row%s, which TokenFuse does not write and this "+
			"drop has no place for. The reader said: %s", load.CommitmentRows, plural(int64(load.CommitmentRows)), load.Sentence)
	}

	c, err := build(st.DB(), cfg.month, cfg.accountID, cfg.accountName)
	if err != nil {
		return err
	}
	if len(c.records) == 0 {
		return fmt.Errorf("There is no call in the folder that went through in %s to drop (%d outside it, %d blocked). "+
			"A drop holds the whole month, so an empty one would replace the month in Cloudability with nothing.",
			cfg.month.Format("2006-01"), c.outside, c.blocked)
	}
	if err := validateDrop(header(), c.records); err != nil {
		return fmt.Errorf("The drop would not be FOCUS 1.1: %w", err)
	}
	w, err := write(cfg.l, cfg.month, now, c.records)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Wrote a FOCUS %s drop for %s, the month to date (%s to %s):\n", focusVersion,
		cfg.month.Format("2006-01"), c.firstDay, c.lastDay)
	fmt.Fprintf(stdout, "  %s  (%d row%s)\n  %s\n", w.dataPath, len(c.records), plural(int64(len(c.records))), w.manifestPath)
	fmt.Fprintf(stdout, "%d call%s, %s USD.", c.calls, plural(c.calls), microsDecimal(c.micros))
	if n := c.blocked - c.blockedNoRow; n > 0 {
		fmt.Fprintf(stdout, " %d blocked call%s counted on %s row%s, at no cost.", n, plural(n), map[bool]string{true: "its", false: "their"}[n == 1], plural(n))
	}
	if c.blockedNoRow > 0 {
		fmt.Fprintf(stdout, " %d blocked call%s in a group that spent nothing (%d group%s) %s no row: FOCUS has no "+
			"Usage row for nothing consumed.", c.blockedNoRow, plural(c.blockedNoRow), c.groupsNoRow, plural(c.groupsNoRow),
			map[bool]string{true: "has", false: "have"}[c.blockedNoRow == 1])
	}
	if c.outside > 0 {
		fmt.Fprintf(stdout, " %d call%s outside %s left out.", c.outside, plural(c.outside), cfg.month.Format("2006-01"))
	}
	fmt.Fprintf(stdout, "\nThe reader said: %s\n", load.Sentence)
	fmt.Fprintf(stdout, "Sync %s to the bucket %s; Cloudability reads the latest epoch folder of each month.\n",
		filepath.Clean(cfg.l.out), cfg.l.rootDir)
	return nil
}
