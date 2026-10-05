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
#   5. every threshold carries a provenance marker from the closed vocabulary:
#      `@claude`, `@decided YYYY-MM-DD` or `@measured <how> YYYY-MM-DD`. A
#      threshold with none, or with one outside the vocabulary (the owner's
#      name as a marker, a bare "decided"), is refused. internal/crew's
#      ValidProvenance is the fuller check (it also rejects an impossible
#      calendar date); this one reads the shape from the shell so a person
#      running the script sees it without building the package.
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

# name<TAB>provenance, one line per threshold; the provenance is empty when the
# threshold has no provenance line at all.
threshold_provenance="$(awk '
	function flush() { if (name != "" && !have) print name "\t"; name=""; have=0 }
	/^[a-z_]+:/ { if (sect == "t") flush(); sect = ($0 ~ /^thresholds:/) ? "t" : ""; next }
	sect=="t" && /^  - name: / { flush(); name=$0; sub(/^  - name: "/,"",name); sub(/"$/,"",name); next }
	sect=="t" && /^    provenance: / {
		p=$0; sub(/^    provenance: "/,"",p); sub(/"$/,"",p); print name "\t" p; have=1
	}
	END { if (sect == "t") flush() }
' "$ROLES")"

n_classes=$(printf '%s\n' "$classes_owners" | grep -c . || true)
n_roles=$(printf '%s\n' "$family_matches" | grep -c . || true)
n_thresholds=$(printf '%s\n' "$threshold_provenance" | grep -c . || true)

# A NOTE ON EVERY `grep -q` BELOW: it reads a here-string, never `printf | grep -q`.
#
# This script runs under `set -o pipefail`, and `grep -q` leaves the moment it
# has matched. If the writer is cut off by that, the pipeline reports failure
# for a line that WAS there and the check reads it as "absent": a false
# MISSING CLASS, DEAD ROLE or UNRECOGNISED PROVENANCE. The same shape was
# measured misjudging scripts/gates-have-teeth.sh's own needle search on
# 2026-10-05 (see run_case there); it has not been seen failing this script on
# its own (0 of 60 runs under load), so this is the same hazard closed here
# before it is met, not a fix for a failure observed here. A here-string has no
# writer process to cut off.

# ----------------------------------------------- property 1: classes, code side

code_classes="$(grep -rohE 'class:[A-Za-z][A-Za-z0-9._*-]*' internal/ tools/ 2>/dev/null \
	| sed 's/^class://' | sort -u)"
yaml_class_ids="$(printf '%s\n' "$classes_owners" | cut -f1 | sort -u)"

while IFS= read -r c; do
	[ -z "$c" ] && continue
	if ! grep -qxF "$c" <<<"$yaml_class_ids"; then
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
		if grep -qxF "$n" <<<"$roster_names"; then
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
		if ! grep -qxF "$need" < <(tr ',' '\n' <<<"$rep_rights"); then
			printf 'RIGHTS GAP          %s decides %s alone (via %s) but holds no %s; rights are: %s\n' \
				"$fam" "$c" "$rep" "$need" "$rep_rights"
			fail=$((fail + 1))
		fi
	done
done <<<"$decides_alone_by_family"

