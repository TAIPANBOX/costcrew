#!/usr/bin/env bash
# Writes scripts/teeth-shard-plan.tsv: which of N shards each teeth case runs in,
# chosen so the shards take about the same time.
#
# Usage: scripts/teeth-plan.sh N [LOG...]
#
# WHY A PLAN AND NOT ROUND ROBIN
#
# Case k going to shard (k % N) balances the COUNT of cases, and the cases are not
# alike: most take three to eight seconds, a few take a minute and a half, because
# the gate they plant a fault against starts a server or the built binary. With four
# shards the slowest took 21 minutes and the fastest 12 in the same run, and the
# run is as long as its slowest shard.
#
# WHAT IT READS
#
# LOG is the output of a green gates-have-teeth.sh run, or a CI log of one (a
# leading timestamp is ignored): every `ok  <case> (fail)  4.2s` line is one
# measurement. With several logs a case's weight is the median of its seconds.
# A case no log mentions keeps the seconds the current plan has for it, and a case
# the plan has not met either gets the median of the rest. With no LOG at all the
# plan is made again from the seconds already in it, which is what to do after a
# case is renamed or removed (the cover gate refuses a plan row that names no case).
#
# HOW IT PLACES THEM
#
# Cases that plant a fault against the SAME test (same package and -run pattern)
# are kept together: the harness runs that test once on the clean tree before the
# first of them, and again in every other shard they are spread over, so a spread
# group pays for the baseline once per shard. Groups, heaviest first, each go to
# the shard that has the least so far (longest-processing-time-first), and the
# rows are written in the harness's own order so a new plan differs from the old
# one only in the cases that moved.
#
# It runs no case and edits no tracked file other than the plan (or the file
# TEETH_PLAN_FILE names instead).

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

harness=scripts/gates-have-teeth.sh
plan=scripts/teeth-shard-plan.tsv
# TEETH_PLAN_FILE writes the plan somewhere else and leaves the committed one alone;
# the existing plan is still the source of the weights a log does not give.
out="${TEETH_PLAN_FILE:-$plan}"

if [ "$#" -lt 1 ] || ! [[ "$1" =~ ^[1-9][0-9]*$ ]]; then
	printf 'usage: %s N [LOG...]   (N, the number of shards, is a whole number of 1 or more)\n' "$0" >&2
	exit 2
fi
n="$1"
shift

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if ! env -u TEETH_SHARD TEETH_LIST=gates "$harness" >"$work/cases.tsv" || [ ! -s "$work/cases.tsv" ]; then
	printf 'MEASURED NOTHING  %s listed no cases, so there is nothing to plan\n' "$harness" >&2
	exit 1
fi

python3 -I - "$n" "$work/cases.tsv" "$plan" "$@" <<'PYEOF' >"$work/plan.tsv" || exit 1
import re, statistics, sys

n = int(sys.argv[1])
cases_path, plan_path, logs = sys.argv[2], sys.argv[3], sys.argv[4:]

cases = []  # (name, group) in harness order
for line in open(cases_path, encoding="utf-8"):
    name, pkg, pattern = line.rstrip("\n").split("\t")
    cases.append((name, pkg + "\t" + pattern))
names = [c[0] for c in cases]
if len(set(names)) != len(names):
    sys.exit("two cases share a name, so a plan keyed by name cannot tell them apart")

measured = {}
line_re = re.compile(r"^ok  (.*?)\s+\((?:fail|pass|gone)\)\s+([0-9]+(?:\.[0-9]+)?)s\s*$")
for path in logs:
    for line in open(path, encoding="utf-8", errors="replace"):
        line = line.lstrip("﻿").rstrip("\n")
        line = re.sub(r"^\d{4}-\d\d-\d\dT[0-9:.]+Z ", "", line)
        m = line_re.match(line)
        if m and m.group(1) in names:
            measured.setdefault(m.group(1), []).append(float(m.group(2)))

previous = {}
try:
    for line in open(plan_path, encoding="utf-8"):
        if line.startswith("#") or not line.strip():
            continue
        shard, secs, name = line.rstrip("\n").split("\t", 2)
        previous[name] = float(secs)
except FileNotFoundError:
    pass

weight = {}
for name in names:
    if name in measured:
        weight[name] = statistics.median(measured[name])
    elif name in previous:
        weight[name] = previous[name]
known = sorted(weight.values())
fallback = statistics.median(known) if known else 5.0
unmeasured = [name for name in names if name not in weight]
for name in unmeasured:
    weight[name] = fallback

groups = {}
for index, (name, key) in enumerate(cases):
    groups.setdefault(key, []).append(name)
order = sorted(groups.values(), key=lambda g: (-sum(weight[x] for x in g), names.index(g[0])))

load = [0.0] * n
shard_of = {}
for group in order:
    target = min(range(n), key=lambda s: (load[s], s))
    for name in group:
        shard_of[name] = target + 1
    load[target] += sum(weight[name] for name in group)

mean = sum(load) / n
print("# Which shard each teeth case runs in. Made by scripts/teeth-plan.sh; read by")
print("# scripts/teeth-shard.sh. Rows are shard, seconds, case name, separated by tabs.")
print("# shards: %d" % n)
print("# Predicted seconds per shard, by these weights: %s" % " ".join("%.0f" % x for x in load))
print("# Slowest shard / mean: %.3f. Weights from %d log(s); %d case(s) had none and took %s."
      % (max(load) / mean, len(logs), len(unmeasured), "the median of the rest, %.1fs" % fallback if unmeasured else "nothing"))
for name in names:
    print("%d\t%.1f\t%s" % (shard_of[name], weight[name], name))

sys.stderr.write("teeth plan: %d cases in %d groups over %d shards, predicted %s s, slowest / mean %.3f\n"
                 % (len(names), len(groups), n, " ".join("%.0f" % x for x in load), max(load) / mean))
PYEOF

mv "$work/plan.tsv" "$out"
