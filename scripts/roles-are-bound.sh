#!/usr/bin/env bash
# Checks that internal/crew/roles.yaml is bound to the code and to the
# roster, both ways: B1A-SPEC.md section 3.
#
# WHAT IT HOLDS
#
#   1. every class named in code (a "// class:X" tag, grep'd rather than
#      parsed as Go) exists in roles.yaml, and every class in roles.yaml is
#      owned by exactly one link.
#   2. every roster name (dumped by internal/crew's TestRosterForTheRolesGate,
#      because the roster is Go source built from a loop plus literals, and
#      re-deriving that in shell would be a second, driftable copy of
#      world.buildCrew) matches exactly one role entry, and every role entry
#      matches at least one roster name. A family for a role nobody has is
#      dead text.
#   3. every decides_alone class of a role is within that role's rights: a
#      table BELOW says which right each class needs (only classes that
#      actually appear in some role's decides_alone list have an entry), and
#      one representative roster member per family stands in for the whole
#      family, because every member of a multi-desk family shares the same
#      skills and therefore the same rights (checked once, structurally, by
#      TestRosterForTheRolesGate always asking RightsFor for "active": a
#      Suspended or Restricted member's ACTUAL rights are not what this
#      property is about).
#   4. the supervisor's hands_to_owner is exactly the set of classes owned by
#      "owner", plus the two named conditions
#      (hands_to_owner_conditions in the file).
#
# WHY A LINE-ORIENTED READER RATHER THAN A YAML PARSER
#
# This machine has no YAML parser on PATH outside Go's own module (jq reads
# JSON, not YAML; python3 has no yaml module installed), and installing one
# is not this gate's decision to make on its own (see the repo's spending
# rule). roles.yaml is authored with exactly one convention because of that:
# every scalar is a single double-quoted line, and every list the code below
# reads (matches, decides_alone, hands_up, hands_to_owner,
# hands_to_owner_conditions) is YAML flow style, "[a, b, c]", on one line. A
# block scalar or a block list would make a line-oriented reader guess where
# a field ends, so roles.yaml has none; see its own header comment.
#
# WHAT THIS DOES NOT DO
#
# It does not check that a class's "up_to" names a real threshold (that is
# internal/crew's mustLoadRoles, which panics at package-init time, so a typo
# there breaks `go test ./...` before this script would ever get a turn), and
# it does not check the prose fields (mission, reads, owes, quality_bar, the
# *_text fields) say anything true: nothing mechanical can.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

# Overridable so scripts/gates-have-teeth.sh can plant "the file is gone"
# without touching the real embed (which every other package in the module
# also builds against) or deleting a tracked file this script itself would
# then have to restore. Nothing else should ever need this.
ROLES="${ROLES_YAML:-internal/crew/roles.yaml}"

if [ ! -f "$ROLES" ]; then
	echo "measured nothing: $ROLES does not exist, which is not a pass." >&2
	exit 1
fi

# jq reads the roster's own -json output (property 2, below); see that
# section's own comment for why -json replaced raw -v text.
if ! command -v jq >/dev/null 2>&1; then
	echo "measured nothing: jq is not on PATH, and property 2 (the roster) is" >&2
	echo "read from JSON this script cannot parse without it." >&2
	exit 1
fi

fail=0

# --------------------------------------------------------- extract roles.yaml

# id<TAB>owner, one line per class.
classes_owners="$(awk '
	/^classes:/   { sect="classes"; next }
	/^roles:/     { sect="roles"; next }
	sect=="classes" && /^  - id: /     { id=$0; sub(/^  - id: "/,"",id); sub(/"$/,"",id) }
	sect=="classes" && /^    owner: /  { o=$0;  sub(/^    owner: "/,"",o); sub(/"$/,"",o); print id"\t"o }
' "$ROLES")"

