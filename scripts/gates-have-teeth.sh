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
	if printf '%s' "$out" | grep -q 'no tests to run'; then
		printf 'MEASURED NOTHING\n%s' "$out"
		return 3
	fi
	printf '%s' "$out"
	return $rc
}

# run_case <name> <expect: fail|pass> <pkg> <pattern> <needle> <file old new>...
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
	if [ "$expect" = fail ] && [ "$rc" -ne 0 ] && [ -n "$needle" ] &&
		! printf '%s' "$out" | grep -qF -- "$needle"; then
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
		printf '%s\n' "$out" | head -4 | sed 's/^/           /'
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
run_case $'rights vocabulary: an explanation for a right nothing grants' \
	fail \
	./internal/web \
	$'TestNoExplanationOutlivesItsRight' \
	$'can no longer' \
	internal/web/agent360.go \
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

# A task somebody stopped stays stopped. crew.TaskFilter{OpenOnly} includes
# blocked, which is right for a board and wrong for a thing that does the work.
run_case 'work done past a reason a person recorded' caught ./tools/run \
	'TestABlockedTaskIsNotWorkedAround' \
	'picked up anyway' \
	tools/run/main.go \
	'if t.State == "blocked" {' \
	'if t.State == "no-such-state" {'

# The model must be asked for an answer rather than for reasoning. Four tasks on
# a full run spent their whole token budget thinking, reached max_tokens with no
# text, and blocked -- billed in full for nothing a person could read.
run_case 'the model is left free to think instead of answering' caught ./tools/run \
	'TestAnthropicIsAskedForAnAnswerRatherThanReasoning' \
	'want disabled' \
	tools/run/live.go \
	'"thinking":   map[string]any{"type": "disabled"},' \
	'"thinking":   map[string]any{"type": "enabled"},'

# Provenance. A live deliverable and a generated one land in the same table with
# the same author and the same state, and for one full run 63 real ones sat
# indistinguishable among 342. Two faults can bring that back: the writer going
# quiet about what it wrote, and the page going quiet about what it was told.
run_case 'a deliverable that does not say a model wrote it' caught ./tools/run \
	'TestARunnerDeliverableIsMarkedLive' \
	'indistinguishable' \
	tools/run/live.go \
	"datetime('now'), 'live')" \
	"datetime('now'), 'fixture')"

run_case 'a marker no page displays' caught ./internal/web \
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
	internal/web/agent360.go \
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

echo
if [ -n "$(git status --porcelain)" ]; then
	printf 'the tree is not clean after the run, so a mutation was left behind.\n'
	printf 'this is a failure of this script, not of any gate:\n'
	git status --porcelain | sed 's/^/    /'
	failures=$((failures + 1))
fi

printf 'teeth: %d passed, %d failed\n' "$((cases - failures))" "$failures"
[ "$failures" -eq 0 ]
