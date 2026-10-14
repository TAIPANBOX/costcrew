package connectors

// Invariant 102: a TokenFuse gateway speaks one wire, so the OpenAI wire is a
// second gateway with its own trace and its own export, and the export does
// not say which door a call came through. The connector reads a second
// folder, the OpenAI-wire door's, and names every row by the folder it was
// read from.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASecondDoorsFolderIsReadAsThatDoors(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	main := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	openai := copyIntoDir(t, "testdata/tokenfuse-focus-openai-door-2026-10-14.csv")
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": main, OpenAIPathInput: openai}); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `door=?`, DoorMain); n != 15 {
		t.Errorf("%d row(s) named as the main gateway's, want the main folder's 15", n)
	}
	if n := countWhere(t, db, `door=?`, DoorOpenAI); n != 3 {
		t.Errorf("%d row(s) named as the OpenAI-wire door's, want its folder's 3", n)
	}
	if n := countWhere(t, db, `door IS NULL`); n != 0 {
		t.Errorf("%d row(s) imported with no door", n)
	}
	if n := countWhere(t, db, `run_id='w2-b12-A' AND door=?`, DoorOpenAI); n != 2 {
		t.Errorf("the run that crossed doors has %d call(s) on the OpenAI-wire door, want 2", n)
	}
	if !strings.Contains(msg, "By door: 15 rows through the main gateway, 3 through the OpenAI-wire door.") {
		t.Errorf("the import's sentence does not count the rows by door:\n%s", msg)
	}

	// The dry run says the same, and a second import changes nothing.
	desc, _, err := Test(db, "tokenfuse-focus", os.Getenv)
	if err != nil || !strings.Contains(desc, "By door: 15 rows through the main gateway, 3 through the OpenAI-wire door.") {
		t.Errorf("the connector's Test does not count the rows by door (%v):\n%s", err, desc)
	}
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `door=?`, DoorOpenAI); n != 3 {
		t.Errorf("a second import moved the OpenAI-wire door's rows to %d", n)
	}
}

func TestOneFolderNamesNoSecondDoor(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv"))
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `door=?`, DoorMain); n != 15 {
		t.Errorf("%d of 15 rows named as the main folder's", n)
	}
	if strings.Contains(msg, "By door") {
		t.Errorf("an import of one folder names doors:\n%s", msg)
	}
}

func TestOneFolderSetAsBothDoorsIsRefused(t *testing.T) {
	main := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	link := filepath.Join(t.TempDir(), "same")
	if err := os.Symlink(main, link); err != nil {
		t.Fatal(err)
	}
	for name, second := range map[string]string{
		"the same path":               main,
		"the same path, not cleaned":  main + string(filepath.Separator) + ".",
		"the same folder another way": link,
	} {
		t.Run(name, func(t *testing.T) {
			st := openFocusStore(t)
			db := st.DB()
			if err := Save(db, "tokenfuse-focus", map[string]string{"path": main, OpenAIPathInput: second}); err != nil {
				t.Fatal(err)
			}
			_, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
			if err == nil || !strings.Contains(err.Error(), "is the main folder") {
				t.Errorf("one folder set as both doors was read (%v): every call would be named twice", err)
			}
			if n := countWhere(t, db, `1=1`); n != 0 {
				t.Errorf("%d row(s) written from a refused configuration", n)
			}
		})
	}
}

func TestAnUnreadableSecondFolderIsNamedAsTheDoors(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	main := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": main,
		OpenAIPathInput: filepath.Join(t.TempDir(), "absent")}); err != nil {
		t.Fatal(err)
	}
	_, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err == nil || !strings.HasPrefix(err.Error(), "the OpenAI-wire door's folder: ") {
		t.Errorf("a missing second folder is not named as the door's: %v", err)
	}
	if c, ok := Get("tokenfuse-focus"); !ok || len(c.Inputs) != 2 || c.Inputs[1].Name != OpenAIPathInput || !c.Inputs[1].Optional {
		t.Errorf("the connector does not offer the OpenAI-wire door's folder as an optional input: %+v", c.Inputs)
	}
}
