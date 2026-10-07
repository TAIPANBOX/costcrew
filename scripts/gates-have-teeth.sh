#!/usr/bin/env bash
# Checks that this repo's gates still FAIL on the faults they exist to catch,
# still PASS on what they must not catch, and REFUSE to report success when
# they measured nothing at all.
#
# WHY
#
# The gates here are Go tests. Every one of them was run against a planted
# fault once, by hand, in the session that wrote it, and then the fault was
# reverted and the proof existed only in a commit message. A test that has
# quietly stopped catching anything looks exactly like a test with nothing to
# catch, and stays that way until the fault it guards ships.
#
# WHY THE THIRD PROPERTY IS SEPARATE, AND SHARPER HERE THAN ELSEWHERE
#
#     go test ./internal/web/ -run TestThisDoesNotExist
#     ok  github.com/TAIPANBOX/costcrew/internal/web  0.607s [no tests to run]
#     exit code: 0
#
# A renamed or deleted test does not report an error. It reports success. So
# every case here asserts the test actually RAN, and a pattern that matches
# nothing is a failure of this harness rather than a green line.
#
# AND A MUTATION THAT DOES NOT COMPILE PROVES NOTHING
#
# This one is not inherited from the other repos; it was learned here on
# 2026-08-23. Three planted faults were recorded as CAUGHT because `go test`
# exited non-zero, and it exited non-zero because removing a line had left an
# unused variable and the package would not build. The test never ran. A build
# failure and a caught fault are the same exit code and the difference is the
# entire point, so every mutation is compiled before the gate is judged.
#
# HOW IT MUTATES WITHOUT LEAVING A MESS
#
# It edits tracked files in place, so it refuses to start unless the tree is
# clean, restores with git after every case, restores again from a trap on any
# exit path including a kill, and asserts the tree is clean before reporting
# success.
#
# A GATE THAT IS ALREADY FAILING CANNOT BE JUDGED
#
# No case proves anything if the gate was already red before the mutation, so
# every gate is run on the unmutated tree first and reported UNJUDGEABLE if it
# was. That applies to the pass-cases too: on a red gate, "the gate failed on
# something it must not catch" sends the reader to look at a harmless mutation
# while the real failure is somewhere else.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

if ! command -v go >/dev/null 2>&1; then
	printf 'the gates here are Go tests and there is no go on PATH, so this\n'
	printf 'harness would report that nothing failed, which is the exact\n'
	printf 'silent pass it exists to catch.\n'
	exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
	printf 'this script mutates tracked files, so it needs a clean tree.\n'
	printf 'commit or stash first; it restores with git and cannot tell your\n'
	printf 'edits from its own.\n'
	exit 1
fi

restore() {
	git reset -q --hard HEAD 2>/dev/null
	git clean -fdq 2>/dev/null
}
baseline_dir="$(mktemp -d)"
cleanup() {
	restore
	rm -rf "$baseline_dir"
}

# EXIT and the signals are two different handlers on purpose.
#
# A single `trap cleanup EXIT INT TERM` restores the tree on a Ctrl-C and then
# CARRIES ON: a bash trap handler returns to where it interrupted unless it
# exits. Observed on 2026-08-23 by killing a run and watching the tree come
# clean and then go dirty again while the script kept mutating, with no
# terminal attached to it any more.
#
# The in-flight `go test` still has to finish before the handler gets its turn,
# so the tree stays mutated for up to the length of one test run after the
# kill. That is bash, not something this can fix, and it is why the message
# says so rather than leaving somebody to wonder.
cleanup_and_stop() {
	printf '\ninterrupted; restoring the tree once the test in flight finishes.\n' >&2
	cleanup
	trap - EXIT
	exit 130
}
trap cleanup EXIT
trap cleanup_and_stop INT TERM

failures=0
cases=0

# gate <pkg> <pattern> runs one gate and says whether it measured anything.
#
# The "[no tests to run]" check is why this is a function rather than an
# inline `go test`: that line is Go reporting success at having done nothing.
gate() {
	local out rc
	out=$(go test "$1" -run "$2" -count=1 2>&1)
	rc=$?
	# A here-string, not `printf | grep -q`: see the note in run_case below.
	if grep -q 'no tests to run' <<<"$out"; then
		printf 'MEASURED NOTHING\n%s' "$out"
		return 3
	fi
	printf '%s' "$out"
	return $rc
}

# run_case <name> <expect: fail|pass|gone> <pkg> <pattern> <needle> <file old new>...
#
# The edits are ARGUMENTS, not a program. They were a python program at first,
# built with printf inside $(...) inside a heredoc, and four of sixteen
# mutations silently did not apply because a quote or a tab did not survive the
# layers. The BROKEN check caught every one, which is the only reason this is a
# note rather than four green lines about gates nobody had tested.
run_case() {
	local name="$1" expect="$2" pkg="$3" pattern="$4" needle="$5"
	shift 5
	cases=$((cases + 1))

	# An expect word this harness does not define matches neither TOOTHLESS
	# below (expect = fail) nor OVEREAGER (expect = pass), so it fell through
	# to the "ok" at the end and reported success regardless of what the
	# mutation did. A word nobody defined once made twenty cases green
	# whatever happened -- "caught", here -- so an unrecognized expect is
	# refused as a failure of this harness rather than read as a pass.
	case "$expect" in
		fail|pass|gone) ;;
		*)
			printf 'UNKNOWN EXPECT  %s\n                %s is not a word this harness defines: it knows\n                fail, pass and gone and nothing else, so this case\n                would have judged nothing and printed ok whatever the\n                mutation did\n' "$name" "$expect"
			failures=$((failures + 1))
			return
			;;
	esac

	local key base
	key="$baseline_dir/$(printf '%s %s' "$pkg" "$pattern" | cksum | tr -d ' ')"
	if [ ! -f "$key" ]; then
		local o r
		o=$(gate "$pkg" "$pattern"); r=$?
		case $r in
			0) printf 'green' >"$key" ;;
			3) printf 'nothing' >"$key" ;;
			*) printf 'red' >"$key" ;;
		esac
	fi
	base="$(cat "$key")"
	if [ "$base" = red ]; then
		printf 'UNJUDGEABLE  %s\n             the gate is already failing on a clean tree, so neither a\n             failure nor a pass after the mutation would prove anything\n' "$name"
		failures=$((failures + 1))
		return
	fi
	if [ "$base" = nothing ]; then
		printf 'NO SUBJECT   %s\n             %s -run %s matches no test. Go reports that as success,\n             so this case would have been a green line for a gate that\n             does not exist.\n' "$name" "$pkg" "$pattern"
		failures=$((failures + 1))
		return
	fi

	if ! apply_edits "$@"; then
		printf 'BROKEN  %s\n        its mutation did not apply, so this case proved nothing\n' "$name"
		failures=$((failures + 1))
		restore
		return
	fi

	# Compile before judging. See the header: a mutation that leaves an unused
	# variable fails `go test` without the test ever running, and that is
	# indistinguishable from a caught fault by exit code alone.
	if ! go build ./... 2>/dev/null; then
		printf 'BROKEN  %s\n        its mutation does not compile, so the gate never ran and a\n        non-zero exit would have been recorded as a catch\n' "$name"
		failures=$((failures + 1))
		restore
		return
	fi

	local out rc
	out=$(gate "$pkg" "$pattern"); rc=$?
	restore

	# expect=gone: the case is ABOUT the subject vanishing, so measuring
	# nothing is the pass. Every other expectation treats it as a failure,
	# because a gate whose test has been renamed away reports success.
	if [ "$expect" = gone ]; then
		if [ "$rc" -eq 3 ]; then
			printf 'ok  %-62s (%s)\n' "$name" "$expect"
		else
			printf 'SILENT PASS  %s\n             the test was removed and the gate still reported success,\n             so every green line above would survive its own test\n             being deleted\n' "$name"
			failures=$((failures + 1))
		fi
		return
	fi
	if [ "$rc" -eq 3 ]; then
		printf 'NO SUBJECT   %s\n             the mutation removed the test itself\n' "$name"
		failures=$((failures + 1))
		return
	fi
	# The needle is searched with a here-string and NOT `printf | grep -qF`.
	# Under `set -o pipefail`, grep -q leaves as soon as it has matched, and
	# once the captured log is larger than the pipe (64 KB on macOS) printf is
	# still writing: it gets SIGPIPE (or EPIPE, "printf: write error: Broken
	# pipe") and the pipeline reports failure for a needle that WAS found. The
	# `!` then reads that as "not saying" and the case fails WRONG REASON on
	# the strength of how long the log was. Measured 2026-10-05: a failing gate
	# with a 260 KB log naming the needle on its first line was misjudged 20
	# runs out of 20; the same log under 64 KB never was. A here-string has no
	# writer process to lose, so the verdict depends on the text alone.
	if [ "$expect" = fail ] && [ "$rc" -ne 0 ] && [ -n "$needle" ] &&
		! grep -qF -- "$needle" <<<"$out"; then
		printf 'WRONG REASON  %s\n              it failed, but not saying: %s\n' "$name" "$needle"
		failures=$((failures + 1))
		return
	fi
	if [ "$expect" = fail ] && [ "$rc" -eq 0 ]; then
		printf 'TOOTHLESS  %s\n           the gate passed on a fault it exists to catch\n' "$name"
		failures=$((failures + 1))
	elif [ "$expect" = pass ] && [ "$rc" -ne 0 ]; then
		printf 'OVEREAGER  %s\n           the gate failed on something it must not catch\n' "$name"
		failures=$((failures + 1))
		# sed -n reads to the end where `head -4` would hang up on printf, which
		# is the same SIGPIPE shape as the needle search above (noise here, since
		# nothing reads this pipeline's status, but noise that looks like a fault).
		printf '%s\n' "$out" | sed -n '1,4s/^/           /p'
	else
		printf 'ok  %-62s (%s)\n' "$name" "$expect"
	fi
}

# apply_edits <file old new>... replaces the FIRST occurrence of old with new
# in each file, and fails if any pattern is not there.
apply_edits() {
	python3 - "$@" <<'PYEOF'
import sys
args = sys.argv[1:]
if len(args) % 3:
    sys.exit("edits come in threes: file, old, new")
for i in range(0, len(args), 3):
    path, old, new = args[i], args[i+1], args[i+2]
    s = open(path).read()
    if old not in s:
        sys.exit("not found in %s: %r" % (path, old[:60]))
    open(path, "w").write(s.replace(old, new, 1))
PYEOF
}

echo
echo "=== faults each gate must catch ==="
run_case $'retired rights: a skill hands one back out' \
	fail \
	./internal/crew \
	$'TestNoSkillGrantsARetiredRight' \
	$'still grants' \
	internal/crew/mandate.go \
	$'"routing":                  {"figures-read"},' \
	$'"routing":                  {"figures-read", "requests-read"},'
run_case $'skill taxonomy: a roster skill loses its rights entry' \
	fail \
	./internal/crew \
	$'TestEverySkillOnTheRosterHasRights' \
	$'have no rightsForSkill entry' \
	internal/crew/mandate.go \
	$'"scenario-modelling":     {"figures-read", "budgets-read"},' \
	$''

# B1a: internal/crew/roles.yaml is bound to the code and to the roster, both
# ways, via scripts/roles-are-bound.sh reachable as TestRolesAreBound (same
# pattern as TestFeatureBindingsHold below, in reverse: that Go test is READ
# BY features-are-bound.sh's own kind of case; this one calls OUT to a shell
# script and this harness plants faults in what the script reads).
#
# The second case mutates internal/crew/roles.yaml to duplicate a class id
# with a second owner. internal/crew's own mustLoadRoles panics on that at
# PACKAGE INIT of the test binary `go test ./internal/crew` builds -- before
# TestRolesAreBound's body runs a single line, since roles_bound_test.go is
# in package crew_test, which imports crew, so crew's init() (running
# mustLoadRoles) always runs first. That is by design, not a toothless gate
# (see roles.go's comment on mustLoadRoles: it exists so exactly this kind
# of corruption breaks `go test ./...` on the spot), and it is also why the
# needle below is the panic's own words rather than the shell script's
# "MULTI-OWNED CLASS" line: running scripts/roles-are-bound.sh directly (not
# through this go-test wrapper) DOES reach that line first, because a plain
# bash process never links the crew package at all, but this harness always
# goes through the wrapper, so the panic is what a reader of ITS output
# actually sees.
run_case $'roles: a class named in code is absent from the file' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'MISSING CLASS' \
	internal/crew/roles.go \
	$'// class:task.accept' \
	$'// class:task.accept-renamed'
run_case $'roles: a class owned by two links' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'is listed twice' \
	internal/crew/roles.yaml \
	$'  - id: "escalation.request"\n    changes: "a decision request written to the owner"\n    owner: "supervisor"\n\nroles:' \
	$'  - id: "escalation.request"\n    changes: "a decision request written to the owner"\n    owner: "supervisor"\n  - id: "anomaly.explain"\n    changes: "planted by gates-have-teeth.sh: a second owner for a class that already has one"\n    owner: "owner"\n\nroles:'
run_case $'roles: a role decides a class its rights do not back' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'RIGHTS GAP' \
	internal/crew/roles.yaml \
	$'decides_alone: ["anomaly.explain", "anomaly.dismiss", "driver.one-time", "task.block"]' \
	$'decides_alone: ["anomaly.explain", "anomaly.dismiss", "driver.one-time", "task.block", "forecast.freeze"]'
run_case $'roles: the file is taken away' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'measured nothing' \
	internal/crew/roles_bound_test.go \
	$'cmd := exec.Command("../../scripts/roles-are-bound.sh")' \
	$'cmd := exec.Command("../../scripts/roles-are-bound.sh")\n\tcmd.Env = append(os.Environ(), "ROLES_YAML=/nonexistent-for-teeth-test.yaml")'

# Invariant 56: the thresholds are the ones the owner decided, and their
# provenance is a closed vocabulary. Three mutants of the values (the cents
# back at the draft, the card's text back at the draft, T.urgent back at the
# draft), the same cents read the other way round by the supervisor's own pass,
# two faults in the provenance gate (the loader's vocabulary and the shell
# gate's check of it), and one non-fault.
run_case $'thresholds: T.anomaly goes back to its draft cents' \
	fail \
	./internal/crew \
	$'TestTAnomalyIsTwoAndAHalfThousand' \
	$'want 250000' \
	internal/crew/roles.yaml \
	$'value_cents: 250000' \
	$'value_cents: 500000'
run_case $'thresholds: T.anomaly goes back to its draft cents, read by the supervisor' \
	fail \
	./internal/finops \
	$'TestAnOptionOfThreeThousandDollarsIsCarriedToTheOwner' \
	$'want 0 and 1' \
	internal/crew/roles.yaml \
	$'value_cents: 250000' \
	$'value_cents: 500000'
run_case $'thresholds: T.anomaly shows the card its draft text' \
	fail \
	./internal/crew \
	$'TestTAnomalyIsTwoAndAHalfThousand' \
	$'the card and the code disagree' \
	internal/crew/roles.yaml \
	$'value: "USD 2,500 per anomaly"' \
	$'value: "USD 5,000 per anomaly"'
run_case $'thresholds: T.urgent goes back to its draft cents' \
	fail \
	./internal/crew \
	$'TestTUrgentIsTwelveAndAHalfThousand' \
	$'want 1250000' \
	internal/crew/roles.yaml \
	$'value_cents: 1250000' \
	$'value_cents: 2500000'
run_case $'thresholds: a threshold goes back to being a draft' \
	fail \
	./internal/crew \
	$'TestEveryThresholdIsMarkedDecidedOnTheDayItWasDecided' \
	$'want it to begin @decided 2026-10-04' \
	internal/crew/roles.yaml \
	$'provenance: "@decided 2026-10-04, the draft value kept"' \
	$'provenance: "@claude 2026-09-02, draft"'
run_case $'thresholds: a provenance names somebody as its marker' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'is not a recognised provenance' \
	internal/crew/roles.yaml \
	$'provenance: "@decided 2026-10-04, halved from the draft'"'"'s USD 5,000"' \
	$'provenance: "@owner 2026-10-04"'
run_case $'thresholds: the loader accepts any provenance at all' \
	fail \
	./internal/crew \
	$'TestProvenanceVocabulary' \
	$'accepted it, want refused' \
	internal/crew/roles.go \
	$'	return fmt.Errorf("provenance %.60q is not @claude, @decided YYYY-MM-DD or @measured <how> YYYY-MM-DD", s)' \
	$'	return nil'
