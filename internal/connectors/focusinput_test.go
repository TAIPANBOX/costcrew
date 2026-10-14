package connectors

// Invariant 104: the gateway's export folders are an input a launcher can set
// on the command line, read at start and then on an interval, and every
// automatic import says on the connector's page what it read or why it read
// nothing.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

func TestCheckFocusInputRefusesAMisconfiguredLauncher(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	file := filepath.Join(dir, "not-a-folder.csv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "same")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		in   FocusInput
		want string
	}{
		"a second door with no main folder": {FocusInput{OpenAI: other}, "needs -focus-dir"},
		"a main folder that is not there":   {FocusInput{Main: filepath.Join(dir, "absent")}, "-focus-dir "},
		"a second folder that is not there": {FocusInput{Main: dir, OpenAI: filepath.Join(dir, "absent")}, "-focus-dir-openai "},
		"a main folder that is a file":      {FocusInput{Main: file}, "is not a folder"},
		"one folder as both doors":          {FocusInput{Main: dir, OpenAI: dir + "/."}, "the same folder"},
		"one folder reached two ways":       {FocusInput{Main: dir, OpenAI: link}, "the same folder"},
		"an interval under a minute":        {FocusInput{Main: dir, Every: 30 * time.Second}, "at least 1m0s"},
		"a negative interval":               {FocusInput{Main: dir, Every: -time.Minute}, "at least 1m0s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CheckFocusInput(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%+v: %v, want an error saying %q", c.in, err, c.want)
			}
		})
	}
	got, err := CheckFocusInput(FocusInput{Main: " " + dir + " ", OpenAI: other, Every: time.Minute})
	if err != nil || got.Main != dir || !got.On() {
		t.Errorf("a good configuration: %+v, %v", got, err)
	}
	if got, err := CheckFocusInput(FocusInput{Main: dir}); err != nil || got.Every != 0 {
		t.Errorf("an interval of 0 (read once) is refused: %v", err)
	}
	if off, err := CheckFocusInput(FocusInput{}); err != nil || off.On() {
		t.Errorf("no folder at all is not off: %+v, %v", off, err)
	}
}

func TestTheLaunchersFoldersAreReadAndRecordedOnTheConnector(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	main := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	openai := copyIntoDir(t, "testdata/tokenfuse-focus-openai-door-2026-10-14.csv")
	// A person had saved another folder on the page: the launcher's wins.
	configureFocus(t, db, t.TempDir())
	f := FocusInput{Main: main, OpenAI: openai}
	if err := ApplyFocusInput(db, f); err != nil {
		t.Fatal(err)
	}
	msg, err := ImportFocus(db, f, st.AsRecorder())
	if err != nil {
		t.Fatalf("ImportFocus: %v", err)
	}
	if n := countWhere(t, db, `1=1`); n != 18 {
		t.Errorf("%d rows read from the launcher's two folders, want 18", n)
	}
	conn, err := Load(db, "tokenfuse-focus")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Config["path"] != main || conn.Config[OpenAIPathInput] != openai {
		t.Errorf("the connector's saved folders are %v, want the launcher's", conn.Config)
	}
	if conn.LastImport == "" || conn.LastImportResult != msg || !strings.Contains(msg, "By door") {
		t.Errorf("the connector does not record what the launcher's import read: %q / %q", conn.LastImport, conn.LastImportResult)
	}
}

func TestAnAutomaticImportOnTheGeneratedEstateSaysWhyItReadNothing(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	f := FocusInput{Main: copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")}
	if err := ApplyFocusInput(db, f); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportFocus(db, f, st.AsRecorder()); err == nil {
		t.Fatal("an automatic import replaced the generated estate without the launcher's yes")
	}
	conn, err := Load(db, "tokenfuse-focus")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conn.LastImportResult, "Not imported (costcrew -focus-dir): this store still holds the generated estate") {
		t.Errorf("the connector's page does not say why the automatic import read nothing: %q", conn.LastImportResult)
	}
	if n := countWhere(t, db, `1=1`); n != 0 {
		t.Errorf("%d rows written by a refused import", n)
	}

	f.ReplaceGenerated = true
	if _, err := ImportFocus(db, f, st.AsRecorder()); err != nil {
		t.Fatalf("with -focus-replace-generated: %v", err)
	}
	if n := countWhere(t, db, `1=1`); n != 15 {
		t.Errorf("%d rows after the launcher's yes, want 15", n)
	}
	tail, _ := st.JournalTail(20)
	found := false
	for _, rec := range tail {
		if rec.Event == "generated_estate_replaced" {
			found = true
		}
	}
	if !found {
		t.Error("no generated_estate_replaced entry in the journal for the launcher's replacement")
	}
}

// lockedLog collects WatchFocus's lines from its goroutine.
type lockedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lockedLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *lockedLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestWatchFocusReadsTheFolderAgainOnItsInterval(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/tokenfuse-focus-runs-2026-10-14.csv")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	writeFocusFile(t, dir, "focus-1.csv", strings.Join(lines[:6], "\n")+"\n")
	f := FocusInput{Main: dir, Every: 20 * time.Millisecond}
	if err := ApplyFocusInput(db, f); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var lg lockedLog
	done := make(chan struct{})
	go func() {
		WatchFocus(ctx, db, f, st.AsRecorder(), lg.logf)
		close(done)
	}()
	waitFor := func(want int) bool {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			if db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n) == nil && n == want {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitFor(5) {
		t.Fatalf("the first import did not read the folder's five calls:\n%s", lg.all())
	}
	// The gateway's next window lands: the next tick reads it.
	writeFocusFile(t, dir, "focus-2.csv", lines[0]+"\n"+strings.Join(lines[6:], "\n")+"\n")
	if !waitFor(15) {
		t.Fatalf("a file written after the start was never read:\n%s", lg.all())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchFocus did not stop when its context ended")
	}
	if !strings.Contains(lg.all(), "imported the gateway's export") {
		t.Errorf("the imports were not logged:\n%s", lg.all())
	}

	// With no interval it reads once and returns; here, a store with no
	// folder saved, so the one import fails and says so.
	bare := openFocusStore(t)
	once := make(chan struct{})
	go func() {
		WatchFocus(context.Background(), bare.DB(), FocusInput{Main: filepath.Join(dir, "absent")}, nil, lg.logf)
		close(once)
	}()
	select {
	case <-once:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchFocus with no interval did not return after one import")
	}
	if !strings.Contains(lg.all(), "was not imported") {
		t.Errorf("a failed import was not logged:\n%s", lg.all())
	}
}
