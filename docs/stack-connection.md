# How CostCrew connects to the stack, and why it cannot break it

`@claude` 2026-08-22. Everything measured here was run on this machine on that
day; the commands are in the section that reports each result.

## The rule this design is built around

**CostCrew is a producer and nothing else. It writes two files and calls
nobody.**

```
CostCrew ──writes──▶ agent-events NDJSON ──pulled by──▶ heraldyx  (mails a person)
         │                                └──pulled by──▶ trailryx  (seals a record)
         └──writes──▶ agent-passport JSON ─read by──────▶ heraldyx  (who answers for it)
```

Both consumers PULL, on their own schedule, from a file. Neither is called by
this console, neither is blocked by it, and neither shares a process with it.
That is the whole of why a change here cannot take a stack service down: the
worst a bad line can do is be refused, and both consumers already refuse lines
by design and count them.

Measured, not asserted: pointed at this console's stream before any of the work
below, trailryx read all 69 lines, refused all 69, reported why, exited 0 and
left a cursor. That is the failure mode. It is not an outage.

## What was changed, and what was deliberately not

Changed, all of it inside this repository:

- **the event TYPE on the wire**, where CostCrew's own word means the same
  thing as one the estate already has;
- **a `run_id`**, which mapped records require;
- **a new event** for an analyst going past its guard, which this console could
  always show and never said out loud;
- **the passport's `owner`**, now the account that answers for the agent rather
  than the installation's own flag.

Not changed, and this is checkable rather than promised:

```
$ for d in trailryx heraldyx genaryx tokenfuse; do (cd ~/Development/$d && git status --porcelain); done
```

Three clean. genaryx shows one file modified on 12 August, ten days before this
work. No stack repository was edited, and `agent-stack-go` stays pinned at
v0.6.0 in `go.mod`: this console started USING more of the contract, it did not
change the contract.

## Why translating the vocabulary is safe

The estate's word for a thing is defined by the consumers' own mapping tables,
not by this console's opinion of what a word ought to mean. Each mapping below
was read out of `trailryx-agentevent`'s `mapping_for` before it was used:

| CostCrew says | goes out as | which trailryx reads as |
|---|---|---|
| `anomaly_detected` | `spend_spike` | BudgetCheck, Warning, no verdict |
| an analyst past its guard | `budget_threshold` | BudgetCheck, Warning, no verdict |

The original type travels in the payload as `costcrew_type`, so nothing a
downstream reader might want is destroyed by the rename, and the agent card
shows both words for the same reason.

### The mapping that was rejected

`budget_exhausted` maps, downstream, to a **denied** verdict. This console
records that an agent went past its guard and does not stop it. Emitting that
type would put a refusal that never happened into a tamper-evident record.

That is the shape of "breaking the stack" that actually matters here. It is not
a crash and no alert would fire: it is a false entry in a record somebody
audits, arriving through a contract that says it is true. So the event carries
`enforced: false`, and that field is stamped by the translation rather than by
the caller, because a caller that forgot it would leave the question open.

### What is deliberately not translated

The shared vocabulary is about a RUN: a budget was checked, a policy decided, a
tool called, a breaker tripped. CostCrew's other events are about a PRACTICE: a
finding was triaged, an agent hired, a sprint planned. Those have no equivalent,
and forcing one would be the same false-claim failure in a different coat.

They keep their own names and the downstream refuses them. That is the correct
outcome and not a gap: trailryx records what agents did, and a roster change is
not that. In the last measured run: 9 mapped, 17 refused as `unknown_type`.

## Why the run id is the pass and not the finding

The contract says a run is "one execution of an agent". A detection pass is
that: the detector woke, read the estate, raised what it found. Nine findings
from one pass are nine records of ONE run.

Naming each finding a run of its own also passes every check, which is why it
is worth writing down: trailryx shards and indexes by run, and a query for the
run would then answer with one row where the answer is nine. Measured after the
change:

```
$ trailryx-node read --data DIR --run detect-1787436295
  ... 9 budget_check records + 1 store_event, proof Full, 10 row(s)
```

## The one hazard this work found, and what now stops it

The store's hash-chained journal and the agent-event stream are both
append-only NDJSON, and nothing about either name stopped `-stack-events` being
pointed at the journal. Two writers appending to one chain break it, and the
break reads as tampering rather than as a configuration mistake.

This is not hypothetical: it happened here, while testing the trailryx ship.
The server now refuses to start when the two paths resolve to the same file,
and says why.

## What is verified end to end, and what is not

Verified on 2026-08-22, with the commands above:

- heraldyx reads this console's stream and passports, finds the owner, and
  mails on the events it emits. Nothing was sent: `HERALDYX_MAIL_FILE` writes
  to a file. The three links in that mail now land in one hop.
- trailryx ingests the mapped events, seals a segment with a manifest, answers
  a query with `proof Full`, and `trailryx-verify` returns VERIFIED on the
  evidence pack while naming what the pack does not prove.

- **genaryx** tails the same file and shows it live. Verified end to end on
  2026-08-22: with this console writing to `<bus>/costcrew.ndjson`, an action
  taken in it appeared on genaryx's own event stream within seconds, tagged
  with the environment name, the agent URI, the severity and the schema. No
  code in this console was needed, and `genaryx-web doctor` reported
  `bus ok - live, tailing <dir>`.

  Two things about genaryx that are worth writing down because they differ
  from trailryx:

  1. **It accepts this console's own vocabulary.** Its `agent-event/v0.2`
     schema requires `schema`, `ts`, `source`, `type` and `agent_id`, and
     constrains `type` only to a non-empty string. So `anomaly_triaged` and
     `anomaly_explained` arrive and are shown, where trailryx refuses them.
     Neither is wrong: one is a live view and the other is a sealed record,
     and they disagree about what belongs in a record.
  2. **The file NAME is the integration.** genaryx tails a directory and keys
     each source's read offset off the file stem, so the stream must be called
     `costcrew.ndjson`. Replacing the file wholesale rather than appending to
     it is read as nothing new, which is how the first attempt here produced
     an empty stream.

  How it was run, without touching anything the estate owns: `TAIPAN_HOME`
  overrides where genaryx looks for environment descriptors, so a scratch home
  carried the descriptor and `~/.taipan/` was never written to. That matters
  beyond tidiness: a descriptor left in the real environments directory would
  be picked up by any later genaryx on this machine, in preference to or in
  confusion with one `taipan up` owns.

NOT verified, and not claimed:

- **genaryx's Agent 360**. The event stream is only half of that card. Its
  identity half comes from **idryx** and its money half from a cloud service,
  both named in the descriptor's `services` block, and this console's passports
  are not what genaryx reads for identity - idryx is. Measured rather than
  assumed: with no `services.idryx`, `POST /api/command/identity_list_identities`
  answers `{"kind":"no_environment"}` with 422, and `money_runs` the same.
  Registering these agents with idryx is a separate piece of work and idryx is
  not running here.
- **tokenfuse**: cannot work as a log integration at all. It is a proxy in the
  request path, and this console's agents make no model calls: their spend is a
  fixture. **The guards in this console are therefore records and not limits**,
  which is why `budget_exhausted` is refused above. TokenFuse is the thing that
  would make a guard bite, and connecting it means giving these agents real
  calls to make, which is a different piece of work.