# family<TAB>csv-of-matched-roster-names, one line per role.
family_matches="$(awk '
	/^roles:/ { sect="roles"; next }
	sect=="roles" && /^  - family: / { fam=$0; sub(/^  - family: "/,"",fam); sub(/"$/,"",fam) }
	sect=="roles" && /^    matches: / {
		m=$0; sub(/^    matches: \[/,"",m); sub(/\]$/,"",m)
		gsub(/"/,"",m); gsub(/, /,",",m); print fam"\t"m
	}
' "$ROLES")"

# family<TAB>csv-of-decides_alone-class-ids, one line per role (may be empty).
decides_alone_by_family="$(awk '
	/^roles:/ { sect="roles"; next }
	sect=="roles" && /^  - family: / { fam=$0; sub(/^  - family: "/,"",fam); sub(/"$/,"",fam) }
	sect=="roles" && /^    decides_alone: / {
		d=$0; sub(/^    decides_alone: \[/,"",d); sub(/\]$/,"",d)
		gsub(/"/,"",d); gsub(/, /,",",d); print fam"\t"d
	}
' "$ROLES")"

# The supervisor's own hands_to_owner and hands_to_owner_conditions, each a
# csv on one line (the "insup" state machine stops at the NEXT "- family:",
# whichever role that turns out to be, so this does not assume the
# supervisor is the last entry in roles:).
sup_hands_to_owner="$(awk '
	/^roles:/ { sect="roles"; next }
	sect=="roles" && /^  - family: "supervisor"/ { insup=1; next }
	sect=="roles" && insup && /^  - family: / { insup=0 }
	sect=="roles" && insup && /^    hands_to_owner: / {
		h=$0; sub(/^    hands_to_owner: \[/,"",h); sub(/\]$/,"",h)
		gsub(/"/,"",h); gsub(/, /,",",h); print h
	}
' "$ROLES")"

sup_conditions="$(awk '
	/^roles:/ { sect="roles"; next }
	sect=="roles" && /^  - family: "supervisor"/ { insup=1; next }
	sect=="roles" && insup && /^  - family: / { insup=0 }
	sect=="roles" && insup && /^    hands_to_owner_conditions: / {
		c=$0; sub(/^    hands_to_owner_conditions: \[/,"",c); sub(/\]$/,"",c)
		gsub(/"/,"",c); gsub(/, /,",",c); print c
	}
' "$ROLES")"

n_classes=$(printf '%s\n' "$classes_owners" | grep -c . || true)
n_roles=$(printf '%s\n' "$family_matches" | grep -c . || true)

# ----------------------------------------------- property 1: classes, code side

code_classes="$(grep -rohE 'class:[A-Za-z][A-Za-z0-9._*-]*' internal/ tools/ 2>/dev/null \
	| sed 's/^class://' | sort -u)"
yaml_class_ids="$(printf '%s\n' "$classes_owners" | cut -f1 | sort -u)"

while IFS= read -r c; do
	[ -z "$c" ] && continue
	if ! printf '%s\n' "$yaml_class_ids" | grep -qxF "$c"; then
		printf 'MISSING CLASS      %s is named in code (a "// class:" tag) but %s does not define it\n' "$c" "$ROLES"
		fail=$((fail + 1))
	fi
done <<<"$code_classes"

# --------------------------------------------- property 1: classes, one owner

dup_ids="$(printf '%s\n' "$classes_owners" | cut -f1 | sort | uniq -d)"
while IFS= read -r id; do
	[ -z "$id" ] && continue
	owners="$(printf '%s\n' "$classes_owners" | awk -F'\t' -v id="$id" '$1==id{print $2}' | sort -u | paste -sd, -)"
	printf 'MULTI-OWNED CLASS   %s is owned by more than one link: %s\n' "$id" "$owners"
	fail=$((fail + 1))
done <<<"$dup_ids"

# --------------------------------------------------------- property 2: roster
#
# @measured 2026-09-03: CI run 33756844013 on PR #47 failed TestRolesAreBound
# with "DEAD ROLE commitment-analyst (matches: commitments) matches no
# roster name" while every one of the 39 roster names present passed the
# FORWARD check; the identical commit's re-run passed clean. roles.yaml was
# not touched by that PR, and 30 local loops of this script alone, 25 more
# with a concurrent `go test ./...` warming the shared cache, and 20 more
# forcing a fresh (uncached) nested run under heavy concurrent load all came
# back clean -- the mechanism was not reproduced locally, macOS rather than
# the CI runner's Linux being the likeliest reason why not.
#
# Two changes follow from that, neither depending on having pinned the exact
# mechanism:
#
#   -count=1 forces a real execution every time. Without it, this exact
#   (-run, -v) pair is itself one of go test's cacheable flag combinations
#   (`go help test`: -run and -v both are), so a second call against the
#   same test binary can replay a PRIOR call's stdout byte for byte instead
#   of running anything -- and go test ./... itself never passes -count=1 to
#   the ./... it builds, so within one CI job this exact nested invocation,
#   unlike this script's own outer TestRolesAreBound, was never actually
#   exempt from that replay.
#
#   -json in place of raw -v text turns each log line into its own
#   self-delimited object (one JSON value per `Action:"output"` event)
#   rather than a line a merged stdout+stderr stream, a concurrent writer,
#   or a scheduling hiccup could reshape before a hand-rolled `grep -oE`
#   ever sees it. `fromjson? // empty` sanitises the raw stream first, so a
#   stray non-JSON line (a build failure on stderr, folded in by 2>&1) is
#   dropped rather than aborting the whole parse.
roster_raw="$(go test ./internal/crew -run '^TestRosterForTheRolesGate$' -v -count=1 -json 2>&1)"
roster_events="$(printf '%s\n' "$roster_raw" | jq -R -c 'fromjson? // empty' 2>/dev/null)"
roster_pass_events=$(printf '%s\n' "$roster_events" | jq -r 'select(.Action=="pass" and .Test=="TestRosterForTheRolesGate") | .Action' 2>/dev/null | grep -c . || true)
if [ "$roster_pass_events" -ne 1 ]; then
	printf 'ROSTER UNREADABLE   TestRosterForTheRolesGate did not pass, so this gate has nothing to check names against:\n'
	printf '%s\n' "$roster_raw" | tail -20
	fail=$((fail + 1))
fi
roster_text="$(printf '%s\n' "$roster_events" | jq -r 'select(.Action=="output" and .Test=="TestRosterForTheRolesGate") | .Output' 2>/dev/null)"
roster_names="$(printf '%s\n' "$roster_text" | grep -oE 'ROSTER \S+' | awk '{print $2}' | sort -u)"
roster_rights="$(printf '%s\n' "$roster_text" | grep -oE 'ROSTER .*')"
n_roster=$(printf '%s\n' "$roster_names" | grep -c . || true)

# forward: every roster name matches exactly one family
while IFS= read -r name; do
	[ -z "$name" ] && continue
	hits=0
	while IFS=$'\t' read -r fam names; do
		[ -z "$fam" ] && continue
		case ",$names," in
		*",$name,"*) hits=$((hits + 1)) ;;
		esac
	done <<<"$family_matches"
	case "$hits" in
	0) printf 'UNMATCHED ROSTER    %s matches no role family\n' "$name"; fail=$((fail + 1)) ;;
	1) ;;
	*) printf 'DOUBLE-MATCHED ROSTER  %s matches %d role families\n' "$name" "$hits"; fail=$((fail + 1)) ;;
	esac