# ------------------------------- property 5: every analyst family has its lists
#
# A family's decides_alone and hands_up are the CLOSED class lists the options
# block, the card and the prompt packet all read. An empty one is not "none":
# it is a family whose job description says what it does in prose and leaves
# the machine nothing to check an option against, which is how eight families
# came to carry an empty decides_alone and five an empty hands_up with only
# prose beside them. `@decided 2026-10-04`: the lists are written from the
# prose, and an empty one is refused unless the family carries an explicit,
# reasoned exemption beside it:
#
#     decides_alone_exempt: "why this family decides nothing alone"
#     hands_up_exempt:      "why this family hands nothing up"
#
# The exemption is for a family whose job truly has nothing to list (a retired
# queue, an onboarding role, a report that is only text). It is refused when it
# is too short to be a reason, and refused when the list beside it is not empty
# (an exemption left behind after the list was written is a stale claim).
#
# Only analyst-link families are held to this. The supervisor's own authority is
# its decides_alone plus hands_to_owner (property 4), and its hands_up is empty
# by design: there is nobody between it and the owner.
#
# fam, link, decides_alone, decides_alone_exempt, hands_up, hands_up_exempt, one
# line per role, separated by the ASCII unit separator and not a tab: `read`
# folds a run of tab delimiters into one, which would slide every field after
# an empty one (an empty list is exactly the case this property is about).
family_lists="$(awk '
	function flush() {
		if (fam != "") print fam "\037" link "\037" da "\037" dax "\037" hu "\037" hux
		fam=""; link=""; da=""; dax=""; hu=""; hux=""
	}
	/^roles:/ { sect="roles"; next }
	/^[a-z_]+:/ { if (sect=="roles") flush(); sect=""; next }
	sect=="roles" && /^  - family: / { flush(); fam=$0; sub(/^  - family: "/,"",fam); sub(/"$/,"",fam); next }
	sect=="roles" && /^    link: /                 { link=$0; sub(/^    link: "/,"",link); sub(/"$/,"",link); next }
	sect=="roles" && /^    decides_alone: /        { da=$0; sub(/^    decides_alone: \[/,"",da); sub(/\]$/,"",da); gsub(/"/,"",da); gsub(/, /,",",da); next }
	sect=="roles" && /^    decides_alone_exempt: / { dax=$0; sub(/^    decides_alone_exempt: "/,"",dax); sub(/"$/,"",dax); next }
	sect=="roles" && /^    hands_up: /             { hu=$0; sub(/^    hands_up: \[/,"",hu); sub(/\]$/,"",hu); gsub(/"/,"",hu); gsub(/, /,",",hu); next }
	sect=="roles" && /^    hands_up_exempt: /      { hux=$0; sub(/^    hands_up_exempt: "/,"",hux); sub(/"$/,"",hux); next }
	END { if (sect=="roles") flush() }
' "$ROLES")"

min_reason=40
while IFS=$'\037' read -r fam link da dax hu hux; do
	[ -z "$fam" ] && continue
	[ "$link" = "analyst" ] || continue
	for pair in "decides_alone|$da|$dax" "hands_up|$hu|$hux"; do
		IFS='|' read -r field list exempt <<<"$pair"
		if [ -z "$list" ] && [ -z "$exempt" ]; then
			printf 'EMPTY LIST          %s has an empty %s and no %s_exempt reason; write the list from its prose or give the exemption\n' \
				"$fam" "$field" "$field"
			fail=$((fail + 1))
		elif [ -z "$list" ] && [ "${#exempt}" -lt "$min_reason" ]; then
			printf 'THIN EXEMPTION      %s: %s_exempt is %d characters, under the %d that make a reason\n' \
				"$fam" "$field" "${#exempt}" "$min_reason"
			fail=$((fail + 1))
		elif [ -n "$list" ] && [ -n "$exempt" ]; then
			printf 'STALE EXEMPTION     %s lists %s and also carries %s_exempt; take the exemption out\n' \
				"$fam" "$field" "$field"
			fail=$((fail + 1))
		fi
	done
done <<<"$family_lists"

# An analyst-link family decides alone only a class the ANALYST link owns: a
# class owned by the supervisor or the owner is by definition not its to decide.
while IFS=$'\037' read -r fam link da dax hu hux; do
	[ -z "$fam" ] && continue
	[ "$link" = "analyst" ] || continue
	[ -z "$da" ] && continue
	IFS=',' read -ra classarr <<<"$da"
	for c in "${classarr[@]}"; do
		own="$(printf '%s\n' "$classes_owners" | awk -F'\t' -v id="$c" '$1==id{print $2; exit}')"
		if [ "$own" != "analyst" ]; then
			printf 'NOT THE ANALYST'"'"'S    %s decides %s alone, but that class is owned by %s\n' "$fam" "$c" "${own:-nobody (not a class)}"
			fail=$((fail + 1))
		fi
	done
done <<<"$family_lists"

# ----------------------------------------- property 6: the never list is bound
#
# never_bound pairs a never: entry with the test that holds it. Only a clause
# this repository can actually enforce carries a pair ("act on a task somebody
# blocked": the runner takes no blocked task); the rest of the list is the
# prompt's wording and the class ownership of purchase, infra.change and
# vendor.negotiate, and is not claimed here. A pair is refused when its verb is
# not in never: (a binding for a clause that was taken out) or its test does not
# exist (the pointer rotted), the same two directions features-are-bound.sh
# holds for a scenario.
never_list="$(awk '
	/^never:/ { sect="never"; next }
	/^[a-z_]+:/ { sect=""; next }
	sect=="never" && /^  - "/ { v=$0; sub(/^  - "/,"",v); sub(/"$/,"",v); print v }
' "$ROLES")"
never_bound="$(awk '
	/^never_bound:/ { sect="nb"; next }
	/^[a-z_]+:/ { sect=""; next }
	sect=="nb" && /^  - verb: / { v=$0; sub(/^  - verb: "/,"",v); sub(/"$/,"",v) }
	sect=="nb" && /^    test: / { t=$0; sub(/^    test: "/,"",t); sub(/"$/,"",t); print v "\t" t }
' "$ROLES")"
while IFS=$'\t' read -r verb tname; do
	[ -z "$verb" ] && continue
	if ! grep -qxF "$verb" <<<"$never_list"; then
		printf 'BINDING WITHOUT CLAUSE  never_bound names %s, which never: does not list\n' "$verb"
		fail=$((fail + 1))
	fi
	if [ -z "$tname" ] || ! grep -rqE "func ${tname}\(" internal/ tools/ 2>/dev/null; then
		printf 'DANGLING NEVER      never_bound pairs %s with %s, which names no test\n' "$verb" "${tname:-nothing}"
		fail=$((fail + 1))
	fi
done <<<"$never_bound"

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

# ------------------------------------------- property 5: threshold provenance

while IFS=$'\t' read -r tname tprov; do
	[ -z "$tname" ] && continue
	if ! grep -qE '^(@claude( .*)?|@decided [0-9]{4}-[0-9]{2}-[0-9]{2}([ ,].*)?|@measured .+ [0-9]{4}-[0-9]{2}-[0-9]{2})$' <<<"$tprov"; then
		printf 'UNRECOGNISED PROVENANCE  threshold %s carries %s; want @claude, @decided YYYY-MM-DD or @measured <how> YYYY-MM-DD\n' \
			"$tname" "${tprov:-no provenance line}"
		fail=$((fail + 1))
	fi
done <<<"$threshold_provenance"

echo
if [ "$n_classes" -eq 0 ] || [ "$n_roles" -eq 0 ] || [ "$n_thresholds" -eq 0 ]; then
	echo "measured nothing: $ROLES parsed to 0 classes, 0 roles or 0 thresholds, which is a" >&2
	echo "failure of this script's own reading, not a clean bill of health." >&2
	exit 1
fi
printf 'roles: %d classes, %d roles, %d roster names, %d thresholds, %d broken\n' "$n_classes" "$n_roles" "$n_roster" "$n_thresholds" "$fail"

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
	printf '%s\n' "$roster_raw" | sed -n '1,60s/^/  /p'
fi

[ "$fail" -eq 0 ]
