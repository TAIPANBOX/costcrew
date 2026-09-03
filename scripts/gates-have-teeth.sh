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
	"datetime('now'), 'live')" \
	"datetime('now'), 'fixture')"

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
	$'if an.Driver != "" && !hideDriver {' \
	$'if an.Driver != "" {'

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

echo
if [ -n "$(git status --porcelain)" ]; then
	printf 'the tree is not clean after the run, so a mutation was left behind.\n'
	printf 'this is a failure of this script, not of any gate:\n'
	git status --porcelain | sed 's/^/    /'
	failures=$((failures + 1))
fi

printf 'teeth: %d passed, %d failed\n' "$((cases - failures))" "$failures"
[ "$failures" -eq 0 ]