run_case $'thresholds: the shell gate stops checking provenance' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesAThresholdWithAnUnrecognisedProvenance' \
	$'the gate passed a threshold with' \
	scripts/roles-are-bound.sh \
	$'	if ! grep -qE '"'"'^(@claude' \
	$'	if false && ! grep -qE '"'"'^(@claude'
run_case $'thresholds: a measured provenance with its how and date is not a fault' \
	pass \
	./internal/crew \
	$'^TestRolesAreBound$' \
	$'' \
	internal/crew/roles.yaml \
	$'provenance: "@decided 2026-10-04, halved from the draft'"'"'s USD 25,000"' \
	$'provenance: "@measured go test ./internal/crew -run TestTUrgentIsTwelveAndAHalfThousand 2026-10-04"'

run_case $'connector status: every entry claims Built regardless of its reader' \
	fail \
	./internal/connectors \
	$'TestBuiltMeansAReaderExists' \
	$'Built must hold exactly' \
	internal/connectors/connectors.go \
	$'if _, ok := readers[Catalogue[i].ID]; ok {' \
	$'if true {'
run_case $'generated estate: the refusal to mix it with real money is dropped' \
	fail \
	./internal/connectors \
	$'TestGeneratedEstateIsNotMixed' \
	$'-replace-generated' \
	internal/connectors/tokenfusefocus.go \
	$'if mixed && !opt.ReplaceGenerated {' \
	$'if false && mixed && !opt.ReplaceGenerated {'
run_case $'sub-cent calls: rounded per row before the sum instead of once after it' \
	fail \
	./internal/connectors \
	$'TestSubCentCallsRoundHalfAwayFromZeroOnceSummed' \
	$'want 4' \
	internal/connectors/tokenfusefocus.go \
	$'SUM(billed_microusd), SUM(tokens_in+tokens_out)' \
	$'SUM(((billed_microusd+5000)/10000)*10000), SUM(tokens_in+tokens_out)'
run_case $'rights vocabulary: an explanation for a right nothing grants' \
	fail \
	./internal/web \
	$'TestNoExplanationOutlivesItsRight' \
	$'can no longer' \
	internal/web/analyst.go \
	$'var rightMeans = map[string]string{' \
	$'var rightMeans = map[string]string{\n\t"ghost-right": "a power no agent has",'
run_case $'session guard: a download route loses its guard' \
	fail \
	./internal/web \
	$'TestEveryRouteRequiresASession' \
	$'turn a stranger away' \
	internal/web/export.go \
	$'\tif s.guard(w, r) == nil {\n\t\treturn\n\t}\n\tsource :=' \
	$'\tsource :='
run_case $'roles: the operator check leaves the one chokepoint' \
	fail \
	./internal/web \
	$'TestAViewerCannotWrite' \
	$'was not refused' \
	internal/web/work.go \
	$'\tif !u.May("operator") {\n\t\tredirectMsg(w, r, back, "your account may read and export, but not act")\n\t\treturn false\n\t}' \
	$'\tif false && !u.May("operator") {\n\t\treturn false\n\t}'
run_case $'roles: an operator can change a role' \
	fail \
	./internal/web \
	$'TestAnOperatorCannotEscalateThroughAccounts' \
	$'promote themselves' \
	internal/web/ops.go \
	$'\t\tif !u.May("admin") {' \
	$'\t\tif false && !u.May("admin") {'
run_case $'ownership: rebrief stops asking who owns it' \
	fail \
	./internal/web \
	$'TestAnOperatorCannotRebriefSomebodyElsesAgent' \
	$'raised the monthly guard' \
	internal/web/roster.go \
	$'\tif !mayManage(u, current) {\n\t\tredirectMsg(w, r, back, "only "+current.Owner+\n\t\t\t", who hired it, or an admin may rebrief it")\n\t\treturn\n\t}' \
	$'\tif false && !mayManage(u, current) {\n\t\treturn\n\t}'
run_case $'ownership: the check refuses everybody, owner included' \
	fail \
	./internal/web \
	$'TestTheOwnerAndAnAdminCanStillManage' \
	$'could not rebrief her own agent' \
	internal/web/roster.go \
	$'func mayManage(u *auth.User, a crew.Analyst) bool {\n\tif u == nil {' \
	$'func mayManage(u *auth.User, a crew.Analyst) bool {\n\treturn false\n\tif u == nil {'
run_case $'transfer: a restart undoes a placement a person made' \
	fail \
	./internal/crew \
	$'TestSeedOwnersDoesNotUndoAPlacementAPersonMade' \
	$'undoing a transfer' \
	internal/crew/owners.go \
	$'`UPDATE analysts SET owner=? WHERE desk=? AND owner=?`,\n\t\t\townerOfDesk[desk], desk, seededBy)' \
	$'`UPDATE analysts SET owner=? WHERE desk=?`,\n\t\t\townerOfDesk[desk], desk)'
run_case $'hire: a fixed owner is stamped instead of who signed' \
	fail \
	./internal/web \
	$'TestHiringMakesYouTheOwner' \
	$'every ownership rule' \
	internal/web/roster.go \
	$'\ta.Owner = u.Username' \
	$'\ta.Owner = "somebody-else"'
run_case $'determinism: rows come back in map order' \
	fail \
	./internal/world \
	$'TestAIUnitsAreOrderedTheSameEveryCall' \
	$'different order' \
	internal/world/telemetry.go \
	$'\tsort.Strings(keys)' \
	$'\t_ = sort.Strings'
run_case $'determinism: a page renders differently twice' \
	fail \
	./internal/web \
	$'TestPagesRenderTheSameTwice' \
	$'rendered differently' \
	internal/world/telemetry.go \
	$'\tsort.Strings(keys)' \
	$'\t_ = sort.Strings'
run_case $'ownership history: spend read from the roster instead' \
	fail \
	./internal/crew \
	$'TestSpendByOwnerReadsTheChargeNotTheRoster' \
	$'carries' \
	internal/crew/ownership.go \
	$'FROM tasks t`' \
	$'FROM tasks t LEFT JOIN analysts a ON a.name = t.assignee`' \
	internal/crew/ownership.go \
	$'SELECT COALESCE(t.owner,\'\')' \
	$'SELECT COALESCE(a.owner,\'\')'
run_case $'ownership history: a restart re-derives every charge' \
	fail \
	./internal/crew \
	$'TestEnsureOwnershipHistoryIsSafeToRunAgain' \
	$'undid the handover' \
	internal/crew/ownership.go \
	$'\t\t WHERE owner IS NULL`)' \
	$'\t\t WHERE 1=1`)'
run_case $'owners: a desk with agents and nobody to answer for them' \
	fail \
	./internal/crew \
	$'TestEveryDeskHasAnOwner' \
	$'has agents and no owner' \
	internal/crew/owners.go \
	$'\t"management": "y.mercer",' \
	$''
run_case $'owners: one password would open every installation' \
	fail \
	./internal/crew \
	$'TestUnusablePasswordIsDifferentEveryTime' \
	$'came back twice' \
	internal/crew/owners.go \
	$'\treturn base64.RawStdEncoding.EncodeToString(b), nil' \
	$'\t_ = base64.RawStdEncoding.EncodeToString(b)\n\treturn "one-string-everywhere", nil'
run_case $'agent card: the stops panel reads the event stream' \
	fail \
	./internal/web \
	$'TestTheStopsPanelDoesNotNeedTheEventStream' \
	$'reading the stream' \
	internal/web/stops.go \
	$'\trows, err := db.Query(`' \
	$'\tif name != "" {\n\t\treturn nil, nil\n\t}\n\trows, err := db.Query(`'

run_case $'csrf: the chokepoint stops checking the token' \
	fail \
	./internal/web \
	$'TestEveryWriteRouteChecksCSRF' \
	$'has to be turned away' \
	internal/web/work.go \
	$'\tif !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {' \
	$'\tif false && !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {'

run_case $'csrf: the accounts handler stops checking its own' \
	fail \
	./internal/web \
	$'TestEveryWriteRouteChecksCSRF' \
	$'has to be turned away' \
	internal/web/ops.go \
	$'\t\tif !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {' \
	$'\t\tif false && !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {'

run_case $'features: a scenario loses its binding' \
	fail \
	./internal/web \
	$'TestFeatureBindingsHold' \
	$'proves nothing' \
	features/roles.feature \
	$'  @test:TestAViewerCannotWrite\n' \
	$''

run_case $'features: a binding points at a test that is gone' \
	fail \
	./internal/web \
	$'TestFeatureBindingsHold' \
	$'names no test' \
	internal/web/roles_test.go \
	$'func TestAViewerCannotWrite(' \
	$'func GoneTestAViewerCannotWrite('

# The same sentence on all three pages that show a mixed cost. Each mutation
# has to COMPILE, which is why these live here and not in an ad-hoc loop: the
# obvious mutation, passing "" instead of the sentence, leaves two variables
# declared and not used, and Go then fails the build with the same exit code as
# a caught fault. That looked like a toothless gate for a while.
run_case 'a KPI that hides which part is real' fail ./internal/finops \
	'TestTheCrewCostKPISaysWhatIsRealMoney' \
	'does not say what of its figure is real' \
	internal/finops/kpi.go \
	'crew.RealMoney(liveMicros, liveTasks)' \
	'crew.RealMoney(liveMicros*0, liveTasks)'

run_case 'a KPI library that crashes on an empty detector' fail ./internal/finops \
	'TestTheKPISaysNothingAboutMoneyNobodySpent' \
	'converting NULL to int' \
	internal/finops/kpi.go \
	"COALESCE(SUM(CASE WHEN state='open' THEN 1 ELSE 0 END),0)," \
	"SUM(CASE WHEN state='open' THEN 1 ELSE 0 END),"

run_case 'a card reporting the whole board as its own' fail ./internal/web \
	'TestTheAgentCardSaysWhatOfItsCostIsReal' \
	'does not say what of its cost is real' \
	internal/web/templates/analyst.html \
	'{{if .RealMoney}}<br><strong>{{.RealMoney}}</strong>{{end}}' \
	'{{if false}}<br><strong>{{.RealMoney}}</strong>{{end}}'

run_case 'a sentence about money nobody spent' fail ./internal/finops \
	'TestTheKPISaysNothingAboutMoneyNobodySpent' \
	'want empty' \
	internal/crew/provenance.go \
	'if tasks == 0 || micros == 0 {' \
	'if tasks < 0 || micros < 0 {'

# The prompt bound must cover the whole prompt. It counted the PIECES a prompt
# is built from and none of the fixed text around them: measured on a real task,
# 225 tokens bounded against a 559-byte prompt. It held, because a real tokeniser
# gives about a quarter of a token per byte, and that is not the point: the
# sentence says one token per byte, "which no tokeniser can exceed".
run_case 'a bound narrower than its own promise' fail ./tools/run \
	'TestThePromptBoundCoversTheWholePrompt' \
	'short by' \
	tools/run/main.go \
	'e.PromptTokens = tokens(prompt(t, a, "0000-00-00", e.Packet))' \
	'e.PromptTokens = tokens(t.Title, t.Goal, a.Mission, a.Role)'

# A deliverable must not show its own syntax. The seeded drafts were written to
# match the renderer, so it handled bold and "## " and everything agreed with
# itself; a model then wrote 44 and the page printed ###, --- and dashes back.
run_case 'a deliverable showing its own markdown' fail ./internal/web \
	'TestADeliverableDoesNotShowItsOwnSyntax' \
	'still in the output' \
	internal/web/work.go \
	'if h, level := heading(p); h != "" {' \
	'if h, level := heading(p); false && h != "" {'

run_case 'a heading glued to the line under it' fail ./internal/web \
	'TestADeliverableDoesNotShowItsOwnSyntax' \
	'still in the output' \
	internal/web/work.go \
	'strings.Split(standalone(src), "\n\n")' \
	'strings.Split(src, "\n\n")'

run_case 'a rule printed as three dashes' fail ./internal/web \
	'TestADeliverableDoesNotShowItsOwnSyntax' \
	'still in the output' \
	internal/web/work.go \
	'if isRule(p) {' \
	'if false && isRule(p) {'

# The body is written by a model, so escaping is the only thing between it and
# the reader. inline() escapes FIRST and puts back two marks after.
run_case 'a model able to put a tag on the page' fail ./internal/web \
	'TestADeliverableCannotPutATagOnThePage' \
	'reached the page' \
	internal/web/work.go \
	'esc := html.EscapeString(s)' \
	'esc := html.UnescapeString(s)'

run_case 'a model left to guess the date' fail ./tools/run \
	'TestTheModelIsToldTheDate' \
	'does not carry the date' \
	internal/deliver/prompt.go \
	'fmt.Fprintf(&b, "\nToday is %s.\n", today)' \
	'fmt.Fprintf(&b, "\n%s", today[:0])'

# One figure covering generated and live spend together. Invariant 16 carried
# this as its open item: the deliverables were marked, the money was not.
run_case 'a crew figure that hides which part is real' fail ./internal/web \
	'TestTheCrewPageSaysWhatOfItsFigureIsReal' \
	'does not say how much of its figure is real' \
	internal/web/templates/staff.html \
	'{{if .RealMoney}}<br><strong>{{.RealMoney}}</strong>{{end}}' \
	'{{if false}}<br><strong>{{.RealMoney}}</strong>{{end}}'

# The live marker must read as a marker. .chip carries 5.97:1 in light mode,
# .tile carries 1.29; the marker was drawn with the container family at 1.2:1.
run_case 'a marker drawn like a panel edge' fail ./internal/web \
	'TestTheLiveMarkerIsDrawnLikeAMarker' \
	'the box is not there' \
	internal/web/assets/app.css \
	'color: var(--ink-2); border: 1px solid var(--ink-3);' \
	'color: var(--ink-3); border: 1px solid var(--line);'

# The page's scroll must not be able to move the sidebar. Three attempts: sticky
# gave it its own scrollbar, static let the page's momentum slide it under the
# cursor, fixed is the only one the page cannot touch.
run_case 'a sidebar the page can move' fail ./internal/web \
	'TestThePageCannotMoveTheSidebar' \
	'is not fixed' \
	internal/web/assets/app.css \
	'  position: fixed; top: 0; left: 0; z-index: 5;' \
	'  position: sticky; top: 0; left: 0; z-index: 5;'

run_case 'content rendered under the sidebar' fail ./internal/web \
	'TestThePageCannotMoveTheSidebar' \
	'renders underneath' \
	internal/web/assets/app.css \
	'main { flex: 1; min-width: 0; margin-left: 190px;' \
	'main { flex: 1; min-width: 0;'

# Many small calls must not add up to more than they cost. A run billed 0.2337
# and the crew page said 0.56. TWO faults produce that number and both must go
# red: rounding each CALL up, and rounding each TASK up, which is the same thing
# when the runner makes one call per task and is what the first fix left behind.
run_case 'each task rounded up on its own' fail ./tools/run \
	'TestTheLedgerDoesNotOverstateManySmallCalls' \
	'overstates the run' \
	internal/crew/provenance.go \
	'whole := r.micros / 10_000' \
	'whole := (r.micros + 9_999) / 10_000'

run_case 'the run is never settled into cents' fail ./tools/run \
	'TestTheLedgerDoesNotOverstateManySmallCalls' \
	'overstates the run' \
	internal/crew/provenance.go \
	'for i := 0; handed < want && i < len(rems); i++ {' \
	'for i := 0; false && handed < want && i < len(rems); i++ {'

# A task somebody stopped stays stopped. crew.TaskFilter{OpenOnly} includes
# blocked, which is right for a board and wrong for a thing that does the work.
run_case 'work done past a reason a person recorded' fail ./tools/run \
	'TestABlockedTaskIsNotWorkedAround' \
	'picked up anyway' \
	tools/run/main.go \
	'if t.State == "blocked" {' \
	'if t.State == "no-such-state" {'

# The model must be asked for an answer rather than for reasoning. Four tasks on
# a full run spent their whole token budget thinking, reached max_tokens with no
# text, and blocked -- billed in full for nothing a person could read.
#
# anthropicBody, and this test with it, moved to internal/deliver/call.go
# with call() (B6B-SPEC.md); this case follows it there. It read expect as
# "caught" until 2026-09-03: a word run_case never defined, so it matched
# neither TOOTHLESS nor OVEREAGER and printed ok whatever the mutation did.
# Renamed to "fail" along with nineteen others once the harness was made to
# refuse a word it does not know instead of passing it through as green.
run_case 'the model is left free to think instead of answering' fail ./internal/deliver \
	'TestAnthropicIsAskedForAnAnswerRatherThanReasoning' \
	'want disabled' \
	internal/deliver/call.go \
	'"thinking":   map[string]any{"type": "disabled"},' \
	'"thinking":   map[string]any{"type": "enabled"},'

# Provenance. A live deliverable and a generated one land in the same table with
# the same author and the same state, and for one full run 63 real ones sat
# indistinguishable among 342. Two faults can bring that back: the writer going
# quiet about what it wrote, and the page going quiet about what it was told.
run_case 'a deliverable that does not say a model wrote it' fail ./tools/run \
	'TestARunnerDeliverableIsMarkedLive' \
	'indistinguishable' \
	tools/run/live.go \
	"'draft', datetime('now'), 'live'" \
	"'draft', datetime('now'), 'fixture'"

run_case 'a marker no page displays' fail ./internal/web \
	'TestTheTaskPageShowsWhichDeliverableWasWrittenLive' \
	'want exactly 1' \
	internal/web/templates/task.html \
	'{{if eq .Source "live"}}' \
	'{{if eq .Source "no-such-source"}}'

echo
echo "=== and what they must NOT catch ==="
run_case $'session guard: a route named in a comment' \
	pass \
	./internal/web \
	$'TestEveryRouteRequiresASession' \
	$'' \
	internal/web/server.go \
	$'func (s *Server) intakeTemplate(' \
	$'// s.mux.HandleFunc("GET /not-a-real-route") appears here in prose only.\nfunc (s *Server) intakeTemplate('
run_case $'rights vocabulary: a right added with its explanation' \
	pass \
	./internal/web \
	$'TestEveryGrantableRightIsExplained|TestNoExplanationOutlivesItsRight' \
	$'' \
	internal/crew/roster.go \
	$'"export-data", "kpi-registry",' \
	$'"export-data", "kpi-registry", "ledger-read",' \
	internal/web/analyst.go \
	$'var rightMeans = map[string]string{' \
	$'var rightMeans = map[string]string{\n\t"ledger-read": "read the charge ledger as it was billed",'
run_case $'owners: a desk moved to a different person' \
	pass \
	./internal/crew \
	$'TestEveryDeskHasAnOwner|TestSeedOwnersPlacesTheWholeRoster' \
	$'' \
	internal/crew/owners.go \
	$'"azure":  "j.ashby",' \
	$'"azure":  "j.calder",'

# The flag with money behind it. `-live` refuses to run without `-ceiling`, and
# stack-k8s hands that ceiling in by `$(COSTCREW_CEILING)` substitution, so the
# only thing standing between a crew and a provider account is a figure this
# manifest has to keep declaring. Until 2026-09-01 the flag test read the
# console alone and this component declared no flags at all, which is how
# estate-gates saw the variable arrive from outside with no reader anywhere.
run_case $'the runner\'s ceiling stops being declared' \
	fail \
	./internal/manifest \
	$'TestEveryFlagEveryBinaryDefinesIsDeclaredAndTheReverse' \
	$'costcrew-run defines -ceiling' \
	components.json \
	$'"ceiling": {\n            "required": false\n          },\n          ' \
	$''

echo
echo "=== subject taken away: a gate must not report success at doing nothing ==="

# The failure Go makes easy, and the reason gate() exists at all.
#
#     go test ./internal/web/ -run TestNoSuchThing
#     ok  ...  [no tests to run]   exit 0
#
# The first version of this case renamed the test by APPENDING to its name, and
# both cases came back TOOTHLESS: -run takes an unanchored regexp, so the
# pattern still matched the renamed function and the subject had not gone
# anywhere. Prefixing removes it.
run_case 'a renamed test is not a passing test' gone ./internal/web \
	'TestAViewerCannotWrite' \
	'MEASURED NOTHING' \
	internal/web/roles_test.go \
	'func TestAViewerCannotWrite(' \
	'func GoneTestAViewerCannotWrite('

run_case 'a deleted gate is not a passing gate' gone ./internal/crew \
	'TestEveryDeskHasAnOwner' \
	'MEASURED NOTHING' \
	internal/crew/owners_test.go \
	'func TestEveryDeskHasAnOwner(' \
	'func GoneTestEveryDeskHasAnOwner('

# B2: a tool is called only under a right the analyst holds, and a query
# reaches only the charges. Two mutants for the two halves of that sentence;
# charges_query.go's own four (table allow-list, the read-only connection's
# _query_only, the semicolon refusal, the row cap) are proven by hand in the
# PR body rather than carried here, the same way B1a's roles teeth case
# calls out to a script instead of duplicating its whole fault list.
run_case $'skills are tools: the dispatcher stops checking the right' \
	fail \
	./tools/run \
	$'TestAToolTheAnalystHasNoRightForIsRefused' \
	$'tool_refused' \
	tools/run/dispatch.go \
	$'if !hasString(rights, def.Right) {' \
	$'if false && !hasString(rights, def.Right) {'
run_case $'skills are tools: charges_query drops its table allow-list' \
	fail \
	./tools/run \
	$'TestChargesQueryHostileInputs' \
	$'want refused' \
	tools/run/charges_query.go \
	$'if !chargesAllowedTables[tb] {' \
	$'if false {'

# T3 review of PR #20: a whole-statement identifier scan against
# sqlite_master, independent of the FROM/JOIN walk above, so a construct
# that walk's structural tracking gets wrong is not the only thing
# standing between the model's text and a table this tool does not allow.
# Targeted at the test written to isolate it (TestRefuseUnknownTablesCatchesARealDisallowedTable),
# not at an end-to-end hostile-input case: tablesInSQL already catches
# every hostile input this file's own tests construct, so an end-to-end
# case would pass on this mutant exactly the way wrapWithLimit's own
# mutant once slipped past TestChargesQueryResultIsCappedAt200Rows.
run_case $'skills are tools: the whole-statement identifier scan is dropped' \
	fail \
	./tools/run \
	$'TestRefuseUnknownTablesCatchesARealDisallowedTable' \
	$'did not refuse' \
	tools/run/charges_query.go \
	$'if real[low] && !chargesAllowedTables[low] {' \
	$'if real[low] && false {'

# WITH is refused unconditionally, anywhere in the statement -- not only
# where a plain "must start with SELECT" check would already catch a
# top-level one, and not only where tablesInSQL's own FROM/JOIN walk would
# independently catch a disallowed table. Targeted at the test built to
# isolate exactly that: a CTE named "charges" shadows the real table, so
# tablesInSQL sees only the allowed name and finds nothing to refuse on
# its own.
run_case $'skills are tools: a CTE naming itself charges is allowed again' \
	fail \
	./tools/run \
	$'TestATableNamedCTEPassesTablesInSQLButNotTheWithBan' \
	$'accepted a CTE' \
	tools/run/charges_query.go \
	$'if withAnywhereRE.MatchString(trimmed) {' \
	$'if false {'

# C7: ai_calls_query is charges_query's own shape, scoped to ai_calls, and
# deliberately its OWN file rather than a shared, parameterised check --
# see internal/deliver's own comment on why (the three cases above plant
# their mutant by an exact literal match against charges_query.go, and a
# shared allow-list check would have broken all three). One teeth case,
# named in C7-SPEC.md section 4 by these exact words: "drop the allow-list
# scan on ai_calls_query".
run_case $'skills are tools: ai_calls_query drops its table allow-list' \
	fail \
	./tools/run \
	$'TestAICallsQueryHostileInputs' \
	$'want refused' \
	tools/run/ai_calls_query.go \
	$'if !aiCallsAllowedTables[tb] {' \
	$'if false {'
# PARTNER-BUDGETS-RIGHT-SPEC.md, invariant 34: a role family's own reads
# line is backed by a right the console actually grants. Two mutants, one
# per direction: dropping budgets-read off stakeholder-briefing reproduces
# the live failure this invariant fixes (finops-partner's own reads line
# promises "the team's budgets"), caught by the generic per-family gate;
# adding a right nothing in that same family's reads line asks for is the
# opposite fault, over-grant rather than under-grant, caught by the
# equality check on the one skill this defect was found on.
run_case $'reads promise: stakeholder-briefing loses budgets-read again' \
	fail \
	./internal/crew \
	$'TestEveryFamilysReadsPromiseIsBackedByARight' \
	$'does not hold it' \
	internal/crew/mandate.go \
	$'"stakeholder-briefing":     {"figures-read", "channel-post", "budgets-read"},' \
	$'"stakeholder-briefing":     {"figures-read", "channel-post"},'
run_case $'reads promise: stakeholder-briefing gains a right nothing asked for' \
	fail \
	./internal/crew \
	$'TestStakeholderBriefingGrantsExactlyItsThreeRights' \
	$'RightsFor(stakeholder-briefing)' \
	internal/crew/mandate.go \
	$'"stakeholder-briefing":     {"figures-read", "channel-post", "budgets-read"},' \
	$'"stakeholder-briefing":     {"figures-read", "channel-post", "budgets-read", "kpi-registry"},'

# B3: an analyst's deliverable ends in options, and only a stamp -- the
# supervisor's own act, or an owner's on a carried one -- applies one.
# B3-SPEC.md section 6 names this mutant by its own words, "let Post apply
# an option": work.go's artifactAction is the one place a person's Post
# reaches the database, and this plants exactly the fault the sentence
# describes, using only internal/crew (already imported here) rather than
# internal/finops.Apply, because the property under test is that POSTING
# must not touch an option's state at all, not that the wrong side effect
# ran. The anchor moved onto C1's own tellOwnerAnomalyExplained call (C1
# added the `if err == nil {` guard this needle now lives inside of) without
# changing what the case proves.
run_case $'options: an analyst'"'"'s Post applies an option' \
	fail \
	./internal/web \
	$'TestOnlyTheOwnersStampAppliesAKeyDecision' \
	$'Post must apply nothing' \
	internal/web/work.go \
	$'\t\t\t\ts.tellOwnerAnomalyExplained(id)\n\t\t\t}\n\t\t} else {' \
	$'\t\t\t\ts.tellOwnerAnomalyExplained(id)\n\t\t\t\tif opts, _ := crew.Options(s.db, id); len(opts) > 0 {\n\t\t\t\t\t_ = crew.MarkOptionApplied(s.db, opts[0].Artifact, opts[0].Ordinal, u.Username)\n\t\t\t\t}\n\t\t\t}\n\t\t} else {'

# C1-SPEC.md section 4's own named mutant, "emit before the post instead of
# after": anomaly_explained is C1's own notification to the anomaly's
# owner, and it must be a CONSEQUENCE of the post actually having succeeded,
# never of the attempt. Moving the call ahead of crew.Post makes it fire
# unconditionally, including on a refused second post (an artifact already
# posted, "a stamp is not taken back") -- exactly the case
# TestARefusedSecondPostTellsNobodyTwice exists to catch: after one real
# post and one refused one, it insists the journal still names the anomaly
# exactly once.
run_case 'anomaly desk: emit before the post instead of after' \
	fail \
	./internal/web \
	$'TestARefusedSecondPostTellsNobodyTwice' \
	$'want still 1' \
	internal/web/work.go \
	$'\t\t\terr = crew.Post(s.db, id, u.Username, "owner")\n\t\t\tif err == nil {\n\t\t\t\t// C1-SPEC.md section 2: AFTER the post has actually\n\t\t\t\t// succeeded, never before -- a refused post (an artifact\n\t\t\t\t// already posted) must tell nobody anything, because it did\n\t\t\t\t// not happen.\n\t\t\t\ts.tellOwnerAnomalyExplained(id)\n\t\t\t}' \
	$'\t\t\ts.tellOwnerAnomalyExplained(id)\n\t\t\terr = crew.Post(s.db, id, u.Username, "owner")'

# Review of this PR's first version found toldAnomalies matching on the
# event name "anomaly_explained" alone, a false positive: internal/anomaly's
# own pre-existing, spec-unchanged state-transition emit fires that same
# name on every Explain/Dismiss/Accept, including the pre-existing direct
# POST /anomalies/{id}/explain route, which has no owner to tell at all.
# Dropping the "owner" field check reintroduces exactly that: a direct
# explain, with nobody ever told, reads "told" again.
run_case 'anomaly desk: told matches the event name alone, not its owner field' \
	fail \
	./internal/web \
	$'TestDirectExplainDoesNotFalselyMarkTheQueueTold' \
	$'even though no owner was ever notified' \
	internal/web/anomaly_told.go \
	$'\t\tif rec.Event != "anomaly_explained" {\n\t\t\tcontinue\n\t\t}\n\t\tif stringField(rec.Data, "owner") == "" {\n\t\t\tcontinue\n\t\t}\n\t\tif id := stringField(rec.Data, "anomaly"); id != "" {' \
	$'\t\tif rec.Event != "anomaly_explained" {\n\t\t\tcontinue\n\t\t}\n\t\tif id := stringField(rec.Data, "anomaly"); id != "" {'

# B7: the bench (tools/bench) scores a named cause against the truth a
# generated fixture's registry already knows, and it can only prove
# anything if the cause it checks was actually hidden first. B7-SPEC.md
# section 5 names this mutant by its own words, "leave the driver: line in
# the bench packet": internal/deliver/packet.go's AnomalySection prints it
# unconditionally, exactly what the unexported anomalySection() in
# tools/run/packet.go did before this step, and what any caller reusing it
# for a bench would need to hide.
run_case 'bench: the driver: line is left in a hiding-mode packet' \
	fail \
	./internal/deliver \
	$'TestBenchPacketHidesTheDriverLabelAndItsKind' \
	$'still names the driver label' \
	internal/deliver/packet.go \
	$'if an.Driver != "" && !hideDriver && !pol.Aggregates() {' \
	$'if an.Driver != "" && !pol.Aggregates() {'

# B7-SPEC.md section 5's second named mutant: "score cause by substring of
# the whole deliverable instead of the named cause". A deliverable can
# carry the driver's own words somewhere in its body (echoed back from the
# task description, say) without ever naming them as ITS cause, and a
# scorer that checked the whole body rather than the extracted named cause
# would credit that as a match.
run_case 'bench: cause scored by substring of the whole body' \
	fail \
	./tools/bench \
	$'TestScoreJudgesTheNamedCauseNotTheWholeBody' \
	$'never named as' \
	tools/bench/score.go \
	$'CauseMatched: causeMatches(an.Driver, named),' \
	$'CauseMatched: causeMatches(an.Driver, body),'

# B7-SPEC.md section 5's third named mutant, the
# finest-unit-per-row-round-once-at-the-aggregate principle invariant 25
# already holds for ai_calls: "count cost per call rounded to cents"
# instead of summing micro-dollars and rounding once at the total. Two
# cases at 0.3 of a cent each round to nothing individually and to a real
# 0.6 of a cent summed first.
run_case 'bench: cost summed after rounding each case to cents' \
	fail \
	./tools/bench \
	$'TestReportTotalSumsMicrosBeforeAnyRounding' \
	$'summed to nothing' \
	tools/bench/report.go \
	$'totalMicros += r.Score.CostMicros' \
	$'totalMicros += (r.Score.CostMicros / 10_000) * 10_000'

# B8: memory, in the store first. An analyst's packet now also carries its
# OWN last three posted deliverables on this desk, each with the fate of
# every option it ended in, and drivers reach back six months instead of
# ninety days, capped at 24 rows with "and N more". B8-SPEC.md section 4
# names four mutants by their own words; these are them.
run_case 'memory: own history is not scoped to the one analyst' \
	fail \
	./internal/deliver \
	$'TestOwnHistoryHidesAnotherAnalystsDeliverableOnTheSameDesk' \
	$'another analyst'"'"'s deliverable on the same desk was shown' \
	internal/deliver/packet.go \
	$'\t\tWHERE ar.author = ? AND ar.state = \'posted\' AND t.desk = ?' \
	$'\t\tWHERE ar.state = \'posted\' AND t.desk = ?' \
	internal/deliver/packet.go \
	$'\t\tLIMIT 3`, a.Name, desk)' \
	$'\t\tLIMIT 3`, desk)'

run_case 'memory: own history drops the fate line' \
	fail \
	./internal/deliver \
	$'TestOwnHistoryShowsTheFateOfEveryOptionState' \
	$'not found for state' \
	internal/deliver/packet.go \
	$'\t\t\tfmt.Fprintf(&b, "  - %s: %s (%s)\\n", o.Class, trimBytes(o.Summary, 80), fateOf(db, o))' \
	$'\t\t\tfmt.Fprintf(&b, "  - %s: %s\\n", o.Class, trimBytes(o.Summary, 80))'

run_case 'memory: drivers keep the old ninety-day window' \
	fail \
	./internal/deliver \
	$'TestDriversSectionReachesOneHundredTwentyDays' \
	$'is missing from driversSection' \
	internal/deliver/packet.go \
	$'\tdriversSectionWindowDays = 180' \
	$'\tdriversSectionWindowDays = 90'

# B8-SPEC.md section 4's fourth named mutant: "trim the anomaly section
# instead of the history section". Prepending ownHistorySection's own
# content to the front of sections, rather than appending it to the end,
# makes memory the thing BoundBytes protects and something else (here,
# whatever was last before this section existed) the thing it cuts instead.
run_case 'memory: history is prepended instead of appended, so it no longer yields first' \
	fail \
	./internal/deliver \
	$'TestOwnHistoryNeverCrowdsOutTheAnomalyUnderTheCap' \
	$'the anomaly section is not intact' \
	internal/deliver/packet.go \
	$'if s := ownHistorySection(db, a, t.Desk); s != "" {\n\t\t\tsections = append(sections, s)\n\t\t}' \
	$'if s := ownHistorySection(db, a, t.Desk); s != "" {\n\t\t\tsections = append([]string{s}, sections...)\n\t\t}'

# B5-SPEC.md section 7's named mutant, "skip the switch check": invariant 31
# (CLAUDE.md), "no clock-driven run spends without the console's switch AND
# the ceiling, both a person's act". The switch is read and checked TWICE on
# purpose (duePreflight, before anything is priced, and again in dueExecute,
# right before the first call, so a person turning it off mid-run still
# stops it) -- both checks read as the identical source line
# `if !enabled {`, and apply_edits replaces one occurrence per triple, so the
# same triple is given twice to disable both; disabling only one still
# leaves the other catching the fault.
#
# The test's fixture gives cadence.ceiling_cents a generous, nonzero value
# WHILE THE SWITCH IS OFF, deliberately: a zero ceiling is "off" by another
# name (section 2) and would refuse a live run on its own, which would make
# this case pass for the wrong reason. @measured 2026-09-03, planting this
# exact mutation by hand: without the nonzero ceiling the case still turned
# red, but on a DIFFERENT assertion (the ceiling-refusal message, not "the
# switch off was accepted"), because a zero ceiling refuses independently of
# the switch. With the nonzero ceiling the mutation is unambiguous: a sprint
# and a task are actually created ("cadence-due: 1 task(s) created..."),
# proving the run proceeded past the switch entirely.
run_case 'due: skip the switch check' \
	fail \
	./tools/run \
	$'TestDueWithTheSwitchOffExitsTwoAndCreatesNothing' \
	$'-due with the switch off was accepted' \
	tools/run/due.go \
	$'if !enabled {' \
	$'if !enabled && false {' \
	tools/run/due.go \
	$'if !enabled {' \
	$'if !enabled && false {'

# C9-SPEC.md section 4's own named mutant, "skip the -due check for a halted
# desk": CLAUDE.md invariant 33. CadenceDue is the ONE function both -due
# and Propose route their cadence-due work through, so disabling the check
# it makes here disables it for both without a second case. `is && false`
# is deliberate rather than deleting the `if` outright, the same shape
# invariant 31's own "skip the switch check" case above uses: it keeps the
# mutation to a single token so a reader can see exactly what was turned
# off, and the source still compiles with the branch simply never taken.
run_case 'due: skip the -due check for a halted desk' \
	fail \
	./internal/crew \
	$'TestCadenceDueSkipsAHaltedDeskAndSaysWhy' \
	$'on the HALTED' \
	internal/crew/plan.go \
	$'if _, is := halted[a.Desk]; is {' \
	$'if _, is := halted[a.Desk]; is && false {'

# B6B-SPEC.md section 4: "a second net/http import under tools/" -- the
# whole point of moving call() into internal/deliver is that neither binary
# can open a second door of its own, and the structural test on each side
# (tools/bench's TestNoFileInThisPackageCanMakeAnHTTPRequest,
# tools/run's own TestLiveDotGoHoldsNoWayToMakeAnHTTPRequestAnyMore) is what
# would catch a future edit re-adding one. Planted as a comment rather than
# a real import: a real, unused "net/http" import would fail to COMPILE, and
# this script's own header explains why that is judged BROKEN rather than
# CAUGHT -- the mutation would prove nothing about the test, only that Go
# refuses an unused import. A comment containing the literal substring
# compiles cleanly and is exactly what the test's own plain
# strings.Contains scan (deliberately naive, so it cannot be fooled by an
# import alias) cannot tell apart from a real one.
run_case 'bench: a second net/http door' \
	fail \
	./tools/bench \
	$'TestNoFileInThisPackageCanMakeAnHTTPRequest' \
	$'contains "net/http"' \
	tools/bench/gateway.go \
	$'package main' \
	$'package main\n\n// net/http, planted only by gates-have-teeth.sh'

run_case 'run: live.go grows a second net/http door' \
	fail \
	./tools/run \
	$'TestLiveDotGoHoldsNoWayToMakeAnHTTPRequestAnyMore' \
	$'contains "net/http"' \
	tools/run/live.go \
	$'package main' \
	$'package main\n\n// net/http, planted only by gates-have-teeth.sh'

# B4-STEP-TWO-SPEC.md section 6's four named mutants, plan-ask: the four
# checks crew.ValidatePlanAnswer holds are each collapsed to one boolean
# gate for exactly this reason (see plan_ask.go's own comment on
# refInvalid) -- three of the four bullets section 3 names (ref in range,
# headroom, budget only down) would otherwise need TWO simultaneous edits
# each to defeat, because a second, independent check happens to catch the
# same fault; one line, mutated to a tautology that still references every
# identifier it reads (so the mutation compiles), is what makes each of
# these a single triple instead of two.
#
# "Accept an item without a ref": refInvalid is forced false while ref,
# refErr and n all stay referenced. The deterministic plan in this test has
# exactly one item, so the accepted-but-invalid ref (0, from the empty
# json.Number ParseInt refuses) indexes deterministic.Items[-1] two lines
# later and panics -- a caught fault by any measure (go test exits
# non-zero), and an honest one: skipping the ref check does not quietly
# accept the item, it corrupts the very next line.
run_case 'plan-ask: accept an item without a ref' \
	fail \
	./internal/crew \
	$'TestAnItemWithNoRefIsRefusedWhole' \
	$'index out of range' \
	internal/crew/plan_ask.go \
	$'refInvalid := refErr != nil || ref < 1 || ref > int64(n)' \
	$'refInvalid := false && (refErr != nil || ref < 1 || ref > int64(n))'

# "Skip the headroom check": the SAME mutation shape, on the OTHER gate
# section 3 names, "assignee has headroom this month".
run_case 'plan-ask: skip the headroom check' \
	fail \
	./internal/crew \
	$'TestARouteToAnAnalystWithNoHeadroomIsRefused' \
	$'expected a refusal for no headroom left' \
	internal/crew/plan_ask.go \
	$'if headroomOf(a, spent) <= 0 {' \
	$'if false && headroomOf(a, spent) <= 0 {'

# "Let budget_cents go up": section 2's own words, "budget_cents may only go
# down"; section 3's own words, "at most the deterministic item's budget".
run_case 'plan-ask: let budget_cents go up' \
	fail \
	./internal/crew \
	$'TestABudgetRaisedAboveTheDeterministicItemIsRefused' \
	$'expected a refusal for a budget raised above the deterministic item' \
	internal/crew/plan_ask.go \
	$'if budget > det.Budget {' \
	$'if false && budget > det.Budget {'

# "Charge the cost to nobody": SettlePlanAsk's own rounding, "up, never
# down" (the same rule SettleLiveSpend already holds), replaced with a flat
# zero -- the call still happened, the row still gets written, and the
# figure a person reads says nothing was spent. micros stays referenced (the
# INSERT's own argument list), so this compiles.
run_case 'plan-ask: charge the settled cost to nobody' \
	fail \
	./internal/crew \
	$'TestSettlePlanAskLandsInSpendInMonthForSupervisor' \
	$'want 0.01' \
	internal/crew/plan_ledger.go \
	$'cents := (micros + 9_999) / 10_000' \
	$'cents := int64(0)'
# C2-SPEC.md section 4's own named mutant, "accept a target-less
# allocation.rule": invariant 33 (CLAUDE.md). allocation.rule alone, of
# every class an analyst's deliverable may name, carries a structured
# target (rule_id, method, share), and crew.ValidateAndSaveOptions refuses
# the class's option whole when that target is absent. Disabling the `if
# o.Class == "allocation.rule"` guard is exactly the sentence's own fault:
# the save-time gate stops checking the one class it exists to check, and a
# target-less allocation.rule option is written to artifact_options
# unrefused.
run_case 'C2: accept a target-less allocation.rule' \
	fail \
	./internal/crew \
	$'TestAllocationRuleWithNoTargetIsRefused' \
	$'allocation.rule with no target was accepted' \
	internal/crew/options.go \
	$'\t\tif o.Class == "allocation.rule" {' \
	$'\t\tif false && o.Class == "allocation.rule" {'
# C8-SPEC.md section 4's own named mutant: "show a refused KPI as zero".
# executiveFigureLine's Blocked check is what keeps cost-per-outcome (always
# refused in this console until C7) from ever falling into the value
# branches below it; removing it does not merely blank the line, because
# ExecutiveFigure.Numeric is a real float64 that defaults to Go's own zero
# value when HasVal is false (internal/finops/kpi.go's own comment on the
# field explains why on purpose) -- so the mutant does not fail to compile
# or panic, it prints "Cost per business outcome: 0.0 (previous period:
# refused, ...)", a refusal wearing a number, which is the exact shape this
# console's own COALESCE history (invariant 24's SUM bug) has been bitten by
# twice already. @measured 2026-09-03, planting this exact mutation by hand
# before adding it here.
run_case 'the executive pack: show a refused KPI as zero' \
	fail \
	./internal/deliver \
	$'TestExecutiveSectionShowsARefusedKPIAsRefusedNeverZero' \
	$'does not show cost-per-outcome as refused' \
	internal/deliver/packet.go \
	$'func executiveFigureLine(f finops.ExecutiveFigure) string {\n\tif f.Blocked != "" {\n\t\treturn fmt.Sprintf("%s: refused, %s\\n", f.Name, f.Blocked)\n\t}\n\tif !f.HasVal {\n\t\treturn "" // neither a value nor a refusal: nothing here to say, never invented\n\t}\n\tif !f.HasPeriod {' \
	$'func executiveFigureLine(f finops.ExecutiveFigure) string {\n\tif !f.HasPeriod {'
# C5-SPEC.md section 4's own named mutant, "rank by current cost": the
# optimizer's packet section ranks its recommendations by saving, and this
# swaps the comparator to read Current (the resource's own size string,
# e.g. "m5.2xlarge") instead of MonthlySavingCents. Go allows > on
# strings, so this still compiles.
#
# @measured 2026-09-03, planting this exact mutation by hand: the golden
# fixture's own five rows happen to keep i-0a1b... ahead of i-0b2c... under
# EITHER comparator (their Current strings sort the same way their savings
# do, by coincidence), so a test that checks only that one pair passes
# right through the mutant. TestRecommendationsSectionCapsAtTenWithAndNMore
# does not share that coincidence: its twelve planted rows all carry the
# SAME Current value, so the mutated comparator degenerates entirely to
# the resource-name tie-break and cuts the two HIGHEST-saving rows instead
# of the two lowest. TestRecommendationsSectionRanksBySavingFromAFixtureImport
# was rewritten to check the full five-row order rather than one pair, so
# it now catches the same mutant too.
#
# Coordinator review of PR #34, 2026-09-03, found that this case only ever
# mutated the comparator's copy in internal/deliver, while web's own
# /rightsizing page carried an identical, separately-maintained copy this
# case never touched: a mutation planted directly in the page's own copy
# compiled clean and passed the whole internal/web suite, since nothing
# there checked row order either. The comparator now lives in exactly one
# place, connectors.RankBySaving (internal/connectors/rightsizing.go), and
# both deliver.recommendationsSection and the page call it rather than
# each carrying their own copy, so this one case protects both callers by
# construction. Retargeted here at RankBySaving's own direct test, the
# fastest of the (now three) tests this mutation breaks -- the other two,
# TestRecommendationsSectionCapsAtTenWithAndNMore /
# TestRecommendationsSectionRanksBySavingFromAFixtureImport (internal/deliver)
# and TestTheRightsizingPageOrdersRowsBySavingNotBySize (internal/web),
# are not wired into their own run_case, the same "either one going
# toothless should still be caught by the other" reasoning this file
# already uses elsewhere: all three were @measured 2026-09-03 against this
# exact mutation by hand (PR report has the transcripts).
run_case 'rightsizing: rank by current cost instead of saving' \
	fail \
	./internal/connectors \
	$'TestRankBySavingOrdersDescendingWithResourceTiebreak' \
	$'position 0: got res-0, want res-3' \
	internal/connectors/rightsizing.go \
	$'if recs[i].MonthlySavingCents != recs[j].MonthlySavingCents {\n\t\t\treturn recs[i].MonthlySavingCents > recs[j].MonthlySavingCents\n\t\t}' \
	$'if recs[i].Current != recs[j].Current {\n\t\t\treturn recs[i].Current > recs[j].Current\n\t\t}'
# C6: vendor seat and renewal data from a saas-seats CSV. C6-SPEC.md section
# 4 names three mutants by their own words; these are them.
#
# "compute waste with floats": 29 idle seats at one cent each is 29 cents
# exact in int64 arithmetic. A dollars-then-back-to-cents float64 round trip
# (idle*perSeat/100.0*100.0, truncated the way a naive rewrite would do it)
# lands on 28.999999999999996 and truncates to 28 -- @measured, python3:
# `29/100*100` is `28.999999999999996`. Most cent amounts survive the same
# round trip exactly, which is why this needed a specific value rather than
# any row in the fixture, and why the fixture's own four rows (chosen for
# the calendar's day-boundary cases below) are round numbers that would not
# have caught it.
run_case 'C6: idle-seat waste computed through a float64 round trip' \
	fail \
	./internal/connectors \
	$'TestSaasSeatsWasteIsCentsExactNotFloatRounded' \
	$'does not say 0.29 wasted' \
	internal/connectors/saasseats.go \
	$'s.WasteCents += money.Cents(int64(idle) * row.PerSeatCents)' \
	$'s.WasteCents += money.Cents(float64(idle) * float64(row.PerSeatCents) / 100.0 * 100.0)'

# "drop the notice deadline": the calendar's own reason for being read by
# the saas-portfolio-manager (roles.yaml: "the renewal calendar ninety days
# out") is the deadline, not the renewal date alone -- a renewal date with
# no notice deadline beside it is a date, not a decision with a deadline.
run_case 'C6: the notice deadline line is dropped from the renewal calendar' \
	fail \
	./internal/deliver \
	$'TestRenewalsSectionListsTheCalendarWithNoticeDeadlines' \
	$'notice deadline: 2026-08-19' \
	internal/deliver/packet.go \
	$'\t\tdeadline := l.NoticeDeadline()\n\t\tfmt.Fprintf(&b, "  notice deadline: %s%s\\n", deadline, noticeStatus(deadline, today))\n\t\tfmt.Fprintf(&b, "  issued/active:   %d/%d over %d days (idle %d)\\n",' \
	$'\t\tfmt.Fprintf(&b, "  issued/active:   %d/%d over %d days (idle %d)\\n",'

# "invent a benchmark figure when none exists": there is no benchmark
# connector anywhere in this practice today (the same honest gap
# roles.yaml's benchmarking-analyst family names for the estate's own
# KPIs), so a number here is never a measurement -- C6-SPEC.md section 2:
# "never a number without a source".
run_case 'C6: a benchmark figure is invented where none exists' \
	fail \
	./internal/deliver \
	$'TestRenewalsSectionSaysNoBenchmark' \
	$'want 3 (once per renewal' \
	internal/deliver/packet.go \
	$'\t\tb.WriteString("  benchmark:       no benchmark\\n")' \
	$'\t\tfmt.Fprintf(&b, "  benchmark:       %s (industry average)\\n", l.PerSeat)'
# C3-SPEC.md section 4's three named mutants. Invariant 32 (CLAUDE.md), "a
# registered driver moves the projection by its own measured effect ... and
# a frozen forecast remembers which drivers it already knew about".
#
# The first two both target ProjectWithDrivers's own single division line:
# ONE multiply-then-divide, done once per driver, is the whole of how a
# recurring driver's rate repeats across a window wider than what has
# landed AND how that repetition stays cents-exact. Each case mutates the
# SAME source line to a different fault and is judged against a DIFFERENT
# test, so the two cases never collide on a shared tree: run_case restores
# with git between every one.
# sofar*windowDays/windowDays is sofar, exactly, for any windowDays >= 1 (the
# only value daysBetween ever returns): a mutation that still COMPILES
# (windowDays stays referenced, so nothing is left unused) while dividing the
# window straight back out, which is what "applied once, un-extended across
# its own window" amounts to in this line.
run_case 'forecast: a recurring driver applies its effect once, un-extended' \
	fail \
	./internal/finops \
	$'TestProjectWithDriversRepeatsARecurringDriverAcrossItsWindow' \
	$'want 10.00' \
	internal/finops/forecast.go \
	$'effect = money.Cents(int64(sofar) * int64(windowDays) / int64(landed))' \
	$'effect = money.Cents(int64(sofar) * int64(windowDays) / int64(windowDays))'

run_case 'forecast: a driver rate rounds to the cent before its window multiplies it' \
	fail \
	./internal/finops \
	$'TestProjectWithDriversRoundsOnceMultiplyingBeforeDividing' \
	$'want 233' \
	internal/finops/forecast.go \
	$'effect = money.Cents(int64(sofar) * int64(windowDays) / int64(landed))' \
	$'effect = money.Cents((int64(sofar) / int64(landed)) * int64(windowDays))'

run_case 'forecast: the largest miss grades a live figure instead of the frozen one' \
	fail \
	./internal/finops \
	$'TestLargestMissGradesTheFrozenFigureNotALiveOne' \
	$'want the FROZEN 154.00' \
	internal/finops/forecast.go \
	$'\treturn Miss{Forecast: top, MissedDrivers: missed}, true, nil\n' \
	$'\tlive, _, _, _ := ProjectWithDrivers(db, top.Source, top.Period)\n\ttop.Forecast = live\n\treturn Miss{Forecast: top, MissedDrivers: missed}, true, nil\n'
# C4-SPEC.md section 4's three named mutants.

# (a) "compute coverage with floats": rounding both sides to the nearest
# whole dollar before dividing, via integer truncation rather than
# money.Pct's own direct cents ratio. TestCoverageIsCommittedOverEligiblePerDeskAndMonth
# would NOT catch this on its own -- 150000/200000 are both exact multiples
# of 100, so truncating to dollars first and the correct cents ratio agree
# by coincidence -- which is exactly why the dedicated fixture exists.
run_case 'commitments: coverage rounds through dollars first' \
	fail \
	./internal/finops \
	$'TestCoverageDoesNotRoundThroughDollarsFirst' \
	$'want close to 33.3' \
	internal/finops/commitments.go \
	$'r.Pct, r.OK = money.Pct(r.CommittedCents, r.EligibleCents)' \
	$'r.Pct = float64(int64(r.CommittedCents)/100) / float64(int64(r.EligibleCents)/100) * 100\n\t\tr.OK = r.EligibleCents != 0'

# (b) "count a Purchase row as usage": the ChargeCategory=Purchase routing
# check in processFocusFile is disabled, so a commitment's own price falls
# through to ai_calls and inflates the desk's derived Usage charges.
run_case 'commitments: a Purchase row counted as usage' \
	fail \
	./internal/connectors \
	$'TestPurchaseRowsAreNeverCountedAsUsage' \
	$'' \
	internal/connectors/tokenfusefocus.go \
	$'if focusField(rec, col, "ChargeCategory") == "Purchase" {' \
	$'if focusField(rec, col, "ChargeCategory") == "Purchase" && false {'

# (c) "put purchase into the apply table": applySideEffect grows a real case
# for the one class roles.yaml's own classes: list gives owner "nobody" --
# never a decision the console applies, only ever an option a person acts on
# outside it (crew.MayDecide refuses it before Owner is even read). The
# planted case reuses driver.one-time's own body, the cheapest real side
# effect this table already has an example of.
#
# Needle changed during Phase C integration: DRIVER-WINDOW-SPEC.md's own
# target guard in applyDriver (internal/finops/apply.go) now refuses a
# one-time driver with no target and no anomaly BEFORE it would ever reach
# the drivers-row write, so this mutation is caught one layer earlier than
# when this case was written -- by applyDriver's own guard, not by
# TestApplyingPurchaseHasNoSideEffect's row-count assertion. The test still
# fails (t.Fatal on the returned error) and the underlying property this
# case exists to prove -- purchase never writes a real side effect -- still
# holds, now doubly so. Verified by hand: applying this exact mutation prints
# "driver.one-time was applied with no target naming its window (and no
# anomaly to take a day from): recorded only, no drivers row written".
run_case 'commitments: purchase in the apply table' \
	fail \
	./internal/finops \
	$'TestApplyingPurchaseHasNoSideEffect' \
	$'no drivers row written' \
	internal/finops/apply.go \
	$'	case "driver.one-time": // class:driver.one-time' \
	$'	case "purchase": // planted by gates-have-teeth.sh, must be caught\n\t\treturn applyDriver(db, opt, t, "one-time")\n\tcase "driver.one-time": // class:driver.one-time'

# DRIVER-WINDOW-SPEC.md section 4's own first-named mutant: "write
# Start = End = day ignoring the target". applyDriver's whole fix was
# reading the option's own target instead of the wall clock; replacing the
# decoded target's own dates with a fixed day (any day but the target's own
# 2026-08-01/2026-08-30) is the same fault the original bug had, just
# without needing time.Now() (this file no longer imports "time" at all,
# and the mutation must still compile on its own -- "_ = tgt" keeps the
# decoded value referenced so dropping its own two fields does not also
# strand it as an unused variable, a second way this exact edit failed to
# compile the first time it was tried).
run_case 'driver-window: write Start = End = day ignoring the target' \
	fail \
	./internal/finops \
	$'TestApplyDriverRecurringWritesADriversRow' \
	$'want 2026-08-01 to 2026-08-30' \
	internal/finops/apply.go \
	$'\t\tstart, end = tgt.Start, tgt.End' \
	$'\t\tstart, end = "2026-09-03", "2026-09-03"; _ = tgt'

# PRICE-DISPLAY-SPEC.md, 2026-09-03: report a task's worst case without the
# loop multiplier -- the exact fault the incident behind invariant 35 was.
# report()'s own printed run total reverts to summing one call's own bound
# (e.WorstMicros) instead of reservedWorstCase(e), the same figure
# execute()'s reserve() call requires before it lets the first round
# through and never itself stops multiplying: a person reading this number
# to choose -ceiling would again be shown less than a live run will
# actually reserve.
run_case 'price display: report a task'"'"'s worst case without the loop multiplier' \
	fail \
	./tools/run \
	$'TestReportsWorstCaseIsWhatTheLiveRunWouldActuallyReserve' \
	$'does not equal what a live run would actually reserve' \
	tools/run/main.go \
	$'\t\t\tworstMicros += reservedWorstCase(e)' \
	$'\t\t\tworstMicros += e.WorstMicros'

# PARTNER-BUDGET-RECOMMENDATIONS-SPEC.md / CLAUDE.md invariant 46's own
# guardrail: a provider's suggested budget must never become this console's
# own budget figure. This is the mutant the spec names literally, "read
# budget_recommendations into CurrentBudgets's own result": CurrentBudgets'
# query is UNIONed with a select against budget_recommendations, so its
# returned map would silently gain a provider's own unverified suggestion
# alongside every finance-set budget. The structural guardrail test reads
# CurrentBudgets' own source and refuses any mention of
# budget_recommendations inside it, so it catches this before the mutated
# query is ever run.
run_case 'guardrail: read budget_recommendations into CurrentBudgets result' \
	fail \
	./internal/deliver \
	$'TestCurrentBudgetsAndSpendInMonthSourceNeverMentionsBudgetRecommendations' \
	$'must never flow into' \
	internal/estate/intake.go \
	$'rows, err := db.Query(`SELECT source, team, month, budget_cents FROM budgets`)' \
	$'rows, err := db.Query(`SELECT source, team, month, budget_cents FROM budgets UNION SELECT provider, team, month, recommended_cents FROM budget_recommendations`)'

# Found in review of PR #51: printing every figure through printf "%.1f"
# .Numeric is right for the three percentages and wrong for cost-per-outcome,
# a small MONEY figure -- a real 0.02 USD/outcome collapses to "0.0" through
# that verb, a real reading arriving through the VALUE branch and reading
# exactly like the refusal invariant 47 already guards the OTHER branch
# against. The fix reads .Value/.PrevValue, the KPI library's OWN string for
# each figure's own precision, rather than reformatting the float; this case
# plants exactly the regression, reverting both lines to the float verb, and
# requires the test built to prove a small real value survives -- the
# refusal test alone cannot catch this, because cost-per-outcome never
# reaches the HasVal=true branch on the estate that test seeds.
run_case 'leadership page: reformat a KPI value instead of printing its own string' \
	fail \
	./internal/web \
	$'TestTheLeadershipPageShowsASmallCostPerOutcomeWithoutLosingItsDigits' \
	$'rounded-away' \
	internal/web/templates/leadership.html \
	$'    <div class="v{{if not .HasVal}} refused{{end}}">{{if .HasVal}}{{.Value}}{{if .Unit}} {{.Unit}}{{end}}{{else}}{{.Blocked}}{{end}}</div>\n    <div class="s">{{if .PrevHasVal}}previous: {{.PrevValue}}{{if .Unit}} {{.Unit}}{{end}}{{else if .HasPeriod}}previous period refused: {{.PrevBlocked}}{{else}}no previous period{{end}}</div>' \
	$'    <div class="v{{if not .HasVal}} refused{{end}}">{{if .HasVal}}{{printf "%.1f" .Numeric}}{{if .Unit}} {{.Unit}}{{end}}{{else}}{{.Blocked}}{{end}}</div>\n    <div class="s">{{if .PrevHasVal}}previous: {{printf "%.1f" .PrevNumeric}}{{if .Unit}} {{.Unit}}{{end}}{{else if .HasPeriod}}previous period refused: {{.PrevBlocked}}{{else}}no previous period{{end}}</div>'

# A document this repository NAMES is one a reader can reach (invariant 48).
# The fault planted here is the one that actually happened, in miniature: a
# document moves or is renamed and the prose that points at it stays behind.
# `docs/stack-connection.md` is real and cited in CLAUDE.md's own first
# paragraph; a single letter turns that citation into a name nobody can open,
# and it is not a *-SPEC.md, so the one exemption does not cover it.
run_case 'documents: a named document that is neither in the tree nor a specification' \
	fail \
	./internal/manifest \
	$'TestEveryDocumentThisRepositoryNamesCanBeFound' \
	$'nobody can open' \
	CLAUDE.md \
	$'`docs/stack-connection.md` says what this console writes' \
	$'`docs/stack-connections.md` says what this console writes'

# A link into another repository is that repository's business: its own path
# ends in `.md` and satisfies the matcher's character class, which is exactly
# how the first real citation of that shape (2026-09-18) was refused as a
# dangling document. The gate must let a full URL through, both as a bare
# address and as the destination of a Markdown link whose text reads like a
# filename, while the dangling bare name beside a URL is still caught: the
# blanking stops at whitespace and never swallows the sentence.
run_case 'documents: a full URL to another repository, bare and as a link' \
	pass \
	./internal/manifest \
	$'TestEveryDocumentThisRepositoryNamesCanBeFound' \
	$'' \
	CLAUDE.md \
	$'`docs/stack-connection.md` says what this console writes' \
	$'`docs/stack-connection.md` (the run is in https://github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md and in [estate-gates/PROVEN.md](https://github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md)) says what this console writes'
run_case 'documents: a dangling bare name right after a URL is still caught' \
	fail \
	./internal/manifest \
	$'TestEveryDocumentThisRepositoryNamesCanBeFound' \
	$'nobody can open' \
	CLAUDE.md \
	$'`docs/stack-connection.md` says what this console writes' \
	$'https://github.com/TAIPANBOX/estate-gates/blob/main/PROVEN.md and `docs/stack-connections.md` says what this console writes'

# Invariant 49: -behind-tls has to reach every cookie's own Secure bit, not
# merely be read and then forgotten. This mutant is the regression the whole
# flag exists to prevent: dropping it from setCookie's own decision reverts
# Secure to r.TLS != nil alone, which is nil on every request this process
# sees behind the documented proxy-in-front deployment (-addr's own help
# text), so the session cookie would silently stop being Secure again.
run_case 'behind-tls: drop the flag from the cookie'"'"'s own Secure decision' \
	fail \
	./internal/web \
	$'TestLoginOverPlainHTTPIsSecureWhenBehindTLS' \
	$'want true' \
	internal/web/server.go \
	$'\tc.Secure = s.behindTLS || r.TLS != nil' \
	$'\tc.Secure = r.TLS != nil'

# Fable finding 3 on invariant 49: the OTHER half of the same line was
# untested. Dropping r.TLS != nil (leaving only s.behindTLS) is invisible to
# every test that talks plain HTTP, since r.TLS is nil there regardless of
# the flag; only a real TLS handshake exercises this half, and
# TestLoginOverRealTLSIsSecureWithoutTheFlag (httptest.NewTLSServer) is the
# one test in this suite that terminates one. @measured: the four tests that
# existed before this one all still PASS against this exact mutant.
run_case 'behind-tls: drop r.TLS from the cookie'"'"'s own Secure decision' \
	fail \
	./internal/web \
	$'TestLoginOverRealTLSIsSecureWithoutTheFlag' \
	$'want true' \
	internal/web/server.go \
	$'\tc.Secure = s.behindTLS || r.TLS != nil' \
	$'\tc.Secure = s.behindTLS'

# Fable finding 2 on invariant 49: TestEveryCookieGoesThroughOneSecurePosture
# used to count only the literal "http.SetCookie(", which a header written
# directly never contains. Planting the escape in cadencePage -- deliberately
# NOT signup, login or logout, the three routes TestEveryCookieCarriesSecureUnderTheFlag
# already enumerates -- proves the source walk catches a stray cookie
# anywhere in the package, not only on the three routes another test already
# watches.
run_case 'behind-tls: a stray Set-Cookie header outside setCookie' \
	fail \
	./internal/web \
	$'TestEveryCookieGoesThroughOneSecurePosture' \
	$'want 0' \
	internal/web/cadence.go \
	$'func (s *Server) cadencePage(w http.ResponseWriter, r *http.Request) {\n\tu := s.guard(w, r)\n\tif u == nil {\n\t\treturn\n\t}\n' \
	$'func (s *Server) cadencePage(w http.ResponseWriter, r *http.Request) {\n\tu := s.guard(w, r)\n\tif u == nil {\n\t\treturn\n\t}\n\tw.Header().Add("Set-Cookie", "evil=1")\n'

# Invariant 50: every wire type is emitted with a severity the envelope
# accepts. The two faults below are the two that existed at v0.2.0, planted
# back exactly as they were: the FOCUS reader journaling the generated
# estate's replacement with "" (costcrew#66, the import refused whole on the
# appliance), and option_refused going out as "warn". The gate is the
# call-site walk in internal/stack, which resolves each site's severity from
# the source and hands it to the real emitter.
run_case 'shared bus: generated_estate_replaced journaled with no severity' \
	fail \
	./internal/stack \
	$'TestEveryWireTypeIsEmittedWithASeverityTheEnvelopeAccepts' \
	$'generated_estate_replaced' \
	internal/connectors/tokenfusefocus.go \
	$'opt.Rec.Emit("generated_estate_replaced", opt.Actor, "info", map[string]any{' \
	$'opt.Rec.Emit("generated_estate_replaced", opt.Actor, "", map[string]any{'
run_case 'shared bus: option_refused journaled with a severity outside the enum' \
	fail \
	./internal/stack \
	$'TestEveryWireTypeIsEmittedWithASeverityTheEnvelopeAccepts' \
	$'option_refused' \
	internal/crew/options.go \
	$'rec.Emit("option_refused", roleName, "low", map[string]any{' \
	$'rec.Emit("option_refused", roleName, "warn", map[string]any{'
# And the non-fault: a move to another of the five is a judgement about how
# loud the event is, not a fault, so the walk must not fire on it. Without
# this the gate could be pinning one literal and pass the two cases above.
run_case 'shared bus: a move to another legal severity is not a fault' \
	pass \
	./internal/stack \
	$'TestEveryWireTypeIsEmittedWithASeverityTheEnvelopeAccepts' \
	$'' \
	internal/connectors/tokenfusefocus.go \
	$'opt.Rec.Emit("generated_estate_replaced", opt.Actor, "info", map[string]any{' \
	$'opt.Rec.Emit("generated_estate_replaced", opt.Actor, "low", map[string]any{'

# costcrew#67, invariant 51: the charge recorded is the gateway's settlement,
# never the runner's own estimate of the bill. The first fault is the incident
# itself (the header ignored, the runner's own price booked); the second reads
# the RUN's cumulative header as this call's own, which coincides with the
# right figure on a one-call run and overstates every multi-task run; the
# third accepts an absurd value; the fourth rounds a settlement to cents per
# call before the run is summed, the exact fault invariant 18 already holds
# against the runner's own figures.
run_case 'settlement: the header is ignored and the runner'"'"'s own price is booked' \
	fail \
	./tools/run \
	$'TestTheChargeRecordedIsTheGatewaysSettlement' \
	$'reported its own estimate as the charge' \
	internal/deliver/settlement.go \
	$'\tif s.Settled {\n\t\treturn s.SettledMicros\n\t}\n\treturn priced' \
	$'\tif false && s.Settled {\n\t\treturn s.SettledMicros\n\t}\n\treturn priced'
run_case 'settlement: the run'"'"'s cumulative header is read as this call'"'"'s own' \
	fail \
	./tools/run \
	$'TestFourTasksUnderOneRunIdAreNotOverCountedByTheCumulativeHeader' \
	$'settled its own call at 5310' \
	internal/deliver/settlement.go \
	$'if m, ok := parseFuseUSD(oneHeader(h, HeaderFuseCostUSD)); ok {' \
	$'if m, ok := parseFuseUSD(oneHeader(h, HeaderFuseSpentUSD)); ok {'
run_case 'settlement: an absurd value becomes a charge' \
	fail \
	./internal/deliver \
	$'TestHostileSettlementHeadersNeverPanicAndNeverBecomeACharge' \
	$'became a charge' \
	internal/deliver/settlement.go \
	$'if m < 0 || int64(m) > MaxSettlementMicros {' \
	$'if m < 0 {'
run_case 'settlement: rounded to cents per call before the run is summed' \
	fail \
	./tools/run \
	$'TestFourTasksUnderOneRunIdAreNotOverCountedByTheCumulativeHeader' \
	$'want 3' \
	internal/deliver/settlement.go \
	$'\tif s.Settled {\n\t\treturn s.SettledMicros\n\t}' \
	$'\tif s.Settled {\n\t\treturn int64(money.Micros(s.SettledMicros).Cents()) * 10_000\n\t}'
# And the other half of the same issue, invariant 44: the tool catalogue the
# loop sends on every round but the last is dropped from the bound again.
run_case 'price display: the tool catalogue is dropped from the worst case' \
	fail \
	./tools/run \
	$'TestTheWorstCaseCoversTheToolCatalogueTheLoopActuallySends' \
	$'does not cover the' \
	tools/run/main.go \
	$'e.WorstMicros = deliver.WorstCaseMicros(e.PromptTokens+e.CatalogueTokens, maxTok, p)' \
	$'e.WorstMicros = deliver.WorstCaseMicros(e.PromptTokens, maxTok, p)'
# And the non-fault: widening the byte cap on a header value is a judgement
# about the wire, not a fault; the hostile-input gate must hold the PROPERTY
# (a megabyte is refused, a sign is refused) and not pin the literal 32.
run_case 'settlement: a wider byte cap on the header value is not a fault' \
	pass \
	./internal/deliver \
	$'TestHostileSettlementHeadersNeverPanicAndNeverBecomeACharge' \
	$'' \
	internal/deliver/settlement.go \
	$'const maxFuseUSDBytes = 32' \
	$'const maxFuseUSDBytes = 64'

# Invariant 53: the supervisor selects among the analysts' own options.
run_case 'supervisor: the analyst link'"'"'s classes are carried again (MayDecide)' \
	fail \
	./internal/finops \
	$'TestTheSupervisorSelectsAnAnalystClassOptionWithinTAnomaly' \
	$'want exactly one recommendation.rightsizing' \
	internal/finops/supervise.go \
	$'may, _ := crew.SupervisorMaySelect(top.Class)' \
	$'may, _ := crew.MayDecide("supervisor", top.Class)'
run_case 'supervisor: option.select removed from the job description' \
	fail \
	./internal/finops \
	$'TestTheSupervisorSelectsAnAnalystClassOptionWithinTAnomaly' \
	$'want exactly one recommendation.rightsizing' \
	internal/crew/roles.yaml \
	$'"task.accept", "option.select", ' \
	$'"task.accept", '
run_case 'supervisor: an analyst class past T.anomaly is applied' \
	fail \
	./internal/finops \
	$'TestAnAnalystClassOptionOverTAnomalyIsCarried' \
	$'is over T.anomaly' \
	internal/finops/supervise.go \
	$'if may && withinThreshold && !contradicted && !alreadySettled {' \
	$'if may && (withinThreshold || analystOwned(top.Class)) && !contradicted && !alreadySettled {'
run_case 'supervisor: a contradiction is settled by the ranking' \
	fail \
	./internal/finops \
	$'TestAContradictedAnalystOptionIsStillCarriedToTheOwner' \
	$'is the owner'"'"'s one question' \
	internal/finops/supervise.go \
	$'if may && withinThreshold && !contradicted && !alreadySettled {' \
	$'if may && withinThreshold && (!contradicted || analystOwned(top.Class)) && !alreadySettled {'
run_case 'supervisor: a second option on a settled anomaly is applied on top' \
	fail \
	./internal/finops \
	$'TestASecondOptionOnAnAnomalyTheSupervisorAlreadyDecidedIsCarried' \
	$'the pass aborted on the second option' \
	internal/finops/supervise.go \
	$'if may && withinThreshold && !contradicted && !alreadySettled {' \
	$'if may && withinThreshold && !contradicted && (!alreadySettled || anomalyID != "") {'
run_case 'supervisor: an owner or nobody class is selected as if it were an analyst'"'"'s' \
	fail \
	./internal/finops \
	$'TestOwnerAndNobodyClassOptionsStayCarriedWithinTAnomaly' \
	$'want 0 and 1 for' \
	internal/crew/roles.go \
	$'	if c.Owner != "analyst" {
		return MayDecide("supervisor", class)
	}' \
	$'	if c.Owner != "analyst" {
		return true, ""
	}'
