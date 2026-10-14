# Sourced by scripts/gates-have-teeth.sh. Not run on its own.
#
# TEETH_SHARD=i/N (1-based) runs only shard i of N of the teeth cases. Case k
# (1-based, in file order) belongs to shard ((k - 1) % N) + 1, so the N shards
# are disjoint and together hold every case. scripts/teeth-shards-cover-every-case.sh
# checks exactly that, from the list each shard would run. Unset, every case runs,
# as it did before the split existed.
#
# The filter lives in this file and not in gates-have-teeth.sh, because a case
# that plants a fault in the filter has to edit a file, and the harness must never
# edit the file it is running from: bash reads a script as it goes, so a rewrite
# under it executes whatever bytes now sit at its old offset. Sourcing reads this
# file before any case edits it, and the functions below are in memory from then on.

# teeth_shard_parse reads TEETH_SHARD into shard_i and shard_n, and refuses
# anything that is not i/N with 1 <= i <= N before any case runs.
teeth_shard_parse() {
	local spec="${TEETH_SHARD:-}"
	shard_i=0
	shard_n=0
	[ -n "$spec" ] || return 0
	if ! [[ "$spec" =~ ^([0-9]+)/([0-9]+)$ ]]; then
		printf 'TEETH_SHARD=%s is not i/N, with i and N whole numbers\n' "$spec" >&2
		exit 1
	fi
	shard_i=$((10#${BASH_REMATCH[1]}))
	shard_n=$((10#${BASH_REMATCH[2]}))
	if [ "$shard_n" -lt 1 ] || [ "$shard_i" -lt 1 ] || [ "$shard_i" -gt "$shard_n" ]; then
		printf 'TEETH_SHARD=%s: i must be from 1 to N, and N at least 1\n' "$spec" >&2
		exit 1
	fi
}

# teeth_shard_takes K is true when case K (1-based) belongs to this run's shard.
teeth_shard_takes() {
	[ "$shard_n" -eq 0 ] && return 0
	[ $(( ($1 - 1) % shard_n )) -eq $(( shard_i - 1 )) ]
}
