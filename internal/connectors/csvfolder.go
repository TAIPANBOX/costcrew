package connectors

// What every folder-of-CSV reader in this package owes its operator, written
// once: the folder walk that never follows a link and names what it passed
// over, and the refusal list that is counted whole but named only for the
// first few (invariant 86).
//
// The FOCUS reader had both (invariant 78). The rightsizing, budget
// recommendation and SaaS seats readers had neither: they named every refused
// row, so a file of a million bad rows built a sentence of a million clauses
// and rendered it onto the connector page; two of them skipped a link without
// saying so, and the SaaS seats reader kept its own copy of the walk, which
// followed the link and read whatever it pointed at. Three copies of one rule
// is how that happened, so the rule lives here and they call it.

import (
	"fmt"
	"strings"
)

// refusalTally counts every refusal and names the first focusRefusalsShown,
// the same bound the FOCUS reader's own sentence uses.
type refusalTally struct {
	count int
	named []string
}

func (t *refusalTally) add(reason string) {
	t.count++
	if len(t.named) < focusRefusalsShown {
		t.named = append(t.named, reason)
	}
}

// addAll folds one file's tally into the import's, keeping the bound.
func (t *refusalTally) addAll(o refusalTally) {
	t.count += o.count
	for _, r := range o.named {
		if len(t.named) >= focusRefusalsShown {
			break
		}
		t.named = append(t.named, r)
	}
}

// clause is the named refusals, and how many were left unnamed.
func (t refusalTally) clause() string {
	s := strings.Join(t.named, "; ")
	if more := t.count - len(t.named); more > 0 {
		s += fmt.Sprintf("; and %d more", more)
	}
	return s
}

// skippedNote is the sentence a reader gives for a folder entry it did not
// open.
func skippedNote(name string) string {
	return name + ": a symbolic link or not a regular file, not followed"
}

// csvFolder is the folder walk every CSV reader in this package uses
// (focusFolder: regular *.csv and *.csv.gz files only, sorted), plus the two
// refusals that come before any file is read: a folder with nothing to read,
// and a folder whose only candidates were links or other non-regular files,
// which names them rather than claiming the folder was empty.
func csvFolder(path string) (files, skipped []string, err error) {
	files, skipped, err = focusFolder(path)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 && len(skipped) > 0 {
		return nil, nil, fmt.Errorf("no regular *.csv or *.csv.gz files in %s; passed over, "+
			"each a symbolic link or not a regular file, not followed: %s", path,
			strings.Join(boundedNames(skipped), ", "))
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no *.csv or *.csv.gz files found in %s", path)
	}
	return files, skipped, nil
}

// boundedNames is the first focusRefusalsShown names and a count of the rest.
func boundedNames(names []string) []string {
	if len(names) <= focusRefusalsShown {
		return names
	}
	out := append([]string(nil), names[:focusRefusalsShown]...)
	return append(out, fmt.Sprintf("and %d more", len(names)-focusRefusalsShown))
}
