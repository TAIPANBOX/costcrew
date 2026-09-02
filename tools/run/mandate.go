package main

// The prompt packet's job-description block.
//
// prompt() in live.go makes one call into this file, right after it writes
// the mission ("Your brief: ..."): B1A-SPEC.md section 2.2 asks for "a block
// 'Your job description' with the same fields in the same words" as the
// card (internal/web/analyst.go), and "You may decide alone: ...; you hand
// to the supervisor: ...; you never: ...". Both read internal/crew/roles.go,
// which is the one source; nothing here is typed independently of it.
//
// The block itself now lives in internal/deliver alongside Prompt, which is
// its only caller (B7-SPEC.md section 3's factoring: tools/bench needs the
// same prompt tools/run sends, and Go will not let it import this package).
// This wrapper keeps the old unexported name so mandate_test.go's direct
// calls needed no change.
import "github.com/TAIPANBOX/costcrew/internal/deliver"

func jobDescriptionBlock(name, desk string) string {
	return deliver.JobDescriptionBlock(name, desk)
}