done <<<"$roster_names"

# reverse: every family matches at least one roster name
while IFS=$'\t' read -r fam names; do
	[ -z "$fam" ] && continue
	found=0
	IFS=',' read -ra arr <<<"$names"
	for n in "${arr[@]}"; do
		if printf '%s\n' "$roster_names" | grep -qxF "$n"; then
			found=1
		fi
	done
	if [ "$found" -eq 0 ]; then
		printf 'DEAD ROLE           %s (matches: %s) matches no roster name\n' "$fam" "$names"
		fail=$((fail + 1))
	fi
done <<<"$family_matches"

# ---------------------------------------- property 3: decides_alone <= rights
#
# Which right a decides_alone class needs, as a function rather than an
# associative array: this machine's /bin/bash is 3.2 (macOS ships nothing
# newer, for licensing reasons that are not this script's to work around),
# and 3.2 has no associative arrays at all -- `declare -A` refuses with
# "invalid option", and the array literal below it silently falls back to an
# INDEXED array, which then tries to evaluate "anomaly.explain" as an
# arithmetic expression and dies on the dot. Grouped by right rather than by
# class, which reads at least as clearly as a table would have. Only classes
# that appear in SOME role's decides_alone list are listed: the rest are
# never checked by this property, because nobody decides them alone.
needs_right() {
	case "$1" in
	anomaly.explain | anomaly.dismiss | driver.one-time | task.block | \
		commentary.variance | commentary.showback | kpi.refuse | \
		sprint.plan | sprint.close | task.assign | task.return | task.accept | \
		option.select | anomaly.accept | driver.recurring | data.halt)
		echo figures-read ;;
	recommendation.rightsizing | recommendation.renewal)
		echo propose-only ;;
	forecast.project | forecast.freeze | recommendation.commitment)
		echo budgets-read ;;
	explainer.publish | escalation.request)
		echo channel-post ;;
	esac
}