run_case 'supervisor: option.select is assumed, not read from the job description' \
	fail \
	./internal/crew \
	$'TestSupervisorMaySelectReadsOptionSelectFromTheJobDescription' \
	$'with option.select removed, want false' \
	internal/crew/roles.go \
	$'		if id == "option.select" {' \
	$'		if id != "" {'
# And the non-fault: T.anomaly written as a strict bound one cent higher is the
# same boundary, and the gate holds the boundary, not the operator.
run_case 'supervisor: the T.anomaly boundary spelled another way is not a fault' \
	pass \
	./internal/finops \
	$'TestAnAnalystClassOptionExactlyAtTAnomalyIsApplied' \
	$'' \
	internal/finops/supervise.go \
	$'withinThreshold := top.FigureCents <= tAnomaly.ValueCents' \
	$'withinThreshold := top.FigureCents < tAnomaly.ValueCents+1'
# Invariant 57: every analyst family has its class lists, and the never list
# carries the blocked-task clause, bound to the test that holds it. Each
# property of the shell gate gets one case that switches that property off and
# requires the Go test that plants its fault to go red; two cases undo the data
# (a list emptied, an exemption renamed away); one never-binding case undoes the
# binding; and one non-fault, a reasoned exemption in other words.
run_case $'job descriptions: a family decides_alone goes back to empty' \
	fail \
	./internal/crew \
	$'TestTheDecidesAloneListsAreWrittenFromTheProse' \
	$'finops-partner decides_alone = []' \
	internal/crew/roles.yaml \
	$'    decides_alone: ["commentary.variance", "commentary.showback"]\n    decides_alone_text: "the brief\'s text."' \
	$'    decides_alone: []\n    decides_alone_text: "the brief\'s text."'
