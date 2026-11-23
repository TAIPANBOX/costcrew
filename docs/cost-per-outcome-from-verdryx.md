# Cost per outcome: counting outcomes the way verdryx counts them

A design note, not a feature. Nothing described here is built. `@claude`
2026-11-23.

## Where it stands

The KPI `cost-per-outcome` (`internal/finops/kpi.go`, computed by
`finops.CostPerOutcome` in `internal/finops/ai.go`) already reads real
outcome tags. The TokenFuse FOCUS export carries `x_outcome` on a call, the
`tokenfuse-focus` reader stores it in `ai_calls.outcome`, and the KPI divides
the month's AI spend by the number of outcomes. On the generated estate there
is no `ai_calls` row, so the KPI refuses, which is what the README's "no
business metric is connected" describes. On an installation that imports a
TokenFuse export whose agents tag outcomes, it reports a figure.

The figure and verdryx's do not count the same thing.

| | costcrew today | verdryx `cost_per_outcome` (`verdryx/costper.py`) |
|---|---|---|
| unit of work | a tagged CALL | a RUN (`run_id`) |
| a run that tags `escalated`, then `case_resolved` | two outcomes | one outcome, `case_resolved` (the last non-empty tag in `step` order) |
| a run that never tags | its cost is in the numerator, it adds nothing to the denominator | one record under `(untagged)`, its cost included |
| a Breaker-blocked call | not spend, counted | not spend, counted |
| per outcome label | one pooled figure | one figure per label plus `overall` |

So an agent that tags each step of a case lowers costcrew's figure without
doing any more work, while verdryx's stays put. The two pages a person reads
for the same agents can disagree by a factor of the number of tagged steps.

## Proposal

Reduce `ai_calls` per run inside costcrew, by verdryx's rule, rather than
import verdryx's report:

1. Group the month's calls by `run_id`. A run's outcome is its LAST non-empty
   `outcome`, ordered by the call's instant (`ts`) and then by row number, the
   closest the FOCUS export comes to tokenfuse's `step`: the export carries no
   step column. Two tagged calls of one run in the same instant with
   different tags are a tie; the run is counted under `(ambiguous)` and named,
   never decided by row order alone.
2. Every call of a run folds into the run, tagged or not, blocked calls as
   counted-not-spend, exactly as now.
3. A run with no tag is one `(untagged)` unit. Its cost stays in the overall
   numerator and it is the coverage caveat the KPI already prints.
4. A call with no `run_id` (an export from before runs were stamped) keeps
   today's per-call rule, and the KPI says how many such calls it counted.
5. The KPI keeps one pooled figure for the library; the AI page adds the
   per-label table verdryx prints, from the same reduction.

Why not import verdryx's output: it would add a second reader of the same
trace and a second place where the reduction lives, and the two would drift.
The rule is short enough to hold in both places and be checked against each
other.

## How it would be held

- A fixture trace with a run that tags twice, a run that never tags, a
  blocked call and a call without a run id, with the expected per-label table
  written by hand from verdryx's rule, run through costcrew's reduction (a
  test that is red on today's per-call count).
- The same fixture run through verdryx (`verdryx cost-per-outcome`) once, its
  output committed beside it as the agreed answer, so a change to either side
  that breaks agreement is visible in a diff.
- A teeth case that counts per call again and one that orders by row number
  without the instant.

## What it would not do

It does not make an outcome a business metric. A tag is what the agent says
happened. Revenue or a resolved ticket measured outside the agent is a
separate connector, and the KPI's caveat should keep saying which one it is.
