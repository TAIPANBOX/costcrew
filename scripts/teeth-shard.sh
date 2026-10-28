# Sourced by scripts/gates-have-teeth.sh. Not run on its own.
#
# TEETH_SHARD=i/N (1-based) runs only shard i of N of the teeth cases. The N
# shards are disjoint and together hold every case, and
# scripts/teeth-shards-cover-every-case.sh checks exactly that, from the list
# each shard would run. Unset, every case runs, as it did before the split existed.
#
# THERE ARE TWO RULES FOR WHICH SHARD A CASE BELONGS TO
#
# 1. The plan. scripts/teeth-shard-plan.tsv names a shard for each case, chosen
#    by scripts/teeth-plan.sh from the seconds each case took in CI, so the
#    shards finish together. The plan is used only when its `# shards: N` line
#    equals this run's N: a plan made for twelve shards says nothing about five.
#    A case the plan has no row for (one added since the plan was made) goes to
#    the shard its name's checksum picks, so it still runs exactly once.
# 2. Without a plan for this N: case k (1-based, in file order) belongs to shard
#    ((k - 1) % N) + 1, the rule the split started with. It is balanced by count,
#    not by time, and it is what a person gets with TEETH_SHARD=1/3 on a laptop.
#
# Both are a partition. Neither prints a red line when it is not, which is why
# the gate lists what each shard would run and compares it with the whole.
#
# The filter lives in this file and not in gates-have-teeth.sh, because a case
# that plants a fault in the filter has to edit a file, and the harness must never
# edit the file it is running from: bash reads a script as it goes, so a rewrite
# under it executes whatever bytes now sit at its old offset. Sourcing reads this
# file before any case edits it, and the functions below are in memory from then on.
# The plan is read into memory here for the same reason.

teeth_shard_plan=scripts/teeth-shard-plan.tsv

# teeth_shard_parse reads TEETH_SHARD into shard_i and shard_n, and refuses
# anything that is not i/N with 1 <= i <= N before any case runs. When the plan
# is for this N it also reads it, into shard_plan_all (every case the plan
# names) and shard_plan_mine (the ones it gives to this shard), one name per line.
teeth_shard_parse() {
	local spec="${TEETH_SHARD:-}"
	shard_i=0
	shard_n=0
	shard_plan_on=0
	shard_plan_all=""
	shard_plan_mine=""
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
	[ -f "$teeth_shard_plan" ] || return 0
	local plan_n
	plan_n=$(sed -n 's/^# shards: \([0-9][0-9]*\)$/\1/p' "$teeth_shard_plan" | head -n 1)
	[ "$plan_n" = "$shard_n" ] || return 0
	local row_shard row_secs row_name
	while IFS=$'\t' read -r row_shard row_secs row_name; do
		case "$row_shard" in '' | '#'*) continue ;; esac
		if ! [[ "$row_shard" =~ ^[0-9]+$ ]] || [ "$((10#$row_shard))" -lt 1 ] || [ "$((10#$row_shard))" -gt "$shard_n" ] || [ -z "$row_name" ]; then
			printf '%s: a row that is not shard<TAB>seconds<TAB>name with shard from 1 to %s: %s\n' "$teeth_shard_plan" "$shard_n" "$row_shard"$'\t'"$row_secs"$'\t'"$row_name" >&2
			exit 1
		fi
		shard_plan_all="$shard_plan_all$row_name"$'\n'
		if [ "$((10#$row_shard))" -eq "$shard_i" ]; then
			shard_plan_mine="$shard_plan_mine$row_name"$'\n'
		fi
	done <"$teeth_shard_plan"
	shard_plan_on=1
}

# teeth_shard_takes K NAME is true when case K (1-based) called NAME belongs to
# this run's shard.
teeth_shard_takes() {
	[ "$shard_n" -eq 0 ] && return 0
	if [ "$shard_plan_on" -eq 1 ]; then
		if [[ $'\n'"$shard_plan_all" == *$'\n'"$2"$'\n'* ]]; then
			[[ $'\n'"$shard_plan_mine" == *$'\n'"$2"$'\n'* ]]
			return
		fi
		local sum
		sum=$(printf %s "$2" | cksum)
		sum=${sum%% *}
		[ $(( sum % shard_n )) -eq $(( shard_i - 1 )) ]
		return
	fi
	[ $(( ($1 - 1) % shard_n )) -eq $(( shard_i - 1 )) ]
}