run_case $'job descriptions: an exemption is renamed so the gate cannot read it' \
	fail \
	./internal/crew \
	$'TestRolesAreBound' \
	$'EMPTY LIST          sustainability-analyst has an empty decides_alone' \
	internal/crew/roles.yaml \
	$'decides_alone_exempt: "it is restricted' \
	$'decides_alone_exemptx: "it is restricted'
run_case $'job descriptions: the shell gate stops refusing an empty list' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesAnEmptyDecidesAlone' \
	$'but not saying "EMPTY LIST' \
	scripts/roles-are-bound.sh \
	$'		if [ -z "$list" ] && [ -z "$exempt" ]; then' \
	$'		if false; then'
run_case $'job descriptions: the shell gate accepts an exemption of any length' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesAnExemptionTooThinToBeAReason' \
	$'the gate passed, want it to refuse' \
	scripts/roles-are-bound.sh \
	$'elif [ -z "$list" ] && [ "${#exempt}" -lt "$min_reason" ]; then' \
	$'elif false; then'
run_case $'job descriptions: the shell gate keeps an exemption beside a written list' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesAnExemptionBesideAListThatIsNotEmpty' \
	$'the gate passed, want it to refuse' \
	scripts/roles-are-bound.sh \
	$'elif [ -n "$list" ] && [ -n "$exempt" ]; then' \
	$'elif false; then'
run_case $'job descriptions: the shell gate lets an analyst decide a class it does not own' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesAnAnalystDecidingAClassItDoesNotOwn' \
	$'the gate passed, want it to refuse' \
	scripts/roles-are-bound.sh \
	$'		if [ "$own" != "analyst" ]; then' \
	$'		if false; then'
run_case $'never list: a binding to a test that does not exist is accepted' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesANeverBindingWhoseTestIsGone' \
	$'the gate passed, want it to refuse' \
	scripts/roles-are-bound.sh \
	$'	if [ -z "$tname" ] || ! grep -rqE "func ${tname}\\(" internal/ tools/ 2>/dev/null; then' \
	$'	if false; then'
run_case $'never list: a binding for a clause that left the list is accepted' \
	fail \
	./internal/crew \
	$'TestRolesAreBoundRefusesANeverBindingWhoseClauseWasTakenOut' \
	$'the gate passed, want it to refuse' \
	scripts/roles-are-bound.sh \
	$'	if ! grep -qxF "$verb" <<<"$never_list"; then' \
	$'	if false; then'
run_case $'never list: the blocked-task clause leaves the list' \
	fail \
	./internal/crew \
	$'TestTheNeverListCarriesTheBlockedClause' \
	$'never_bound names' \
	internal/crew/roles.yaml \
	$'  - "act on a task somebody blocked"\n' \
	$''
run_case $'never list: the blocked-task clause loses its binding' \
	fail \
	./internal/crew \
	$'TestTheBlockedClauseIsBoundToTheRunnersTest' \
	$'is bound to ""' \
	internal/crew/roles.yaml \
	$'never_bound:\n  - verb: "act on a task somebody blocked"\n    test: "TestABlockedTaskIsNotWorkedAround"\n' \
	$'never_bound: []\n'
run_case $'job descriptions: an exemption reworded is not a fault' \
	pass \
	./internal/crew \
	$'^TestRolesAreBound$' \
	$'' \
	internal/crew/roles.yaml \
	$'hands_up_exempt: "it is onboarding and produces nothing until a public dataset is named, so there is nothing to hand up; this exemption ends the day it is activated."' \
	$'hands_up_exempt: "this family hands nothing up today, for a reason written out in full so that a later reader can judge it."'

# Invariant 54: a call goes through the gateway that fronts its engine's wire,
# or is refused; it never goes direct while a gateway is configured.
run_case 'gateway route: an openrouter call ignores the gateway again' \
	fail \
	./internal/deliver \
	$'TestAGatewayIsNeverSilentlyIgnoredForAnOpenRouterCall' \
	$'the gateway was ignored and the spend left it' \
	internal/deliver/call.go \
	$'	if !g.On() {\n		return "", nil\n	}' \
	$'	if !g.On() || engine == "openrouter" {\n		return "", nil\n	}'
run_case 'gateway route: bedrock is let through to AWS with a gateway on' \
	fail \
	./internal/deliver \
	$'TestABedrockCallWithAGatewayConfiguredIsRefused' \
	$'want one wrapping ErrNoGatewayRoute' \
	internal/deliver/call.go \
	$'	case "bedrock":\n		return "", fmt.Errorf("%w: engine %q speaks neither wire' \
	$'	case "bedrock-disabled":\n		return "", fmt.Errorf("%w: engine %q speaks neither wire'
run_case 'gateway route: Call stops asking whether the engine has a route' \
	fail \
	./internal/deliver \
	$'TestACallWithNoGatewayRouteForItsEngineIsRefusedBeforeAnyRequest' \
	$'want one wrapping ErrNoGatewayRoute' \
	internal/deliver/call.go \
	$'	if _, err := gw.RouteFor(engine); err != nil {' \
	$'	if _, err := gw.RouteFor(engine); err != nil && false {'
run_case 'gateway route: the openrouter URL is always the direct host' \
	fail \
	./internal/deliver \
	$'TestAnOpenRouterRequestThroughTheGatewayCarriesTheSameFuseHeaders' \
	$'want the OpenAI-shaped gateway' \
	internal/deliver/call.go \
	$'	if base == "" {' \
	$'	if true {'
run_case 'gateway route: the x-fuse headers are left off the OpenAI request' \
	fail \
	./internal/deliver \
	$'TestAnOpenRouterRequestThroughTheGatewayCarriesTheSameFuseHeaders' \
	$'on the OpenAI request and' \
	internal/deliver/call.go \
	$'	if routed {\n		SetFuseHeaders(req, gw)' \
	$'	if routed && false {\n		SetFuseHeaders(req, gw)'
run_case 'gateway route: the OpenAI door'"'"'s settlement is not read' \
	fail \
	./internal/deliver \
	$'TestCallOpenRouterCarriesTheGatewaysSettlementOnItsResult' \
	$'want true/58110' \
	internal/deliver/call.go \
	$'	if gw.OpenAIURL != "" {\n		st = ParseSettlement(resp.Header)' \
	$'	if false {\n		st = ParseSettlement(resp.Header)'
run_case 'gateway route: a header on the direct openrouter route becomes a charge' \
	fail \
	./internal/deliver \
	$'TestASettlementHeaderOnTheDirectOpenRouterRouteIsNeverACharge' \
	$'a settlement was read off a direct call' \
	internal/deliver/call.go \
	$'	if gw.OpenAIURL != "" {\n		st = ParseSettlement(resp.Header)' \
	$'	if true {\n		st = ParseSettlement(resp.Header)'
run_case 'gateway route: a 402 from the OpenAI gateway is an ordinary failure' \
	fail \
	./internal/deliver \
	$'TestA402FromTheOpenAIGatewayIsAGatewayRefusal' \
	$'want a GatewayRefusal' \
	internal/deliver/call.go \
	$'	if resp.StatusCode == http.StatusPaymentRequired && gw.OpenAIURL != "" {' \
	$'	if resp.StatusCode == http.StatusPaymentRequired && gw.OpenAIURL != "" && false {'
run_case 'gateway route: the run preflight lets an unfronted engine through' \
	fail \
	./tools/run \
	$'TestAnEngineNoConfiguredGatewayFrontsRefusesTheRunBeforeTheFirstCall' \
	$'was let through' \
	tools/run/live.go \
	$'	if !gw.on() {\n		return nil\n	}\n	probe' \
	$'	if true {\n		return nil\n	}\n	probe'
run_case 'gateway route: the tool loop stops asking whether the engine has a route' \
	fail \
	./tools/run \
	$'TestAnAnthropicTaskWithOnlyAnOpenAIGatewayIsRefusedAndNeverGoesDirect' \
	$'reached [api.anthropic.com]' \
	tools/run/loop.go \
	$'	if _, err := gw.RouteFor(e.Engine); err != nil {' \
	$'	if _, err := gw.RouteFor(e.Engine); err != nil && false {'
run_case 'gateway route: the openrouter round ignores the gateway (the unfixed loop)' \
	fail \
	./tools/run \
	$'TestAnOpenRouterTaskThroughTheOpenAIGatewayIsChargedItsSettlementPerRound' \
	$'reached the direct endpoint' \
	internal/deliver/call.go \
	$'	if base == "" {' \
	$'	if true {'
run_case 'gateway route: the supervisor'"'"'s ask is not given the OpenAI gateway' \
	fail \
	./internal/web \
	$'TestAskPlanForAnOpenRouterSupervisorGoesThroughTheOpenAIGateway' \
	$'saw 0 request' \
	internal/web/planning.go \
	$'URL: s.gateway, OpenAIURL: s.gatewayOpenAI, RunID: runID,' \
	$'URL: s.gateway, RunID: runID,'
run_case 'gateway route: the bench stops checking the route before the store opens' \
	fail \
	./tools/bench \
	$'TestLiveOpenRouterWithOnlyAnAnthropicGatewayRefusesBeforeTheStoreOpens' \
	$'the store was opened before the route was checked' \
	tools/bench/main.go \
	$'(deliver.Gateway{URL: gatewayURL, OpenAIURL: gatewayOpenAIURL}).RouteFor(*engine); err != nil {' \
	$'(deliver.Gateway{URL: gatewayURL, OpenAIURL: gatewayOpenAIURL}).RouteFor(*engine); err != nil && false {'
# And the non-fault: rewording the sentence of a refusal is not a fault; the
# gates hold the type and the engine's name, not the prose.
run_case 'gateway route: rewording a refusal is not a fault' \
	pass \
	./internal/deliver \
	$'TestACallWithNoGatewayRouteForItsEngineIsRefusedBeforeAnyRequest' \
	$'' \
	internal/deliver/call.go \
	$'speaks the Anthropic wire and only an OpenAI-shaped ' \
	$'speaks the Anthropic wire, and only an OpenAI-shaped '

# Invariant 55 (costcrew#82): a task the gateway stopped keeps what it settled.
run_case 'stopped task: an unanswered round erases the settled ones before it' \
	fail \
	./tools/run \
	$'TestAStoppedTaskRecordsWhatTheGatewaySettledForItsRounds' \
	$'want 200000' \
	tools/run/loop.go \
	$'	if err != nil && !rr.answered() {' \
	$'	if false && err != nil && !rr.answered() {'
run_case 'stopped task: the error path books nothing' \
	fail \
	./tools/run \
	$'TestAStoppedTaskRecordsWhatTheGatewaySettledForItsRounds' \
	$'want 200000' \
	tools/run/live.go \
	$'		if charge > 0 {' \
	$'		if charge > 0 && false {'
run_case 'stopped task: every unsettled round is skipped, not only an unanswered one' \
	fail \
	./tools/run \
	$'TestARoundThatAnsweredWithoutAHeaderStillMakesTheTaskPricedByTheRunner' \
	$'a third kind of number' \
	tools/run/loop.go \
	$'	if err != nil && !rr.answered() {' \
	$'	if !rr.Settlement.Settled {'
run_case 'stopped task: the headline hides a gateway total above the booked one' \
	fail \
	./tools/run \
	$'TestTheHeadlineSaysWhenTheGatewaysTotalIsHigherThanWhatWasBooked' \
	$'does not name the gap' \
	tools/run/live.go \
	$'		if gwSpent > run.total() {' \
	$'		if gwSpent > run.total() && false {'

# Invariant 58: an admin answering a decision addressed to somebody else gives a
# reason, and the option, the card, the journal and the bus say the answer was
# given on that owner's behalf. Each mutant undoes one piece of that and is
# caught by the test that holds it; one non-fault rewords a refusal.
run_case $'admin answers: the reason check is skipped' \
	fail \
	./internal/web \
	$'TestAnAdminAnsweringForAnotherOwnerMustGiveAReason' \
	$'an admin applied another owner' \
	internal/web/decisions.go \
	$'		if answersForOther(u, owner) {' \
	$'		if false {'
run_case $'admin answers: every answerer is treated as answering for somebody else' \
	fail \
	./internal/web \
	$'TestAnOwnersOwnAnswerCarriesNoOnBehalfOf' \
	$'an owner' \
	internal/web/decisions.go \
	$'	return mayAnswerFor(u, owner) && u.Username != owner' \
	$'	return mayAnswerFor(u, owner)'
run_case $'admin answers: the owner is treated as having answered for themselves when an admin did' \
	fail \
	./internal/web \
	$'TestAnAdminAnswerWithAReasonIsMarkedOnBehalfOfTheOwner' \
	$'option marks' \
	internal/web/decisions.go \
	$'			ans = crew.AdminAnswerFor(u.Username, owner, reason)' \
	$'			ans = crew.OwnerAnswer(u.Username + reason[:0])'
run_case $'admin answers: the answer type accepts any reason' \
	fail \
	./internal/finops \
	$'TestApplyAsRefusesAnAnswerOnBehalfWithNoReasonBeforeAnySideEffect' \
	$'was applied' \
	internal/crew/answer.go \
	$'	_, err := ValidBehalfReason(a.Reason)\n	return err' \
	$'	return nil'