while IFS=$'\t' read -r fam classes; do
	[ -z "$fam" ] && continue
	[ -z "$classes" ] && continue
	names="$(printf '%s\n' "$family_matches" | awk -F'\t' -v f="$fam" '$1==f{print $2}')"
	rep="${names%%,*}"
	if [ -z "$rep" ]; then
		printf 'NO REPRESENTATIVE   %s decides classes alone but matches no roster name to check rights against\n' "$fam"
		fail=$((fail + 1))
		continue
	fi
	rep_rights="$(printf '%s\n' "$roster_rights" | awk -v r="$rep" '$2==r{print $4}')"
	IFS=',' read -ra classarr <<<"$classes"
	for c in "${classarr[@]}"; do
		need="$(needs_right "$c")"
		[ -z "$need" ] && continue
		if ! printf '%s\n' "$rep_rights" | tr ',' '\n' | grep -qxF "$need"; then
			printf 'RIGHTS GAP          %s decides %s alone (via %s) but holds no %s; rights are: %s\n' \
				"$fam" "$c" "$rep" "$need" "$rep_rights"
			fail=$((fail + 1))
		fi
	done
done <<<"$decides_alone_by_family"

# --------------------------------------- property 4: supervisor hands to owner

owner_classes="$(printf '%s\n' "$classes_owners" | awk -F'\t' '$2=="owner"{print $1}' | sort -u)"
sup_list="$(printf '%s\n' "$sup_hands_to_owner" | tr ',' '\n' | sort -u | grep -v '^$' || true)"

missing="$(comm -23 <(printf '%s\n' "$owner_classes") <(printf '%s\n' "$sup_list") || true)"
extra="$(comm -13 <(printf '%s\n' "$owner_classes") <(printf '%s\n' "$sup_list") || true)"
while IFS= read -r c; do
	[ -z "$c" ] && continue
	printf 'HANDS TO OWNER GAP     the owner owns %s, and the supervisor does not hand it up\n' "$c"
	fail=$((fail + 1))
done <<<"$missing"
while IFS= read -r c; do
	[ -z "$c" ] && continue
	printf 'HANDS TO OWNER EXTRA   the supervisor hands up %s, which the owner does not own\n' "$c"
	fail=$((fail + 1))
done <<<"$extra"

n_conditions=$(printf '%s\n' "$sup_conditions" | tr ',' '\n' | grep -c . || true)
if [ "$n_conditions" -ne 2 ]; then
	printf 'CONDITIONS COUNT       hands_to_owner_conditions has %d entries, want 2 (a lasting halt, a disagreement on the same evidence)\n' "$n_conditions"
	fail=$((fail + 1))
fi

echo
if [ "$n_classes" -eq 0 ] || [ "$n_roles" -eq 0 ]; then
	echo "measured nothing: $ROLES parsed to 0 classes or 0 roles, which is a" >&2
	echo "failure of this script's own reading, not a clean bill of health." >&2
	exit 1
fi
printf 'roles: %d classes, %d roles, %d roster names, %d broken\n' "$n_classes" "$n_roles" "$n_roster" "$fail"

# @measured 2026-09-03: the CI incident this section answers (see property
# 2's own comment) printed one DEAD ROLE line and the summary above and
# nothing else, so the next reader had no way to tell whether "commitments"
# was ever actually read. Whenever anything is broken, whichever property
# caught it, print what this run actually saw: every roster name it read,
# and the nested go test's own raw output, so a failure that cannot be
# reproduced by hand still carries its own evidence.
if [ "$fail" -ne 0 ]; then
	echo
	echo "roster names read ($n_roster):"
	if [ "$n_roster" -eq 0 ]; then
		echo "  (none)"
	else
		printf '%s\n' "$roster_names" | sed 's/^/  ROSTER NAME READ  /'
	fi
	echo
	echo "nested go test output (-json), first 60 lines:"
	printf '%s\n' "$roster_raw" | head -60 | sed 's/^/  /'
fi

[ "$fail" -eq 0 ]
