#!/usr/bin/env bash
# Checks that the teeth shards are a partition of the teeth cases: every case
# belongs to exactly one shard, and no shard is empty.
#
# WHY
#
# The teeth run is split across CI shards (TEETH_SHARD=i/N, scripts/teeth-shard.sh)
# so that its slowest job stops being one job that runs every case in turn. A
# split is only a speed-up if it is a partition. A case no shard takes is a fault
# nobody plants any more, and the run stays green without it. A case two shards
# both take is paid for twice. Neither prints a red line, so this compares the
# list each shard would run against the full list.
#
# It reads the lists the harness prints in TEETH_LIST mode, so it measures the
# filter the harness really uses, not a copy of it. It runs no case and mutates
# nothing.
#
# WITH NO N, IT ALSO CHECKS WHAT CI RUNS
#
# N then comes from .github/workflows/ci.yml, because a partition checked for N
# that CI does not run proves nothing about CI. The workflow is read for the two
# places the shard count is written, which have to agree: the matrix list
# (`shard: [1, 2, ...]`) must be exactly 1..N, and every TEETH_SHARD must end in /N.
# A matrix that lacks shard 12 of 12 leaves that shard's cases unrun, the teeth
# job stays green, and no line goes red. And when scripts/teeth-shard-plan.tsv
# exists, it has to be a plan for that N, and every row must name a case the
# harness has (a renamed case leaves a row that places nothing).
#
# Usage: scripts/teeth-shards-cover-every-case.sh [N]

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

harness=scripts/gates-have-teeth.sh
workflow=.github/workflows/ci.yml
plan=scripts/teeth-shard-plan.tsv
shards="${1:-}"

if [ ! -f "$harness" ]; then
	printf 'MEASURED NOTHING  %s is not there, so there is no case list to split\n' "$harness"
	exit 1
fi

failed=0

# ci_shards reads the shard count CI runs into ci_n, and sets failed if the
# workflow writes it two ways.
ci_shards() {
	if [ ! -f "$workflow" ]; then
		printf 'MEASURED NOTHING  %s is not there, so there is no shard count to check\n' "$workflow"
		exit 1
	fi
	local denominators matrix listed wanted
	denominators=$(sed -n 's/^[[:space:]]*TEETH_SHARD:[[:space:]]*\${{[[:space:]]*matrix\.shard[[:space:]]*}}\/\([0-9][0-9]*\)[[:space:]]*$/\1/p' "$workflow" | LC_ALL=C sort -u)
	matrix=$(sed -n 's/^[[:space:]]*shard:[[:space:]]*\[\(.*\)\][[:space:]]*$/\1/p' "$workflow")
	if [ -z "$denominators" ] || [ -z "$matrix" ]; then
		printf 'MEASURED NOTHING  %s has no TEETH_SHARD: ${{ matrix.shard }}/N line or no shard: [..] matrix to read\n' "$workflow"
		exit 1
	fi
	if [ "$(printf '%s\n' "$denominators" | grep -c .)" -ne 1 ]; then
		printf 'CI DISAGREES  %s gives TEETH_SHARD more than one total: %s\n' "$workflow" "$(printf '%s' "$denominators" | tr '\n' ' ')"
		failed=1
	fi
	ci_n=$(printf '%s\n' "$denominators" | sort -n | tail -n 1)
	listed=$(printf '%s' "$matrix" | tr ',' '\n' | tr -d ' ' | grep . | sort -n | tr '\n' ' ')
	wanted=$(seq 1 "$ci_n" | tr '\n' ' ')
	if [ "$listed" != "$wanted" ]; then
		printf 'CI DISAGREES  %s runs the matrix [%s] but TEETH_SHARD says /%s, so a shard the totals expect is never started and its cases never run\n' "$workflow" "$(printf '%s' "$listed" | sed 's/ $//; s/ /, /g')" "$ci_n"
		failed=1
	fi
}

from_ci=0
if [ -z "$shards" ]; then
	ci_shards
	shards="$ci_n"
	from_ci=1