run_case $'admin answers: the reason has no length cap' \
	fail \
	./internal/crew \
	$'TestBehalfReasonIsRequiredCappedAndPlain' \
	$'accepted as' \
	internal/crew/answer.go \
	$'	if len(r) > BehalfReasonMaxBytes {' \
	$'	if false {'
run_case $'admin answers: the reason is dropped from the event' \
	fail \
	./internal/web \
	$'TestAnAdminAnswerWithAReasonIsMarkedOnBehalfOfTheOwner' \
	$'the journal entry reads' \
	internal/crew/answer.go \
	$'		data["on_behalf_reason"] = trimmedReason(ans)\n' \
	$''
run_case $'admin answers: a refusal a person made is not journaled' \
	fail \
	./internal/web \
	$'TestAnOwnersOwnRefusalIsJournaledAsTheOwnersAndCarriesNoOnBehalfOf' \
	$'no option_refused entry' \
	internal/crew/answer.go \
	$'	if rec != nil {\n		data := map[string]any{\n			"artifact": artifactID,' \
	$'	if false && rec != nil {\n		data := map[string]any{\n			"artifact": artifactID,'
run_case $'admin answers: the card stops naming who answered for whom' \
	fail \
	./internal/web \
	$'TestTheDecisionCardNamesWhoAnsweredAndForWhom' \
	$'the decision card does not say' \
	internal/web/templates/decision.html \
	$'{{.DecidedBy}} answered on behalf of owner {{.OnBehalfOf}}' \
	$'{{.DecidedBy}} answered'
run_case $'admin answers: the refusal event goes out with a severity outside the enum' \
	fail \
	./internal/stack \
	$'TestEveryWireTypeIsEmittedWithASeverityTheEnvelopeAccepts' \
	$'option_refused' \
	internal/crew/answer.go \
	$'rec.Emit("option_refused", ans.Actor, "low", data, nil)' \
	$'rec.Emit("option_refused", ans.Actor, "warn", data, nil)'
run_case $'admin answers: a reworded refusal message is not a fault' \
	pass \
	./internal/web \
	$'TestAnAdminAnsweringForAnotherOwnerMustGiveAReason' \
	$'' \
	internal/web/decisions.go \
	$'"you are answering for "+owner+", so a reason is needed: "+rerr.Error()' \
	$'"a reason is needed because you are answering for "+owner+": "+rerr.Error()'
run_case $'unit showback: the showback drops every unit a rule covers' \
	fail \
	./internal/finops \
	$'TestAfterTheStampsTheShowbackHasOneRowPerUnitAndBalancesToTheCent' \
	$'showback has 0 rows' \
	internal/finops/unitrules.go \
	$'	for _, u := range UnitsOf(a, rules) {' \
	$'	for _, u := range UnitsOf(a, rules)[:0] {'
run_case $'unit showback: the spend of a unit with no rule vanishes from the file' \
	fail \
	./internal/finops \
	$'TestBeforeAnyRuleTheUnitsAreOneVisibleUnruledRowAndTheFileBalances' \
	$'with no rule the showback is' \
	internal/finops/unitrules.go \
	$'	if haveUnruled {' \
	$'	if haveUnruled && false {'
run_case $'unit showback: an unruled unit\'s name is printed in the unruled row' \
	fail \
	./internal/web \
	$'TestAHostileUnitNameNeverReachesTheShowbackFileOrTheMarkup' \
	$'reached the showback file' \
	internal/finops/unitrules.go \
	$'		haveUnruled = true\n' \
	$'		haveUnruled = true\n		unruled.BusinessUnit += u.Unit + ";"\n'
run_case $'unit showback: the export is emptied at the route' \
	fail \
	./internal/web \
	$'TestTheShowbackCarriesOneRowPerUnitOnlyAfterTheOwnersStamp' \
	$'before any stamp the showback is' \
	internal/web/money.go \
	$'	for _, r := range sb {' \
	$'	for _, r := range sb[:0] {'
run_case $'unit showback: the chargeback page stops listing the units' \
	fail \
	./internal/web \
	$'TestAnUnstampedUnitIsNamedAsUnruledOnTheChargebackPage' \
	$'does not say "Customer units"' \
	internal/web/templates/chargeback.html \
	$'{{- if .Units}}' \
	$'{{- if false}}'
run_case $'unit showback: the close pack stops naming the units' \
	fail \
	./internal/deliver \
	$'TestClosePackSectionNamesAUnitWithNoRuleAndTheShapeOfAProposal' \
	$'the close pack does not carry' \
	internal/deliver/packet.go \
	$'uerr == nil && len(units) > 0 {' \
	$'uerr == nil && len(units) > 99999 {'
run_case $'unit rule: applied for a unit that has no rows' \
	fail \
	./internal/finops \
	$'TestAUnitRuleForAUnitWithNoRowsIsRefusedAndTheOptionStaysOpen' \
	$'a rule for a unit that has no rows was applied' \
	internal/finops/unitrules.go \
	$'	if reason != "" {\n		return fmt.Errorf("allocation.rule for unit %q refused' \
	$'	if false {\n		return fmt.Errorf("allocation.rule for unit %q refused'
run_case $'unit rule: rows a reader did not write count as a unit' \
	fail \
	./internal/finops \
	$'TestAUnitRuleOnRowsTheTokenFuseReaderDidNotWriteIsRefused' \
	$'whose rows no reader wrote' \
	internal/crew/unitrule.go \
	$'WHERE team=? AND provenance=?' \
	$'WHERE team=? AND (provenance=? OR provenance IS NULL)'
run_case $'unit rule: a roster team\'s name is accepted as a unit' \
	fail \
	./internal/finops \
	$'TestAUnitRuleForARosterTeamIsRefused' \
	$'a unit rule was applied to the roster team' \
	internal/crew/unitrule.go \
	$'		if tm.Name == unit {' \
	$'		if false && tm.Name == unit {'
run_case $'unit rule: a proposal for a unit with no rows reaches the owner' \
	fail \
	./internal/finops \
	$'TestAProposalForAUnitWithNoRowsIsRefusedWhenItIsWritten' \
	$'a proposal for a unit with no rows was accepted' \
	internal/crew/options.go \
	$'if tgt, isUnit, _ := ParseUnitTarget(o.Target); isUnit {' \
	$'if tgt, isUnit, _ := ParseUnitTarget(o.Target); isUnit && false {'
run_case $'unit rule: a name that opens as a spreadsheet formula is accepted' \
	fail \
	./internal/crew \
	$'TestUnitRuleTargetHostileInputs' \
	$'spreadsheet_formula' \
	internal/crew/unitrule.go \
	$'strings.ContainsRune("=+-@", rune(s[0]))' \
	$'strings.ContainsRune("", rune(s[0]))'
run_case $'unit rule: the supervisor applies an allocation.rule on its own' \
	fail \
	./internal/finops \
	$'TestTheSupervisorNeverAppliesAUnitRule' \
	$'the supervisor applied' \
	internal/crew/roles.go \
	$'	if c.Owner != "analyst" {\n		return MayDecide("supervisor", class)' \
	$'	if c.Owner != "analyst" {\n		return true, ""'
run_case $'unit rule: any operator may stamp another owner\'s unit rule' \
	fail \
	./internal/web \
	$'TestOnlyTheOwnerOrAnAdminCanStampAUnitRule' \
	$'stamp wrote' \
	internal/web/decisions.go \
	$'	return u.May("operator") && u.Username == owner' \
	$'	return u.May("operator")'
run_case $'money found: the crew-cost KPI sums the absolute excess again' \
	fail \
	./internal/finops \
	$'TestTheCrewCostKPIDoesNotCountADropAsMoneyFound' \
	$'does not state the signed figure' \
	internal/finops/kpi.go \
	$'	found, err := FoundMonthly(db)\n' \
	$'	var found money.Cents\n	err = db.QueryRow(`SELECT COALESCE(SUM(ABS(excess_cents)),0) FROM anomalies WHERE state IN (\'explained\',\'accepted\')`).Scan(&found)\n'
run_case $'unit rule: a reworded refusal message is not a fault' \
	pass \
	./internal/finops \
	$'TestAUnitRuleForAUnitWithNoRowsIsRefusedAndTheOptionStaysOpen' \
	$'' \
	internal/finops/unitrules.go \
	$'"allocation.rule for unit %q refused: %s"' \
	$'"the unit rule for %q was refused: %s"'
run_case $'unit showback: a reworded sentence on the units panel is not a fault' \
	pass \
	./internal/web \
	$'TestAnUnstampedUnitIsNamedAsUnruledOnTheChargebackPage' \
	$'' \
	internal/web/templates/chargeback.html \
	$'charged as it reports them.' \
	$'shown the way it labelled them.'

# Invariants 61 to 64: sessions stored as hashes, one answer for every failed
# sign-in, private data files, and the console's egress. Every case plants one
# fault in the product, in the way a silent failure would look: the session
# still works, the login still refuses, the file is still there, the page still
# renders, and only the property under the gate is gone.
#
# 61. The first mutant keeps the whole login working and stores the cookie
# itself, which is the only shape this fault has: nothing visible changes.
run_case $'sessions: the cookie is stored as it is, not as its hash' \
	fail \
	./internal/auth \
	$'TestTheDatabaseHoldsNoSessionTokenInTheClear' \
	$'stores the cookie value itself' \
	internal/auth/auth.go \
	$'	return hex.EncodeToString(sum[:])\n}\n\nfunc (a *Auth) StartSession' \
	$'	_ = sum\n	return token\n}\n\nfunc (a *Auth) StartSession'
run_case $'sessions: a hash read from the database is accepted as a cookie' \
	fail \
	./internal/auth \
	$'TestAHashReadFromTheDatabaseIsNotACookie' \
	$'as a cookie signed in as alice' \
	internal/auth/auth.go \
	$'WHERE s.token_hash=? AND s.expires > ?`, sessionKey(token), now))' \
	$'WHERE (s.token_hash=? OR s.token_hash=?) AND s.expires > ?`, sessionKey(token), token, now))'
run_case $'sessions: the migration renames the old table instead of erasing it' \
	fail \
	./internal/auth \
	$'TestOldClearTextSessionsAreEndedAndErasedByTheMigration' \
	$'still readable in app.db' \
	internal/store/store.go \
	$'conn.ExecContext(ctx, `DROP TABLE sessions`)' \
	$'conn.ExecContext(ctx, `ALTER TABLE sessions RENAME TO sessions_old`)'
# Two edits, not one: the second check inside the write lock exists for two
# processes starting together on an old database, and with only the first
# removed it makes the sequential fault equivalent (measured: TOOTHLESS).
run_case $'sessions: the migration runs on every start and signs everybody out' \
	fail \
	./internal/auth \
	$'TestTheMigrationRunsOnceAndKeepsTheSessionsItDidNotWrite' \
	$'no longer resolves' \
	internal/store/store.go \
	$'	if current {\n		return nil\n	}' \
	$'	if false && current {\n		return nil\n	}' \
	internal/store/store.go \
	$'	if current || !exists {' \
	$'	if !exists {'
run_case $'sessions: the CSRF token is keyed on the storage hash, not the cookie' \
	fail \
	./internal/auth \
	$'TestCSRFStaysBoundToTheCookieValue' \
	$'CSRFToken =' \
	internal/auth/auth.go \
	$'	m.Write([]byte(session))' \
	$'	m.Write([]byte(sessionKey(session)))'
run_case $'sessions: a reworded journal reason for the reset is not a fault' \
	pass \
	./internal/auth \
	$'TestOldClearTextSessionsAreEndedAndErasedByTheMigration' \
	$'' \
	internal/store/store.go \
	$'"session tokens are now stored as hashes; everybody signs in again once"' \
	$'"sessions ended because tokens are now kept as hashes"'

# 62. A locked account that tells its state is the fault; so is the fast one,
# the one that skips the hash, and the one that has its own words.
run_case $'sign-in: a locked account says it is locked' \
	fail \
	./internal/auth \
	$'TestEveryFailedSignInSaysTheSame' \
	$'a locked account says' \
	internal/auth/auth.go \
	$'	if u.LockedUntil > now {\n		burn(password)\n		return nil, LoginRefused, nil\n	}' \
	$'	if u.LockedUntil > now {\n		burn(password)\n		return nil, fmt.Sprintf("locked for another %ds after repeated failures", int(u.LockedUntil-now)), nil\n	}'
run_case $'sign-in: a locked account says it is locked, seen through the form' \
	fail \
	./internal/web \
	$'TestEveryFailedSignInLooksTheSameFromOutside' \
	$'a locked account answers' \
	internal/auth/auth.go \
	$'	if u.LockedUntil > now {\n		burn(password)\n		return nil, LoginRefused, nil\n	}' \
	$'	if u.LockedUntil > now {\n		burn(password)\n		return nil, fmt.Sprintf("locked for another %ds after repeated failures", int(u.LockedUntil-now)), nil\n	}'
run_case $'sign-in: a locked account skips the password hash' \
	fail \
	./internal/auth \
	$'TestALockedAccountCostsAsMuchAsAnUnknownOne' \
	$'skips the password hash' \
	internal/auth/auth.go \
	$'	if u.LockedUntil > now {\n		burn(password)\n' \
	$'	if u.LockedUntil > now {\n'
run_case $'sign-in: an unknown account gets words of its own' \
	fail \
	./internal/auth \
	$'TestEveryFailedSignInSaysTheSame' \
	$'wrong password says' \
	internal/auth/auth.go \
	$'	if u == nil {\n		burn(password)\n		return nil, LoginRefused, nil\n	}' \
	$'	if u == nil {\n		burn(password)\n		return nil, "no such account", nil\n	}'
run_case $'sign-in: the lockout itself is never reached' \
	fail \
	./internal/auth \
	$'TestEveryFailedSignInSaysTheSame' \
	$'the lockout itself is gone' \
	internal/auth/auth.go \
	$'if failed >= 3 {' \
	$'if failed >= 3000 {'
run_case $'sign-in: a reworded refusal is not a fault' \
	pass \
	./internal/auth \
	$'TestEveryFailedSignInSaysTheSame' \
	$'' \
	internal/auth/auth.go \
	$'"could not sign in: the account name or the password is not right, "' \
	$'"sign-in refused: check the account name and the password, "'

# 63. Modes, measured on the files the store really creates.
run_case $'files: the data directory is created 0755' \
	fail \
	./internal/store \
	$'TestTheDataDirectoryIsPrivateWhenTheStoreCreatesIt' \
	$'want 0700' \
	internal/store/store.go \
	$'os.MkdirAll(dir, dirMode); err != nil {' \
	$'os.MkdirAll(dir, 0o755); err != nil {'
run_case $'files: the journal is created 0644' \
	fail \
	./internal/store \
	$'TestTheJournalIsCreatedPrivateByTheFirstAppend' \
	$'want 0600' \
	internal/store/store.go \
	$'os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)' \
	$'os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)'
run_case $'files: Open stops tightening files that already exist' \
	fail \
	./internal/store \
	$'TestFilesFromBeforeTheChangeAreTightenedOnOpen' \
	$'still has mode' \
	internal/store/store.go \
	$'	s.tighten(os.Chmod)\n	return s, nil' \
	$'	return s, nil'
run_case $'files: Open chmods a data directory that was already there' \
	fail \
	./internal/store \
	$'TestAnExistingDataDirectoryKeepsItsMode' \
	$'was changed to' \
	internal/store/store.go \
	$'os.MkdirAll(dir, dirMode); err != nil {' \
	$'os.MkdirAll(dir, dirMode); err != nil || os.Chmod(dir, dirMode) != nil {'
run_case $'files: a chmod the filesystem refuses is swallowed' \
	fail \
	./internal/store \
	$'TestAChmodTheFilesystemRefusesIsAWarningNotSilenceNotAnOutage' \
	$'warnings =' \
	internal/store/store.go \
	$'err != nil && !os.IsNotExist(err) {' \
	$'err != nil && false {'
run_case $'files: the session secret leaves the ignore list' \
	fail \
	./internal/store \
	$'TestTheSessionSecretAndTheDatabaseFilesCannotBeCommitted' \
	$'.gitignore has no' \
	.gitignore \
	$'\n.session-secret\n' \
	$'\n'
run_case $'files: the passports are written 0600, unreadable to the services they are for' \
	fail \
	./internal/stack \
	$'TestSharedFilesStayReadableByOtherServices' \
	$'other services can no longer read it' \
	internal/stack/stack.go \
	$'0o644); err != nil {' \
	$'0o600); err != nil {'
run_case $'files: a reworded warning about a refused chmod is not a fault' \
	pass \
	./internal/store \
	$'TestAChmodTheFilesystemRefusesIsAWarningNotSilenceNotAnOutage' \
	$'' \
	internal/store/store.go \
	$'"could not make %s private (0600): %v"' \
	$'"%s is not private (0600), because: %v"'

# 64. One construction planted in a real file per case. The first two are the
# console and its command; the third is a package the console imports, which
# only the second walk can see.
run_case $'egress: a handler builds an http.Client' \
	fail \
	./internal/web \
	$'TestTheConsoleConstructsNoOutboundHTTPClientOrRequest' \
	$'http.Client' \
	internal/web/server.go \
	$'func (s *Server) logout(w http.ResponseWriter, r *http.Request) {\n' \
	$'func (s *Server) logout(w http.ResponseWriter, r *http.Request) {\n	_ = &http.Client{}\n'
run_case $'egress: the command line takes a reference to http.Get' \
	fail \
	./internal/web \
	$'TestTheConsoleConstructsNoOutboundHTTPClientOrRequest' \
	$'http.Get' \
	cmd/costcrew/main.go \
	$'func reportStoreWarnings(st *store.Store) {\n' \
	$'func reportStoreWarnings(st *store.Store) {\n	_ = http.Get\n'
run_case $'egress: a package the console imports starts building requests' \
	fail \
	./internal/web \
	$'TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork' \
	$'want exactly [internal/deliver internal/sso]' \
	internal/money/money.go \
	$'import (\n' \
	$'import (\n	"net/http"\n' \
	internal/money/money.go \
	$'\nfunc (c Cents) Float() float64 { return float64(c) / 100 }' \
	$'\nvar _ = http.Get\n\nfunc (c Cents) Float() float64 { return float64(c) / 100 }'
run_case $'egress: the console imports the package that pushes budgets elsewhere' \
	fail \
	./internal/web \
	$'TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork' \
	$'the console imports internal/enforce' \
	internal/web/server.go \
	$'import (\n' \
	$'import (\n	_ "github.com/TAIPANBOX/costcrew/internal/enforce"\n'
run_case $'egress: a second place reaches deliver.Call' \
	fail \
	./internal/web \
	$'TestDeliverCallIsReachedFromExactlyOnePlace' \
	$'is reached from' \
	internal/web/planning.go \
	$'	res, callErr := deliver.Call(' \
	$'	_ = deliver.Call\n	res, callErr := deliver.Call('
run_case $'egress: ordinary server code in a handler is not a fault' \
	pass \
	./internal/web \
	$'TestTheConsoleConstructsNoOutboundHTTPClientOrRequest' \
	$'' \
	internal/web/server.go \
	$'func (s *Server) logout(w http.ResponseWriter, r *http.Request) {\n' \
	$'func (s *Server) logout(w http.ResponseWriter, r *http.Request) {\n	_ = http.StatusTeapot\n'
# The image holds what the manifest says it holds (costcrew#75), and its base
# images are pinned by digest. The first fault is the one that happened: a
# binary the documentation names is not copied into the runtime stage.
run_case 'image: a declared binary is not copied into the runtime stage' \
	fail \
	./internal/manifest \
	$'TestTheDockerfileShipsExactlyTheBinariesTheManifestSaysItDoes' \
	$'costcrew-enforce is declared in the image' \
	Dockerfile \
	$'COPY --from=build /out/costcrew-enforce /usr/local/bin/costcrew-enforce\n' \
	$''
run_case 'image: a base image goes back to a tag with no digest' \
	fail \
	./internal/manifest \
	$'TestEveryBaseImageIsPinnedByDigest' \
	$'names no @sha256: digest' \
	Dockerfile \
	$'static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab' \
	$'static-debian12:nonroot'
run_case 'image: the comparison stops reporting a declared binary the Dockerfile lacks' \
	fail \
	./internal/manifest \
	$'TestTheDockerfileComparisonSeesEachWayTheyCanDisagree' \
	$'expected a disagreement containing' \
	internal/manifest/image_test.go \
	$'		if !d.copied[name] {\n			out = append(out, name+" is declared in the image' \
	$'		if false {\n			out = append(out, name+" is declared in the image'
run_case 'image: a reworded comment above a digest is not a fault' \
	pass \
	./internal/manifest \
	$'TestEveryBaseImageIsPinnedByDigest' \
	$'' \
	Dockerfile \
	$'\n# golang:1.27-alpine\nFROM' \
	$'\n# the golang 1.27 alpine build image\nFROM'
# Invariant 59: the HTTP edge. Each fault below is one the three defects were
# made of, planted back; the last of each group is a harmless edit the gate must
# not mind, so a gate that merely pinned today's literal would read OVEREAGER.
run_case $'http edge: a hand-written HTML writer is back in the results export' \
	fail \
	./internal/web \
	$'TestResultsExportHasNoHandWrittenHTMLWriter' \
	$'exportResultsHTML calls Fprintf' \
	internal/web/practice.go \
	$'\t_, _ = buf.WriteTo(w)\n}\n' \
	$'\t_, _ = buf.WriteTo(w)\n\tfmt.Fprintf(w, "<!-- %s -->", a.Unallocated)\n}\n'
run_case $'http edge: the results export is built with text/template, which does not escape' \
	fail \
	./internal/web \
	$'TestResultsExportEscapesWhatAnImportedRowCarries' \
	$'is written into /export/results.html raw' \
	internal/web/practice.go \
	$'\t"html/template"\n' \
	$'\t"text/template"\n'
run_case $'http edge: the Content-Security-Policy is not sent' \
	fail \
	./internal/web \
	$'TestEveryRouteCarriesTheSecurityHeaders' \
	$'no Content-Security-Policy' \
	internal/web/edge.go \
	$'\th.Set("Content-Security-Policy", contentSecurityPolicy)\n' \
	$'\t_ = contentSecurityPolicy\n'
run_case $'http edge: X-Frame-Options is not sent' \
	fail \
	./internal/web \
	$'TestEveryRouteCarriesTheSecurityHeaders' \
	$'X-Frame-Options' \
	internal/web/edge.go \
	$'\th.Set("X-Frame-Options", "DENY")\n' \
	$'\t_ = "DENY"\n'
run_case $'http edge: the policy allows inline script' \
	fail \
	./internal/web \
	$'TestTheContentSecurityPolicyAllowsNoScript' \
	$'script-src' \
	internal/web/edge.go \
	$'const contentSecurityPolicy = "default-src \'none\'; style-src' \
	$'const contentSecurityPolicy = "default-src \'none\'; script-src \'self\' \'unsafe-inline\'; style-src'
run_case $'http edge: Sign out needs a script again' \
	fail \
	./internal/web \
	$'TestNoPageReliesOnWhatThePolicyForbids' \
	$'relies on' \
	internal/web/templates/layout.html \
	$'<form class="signout" method="post" action="/logout">' \
	$'<form class="signout" method="post" action="/logout" onsubmit="return true">'
run_case $'http edge: Strict-Transport-Security is sent over plain HTTP too' \
	fail \
	./internal/web \
	$'TestStrictTransportSecurityFollowsTheCookiePosture' \
	$'Strict-Transport-Security' \
	internal/web/edge.go \
	$'\tif s.behindTLS || r.TLS != nil {\n\t\th.Set("Strict-Transport-Security"' \
	$'\tif true {\n\t\th.Set("Strict-Transport-Security"'
run_case $'http edge: Strict-Transport-Security ignores a TLS handshake this process made' \
	fail \
	./internal/web \
	$'TestStrictTransportSecurityFollowsTheCookiePosture' \
	$'TLS terminated here' \
	internal/web/edge.go \
	$'\tif s.behindTLS || r.TLS != nil {\n\t\th.Set("Strict-Transport-Security"' \
	$'\tif s.behindTLS {\n\t\th.Set("Strict-Transport-Security"'
run_case $'http edge: the body cap is not applied' \
	fail \
	./internal/web \
	$'TestAnOversizedPostIsRefusedAndChangesNothing' \
	$'want 413' \
	internal/web/server.go \
	$'\tif !limitBody(w, r) {' \
	$'\tif false {'
run_case $'http edge: a chunked body is handed to the handler uncapped' \
	fail \
	./internal/web \
	$'TestAnOversizedPostIsRefusedAndChangesNothing' \
	$'chunked:' \
	internal/web/edge.go \
	$'\tif r.ContentLength >= 0 {\n\t\tr.Body = http.MaxBytesReader' \
	$'\tif true {\n\t\tr.Body = http.MaxBytesReader'
run_case $'http edge: the intake upload is held to the general cap' \
	fail \
	./internal/web \
	$'TestIntakeStillAcceptsAFileUpToItsOwnCap' \
	$'answered 413' \
	internal/web/edge.go \
	$'\t\treturn maxIntake + multipartOverhead\n' \
	$'\t\treturn maxBody\n'
run_case $'http edge: the intake round trip is held to the general cap' \
	fail \
	./internal/web \
	$'TestIntakeApplyAcceptsTheEncodedFileItCheckedAndRefusesMore' \
	$'refused as too large' \
	internal/web/edge.go \
	$'\t\treturn 6*maxIntake + multipartOverhead\n' \
	$'\t\treturn maxBody\n'
