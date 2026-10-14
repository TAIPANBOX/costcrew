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
# Usage: scripts/teeth-shards-cover-every-case.sh [N]   (N defaults to 4)

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

harness=scripts/gates-have-teeth.sh
shards="${1:-4}"

if [ ! -f "$harness" ]; then
	printf 'MEASURED NOTHING  %s is not there, so there is no case list to split\n' "$harness"
	exit 1
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

failed=0
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
if [ "$failed" -ne 0 ]; then
	printf 'the teeth shards are not a partition of the %s cases\n' "$total"
	exit 1
fi

printf 'teeth shards: %s shards of the %s cases, every case in exactly one shard\n' "$shards" "$total"