fi
if ! [[ "$shards" =~ ^[1-9][0-9]*$ ]]; then
	printf 'the shard count must be a whole number of 1 or more, not %s\n' "$shards"
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The whole list. env -u keeps a TEETH_SHARD exported by the caller from narrowing it.
if ! env -u TEETH_SHARD TEETH_LIST=1 "$harness" >"$work/full.raw"; then
	printf 'MEASURED NOTHING  %s would not list its cases\n' "$harness"
	exit 1
fi
grep . "$work/full.raw" | LC_ALL=C sort >"$work/full"
total=$(grep -c . "$work/full")
if [ "$total" -eq 0 ]; then
	printf 'MEASURED NOTHING  %s listed no cases, so the shards would split nothing\n' "$harness"
	exit 1
fi

: >"$work/union"
for i in $(seq 1 "$shards"); do
	if ! env TEETH_LIST=1 TEETH_SHARD="$i/$shards" "$harness" >"$work/shard.raw"; then
		printf 'MEASURED NOTHING  shard %s/%s would not list its cases\n' "$i" "$shards"
		exit 1
	fi
	grep . "$work/shard.raw" | LC_ALL=C sort >"$work/shard-$i"
	count=$(grep -c . "$work/shard-$i")
	if [ "$count" -eq 0 ]; then
		printf 'MEASURED NOTHING  shard %s/%s holds no case, so it would run nothing\n' "$i" "$shards"
		failed=1
	fi
	printf '  shard %s/%s  %s cases\n' "$i" "$shards" "$count"
	cat "$work/shard-$i" >>"$work/union"
done
LC_ALL=C sort -o "$work/union" "$work/union"

# comm compares the two sorted lists as multisets: a name the full list holds
# that no shard runs is missing; a name the shards run more often than the full
# list holds it is extra.
missing=$(LC_ALL=C comm -23 "$work/full" "$work/union")
extra=$(LC_ALL=C comm -13 "$work/full" "$work/union")
if [ -n "$missing" ]; then
	printf 'NOT IN ANY SHARD  a case no shard would run, so the teeth run would stay green without it:\n'
	printf '%s\n' "$missing" | sed -n '1,5s/^/    /p'
	failed=1
fi
if [ -n "$extra" ]; then
	printf 'EXTRA  a shard runs a case more than once, or a name the file does not hold:\n'
	printf '%s\n' "$extra" | sed -n '1,5s/^/    /p'
	failed=1
fi

# The plan, when there is one for this N. A row that names no case places
# nothing, and a plan for another N is not read at all (scripts/teeth-shard.sh
# falls back to counting), which is a split CI still runs but no longer balanced.
if [ -f "$plan" ]; then
	plan_n=$(sed -n 's/^# shards: \([0-9][0-9]*\)$/\1/p' "$plan" | head -n 1)
	if [ "$plan_n" = "$shards" ]; then
		grep -v '^#' "$plan" | grep . | awk -F '\t' '{ print $3 }' | LC_ALL=C sort >"$work/planned"
		stale=$(LC_ALL=C comm -13 "$work/full" "$work/planned")
		if [ -n "$stale" ]; then
			printf 'PLAN NAMES NO CASE  a row of %s that the harness has no case for, so it places nothing (run scripts/teeth-plan.sh %s):\n' "$plan" "$shards"
			printf '%s\n' "$stale" | sed -n '1,5s/^/    /p'
			failed=1
		fi
		unplanned=$(LC_ALL=C comm -23 "$work/full" "$work/planned" | grep -c .)
		printf '  plan %s: %s cases placed by the plan, %s placed by their position in the file\n' "$plan" "$((total - unplanned))" "$unplanned"
	elif [ "$from_ci" -eq 1 ]; then
		printf 'PLAN FOR ANOTHER N  %s is a plan for %s shards and CI runs %s, so CI counts cases instead of balancing them (run scripts/teeth-plan.sh %s)\n' "$plan" "${plan_n:-no number of}" "$shards" "$shards"
		failed=1
	fi
fi

if [ "$failed" -ne 0 ]; then
	printf 'the teeth shards are not a partition of the %s cases, or CI does not run the shards the partition was checked for\n' "$total"
	exit 1
fi

printf 'teeth shards: %s shards of the %s cases, every case in exactly one shard\n' "$shards" "$total"