run_case $'http edge: the intake reads a file over its cap in part again' \
	fail \
	./internal/web \
	$'TestIntakeRefusesAFileOverItsCapInsteadOfCuttingIt' \
	$'want a redirect with the reason' \
	internal/web/intake.go \
	$'io.LimitReader(f, maxIntake+1)' \
	$'io.LimitReader(f, maxIntake)'
run_case $'http edge: the write timeout is not longer than a model call' \
	fail \
	./internal/web \
	$'TestTheServerSetsEveryTimeoutAndOutlastsTheLongestHandler' \
	$'is under twice' \
	internal/web/edge.go \
	$'\tWriteTimeout = 2 * planAskTimeout\n' \
	$'\tWriteTimeout = planAskTimeout\n'
run_case $'http edge: the write timeout is not set' \
	fail \
	./internal/web \
	$'TestTheServerSetsEveryTimeoutAndOutlastsTheLongestHandler' \
	$'WriteTimeout is 0s' \
	internal/web/edge.go \
	$'\t\tWriteTimeout:      WriteTimeout,\n' \
	$''
run_case $'http edge: the console builds a literal http.Server again' \
	fail \
	./cmd/costcrew \
	$'TestTheConsoleServesThroughTheServerThatOwnsItsTimeouts' \
	$'builds a literal http.Server' \
	cmd/costcrew/main.go \
	$'\tsrv := web.NewHTTPServer(addr, web.New(st, au, web.Stack{' \
	$'\tsrv := &http.Server{Addr: addr, Handler: web.New(st, au, web.Stack{' \
	cmd/costcrew/main.go \
	$'\t\tBehindTLS: behindTLS, OIDC: oidcProv,\n\t}))\n' \
	$'\t\tBehindTLS: behindTLS, OIDC: oidcProv,\n\t})}\n'
run_case $'http edge: reordered policy directives and a reworded refusal are not a fault' \
	pass \
	./internal/web \
	$'TestTheContentSecurityPolicyAllowsNoScript|TestAnOversizedPostIsRefusedAndChangesNothing' \
	$'' \
	internal/web/edge.go \
	$'"form-action \'self\'; base-uri \'none\'; frame-ancestors \'none\'"' \
	$'"frame-ancestors \'none\'; base-uri \'none\'; form-action \'self\'"' \
	internal/web/edge.go \
	$'request too large: this page accepts at most %d bytes' \
	$'request too large: at most %d bytes are read here'
# Invariant 65 (costcrew#73): every gateway call names the person it spends
# for, and an analyst with no owner is refused rather than sent with an empty
# chain. Twelve faults and two harmless rewordings.
run_case $'owner chain: the header is left out of the one function that sets them' \
	fail \
	./internal/deliver \
	$'TestEveryRequestShapeCarriesTheOnBehalfOfChain' \
	$'x-fuse-on-behalf-of' \
	internal/deliver/call.go \
	$'		req.Header.Set("x-fuse-on-behalf-of", strings.Join(gw.OnBehalfOf, ","))' \
	$'		_ = strings.Join(gw.OnBehalfOf, ",")'
run_case $'owner chain: the tool loop builds its own headers again' \
	fail \
	./tools/run \
	$'TestTheToolLoopAndDeliverCallSendTheSameFuseHeaders' \
	$'the tool loop sends' \
	tools/run/loop.go \
	$'		// never learned x-fuse-on-behalf-of (costcrew#73).\n		deliver.SetFuseHeaders(req, gw)' \
	$'		// never learned x-fuse-on-behalf-of (costcrew#73).\n		req.Header.Set("x-fuse-run-id", gw.RunID)\n		req.Header.Set("x-fuse-agent-id", gw.AgentID)\n		req.Header.Set("x-fuse-budget-usd", gw.BudgetUSD)\n		if gw.ParentRunID != "" {\n			req.Header.Set("x-fuse-parent-run-id", gw.ParentRunID)\n		}'
run_case $'owner chain: the owner goes into the chain unescaped' \
	fail \
	./internal/deliver \
	$'TestHostileOwnersCannotForgeOrBreakAChain' \
	$'TestHostileOwnersCannotForgeOrBreakAChain' \
	internal/deliver/call.go \
	$'	chain := []string{"user://" + host + "/" + url.PathEscape(owner), agent}' \
	$'	chain := []string{"user://" + host + "/" + owner, agent}'
run_case $'owner chain: an empty owner is accepted' \
	fail \
	./internal/deliver \
	$'TestAnAnalystWithNoOwnerGetsNoChainAndIsNamed' \
	$'is exactly what must never be sent' \
	internal/deliver/call.go \
	$'	owner = strings.TrimSpace(owner)\n	if owner == "" {' \
	$'	owner = strings.TrimSpace(owner)\n	if false {'
run_case $'owner chain: the door lets a gateway call through with no chain' \
	fail \
	./internal/deliver \
	$'TestAGatewayCallWithNoOwnerChainIsRefusedBeforeAnyRequest' \
	$'the gateway was reached by a call that names nobody' \
	internal/deliver/call.go \
	$'	if len(gw.OnBehalfOf) == 0 {' \
	$'	if false {'
run_case $'owner chain: pricing no longer refuses an ownerless analyst' \
	fail \
	./tools/run \
	$'TestWithAGatewayAnOwnerlessAnalystsTaskIsRefusedWhenItIsPriced' \
	$'was priced to run' \
	tools/run/live.go \
	$'func refuseOwnerless(ests []estimate, gw gatewayConfig) {\n	if !gw.on() {' \
	$'func refuseOwnerless(ests []estimate, gw gatewayConfig) {\n	if true {'
run_case $'owner chain: the runner builds its Gateway without the chain' \
	fail \
	./tools/run \
	$'TestEveryRoundOfAnAnthropicTaskCarriesTheAnalystsOwner' \
	$'no owner chain' \
	tools/run/live.go \
	$'		BudgetUSD:  gatewayBudgetUSD(cfg.CeilingUSD, taskGuard),\n		OnBehalfOf: chain,' \
	$'		BudgetUSD:  gatewayBudgetUSD(cfg.CeilingUSD, taskGuard),\n		OnBehalfOf: chain[:0],'
run_case $'owner chain: the bench builds its Gateway without the chain' \
	fail \
	./tools/bench \
	$'TestLiveSendsTheAnalystsOwnerOnEveryCase' \
	$'no owner chain' \
	tools/bench/gateway.go \
	$'		BudgetUSD:  budgetUSD,\n		OnBehalfOf: chain,' \
	$'		BudgetUSD:  budgetUSD,\n		OnBehalfOf: chain[:0],'
run_case $'owner chain: the console builds its plan-ask Gateway without the chain' \
	fail \
	./internal/web \
	$'TestThePlanAskNamesTheAskingPersonAsTheRoot' \
	$'want 1' \
	internal/web/planning.go \
	$'		BudgetUSD:  deliver.GatewayBudgetUSD(sup.PerTask, sup.PerTask),\n		OnBehalfOf: chain,' \
	$'		BudgetUSD:  deliver.GatewayBudgetUSD(sup.PerTask, sup.PerTask),\n		OnBehalfOf: chain[:0],'
run_case $'owner chain: the plan-ask names the supervisor\'s roster owner, not the person who asked' \
	fail \
	./internal/web \
	$'TestThePlanAskNamesTheAskingPersonAsTheRoot' \
	$'x-fuse-on-behalf-of =' \
	internal/web/planning.go \
	$'deliver.OnBehalfOfChain(s.host, u.Username, "supervisor")' \
	$'deliver.OnBehalfOfChain(s.host, sup.Owner, "supervisor")'
run_case $'owner chain: the roster placeholder is accepted as an owner' \
	fail \
	./internal/deliver \
	$'TestAnUnclaimedRosterOwnerIsNoOwner' \
	$'the placeholder owner produced a chain' \
	internal/deliver/call.go \
	$'	if strings.TrimSpace(a.Owner) == crew.SeededOwner("") {' \
	$'	if false {'
run_case $'owner chain: a chain the gateway would silently ignore is sent' \
	fail \
	./internal/deliver \
	$'TestAChainTheGatewayWouldSilentlyIgnoreIsRefusedHere' \
	$'that TokenFuse ignores' \
	internal/deliver/call.go \
	$'	if n := len(strings.Join(chain, ",")); n > maxOnBehalfOfBytes {' \
	$'	if n := len(strings.Join(chain, ",")); n < 0 {'
run_case $'owner chain: a reworded placeholder refusal is not a fault' \
	pass \
	./internal/deliver \
	$'TestAnUnclaimedRosterOwnerIsNoOwner' \
	$'' \
	internal/deliver/call.go \
	$'it is refused rather than "+' \
	$'we refuse it rather than "+'
run_case $'owner chain: a reworded refusal prefix in execute is not a fault' \
	pass \
	./tools/run \
	$'TestAnAnalystWithNoOwnerIsRefusedBeforeAnyCall' \
	$'' \
	tools/run/live.go \
	$'refused before the call: %w", herr' \
	$'not run: %w", herr'

# Invariant 66: a task a person blocks while its call is in flight does not get
# its deliverable, and the call it already paid for is still recorded. Five
# faults and one harmless rewording.
run_case $'blocked meanwhile: the draft insert stops looking at the task\'s state' \
	fail \
	./tools/run \
	$'TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable' \
	$'were written for a task a person blocked' \
	tools/run/live.go \
	$'WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE id = ? AND state = \'blocked\')' \
	$'WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE id = ? AND state = \'a state nobody uses\')'
run_case $'blocked meanwhile: the discarded answer\'s charge is not booked' \
	fail \
	./tools/run \
	$'TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable' \
	$'tasks.live_micros =' \
	tools/run/live.go \
	$'			if charge > 0 {\n				if e2 := recordCharge(db, e.Task.ID, charge); e2 != nil {\n					fmt.Fprintf(os.Stderr, "  could not record the charge of the discarded' \
	$'			if false {\n				if e2 := recordCharge(db, e.Task.ID, charge); e2 != nil {\n					fmt.Fprintf(os.Stderr, "  could not record the charge of the discarded'
run_case $'blocked meanwhile: the run counts a discarded answer as a failed task' \
	fail \
	./tools/run \
	$'TestARunLeavesAPersonsBlockAloneAndCountsTheDiscardedAnswer' \
	$'the summary does not say 0 of 1 done and 1 discarded' \
	tools/run/live.go \
	$'			if errors.As(err, &d) {\n				// Already blocked by a person' \
	$'			if errors.As(err, &d) && false {\n				// Already blocked by a person'
run_case $'blocked meanwhile: the discard is not said' \
	fail \
	./tools/run \
	$'TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable' \
	$'no line says the answer was discarded' \
	tools/run/live.go \
	$'DISCARDED: the answer came back' \
	$'dropped: the answer came back'
run_case $'blocked meanwhile: the summary does not count the discard' \
	fail \
	./tools/run \
	$'TestARunLeavesAPersonsBlockAloneAndCountsTheDiscardedAnswer' \
	$'the summary does not say' \
	tools/run/live.go \
	$'				discarded++\n' \
	$''
run_case $'blocked meanwhile: a failed call overwrites the person\'s reason' \
	fail \
	./tools/run \
	$'TestAFailedCallLeavesAPersonsBlockReasonAlone' \
	$'a failed call rewrote the person\'s block' \
	tools/run/live.go \
	$'WHERE id=? AND state <> \'blocked\'`,' \
	$'WHERE id=?`,'
run_case $'blocked meanwhile: a reworded discard line is not a fault' \
	pass \
	./tools/run \
	$'TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable' \
	$'' \
	tools/run/live.go \
	$'so no draft was saved; the call cost' \
	$'so nothing was saved; the call cost'

# What a download carries is data (invariant 78): Markdown escaped, CSV
# neutralised by column kind, one file name per download, and x_unit, the
# field those names most often arrive in, held to the plain-name rule.
run_case 'downloads: Markdown values written raw into the packet' \
	fail \
	./internal/web \
	$'TestTheExecPacketKeepsAnImportedValueAsText' \
	$'raw <script> tag' \
	internal/web/download.go \
	$'func mdText(s string) string {\n' \
	$'func mdText(s string) string {\n\treturn s\n'
run_case 'downloads: a reworded comment in the Markdown escaping is not a fault' \
	pass \
	./internal/web \
	$'TestTheExecPacketKeepsAnImportedValueAsText' \
	$'' \
	internal/web/download.go \
	$'// mdText makes a value safe to put in Markdown prose or a table cell.' \
	$'// mdText makes a value safe for Markdown prose and for a table cell.'
run_case 'downloads: the file name concatenated into the header again' \
	fail \
	./internal/web \
	$'TestADownloadsFileNameIsOneParameterWhateverItCarries' \
	$'does not parse' \
	internal/web/download.go \
	$'	v := mime.FormatMediaType(disposition, map[string]string{"filename": filename})' \
	$'	v := disposition + "; filename=" + filename\n	_ = mime.FormatMediaType'
run_case 'downloads: a second place names a download' \
	fail \
	./internal/web \
	$'TestEveryDownloadIsNamedThroughOneHelper' \
	$'the Content-Disposition header is set in' \
	internal/web/money.go \
	$'func (s *Server) exportCrewCSV(w http.ResponseWriter, r *http.Request) {\n' \
	$'func (s *Server) exportCrewCSV(w http.ResponseWriter, r *http.Request) {\n	w.Header().Set("Content-Disposition", "attachment; filename=crew.csv")\n'
run_case 'downloads: a CSV cell that starts a formula is written as it came' \
	fail \
	./internal/web \
	$'TestACSVExportNeutralisesAFormulaAndKeepsANegativeNumber' \
	$'starts a formula' \
	internal/web/download.go \
	$'func csvCell(kind csvKind, v string) string {\n' \
	$'func csvCell(kind csvKind, v string) string {\n\treturn v\n'
run_case 'downloads: a number column neutralised like a text one' \
	fail \
	./internal/web \
	$'TestACSVExportNeutralisesAFormulaAndKeepsANegativeNumber' \
	$'a negative amount in a number' \
	internal/web/download.go \
	$'	if kind == csvNumber && plainNumber.MatchString(v) {' \
	$'	if false && kind == csvNumber && plainNumber.MatchString(v) {'
run_case 'downloads: x_unit carried unchecked into charges.team' \
	fail \
	./internal/connectors \
	$'TestXUnitIsRefusedUnlessItIsAPlainBoundedName' \
	$'the row was not refused' \
	internal/connectors/tokenfusefocus.go \
	$'	if reason := plainUnitName(unit); reason != "" {' \
	$'	if reason := plainUnitName(unit); false && reason != "" {'
run_case 'downloads: every refused row named, however many' \
	fail \
	./internal/connectors \
	$'TestRefusalsAreCountedWholeButNamedOnlyForTheFirstFew' \
	$'refusals, want the first' \
	internal/connectors/tokenfusefocus.go \
	$'	if len(s.Refusals) < focusRefusalsShown {\n		s.Refusals = append(s.Refusals, reason)' \
	$'	if true {\n		s.Refusals = append(s.Refusals, reason)' \
	internal/connectors/tokenfusefocus.go \
	$'		if len(s.Refusals) < focusRefusalsShown {\n			s.Refusals = append(s.Refusals, r)' \
	$'		if true {\n			s.Refusals = append(s.Refusals, r)'
run_case 'downloads: a link in the import folder followed' \
	fail \
	./internal/connectors \
	$'TestALinkInTheFolderIsNotFollowedAndIsNamed' \
	$'the link\'s target was read' \
	internal/connectors/tokenfusefocus.go \
	$'		if !e.Type().IsRegular() {' \
	$'		if false && !e.Type().IsRegular() {' \
	internal/connectors/tokenfusefocus.go \
	$'	} else if !fi.Mode().IsRegular() {' \
	$'	} else if false && !fi.Mode().IsRegular() {'
# No cache keeps a page or a download (invariant 79).
run_case 'no-store: the header left off' \
	fail \
	./internal/web \
	$'TestEveryGuardedResponseIsMarkedNoStore' \
	$'want no-store' \
	internal/web/server.go \
	$'	noStore(w, r)\n' \
	$''
run_case 'no-store: the stylesheet marked no-store too' \
	fail \
	./internal/web \
	$'TestTheStylesheetIsNotMarkedNoStore' \
	$'the one response worth caching' \
	internal/web/edge.go \
	$'	if strings.HasPrefix(r.URL.Path, "/static/") {' \
	$'	if false && strings.HasPrefix(r.URL.Path, "/static/") {'
run_case 'no-store: a reworded comment is not a fault' \
	pass \
	./internal/web \
	$'TestEveryGuardedResponseIsMarkedNoStore' \
	$'' \
	internal/web/edge.go \
	$'// noStore marks every response but the stylesheet as one no cache may keep.' \
	$'// noStore marks every response except the stylesheet as one no cache may keep.'
# A link leads somewhere, and a tool says what it does.
run_case 'team page: a unit off the roster answers 404 again' \
	fail \
	./internal/web \
	$'TestEveryTeamLinkOnTheMoneyPagesLeadsToAPage' \
	$'leads nowhere' \
	internal/web/drill.go \
	$'		if !charged || name == "" {' \
	$'		if true || !charged || name == "" {'
run_case 'idryxsource: an agent with no rights written as tools null' \
	fail \
	./tools/idryxsource \
	$'TestAnAgentWithNoRightsHasAnEmptyToolsList' \
	$'want "tools":[]' \
	tools/idryxsource/main.go \
	$'		Tools: append(make([]string, 0, len(a.Rights)), a.Rights...),' \
	$'		Tools: append([]string(nil), a.Rights...),'
run_case 'spiffe: Close decides with no lock' \
	fail \
	./internal/spiffe \
	$'TestCloseDecidesUnderTheLockAndClosesOnce' \
	$'without the Source\'s lock held' \
	internal/spiffe/spiffe.go \
	$'	s.mu.Lock()\n	defer s.mu.Unlock()\n	if s.closed {' \
	$'	if s.closed {'
run_case 'spiffe: Identity asks a source it has closed' \
	fail \
	./internal/spiffe \
	$'TestIdentityAfterCloseKeepsTheLastIdentityAndAsksNothing' \
	$'asked a closed source' \
	internal/spiffe/spiffe.go \
	$'	if closed {\n		s.mu.RLock()' \
	$'	if false && closed {\n		s.mu.RLock()'
run_case 'parity: the usage names a flag capture does not have' \
	fail \
	./tools/parity \
	$'TestTheUsageNamesEveryFlagEachSubcommandDefines' \
	$'which it does not define' \
	tools/parity/main.go \
	$'[-per-family N] [-from GOLDEN]' \
	$'[-max N] [-from GOLDEN]'
run_case 'parity: the Go audit hash left unscrubbed' \
	fail \
	./tools/parity \
	$'TestTheJournalHashIsScrubbedInTheMarkupTheGoConsoleRenders' \
	$'still differ after normalising' \
	tools/parity/main.go \
	$'regexp.MustCompile(`<td class="tight"><code>[0-9a-f]{8,64}</code></td>`)' \
	$'regexp.MustCompile(`<td class="qtight"><code>[0-9a-f]{8,64}</code></td>`)'
run_case 'plan-ask: a local supervisor refused with the generic sentence' \
	fail \
	./internal/web \
	$'TestAskPlanForALocalSupervisorSaysWhyItIsRefused' \
	$'the refusal does not say' \
	internal/web/planning.go \
	$'	case engine == engines.LocalID:' \
	$'	case false && engine == engines.LocalID:'
run_case 'engines page: only the first three families listed' \
	fail \
	./internal/web \
	$'TestTheEnginesPageListsEveryFamilyInTheCatalogue' \
	$'does not list the' \
	internal/web/ops.go \
	$'	for _, f := range families {' \
	$'	for _, f := range families[:3] {'
# 74 and 75. Sign-in through the organisation's identity provider. Every case
# switches off one check the flow makes, in the place it is made, and requires
# the test written for that check to go red for that reason. go-oidc's own
# checks (signature, audience, expiry) are switched off through its Config,
# which is how a mistake in this repository would switch them off.
run_case $'oidc: the signature is not checked' \
	fail \
	./internal/sso \
	$'TestTheIDTokenIsCheckedClaimByClaim' \
	$'bad_signature' \
	internal/sso/flow.go \
	$'&oidc.Config{ClientID: p.cfg.ClientID}' \
	$'&oidc.Config{ClientID: p.cfg.ClientID, InsecureSkipSignatureCheck: true}'
run_case $'oidc: the audience is not checked' \
	fail \
	./internal/web \
	$'TestEveryRefusedSignInLeavesNoSessionAndNoAccount' \
	$'wrong_audience' \
	internal/sso/flow.go \
	$'&oidc.Config{ClientID: p.cfg.ClientID}' \
	$'&oidc.Config{SkipClientIDCheck: true}'
run_case $'oidc: the expiry is not checked' \
	fail \
	./internal/web \
	$'TestEveryRefusedSignInLeavesNoSessionAndNoAccount' \
	$'expired_token' \
	internal/sso/flow.go \
	$'&oidc.Config{ClientID: p.cfg.ClientID}' \
	$'&oidc.Config{ClientID: p.cfg.ClientID, SkipExpiryCheck: true}'
run_case $'oidc: the nonce is not checked' \
	fail \
	./internal/web \
	$'TestEveryRefusedSignInLeavesNoSessionAndNoAccount' \
	$'missing_nonce' \
	internal/sso/flow.go \
	$'	if idt.Nonce == "" {' \
	$'	if false {' \
	internal/sso/flow.go \
	$'	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pend.nonce)) != 1 {' \
	$'	if false {'
run_case $'oidc: an iat from the future is accepted' \
	fail \
	./internal/sso \
	$'TestTheIDTokenIsCheckedClaimByClaim' \
	$'want it refused for "in the future"' \
	internal/sso/flow.go \
	$'	case idt.IssuedAt.After(now.Add(MaxClockSkew)):' \
	$'	case false:'
run_case $'oidc: an iat from before the sign-in began is accepted' \
	fail \
	./internal/sso \
	$'TestTheIDTokenIsCheckedClaimByClaim' \
	$'want it refused for "before this sign-in began"' \
	internal/sso/flow.go \
	$'	case idt.IssuedAt.Before(pend.created.Add(-MaxClockSkew)):' \
	$'	case false:'
run_case $'oidc: the authorized party is not checked' \
	fail \
	./internal/sso \
	$'TestTheIDTokenIsCheckedClaimByClaim' \
	$'want it refused for "issued to"' \
	internal/sso/flow.go \
	$'	if (len(idt.Audience) > 1 && azp != p.cfg.ClientID) || (azp != "" && azp != p.cfg.ClientID) {' \
	$'	if false {'
# The end-to-end replay test alone was TOOTHLESS against this one, measured
# 2026-10-07: the fake provider's own one-use code refuses the second exchange,
# so the replay failed for the provider's reason, not this console's. The sso
# test also requires the replay to never reach the token endpoint.
run_case $'oidc: a state is read instead of spent' \
	fail \
	./internal/sso \
	$'TestAStateIsSpentByItsFirstUse' \
	$'unknown or was already used' \
	internal/sso/flow.go \
	$'`DELETE FROM oidc_pending WHERE state_hash=? RETURNING nonce, verifier, created, expires`' \
	$'`SELECT nonce, verifier, created, expires FROM oidc_pending WHERE state_hash=?`'
run_case $'oidc: the state is not bound to the browser that started it' \
	fail \
	./internal/web \
	$'TestACallbackInABrowserThatDidNotStartItIsRefused' \
	$'callback without the state cookie' \
	internal/sso/flow.go \
	$'	if browserState == "" || subtle.ConstantTimeCompare([]byte(state), []byte(browserState)) != 1 {' \
	$'	if false {'
run_case $'oidc: an unmapped group gets a default role' \
	fail \
	./internal/web \
	$'TestEveryRefusedSignInLeavesNoSessionAndNoAccount' \
	$'unmapped_group' \
	internal/sso/sso.go \
	$'	best := ""' \
	$'	best := "viewer"'
run_case $'oidc: removal from the group leaves the sessions alive' \
	fail \
	./internal/web \
	$'TestRemovalFromTheGroupEndsEverySessionAtTheNextSignIn' \
	$'the session from before the removal still signs in' \
	internal/auth/external.go \
	$'`DELETE FROM sessions WHERE username=?`, linked)' \
	$'`DELETE FROM sessions WHERE username=? AND 0`, linked)'
run_case $'oidc: a role change at the provider is not applied' \
	fail \
	./internal/web \
	$'TestARoleDowngradeAtTheProviderAppliesAtTheNextSignIn' \
	$'after the provider moved her to viewers' \
	internal/auth/external.go \
	$'		if u.Role != role {' \
	$'		if false {'
run_case $'oidc: an identity adopts a local account by its name' \
	fail \
	./internal/auth \
	$'TestALocalAccountIsNeverAdoptedByName' \
	$'an identity named like a local admin signed in' \
	internal/auth/external.go \
	$'		} else if u != nil || taken {' \
	$'		} else if (u != nil || taken) && false {'
run_case $'oidc: -oidc-only still takes any password' \
	fail \
	./internal/web \
	$'TestOIDCOnlyRefusesPasswordsExceptTheCommandLinesBreakGlass' \
	$'a local password under -oidc-only' \
	internal/web/server.go \
	$'		authenticate = s.au.AuthenticateBreakGlass' \
	$'		_ = s.au.AuthenticateBreakGlass'
run_case $'oidc: registration stays open while a provider is configured' \
	fail \
	./internal/web \
	$'TestRegistrationIsClosedWhileAProviderIsConfigured' \
	$'GET /signup' \
	internal/web/server.go \
	$'	if s.oidc != nil {\n		return false, nil\n	}\n	return s.au.SignupOpen()' \
	$'	return s.au.SignupOpen()'
run_case $'oidc: the sign-in page posts a form to the provider' \
	fail \
	./internal/web \
	$'TestTheSignInPageReachesTheProviderByALinkNotAForm' \
	$'has no link to' \
	internal/web/oidc.go \
	$'	out := `<p class="row"><a class="button" href="` + sso.StartPath +\n		`">Sign in with your organisation</a></p>`' \
	$'	out := `<form method="get" action="` + s.oidc.Config().Issuer + `/authorize"><button>Sign in with your organisation</button></form>`'
run_case $'oidc: the client follows a redirect from the provider' \
	fail \
	./internal/sso \
	$'TestNoRedirectFromTheProviderIsFollowed' \
	$'followed the provider\'s redirect' \
	internal/sso/client.go \
	$'		CheckRedirect: func(req *http.Request, _ []*http.Request) error {\n' \
	$'		CheckRedirect: func(req *http.Request, _ []*http.Request) error {\n			return nil\n'
run_case $'oidc: a response body is read whole' \
	fail \
	./internal/sso \
	$'TestAResponseOverTheCapIsRefusedNotRead' \
	$'larger than 1 MiB' \
	internal/sso/client.go \
	$'	resp.Body = &capped{r: resp.Body, left: maxResponseBytes}' \
	$'	_ = &capped{r: resp.Body, left: maxResponseBytes}'
run_case $'oidc: the client reaches plain http off this machine' \
	fail \
	./internal/sso \
	$'TestTheSignInClientReachesOnlyHTTPSOrLoopback' \
	$'want refused before any connection' \
	internal/sso/client.go \
	$'	if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && Loopback(req.URL.Hostname())) {' \
	$'	if false {'
run_case $'oidc: the client secret prints' \
	fail \
	./internal/sso \
	$'TestTheClientSecretComesFromOnePlaceAndNeverPrints' \
	$'the client secret is printed' \
	internal/sso/sso.go \
	$'func (Secret) Format(f fmt.State, _ rune) { fmt.Fprint(f, "[redacted]") }' \
	$'func (s Secret) Format(f fmt.State, _ rune) { fmt.Fprint(f, string(s)) }'
run_case $'oidc: the sign-in package grows a second outbound construction' \
	fail \
	./internal/web \
	$'TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork' \
	$'may reach the network only from client.go' \
	internal/sso/flow.go \
	$'import (\n' \
	$'import (\n	"net/http"\n' \
	internal/sso/flow.go \
	$'\nfunc randomToken() (string, error) {' \
	$'\nvar _ = http.Get\n\nfunc randomToken() (string, error) {'
run_case $'oidc: what the provider says reaches the journal unbounded' \
	fail \
	./internal/sso \
	$'TestWhatTheProviderSaysReachesTheJournalBoundedAndPlain' \
	$'want at most 512' \
	internal/sso/flow.go \
	$'Detail: bound(fmt.Sprintf(format, args...), maxDetailBytes)}' \
	$'Detail: fmt.Sprintf(format, args...)}'
run_case $'oidc: a discovered authorization endpoint is not checked' \
	fail \
	./internal/sso \
	$'TestADiscoveredAuthorizationEndpointOverPlainHTTPIsRefused' \
	$'want a refusal naming the authorization endpoint' \
	internal/sso/flow.go \
	$'	if _, err := endpoint("the discovered authorization endpoint", prov.Endpoint().AuthURL); err != nil {' \
	$'	if _, err := endpoint("the discovered authorization endpoint", prov.Endpoint().AuthURL); err != nil \x26\x26 false {'
run_case $'oidc: rewording what a refused person is shown is not a fault' \
	pass \
	./internal/web \
	$'TestEveryRefusedSignInLeavesNoSessionAndNoAccount' \
	$'' \
	internal/sso/flow.go \
	$'MsgStartAgain  = "the sign-in could not be completed; start it again"' \
	$'MsgStartAgain  = "signing in did not finish; please start again"'
run_case $'oidc: a comment naming http.Client in the flow is not a second door' \
	pass \
	./internal/web \
	$'TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork' \
	$'' \
	internal/sso/flow.go \
	$'\nfunc randomToken() (string, error) {' \
	$'\n// Not an http.Client, nor http.Get: a comment the walk must not read.\nfunc randomToken() (string, error) {'

run_case $'cloud focus: a negative BilledCost is refused as the gateway reader refuses it' \
	fail \
	./internal/connectors \
	$'TestANegativeBilledCostIsKeptNotRefused' \
	$'a negative cost was refused' \
	internal/connectors/cloudfocus.go \
	$'row.Micros = micros' \
	$'if micros < 0 { return row, fmt.Errorf("BilledCost %q is negative", costStr) }; row.Micros = micros'
run_case $'cloud focus: a Purchase is filed as Usage' \
	fail \
	./internal/connectors \
	$'TestEveryChargeCategoryLandsAndAPurchaseIsNeverUsage' \
	$'leaked into it' \
	internal/connectors/cloudfocus.go \
	$'row.Category = field("ChargeCategory")' \
	$'row.Category = field("ChargeCategory"); if row.Category == "Purchase" { row.Category = "Usage" }'
run_case $'cloud focus: each row is rounded to cents before the day is summed' \
	fail \
	./internal/connectors \
	$'TestCloudFocusMoneyIsNeverFloatAndRoundsOnce' \
	$'want 4 (35000 micros rounded once)' \
	internal/connectors/cloudfocus.go \
	$'COALESCE(invoice_id,\'\'), SUM(billed_microusd)' \
	$'COALESCE(invoice_id,\'\'), SUM(((billed_microusd+5000)/10000)*10000)'
run_case $'cloud focus: the charges row is written without its provenance' \
	fail \
	./internal/connectors \
	$'TestAWSDataExportsFocusIsRead' \
	$'charges differ' \
	internal/connectors/cloudfocus.go \
	$'int64(cents), nullIfEmpty(g.invoice), spec.id); err != nil {' \
	$'int64(cents), nullIfEmpty(g.invoice), nil); err != nil {'
run_case $'cloud focus: a revised file leaves its earlier version\'s rows behind' \
	fail \
	./internal/connectors \
	$'TestARevisedFileReplacesItsOwnEarlierVersion' \
	$'the old version\'s rows survived' \
	internal/connectors/cloudfocus.go \
	$'AND file_sha256<>?`, w.connector, f.rel, sha); err != nil {' \
	$'AND file_sha256<>? AND 1=0`, w.connector, f.rel, sha); err != nil {'
run_case $'cloud focus: a refused file\'s rows are not rolled back' \
	fail \
	./internal/connectors \
	$'TestCloudFocusHostileInput' \
	$'rows of a refused file survived' \
	internal/connectors/cloudfocus.go \
	$'if _, err := tx.Exec("ROLLBACK TO " + sp); err != nil {' \
	$'if _, err := tx.Exec("SAVEPOINT roll_" + sp); err != nil {'
run_case $'cloud focus: the gzip inflation cap is lifted' \
	fail \
	./internal/connectors \
	$'TestAGzipBombIsRefusedByName' \
	$'does not name the file and the setting' \
	internal/connectors/cloudfocus.go \
	$'capped := &capReader{r: r, left: conf.maxUnpacked,' \
	$'capped := &capReader{r: r, left: math.MaxInt64,'
run_case $'cloud focus: the one-record cap is lifted' \
	fail \
	./internal/connectors \
	$'TestARecordWithNoEndIsRefusedBeforeItFillsMemory' \
	$'does not say a record is too long' \
	internal/connectors/cloudfocus.go \
	$'rec := &recordReader{r: capped, max: cloudMaxRecordBytes}' \
	$'rec := &recordReader{r: capped, max: math.MaxInt64}'
run_case $'cloud focus: a symlink in the folder is followed' \
	fail \
	./internal/connectors \
	$'TestCloudFocusReadsANestedSyncedFolderAndIgnoresLinks' \
	$'the symlinked file was followed' \
	internal/connectors/cloudfocus.go \
	$'if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {' \
	$'if false {'
run_case $'cloud focus: real rows are mixed into the generated estate' \
	fail \
	./internal/connectors \
	$'TestCloudFocusRefusesToMixWithTheGeneratedEstate' \
	$'Import mixed real cloud rows into the generated estate' \
	internal/connectors/cloudfocus.go \
	$'if mixed && !opt.ReplaceGenerated {' \
	$'if false {'
run_case $'cloud focus: a file\'s refusal list grows with the file' \
	fail \
	./internal/connectors \
	$'TestARefusalListIsBoundedAtItsSource' \
	$'want 20 and 100000' \
	internal/connectors/cloudfocus.go \
	$'if len(s.Refusals) < cloudRefusalsShown {' \
	$'if true {'
run_case $'cloud focus: the sentence\'s refusal list grows with the folder' \
	fail \
	./internal/connectors \
	$'TestRefusalsAcrossManyFilesAreBoundedToo' \
	$'does not count thirty and show twenty' \
	internal/connectors/cloudfocus.go \
	$'for _, r := range o.Refusals {\n\t\tif len(s.Refusals) < cloudRefusalsShown {' \
	$'for _, r := range o.Refusals {\n\t\tif true {'
run_case $'cloud focus: a row longer than a day lands on its last day, not its first' \
	fail \
	./internal/connectors \
	$'TestARowLongerThanADayLandsWholeOnItsFirstDay' \
	$'charges differ' \
	internal/connectors/cloudfocus.go \
	$'row.Day = st.Format("2006-01-02")' \
	$'row.Day = en.Add(-time.Nanosecond).Format("2006-01-02")'
run_case $'cloud focus: the 366 day limit on one row is lifted' \
	fail \
	./internal/connectors \
	$'TestCloudFocusHostileInput' \
	$'does not say "366 day"' \
	internal/connectors/cloudfocus.go \
	$'if span > cloudMaxSpan {' \
	$'if false {'
run_case $'cloud focus: a long row is not counted in the sentence' \
	fail \
	./internal/connectors \
	$'TestAFocus10FileIsReadAsWell' \
	$'cover more than one day' \
	internal/connectors/cloudfocus.go \
	$'row.Multiday = span > 24*time.Hour' \
	$'row.Multiday = false'
run_case $'cloud focus: another provider\'s rows are read as this desk\'s' \
	fail \
	./internal/connectors \
	$'TestCloudFocusHostileInput' \
	$'another_provider\'s_export' \
	internal/connectors/cloudfocus.go \
	$'if !conf.providers[strings.ToLower(provider)] {' \
	$'if false {'
run_case $'cloud focus: E notation is refused' \
	fail \
	./internal/connectors \
	$'TestFocusDecimalsInENotation' \
	$'"35.2E-7": got' \
	internal/connectors/cloudfocus.go \
	$'if i := strings.IndexAny(s, "eE"); i >= 0 {' \
	$'if i := strings.IndexAny(s, "~"); i >= 0 {'
run_case $'cloud focus: the tag key is matched case-sensitively only' \
	fail \
	./internal/connectors \
	$'TestTheTeamComesFromAConfigurableTagKey' \
	$'want ops (second key' \
	internal/connectors/cloudfocus.go \
	$'if strings.EqualFold(k, want) {' \
	$'if k == want {'
run_case $'cloud focus: a copy of a file is counted as a second file' \
	fail \
	./internal/connectors \
	$'TestTheSameBytesTwiceAreOneFileAndTwoFilesOnOneDayAdd' \
	$'the copy is not named' \
	internal/connectors/cloudfocus.go \
	$'if seen[sha] {' \
	$'if false && seen[sha] {'
run_case $'cloud focus: Test demands a setting that has a default' \
	fail \
	./internal/connectors \
	$'TestTheCloudFocusReadersAreBuiltAndAskForAFolder' \
	$'Test with only the folder set' \
	internal/connectors/connectors.go \
	$'if !in.Optional && strings.TrimSpace(conn.Config[in.Name]) == "" {' \
	$'if strings.TrimSpace(conn.Config[in.Name]) == "" {'
run_case $'cloud focus: the connector page stops offering replace-generated' \
	fail \
	./internal/web \
	$'TestAnAWSExportReachesTheConsoleThroughTheConnectorPage' \
	$'the connector page does not contain' \
	internal/web/templates/connector.html \
	$'(eq .C.ID "aws-data-exports") ' \
	$''
run_case $'cloud focus: a reworded currency refusal is not a fault' \
	pass \
	./internal/connectors \
	$'TestCloudFocusHostileInput' \
	$'' \
	internal/connectors/cloudfocus.go \
	$'return row, fmt.Errorf("currency %q, this reader is USD only", currency)' \
	$'return row, fmt.Errorf("BillingCurrency %q, and this reader is USD only", currency)'
# ---- tools/{enforce,parity,stack,idryxsource,recon} and internal/spiffe ----
#
# These binaries had no tests at all; their main() is now a one-line wrapper
# over run(). Each case breaks one thing a person relies on and requires the
# test that was written for it to name itself in the failure.

run_case $'enforce: the default run falls back to the wrong month' \
	fail \
	./tools/enforce \
	$'TestTheDefaultPeriodIsTheLastClosedMonth' \
	$'--- FAIL: TestTheDefaultPeriodIsTheLastClosedMonth' \
	tools/enforce/main.go \
	$'world.DayBefore(world.LastDay, 40)[:7]' \
	$'world.DayBefore(world.LastDay, 10)[:7]'
run_case $'enforce: a team budget is the last desk\'s, not the sum across desks' \
	fail \
	./tools/enforce \
	$'TestATeamsBudgetIsTheSumAcrossDesksForTheMonthAsked' \
	$'--- FAIL: TestATeamsBudgetIsTheSumAcrossDesksForTheMonthAsked' \
	tools/enforce/main.go \
	$'out[b.Team] += b.Budget' \
	$'out[b.Team] = b.Budget'
run_case $'enforce: the tool is not off without an address and a key' \
	fail \
	./tools/enforce \
	$'TestItIsOffWithoutAnAddressAndAKey' \
	$'--- FAIL: TestItIsOffWithoutAnAddressAndAKey' \
	tools/enforce/main.go \
	$'if !cfg.On() {' \
	$'if false {'
run_case $'enforce: -apply is handed the plan\'s own fingerprint, so any value is approved' \
	fail \
	./tools/enforce \
	$'TestAWrongFingerprintSendsNothing' \
	$'--- FAIL: TestAWrongFingerprintSendsNothing' \
	tools/enforce/main.go \
	$'enforce.Apply(ctx, cfg, plan, *expect)' \
	$'enforce.Apply(ctx, cfg, plan, fp)'
run_case $'enforce: a lowering is no longer marked as the direction that stops work' \
	fail \
	./tools/enforce \
	$'TestTheDefaultRunPrintsTheDiffAndSendsNothing' \
	$'--- FAIL: TestTheDefaultRunPrintsTheDiffAndSendsNothing' \
	tools/enforce/main.go \
	$'case c.Lowered:' \
	$'case c.Lowered && false:'
run_case $'enforce: a wider UNIT column is not a fault' \
	pass \
	./tools/enforce \
	$'.' \
	$'' \
	tools/enforce/main.go \
	$'"%-22s %14s %14s\\n", "UNIT"' \
	$'"%-24s %14s %14s\\n", "UNIT"'

run_case $'parity: a redirect to a different page is no longer a difference' \
	fail \
	./tools/parity \
	$'TestCompareNamesEachKindOfDifference' \
	$'--- FAIL: TestCompareNamesEachKindOfDifference' \
	tools/parity/main.go \
	$'ea.SHA256 != eb.SHA256 || ea.Location != eb.Location {' \
	$'ea.SHA256 != eb.SHA256 {'
run_case $'parity: a comparison over an empty capture passes' \
	fail \
	./tools/parity \
	$'TestACaptureOfNothingIsRefusedNotPassed' \
	$'--- FAIL: TestACaptureOfNothingIsRefusedNotPassed' \
	tools/parity/main.go \
	$'if ma.Count == 0 || mb.Count == 0 {' \
	$'if false {'
run_case $'parity: a planted fault may change the body more than once' \
	fail \
	./tools/parity \
	$'TestMutateRefusesZeroOrManyOccurrencesAndUnknownPaths' \
	$'--- FAIL: TestMutateRefusesZeroOrManyOccurrencesAndUnknownPaths' \
	tools/parity/main.go \
	$'n != 1 {' \
	$'n < 1 {'
run_case $'parity: the CSRF token is not scrubbed, so two captures of one console differ' \
	fail \
	./tools/parity \
	$'TestCapturingTheSameConsoleTwiceGivesTheSameBytes' \
	$'--- FAIL: TestCapturingTheSameConsoleTwiceGivesTheSameBytes' \
	tools/parity/main.go \
	$'name="csrf" value="[^"]*"`),' \
	$'name="csrfX" value="[^"]*"`),'
run_case $'parity: the crawl walks /logout and ends its own session' \
	fail \
	./tools/parity \
	$'TestTheCrawlIsBoundedPerFamilyAndAvoidsForbiddenRoutes' \
	$'--- FAIL: TestTheCrawlIsBoundedPerFamilyAndAvoidsForbiddenRoutes' \
	tools/parity/main.go \
	$'"/logout": true,' \
	$'"/logout": false,'
run_case $'parity: a reworded justification for a scrub is not a fault' \
	pass \
	./tools/parity \
	$'.' \
	$'' \
	tools/parity/main.go \
	$'"minted per session; carries no product meaning",' \
	$'"minted per session",'

run_case $'stack: three of four requested agents are connected without a word' \
	fail \
	./tools/stack \
	$'TestSelectionErrorsAreRefusedAndWriteNothing' \
	$'--- FAIL: TestSelectionErrorsAreRefusedAndWriteNothing' \
	tools/stack/main.go \
	$'if len(want) > 0 {' \
	$'if false {'
run_case $'stack: an emit that wrote no event reports success' \
	fail \
	./tools/stack \
	$'TestEmitThatMeasuredNothingFails' \
	$'--- FAIL: TestEmitThatMeasuredNothingFails' \
	tools/stack/main.go \
	$'if written == 0 {' \
	$'if false {'
run_case $'stack: a blocked event is no longer raised above info' \
	fail \
	./tools/stack \
	$'TestSeverityOfRaisesOnlyGovernanceEvents' \
	$'--- FAIL: TestSeverityOfRaisesOnlyGovernanceEvents' \
	tools/stack/main.go \
	$'case strings.Contains(kind, "blocked"), strings.Contains(kind, "suspend"):' \
	$'case strings.Contains(kind, "suspend"):'
run_case $'stack: the passport the tool built is not round-tripped through the contract' \
	fail \
	./tools/stack \
	$'TestADocumentTheContractWouldRefuseIsNeverWritten' \
	$'--- FAIL: TestADocumentTheContractWouldRefuseIsNeverWritten' \
	tools/stack/main.go \
	$'if _, err := passport.Parse(buf); err != nil {' \
	$'if _, err := passport.Parse(buf); false && err != nil {'
run_case $'stack: the supervisor is made its own parent' \
	fail \
	./tools/stack \
	$'TestConnectAllWritesOneValidPassportPerAnalyst' \
	$'--- FAIL: TestConnectAllWritesOneValidPassportPerAnalyst' \
	tools/stack/main.go \
	$'if j.Name != supervisor {' \
	$'if true {'
run_case $'stack: a reworded attestation note is not a fault' \
	pass \
	./tools/stack \
	$'.' \
	$'' \
	tools/stack/main.go \
	$'Idryx will read it as declared.' \
	$'Idryx will read it as declared by the installation.'

run_case $'idryxsource: agents are written in reverse name order' \
	fail \
	./tools/idryxsource \
	$'TestAgentsAreSortedByName' \
	$'--- FAIL: TestAgentsAreSortedByName' \
	tools/idryxsource/main.go \
	$'return roster[i].Name < roster[j].Name' \
	$'return roster[i].Name > roster[j].Name'
run_case $'idryxsource: an agent acts on behalf of itself, not its parent' \
	fail \
	./tools/idryxsource \
	$'TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	$'--- FAIL: TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	tools/idryxsource/main.go \
	$'e.OnBehalfOf = "agent://" + host + "/" + a.Parent' \
	$'e.OnBehalfOf = "agent://" + host + "/" + a.Name'
run_case $'idryxsource: the skills are written where the rights belong' \
	fail \
	./tools/idryxsource \
	$'TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	$'--- FAIL: TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	tools/idryxsource/main.go \
	$'append(make([]string, 0, len(a.Rights)), a.Rights...)' \
	$'append(make([]string, 0, len(a.Skills)), a.Skills...)'
run_case $'idryxsource: the hire date is passed on as a bare date, not RFC 3339' \
	fail \
	./tools/idryxsource \
	$'TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	$'--- FAIL: TestTheRosterIsWrittenAsAnIdryxAgentsSource' \
	tools/idryxsource/main.go \
	$'e.Created = t.UTC().Format(time.RFC3339)' \
	$'e.Created = t.UTC().Format("2006-01-02")'
run_case $'idryxsource: a reworded -host help text is not a fault' \
	pass \
	./tools/idryxsource \
	$'.' \
	$'' \
	tools/idryxsource/main.go \
	$'"the agent:// authority, matching -stack-host"' \
	$'"the agent:// authority (match -stack-host)"'

run_case $'recon: a resource line that does not add up is not reported' \
	fail \
	./tools/recon \
	$'TestAResourceOffByACentIsNamedAsAMismatch' \
	$'--- FAIL: TestAResourceOffByACentIsNamedAsAMismatch' \
	tools/recon/main.go \
	$'if v != byKey[k] {' \
	$'if v == byKey[k] {'
run_case $'recon: the gap to a SaaS invoice loses its sign' \
	fail \
	./tools/recon \
	$'TestEveryLicenceShowsItsGapToTheInvoice' \
	$'--- FAIL: TestEveryLicenceShowsItsGapToTheInvoice' \
	tools/recon/main.go \
	$'l.PerSeat, paid, bill, paid-bill)' \
	$'l.PerSeat, paid, bill, bill-paid)'
run_case $'recon: a commitment\'s monthly figure uses 720 hours' \
	fail \
	./tools/recon \
	$'TestEveryCommitmentShowsItsShareOfCommittableSpend' \
	$'--- FAIL: TestEveryCommitmentShowsItsShareOfCommittableSpend' \
	tools/recon/main.go \
	$'monthly := c.Hourly * 730' \
	$'monthly := c.Hourly * 720'
run_case $'recon: a wider vendor column is not a fault' \
	pass \
	./tools/recon \
	$'.' \
	$'' \
	tools/recon/main.go \
	$'"  %-12s %-16s %3d seats' \
	$'"  %-14s %-16s %3d seats'

run_case $'spiffe: a bare socket path is not treated as a unix socket' \
	fail \
	./internal/spiffe \
	$'TestABarePathIsAUnixSocket' \
	$'--- FAIL: TestABarePathIsAUnixSocket' \
	internal/spiffe/spiffe.go \
	$'socket = "unix://" + socket' \
	$'socket = "" + socket'
run_case $'spiffe: a rotated identity is remembered from startup, not re-read' \
	fail \
	./internal/spiffe \
	$'TestARotatedIdentityIsPickedUp' \
	$'--- FAIL: TestARotatedIdentityIsPickedUp' \
	internal/spiffe/spiffe.go \
	$'if svid, err := s.src.GetX509SVID(); err == nil && len(svid.Certificates) > 0 {' \
	$'if svid, err := s.src.GetX509SVID(); false && err == nil && len(svid.Certificates) > 0 {'
run_case $'spiffe: the certificate serial is written in decimal, not hex' \
	fail \
	./internal/spiffe \
	$'TestOpenReadsTheIssuedIdentity' \
	$'--- FAIL: TestOpenReadsTheIssuedIdentity' \
	internal/spiffe/spiffe.go \
	$'svid.Certificates[0].SerialNumber.Text(16))' \
	$'svid.Certificates[0].SerialNumber.Text(10))' \
	internal/spiffe/spiffe.go \
	$'svid.Certificates[0].SerialNumber.Text(16))' \
	$'svid.Certificates[0].SerialNumber.Text(10))'
run_case $'spiffe: a failed Open stops telling the operator where to look' \
	fail \
	./internal/spiffe \
	$'TestOpenFailsLoudlyWhenNothingIsListening' \
	$'--- FAIL: TestOpenFailsLoudlyWhenNothingIsListening' \
	internal/spiffe/spiffe.go \
	$'Either nothing is listening there' \
	$'Something went wrong there'
run_case $'spiffe: a longer first-SVID wait is not a fault' \
	pass \
	./internal/spiffe \
	$'.' \
	$'' \
	internal/spiffe/spiffe.go \
	$'context.WithTimeout(ctx, 20*time.Second)' \
	$'context.WithTimeout(ctx, 30*time.Second)'

# Invariant 72 (the local engine: a model the organisation hosts itself): where
# a local call goes, and what bounds a run whose price is 0. Each mutant undoes
# one piece and is caught by the test that holds it; three non-faults reword a
# refusal, change a timeout and reword the other refusal, which the gates must
# not mind.
run_case $'local engine: a gateway that does not front the OpenAI wire still lets a local call through' \
	fail \
	./internal/deliver \
	$'TestTheLocalEngineIsNeverSentDirectBehindAGatewaysBack' \
	$'want one wrapping ErrNoGatewayRoute' \
	internal/deliver/call.go \
	$'	case "local":\n		if g.OpenAIURL != "" {\n			return g.OpenAIURL, nil\n		}' \
	$'	case "local":\n		if true {\n			return g.OpenAIURL, nil\n		}'
run_case $'local engine: the call ignores the gateway and goes to the operator\'s server' \
	fail \
	./internal/deliver \
	$'TestACallToTheLocalEngineThroughTheOpenAIGatewayIsMetered' \
	$'was called directly' \
	internal/deliver/local.go \
	$'		if base != "" {\n			return base + OpenAICompletionsPath, true, nil\n		}' \
	$'		if false && base != "" {\n			return base + OpenAICompletionsPath, true, nil\n		}'
run_case $'local engine: the direct route is a vendor\'s host' \
	fail \
	./internal/deliver \
	$'TestNoVendorHostAppearsInTheLocalRoute' \
	$'OpenRouterDirectEndpoint' \
	internal/deliver/local.go \
	$'		return gw.ModelURL + "/chat/completions", false, nil' \
	$'		return OpenRouterDirectEndpoint, false, nil'
run_case $'local engine: a server that reports no usage is counted as using nothing' \
	fail \
	./internal/deliver \
	$'TestAServerThatReportsNoUsageIsCountedAtTheWorstCase' \
	$'prompt tokens, want the request' \
	internal/deliver/local.go \
	$'	return requestBytes, maxTok, true' \
	$'	return 0, 0, true'
run_case $'local engine: the tool loop does not count an unreported round' \
	fail \
	./tools/run \
	$'TestALocalServerThatReportsNoUsageIsCountedAtTheWorstCaseNotZero' \
	$'was counted as using nothing' \
	tools/run/loop.go \
	$'	if local {\n		// A server that reports no usage must not make the round free.' \
	$'	if false {\n		// A server that reports no usage must not make the round free.'
run_case $'local engine: an address with a password in it is accepted' \
	fail \
	./internal/deliver \
	$'TestNormalizeModelURL' \
	$'was accepted as' \
	internal/deliver/local.go \
	$'		if u.User != nil {' \
	$'		if false && u.User != nil {'
run_case $'local engine: an empty key is sent as a bearer token' \
	fail \
	./internal/deliver \
	$'TestACallToTheLocalEngineGoesToTheOperatorsServerWithNoKey' \
	$'Authorization = ' \
	internal/deliver/local.go \
	$'	if k := modelKey(); k != "" {' \
	$'	if k := modelKey(); true {'
run_case $'local engine: an unreachable server is reported without its URL' \
	fail \
	./internal/deliver \
	$'TestAnUnreachableServerIsOneLineNamingItsURL' \
	$'does not name the server' \
	internal/deliver/local.go \
	$'"%s at %s did not answer: %s", what, base, trim(reason, 160))' \
	$'"%s did not answer: %s", what, trim(reason, 160))'
run_case $'local engine: a local task loops one round, not six' \
	fail \
	./internal/deliver \
	$'TestTheLocalEngineLoopsAndSendsTheOpenAICatalogue' \
	$'LoopsFor(local)' \
	internal/deliver/estimate.go \
	$'	case "anthropic", "openrouter", "local":\n		return MaxToolRounds' \
	$'	case "anthropic", "openrouter":\n		return MaxToolRounds'
run_case $'local engine: it reads as unmetered, so the estimator waves it through' \
	fail \
	./internal/engines \
	$'TestTheLocalEngineIsKnownAndReadsAsMetered' \
	$'reads as unmetered' \
	internal/engines/engines.go \
	$'Family: SelfHosted, Metered: true,' \
	$'Family: SelfHosted, Metered: false,'
run_case $'local engine: a negative price is accepted' \
	fail \
	./internal/engines \
	$'TestConfigureLocalRefusesAPriceThatIsNotAPrice' \
	$'accepted' \
	internal/engines/local.go \
	$'v.p < 0 {' \
	$'v.p < -1e18 {'
run_case $'local engine: a run priced at zero starts with no token ceiling' \
	fail \
	./tools/run \
	$'TestALocalRunAtPriceZeroIsRefusedAtStartWithoutATokenCeiling' \
	$'started with no token ceiling' \
	tools/run/local.go \
	$'	if zero > 0 && gw.MaxRunTokens <= 0 {' \
	$'	if false && zero > 0 && gw.MaxRunTokens <= 0 {'
run_case $'local engine: the whole run\'s worst case is not checked against the token ceiling' \
	fail \
	./tools/run \
	$'TestTheWholeRunsWorstCaseOverTheTokenCeilingIsRefusedBeforeAnyCall' \
	$'want the whole-run token refusal' \
	tools/run/local.go \
	$'	if gw.MaxRunTokens > 0 && tokens > int64(gw.MaxRunTokens) {' \
	$'	if false && gw.MaxRunTokens > 0 && tokens > int64(gw.MaxRunTokens) {'
run_case $'local engine: a token reservation is never refused' \
	fail \
	./tools/run \
	$'TestTheTokenCeilingRefusesTheNextTaskOnceTheLastOneUsedIt' \
	$'want a refusal' \
	tools/run/live.go \
	$'	if r.tokensSpent+r.tokensReserved+worst > r.tokenCeiling {' \
	$'	if false {'
run_case $'local engine: settling does not book the tokens a task used' \
	fail \
	./tools/run \
	$'TestTheTokenCeilingRefusesTheNextTaskOnceTheLastOneUsedIt' \
	$'want 61000' \
	tools/run/live.go \
	$'	r.tokensSpent += actual' \
	$'	r.tokensSpent += 0 * actual'
run_case $'local engine: a refused token reservation keeps the money it took' \
	fail \
	./tools/run \
	$'TestTheTokenCeilingRefusesTheNextTaskOnceTheLastOneUsedIt' \
	$'a refusal reserves nothing' \
	tools/run/live.go \
	$'		run.settle(reserveMicros, 0)\n		return refusal{err}' \
	$'		return refusal{err}'
run_case $'local engine: the local round sends a different request than the openrouter one' \
	fail \
	./tools/run \
	$'TestBothOpenAIEnginesSendTheSameRequestShape' \
	$'the request bodies differ' \
	tools/run/loop.go \
	$'	body, err := openRouterRoundBody(model, messages, tools, maxTok)' \
	$'	if engine == engines.LocalID {\n		tools = nil\n	}\n	body, err := openRouterRoundBody(model, messages, tools, maxTok)'
run_case $'local engine: the bus says a vendor price was involved' \
	fail \
	./tools/run \
	$'TestThePriceBasisOfALocalCallIsLocalWhateverTheGatewaySaid' \
	$'want "local"' \
	tools/run/local.go \
	$'	if engine == engines.LocalID {\n		return "local"\n	}\n	return s.PriceBasis' \
	$'	return s.PriceBasis'
run_case $'local engine: execute is not handed the operator\'s server' \
	fail \
	./tools/run \
	$'TestALocalTaskRunsTheToolLoopAndIsChargedAtTheOperatorsPrice' \
	$'execute:' \
	tools/run/live.go \
	$'	gh.ModelURL = gw.ModelURL' \
	$'	_ = gw.ModelURL'
run_case $'local engine: a server that is not there is not noticed at start' \
	fail \
	./tools/run \
	$'TestAnUnreachableLocalServerStopsTheRunAtStartWithOneLine' \
	$'started' \
	tools/run/local.go \
	$'			return deliver.ProbeModelServer(context.Background(), gw.ModelURL)' \
	$'			_ = context.Background()\n			return nil'
run_case $'local engine: the dry run stops warning about tasks money cannot bound' \
	fail \
	./tools/run \
	$'TestTheDryRunSaysWhichLocalTasksMoneyCannotBound' \
	$'does not warn' \
	tools/run/main.go \
	$'	if localAtZero > 0 {' \
	$'	if false && localAtZero > 0 {'
run_case $'local engine: rewording the gateway refusal is not a fault' \
	pass \
	./internal/deliver \
	$'TestRouteForLocalFollowsTheOpenAIGatewayOrRefuses' \
	$'' \
	internal/deliver/call.go \
	$'so the call is refused rather than made directly to the local model server; set ' \
	$'so the call is refused and not made directly to the local model server; set '
run_case $'local engine: rewording the zero-price refusal is not a fault' \
	pass \
	./tools/run \
	$'TestALocalRunAtPriceZeroIsRefusedAtStartWithoutATokenCeiling' \
	$'' \
	tools/run/local.go \
	$'so money cannot bound "+' \
	$'so money can not bound "+'
run_case $'local engine: a longer round timeout is not a fault' \
	pass \
	./internal/deliver \
	$'TestACallToTheLocalEngineGoesToTheOperatorsServerWithNoKey' \
	$'' \
	internal/deliver/local.go \
	$'const LocalRoundTimeout = 5 * time.Minute' \
	$'const LocalRoundTimeout = 6 * time.Minute'
# Invariants 70 and 71: what a model is sent is what -prompt-data allows, and a
# pseudonym is stable, private and reversible. Each case plants the fault one
# of the new tests exists for, in the product, and requires that test to say
# so: a source of names dropped from the list the mask reads, the mask removed,
# typed text or a driver label sent, a service sent under aggregates, a past
# deliverable sent, the SQL tools offered, a tool run when asked for anyway, a
# tool result left unmasked, the model's tokens not put back into a tool call,
# or put back by splicing into its text, a draft saved in tokens, an answer
# handed back in tokens, a name given a new token each time, two names given
# one, a key anybody can read, a misspelling accepted by either binary, the
# mode missing from an event, a column nobody has decided about. The last case
# is two edits that are not faults (a longer token, a reworded stand-in), which
# the gates must not mind.
run_case $'prompt data: the invoice ids are left out of the list of names to mask' \
	fail \
	./internal/deliver \
	$'TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets' \
	$'(charges.invoice_id)' \
	internal/deliver/pseudonym.go \
	$'{kindInvoice, "charges", "invoice_id", ""}, {kindInvoice, "ai_calls", "invoice_id", ""},\n' \
	$''
run_case $'prompt data: a business unit a person named is left out of the list of names to mask' \
	fail \
	./internal/deliver \
	$'TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets' \
	$'(unit_rules.business_unit)' \
	internal/deliver/pseudonym.go \
	$', {kindTeam, "unit_rules", "business_unit", ""}' \
	$''
run_case $'prompt data: the packet is not masked at all' \
	fail \
	./internal/deliver \
	$'TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets' \
	$'(masked mode) leaks' \
	internal/deliver/packet.go \
	$'\t\tjoined = pol.maskStore(db, joined, []string{a.Name})\n' \
	$'\t\tjoined = strings.Clone(joined)\n'
run_case $'prompt data: a past deliverable body is sent under masked' \
	fail \
	./internal/deliver \
	$'TestFreeTextIsWithheldUnderMaskedAndAggregates' \
	$'sent free text' \
	internal/deliver/packet.go \
	$'\tif pol.Full() {\n\t\tb.WriteString(trimBytes(body, 600))' \
	$'\tif true {\n\t\tb.WriteString(trimBytes(body, 600))'
run_case $'prompt data: the drivers a forecast names are sent under masked' \
	fail \
	./internal/deliver \
	$'TestFreeTextIsWithheldUnderMaskedAndAggregates' \
	$'sent free text' \
	internal/deliver/packet.go \
	$'\t\t\tif i := strings.Index(basis, driversAppliedMarker); i >= 0 {' \
	$'\t\t\tif i := strings.Index(basis, driversAppliedMarker); i >= 0 && false {'
run_case $'prompt data: aggregates sends the service of an anomaly' \
	fail \
	./internal/deliver \
	$'TestAggregatesCarryNoRowLevelSections' \
	$'aggregates sent row-level text' \
	internal/deliver/packet.go \
	$'\tif !pol.Aggregates() {\n\t\tfmt.Fprintf(&b, "service:   %s\\n", an.Service)' \
	$'\tif true {\n\t\tfmt.Fprintf(&b, "service:   %s\\n", an.Service)'
run_case $'prompt data: the answer comes back from the shared caller still in tokens' \
	fail \
	./internal/deliver \
	$'TestCallHandsBackTheAnswerWithItsRealNames' \
	$'the caller must see the real name' \
	internal/deliver/call.go \
	$'\t\tres.Text = ActivePolicy().Reidentify(res.Text)\n' \
	$'\t\tres.Text = res.Text + ""\n'
run_case $'prompt data: a name gets a new token every time it is masked' \
	fail \
	./internal/deliver \
	$'TestTheSameNameIsTheSameTokenInEveryRoundAndEveryText' \
	$'instead of' \
	internal/deliver/pseudonym.go \
	$'\tif t, ok := p.fwd[value]; ok {\n\t\treturn t\n\t}\n' \
	$''
run_case $'prompt data: two names are given the same token' \
	fail \
	./internal/deliver \
	$'TestTokensNeverCollideEvenWhenThereAreMoreNamesThanFourHexDigitsHold' \
	$'were given the same token' \
	internal/deliver/pseudonym.go \
	$'\t\tif _, taken := p.rev[cand]; !taken {' \
	$'\t\tif true {'
run_case $'prompt data: a misspelling falls back to full' \
	fail \
	./internal/deliver \
	$'TestPromptDataIsAClosedVocabulary' \
	$'want a refusal' \
	internal/deliver/promptdata.go \
	$'\tcase PromptFull, PromptMasked, PromptAggregates:\n\t\treturn m, nil\n\t}' \
	$'\tcase PromptFull, PromptMasked, PromptAggregates:\n\t\treturn m, nil\n\tdefault:\n\t\treturn PromptFull, nil\n\t}'
run_case $'prompt data: the data directory the key creates is readable by others' \
	fail \
	./internal/deliver \
	$'TestADataDirectoryTheKeyCreatesIsPrivateAndAnExistingOneIsLeftAlone' \
	$'the data directory the key made' \
	internal/deliver/pseudonym.go \
	$'\tif err := os.MkdirAll(dir, 0o700); err != nil {' \
	$'\tif err := os.MkdirAll(dir, 0o755); err != nil {'
run_case $'prompt data: the key is no longer ignored by version control' \
	fail \
	./internal/deliver \
	$'TestThePseudonymKeyCannotBeCommitted' \
	$'has no "prompt-data.key" line' \
	.gitignore \
	$'events.ndjson\nprompt-data.key\n' \
	$'events.ndjson\n'
run_case $'prompt data: the key is made readable by others' \
	fail \
	./internal/deliver \
	$'TestTheKeyLivesInTheDataDirWithMode0600' \
	$'is mode' \
	internal/deliver/pseudonym.go \
	$'\tif err := tmp.Chmod(0o600); err != nil {' \
	$'\tif err := tmp.Chmod(0o644); err != nil {'
run_case $'prompt data: a key anybody can read is accepted' \
	fail \
	./internal/deliver \
	$'TestAKeyFileOthersCanReadIsRefused' \
	$'world-readable key file was accepted' \
	internal/deliver/pseudonym.go \
	$'\tif fi.Mode().Perm()&0o077 != 0 {' \
	$'\tif false {'
run_case $'prompt data: the typed goal is sent under masked' \
	fail \
	./internal/deliver \
	$'TestTheOperatorsGoalAndATaskGoalAreWithheldUnderMaskedAndAggregates' \
	$'sent the typed goal' \
	internal/deliver/prompt.go \
	$'\t\tif pol.Full() {\n\t\t\tfmt.Fprintf(&b, "What it asks for: %s\\n", t.Goal)' \
	$'\t\tif true {\n\t\t\tfmt.Fprintf(&b, "What it asks for: %s\\n", t.Goal)'
run_case $'prompt data: the prompt stops stating its mode' \
	fail \
	./internal/deliver \
	$'TestThePromptSaysWhichModeItWasBuiltUnder' \
	$'does not state its mode exactly once' \
	internal/deliver/prompt.go \
	$'\tb.WriteString(pol.ModeLine() + "\\n")\n' \
	$''
run_case $'prompt data: a mission somebody typed when hiring is sent under masked' \
	fail \
	./internal/deliver \
	$'TestOnlyTheRoleFamilysOwnBriefIsSentUnderMasked' \
	$'a hand-typed mission was sent' \
	internal/deliver/prompt.go \
	$'\treturn WithheldFreeText\n}\n\n// optionsBlockInstructions' \
	$'\treturn a.Mission\n}\n\n// optionsBlockInstructions'
run_case $'prompt data: the plan packet lists every analyst under aggregates' \
	fail \
	./internal/deliver \
	$'TestThePlanPacketLeaksNoIdentifierOrGoalUnderMaskedAndAggregates' \
	$'carries the token' \
	internal/deliver/plan_packet.go \
	$'\tif ActivePolicy().Aggregates() {\n\t\t// One line per analyst' \
	$'\tif false {\n\t\t// One line per analyst'
run_case $'prompt data: the operator goal is sent in the plan packet' \
	fail \
	./internal/deliver \
	$'TestThePlanPacketLeaksNoIdentifierOrGoalUnderMaskedAndAggregates' \
	$'the plan prompt leaks' \
	internal/deliver/plan_packet.go \
	$'\tif !pol.Full() {\n\t\tgoal = WithheldFreeText' \
	$'\tif false {\n\t\tgoal = WithheldFreeText'
run_case $'prompt data: full mode changes the text of a section' \
	fail \
	./internal/deliver \
	$'TestFullModeBuildsTheSamePacketItAlwaysDid' \
	$'no longer the one main built' \
	internal/deliver/packet.go \
	$'\t\t\tfmt.Fprintf(&b, "driver:    %s\\n", an.Driver)' \
	$'\t\t\tfmt.Fprintf(&b, "driver:   %s\\n", an.Driver)'
run_case $'prompt data: a column nobody has decided about is added to the schema' \
	fail \
	./internal/deliver \
	$'TestEveryTextColumnIsClassified' \
	$'plan_asks.signed_off_by' \
	internal/crew/crew.go \
	$'  outcome TEXT NOT NULL, reason TEXT, created TEXT);\nCREATE INDEX IF NOT EXISTS tasks_sprint' \
	$'  outcome TEXT NOT NULL, reason TEXT, created TEXT, signed_off_by TEXT);\nCREATE INDEX IF NOT EXISTS tasks_sprint'
run_case $'prompt data: the SQL tools are offered under masked' \
	fail \
	./tools/run \
	$'TestSQLToolsAreNotOfferedUnderMaskedOrAggregates' \
	$'offered charges_query' \
	internal/deliver/promptdata.go \
	$'\t\t"anomaly": true, "series": true,' \
	$'\t\t"charges_query": true, "ai_calls_query": true, "anomaly": true, "series": true,'
run_case $'prompt data: a tool the policy does not offer is run when asked for' \
	fail \
	./tools/run \
	$'TestSQLToolsAreNotOfferedUnderMaskedOrAggregates' \
	$'gave outcome' \
	tools/run/dispatch.go \
	$'\tif !pol.ToolOffered(def.Name) {' \
	$'\tif false {'
run_case $'prompt data: a tool result is not masked' \
	fail \
	./tools/run \
	$'TestNoRealIdentifierLeavesInAnyToolResult' \
	$'leaks identifier' \
	tools/run/dispatch.go \
	$'\t\t\tr.Text = pol.MaskText(r.Text, a.Name)\n' \
	$'\t\t\t_ = a\n'
run_case $'prompt data: aggregates names the person who closed a period' \
	fail \
	./tools/run \
	$'TestNoRealIdentifierLeavesInAnyToolResult' \
	$'which names a row-level thing' \
	tools/run/tools.go \
	$'\t\t\tif deliver.ActivePolicy().Aggregates() {\n\t\t\t\treturn fmt.Sprintf("FROZEN' \
	$'\t\t\tif false {\n\t\t\t\treturn fmt.Sprintf("FROZEN'
run_case $'prompt data: the model\'s tokens are not put back into a tool call' \
	fail \
	./tools/run \
	$'TestAMaskedToolResultIsTheFullResultWithItsNamesMasked' \
	$'is not the masked full result' \
	tools/run/dispatch.go \
	$'\targs = reidentifyArgs(pol, args)\n' \
	$'\t_ = reidentifyArgs(pol, args)\n'
run_case $'prompt data: a name is spliced into the text of a tool call' \
	fail \
	./tools/run \
	$'TestPuttingANameBackCannotWriteArgumentsOfItsOwn' \
	$'the name rewrote the call' \
	tools/run/dispatch.go \
	$'\tdec := json.NewDecoder(bytes.NewReader(args))\n\tdec.UseNumber()\n\tvar v any\n\tif err := dec.Decode(&v); err != nil {\n\t\treturn args\n\t}\n\tout, err := json.Marshal(mapStrings(v, pol.Reidentify))\n\tif err != nil {\n\t\treturn args\n\t}\n\treturn out\n' \
	$'\t_ = bytes.NewReader(args)\n\treturn json.RawMessage(pol.Reidentify(string(args)))\n'
run_case $'prompt data: the examples in the tool schemas are not masked' \
	fail \
	./tools/run \
	$'TestMaskingTheCatalogueOnlyChangesItsExamples' \
	$'the schema of' \
	tools/run/tools.go \
	$'\tt.Schema = maskSchema(pol, t.Schema).(map[string]any)\n' \
	$''
run_case $'prompt data: the draft is saved in tokens' \
	fail \
	./tools/run \
	$'TestWhatReachesTheModelOverTheWireLeaksNoIdentifierAndTheDraftComesBackNamed' \
	$'the saved draft was not re-identified' \
	tools/run/loop.go \
	$'\t\tres, err := anthropicToolLoop(ctx, db, roDB, e, sentPrompt, maxTok, gw, a, b)\n\t\tres.Text = deliver.ActivePolicy().Reidentify(res.Text)\n' \
	$'\t\tres, err := anthropicToolLoop(ctx, db, roDB, e, sentPrompt, maxTok, gw, a, b)\n'
run_case $'prompt data: a tool_call event stops carrying the mode' \
	fail \
	./tools/run \
	$'TestTheModeIsOnTheToolCallEventsAndTheCrewRanSummary' \
	$'a tool_call event says prompt_data=' \
	tools/run/bus.go \
	$'\t\t"prompt_data":   b.mode(),\n' \
	$''
run_case $'prompt data: the crew_ran summary stops carrying the mode' \
	fail \
	./tools/run \
	$'TestTheModeIsOnTheToolCallEventsAndTheCrewRanSummary' \
	$'crew_ran on the bus says prompt_data=' \
	tools/run/bus.go \
	$'\t\t"prompt_data":    b.mode(),\n' \
	$''
run_case $'prompt data: the runner starts on a misspelt setting' \
	fail \
	./tools/run \
	$'TestAMisspeltPromptDataFlagRefusesToStartTheRunner' \
	$'opened a store before refusing' \
	tools/run/main.go \
	$'\tif _, err := deliver.ConfigurePromptData(*promptData, *dir); err != nil {' \
	$'\tif _, err := deliver.ConfigurePromptData(*promptData, *dir); err != nil && false {'
run_case $'prompt data: the console starts on a misspelt setting' \
	fail \
	./cmd/costcrew \
	$'TestAMisspeltPromptDataFlagRefusesToStartTheConsole' \
	$'started the console' \
	cmd/costcrew/main.go \
	$'\tif _, err := deliver.ConfigurePromptData(*promptData, *dir); err != nil {' \
	$'\tif _, err := deliver.ConfigurePromptData(*promptData, *dir); err != nil && false {'
run_case $'prompt data: a longer token and a reworded stand-in are not faults' \
	pass \
	./internal/deliver \
	$'TestATokenIsReadableAndShaped|TestTokensNeverCollideEvenWhenThereAreMoreNamesThanFourHexDigitsHold|TestFreeTextIsWithheldUnderMaskedAndAggregates|TestReidentifyBringsBackExactlyTheNamesThatWereMasked' \
	$'' \
	internal/deliver/pseudonym.go \
	$'\tfor n := 4; n <= len(digest); n++ {' \
	$'\tfor n := 6; n <= len(digest); n++ {' \
	internal/deliver/promptdata.go \
	$'[withheld: free text is not sent to the model' \
	$'[withheld: typed text is not sent to the model'

run_case $'reasoning tokens: the tool loop reads completion_tokens alone' \
	fail \
	./tools/run \
	$'TestTheTokenCeilingCountsAThinkingModelsReasoning' \
	$'the token ceiling counted 73 tokens' \
	tools/run/loop.go \
	$'inTok, outTok := out.Usage.PromptTokens, out.Usage.OutputTokens()\n' \
	$'inTok, outTok := out.Usage.PromptTokens, out.Usage.CompletionTokens\n'
run_case $'reasoning tokens: the shared rule ignores total_tokens' \
	fail \
	./internal/deliver \
	$'TestALocalCallCountsAThinkingModelsReasoningAsOutput|TestAnOpenRouterCallCountsAThinkingModelsReasoningAsOutput' \
	$'counted 14 in and 59 out, want 14 and 619' \
	internal/deliver/openai_usage.go \
	$'beyondPrompt > out {' \
	$'false && beyondPrompt > out {'
run_case $'reasoning tokens: a total smaller than its parts is believed' \
	fail \
	./internal/deliver \
	$'TestALocalCallCountsAThinkingModelsReasoningAsOutput|TestOpenAIUsageOutputTokens' \
	$'counted 14 in and 6 out, want 14 and 59' \
	internal/deliver/openai_usage.go \
	$'beyondPrompt > out {' \
	$'beyondPrompt > out || true {'
run_case $'reasoning tokens: the rule written as one max is not a fault' \
	pass \
	./internal/deliver \
	$'TestALocalCallCountsAThinkingModelsReasoningAsOutput|TestAnOpenRouterCallCountsAThinkingModelsReasoningAsOutput|TestOpenAIUsageOutputTokens' \
	$'' \
	internal/deliver/openai_usage.go \
	$'\tif beyondPrompt := u.TotalTokens - u.PromptTokens; beyondPrompt > out {\n\t\treturn beyondPrompt\n\t}\n\treturn out\n' \
	$'\treturn max(out, u.TotalTokens-u.PromptTokens)\n'

echo
if [ -n "$(git status --porcelain)" ]; then
	printf 'the tree is not clean after the run, so a mutation was left behind.\n'
	printf 'this is a failure of this script, not of any gate:\n'
	git status --porcelain | sed 's/^/    /'
	failures=$((failures + 1))
fi

printf 'teeth: %d passed, %d failed\n' "$((cases - failures))" "$failures"
[ "$failures" -eq 0 ]
