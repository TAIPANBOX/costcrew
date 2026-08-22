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

- **genaryx's Agent 360**, with idryx behind it. Done on 2026-08-23.

  idryx's `-passports` flag ENRICHES an identity already in the graph with
  owner, runtime, parent and attestation; it does not create one. So a console
  that writes passports and nothing else is invisible to the graph, which is
  exactly what "CostCrew's agents are not in Agent 360" turned out to mean.
  `tools/idryxsource` writes the other half - the `agents` source idryx asks
  for - and only the half it asks for: an agent's guards, skills and first-pass
  rate stay here, because an identity graph is about who a thing is and what it
  may reach, not how well it is doing its job.

  ```
  idryxsource -data ./local -out agents.json
  idryx serve -source agents -passports ./local/passports agents.json
  ```

  Then `services.idryx` in the descriptor, and the call that answered 422
  answers 200 with 41 identities, every one of them this console's, each
  carrying `"source": "passport"`. Opened in genaryx's own console, the
  Agent 360 card for `agent://costcrew.local/triage-aws` shows:

  - **IDENTITY**: source passport, owner yurii, privileged no, and idryx's own
    `bom_incomplete` alert, "agent-bom incomplete: missing attestation" -
    a fair criticism of this console's passports from a tool that is not ours;
  - **ACCESS**: the four rights this console granted it - figures-read,
    propose-only, requests-read, sql-readonly - each marked NO USAGE SIGNAL;
  - **DELEGATION**: the agent on the graph, once events naming it were on the
    bus.

  The money half was closed in the same run by naming the TokenFuse control
  plane as `services.cloud`, so all three planes read `ok` from
  `genaryx-web doctor`: bus, identity and money.

NOT verified, and not claimed:

- **The event-driven half of that card.** RHYTHM, STOPS and EVENTS stayed
  empty. The bus is live and this console's events do arrive on it (proved
  separately with the SSE stream), but those sections read genaryx's own
  ingested store and they had not filled in the time they were given.

- **The console bundle in `apps/web/dist` cannot show any of this.** It is a
  `--mode mock` build: it carries `meridian.io` fixtures and a fetch shim that
  answers every request with "network access is disabled in this sandbox". The
  `--mode web` build talks to the backend and is what the above was seen in.
  Anybody pointing genaryx at a real environment needs `npm run build`, not the
  bundle in the tree.
## TokenFuse, and a correction

Twice in this session I wrote that TokenFuse could not be connected until this
console's agents made real model calls. **That was wrong**, and it was wrong
because it came from reading the README rather than the code.

TokenFuse's control plane carries budgets ABOVE the run:
`POST /v1/units/{unit}/budget` takes `{"budget_usd": N}` and needs no run to
exist, and gateways poll `/v1/unit-budgets` every three seconds. Its identity
map binds a credential to the agents it may speak as, and those agents to a
unit. That is this console's model exactly: agents, desks and teams, with a
monthly budget per team.

So the useful division is: **this console is where a budget is DECIDED, and
TokenFuse is where it is ENFORCED.** Pushing one turns a record into a limit,
before any call is ever made.

Verified on 2026-08-22, against `tokenfuse-cloud` on loopback with the dev
credential, and no model call anywhere near it: ten team budgets planned,
nothing sent, control plane still `{}`; then applied, and read back from the
far end matching to the cent.

### Why this one is built differently from the other three

heraldyx, trailryx and genaryx all PULL from a file. This one makes an HTTP
call that CHANGES the number a gateway uses to decide whether to refuse a call.
A budget pushed too low stops real traffic. That is a different risk class and
it gets a different design:

- off unless an address and a key are configured, and the key is read from the
  environment and never written anywhere;
- planning sends NOTHING, and the test for that watches the far end rather
  than a return value;
- a budget going DOWN is counted separately and labelled, because that is the
  direction that stops work;
- nothing is ever deleted: raise, lower or add, but removing a budget is a
  decision about somebody else's system;
- and the approval binds to the diff it was given for.

That last one was a real hole, found by trying to break it rather than by
reading it. The first version was a two-step that RE-PLANNED on the second
step, so a person could read one diff, think about it, and send another, with
nothing anywhere saying so. A plan now prints a fingerprint and the apply
takes it back:

```
$ enforce -cloud http://127.0.0.1:8791
  ... the diff ...
  Nothing was sent. To send exactly this and nothing else:
    enforce -apply 151b53cce6d9

$ # somebody changes a budget by hand
$ enforce -cloud http://127.0.0.1:8791 -apply 151b53cce6d9
  enforce: this is not the plan that was approved: it was shown as
  151b53cce6d9 and is now a68bf415d2cb.
```

### Proven against a real gateway, and the two switches it needs

Verified on 2026-08-22 with a gateway actually running, not just the control
plane. The whole chain: this console's budget, to the control plane, to a
gateway that polls it, to a call that is refused.

```
  budget in the control plane: 0.01 USD
  call 1 -> HTTP 200
  call 2 -> HTTP 402
     {"error":{"type":"unit_budget_exceeded","budget_usd":0.01,
               "spent_usd":0.0525,"reason":"unit 'ml-platform' monthly budget exceeded",
               "retryable":false}}
```

and then back the other way, which is the half that shows who is in control:

```
  $ enforce -apply <fingerprint>       # this console's own figure
    ml-platform is now 22,880.28 USD
  the same call -> HTTP 200
```

**Nothing could spend, structurally rather than by promise.** With no
`TOKENFUSE_UPSTREAM` the gateway answers from a built-in stub and never
contacts a provider, and it says so itself at startup in the strongest terms
("Every figure it reports from now on is fictional"). No provider credential
existed in that environment. The fictional token counts are irrelevant to what
was being tested, which is the refusal.

Two switches this took to find, and neither is the obvious one:

1. **`TOKENFUSE_MODE=enforce`.** The default is `shadow`, which records the
   unit spend and does not block. With it on shadow the cloud showed the unit
   fifteen times over its budget while every call still returned 200. The cap
   is checked in `Mode::Enforce` only, and the default being safe is right:
   a proxy dropped in front of production should not start refusing traffic
   because somebody set a number in another console.
2. **The identity map**, `TOKENFUSE_IDENTITY_MAP`, which binds the credential
   to the agents it may present and those agents to a unit. Without it a call
   resolves to no unit and the cap is skipped entirely. `TOKENFUSE_IDENTITY_STRICT`
   is a different control and does NOT gate this: it governs the binding check.

So the honest statement of what this console can do is: **it decides the
number.** Whether that number refuses anything is a decision made in the
gateway's own configuration, by whoever runs the perimeter, and that is the
right place for it.

## A real SPIFFE identity, and what it can honestly attest

`-spiffe-socket <path>` makes this console fetch its own X.509-SVID from a
SPIFFE Workload API. Verified on 2026-08-23 against SPIRE 1.15.3, built from
source because the project publishes no macOS binary, with its own trust domain
`costcrew.local` and the console registered as a workload by three selectors:
the user it runs as, the binary's path, and that binary's SHA-256.

```
CostCrew: attested as spiffe://costcrew.local/console, valid until ... (serial 9f7d...)
```

Two things proved it is an attestation rather than a setting, both by trying to
break it:

- **A changed binary is refused.** Rebuilding the console changed its SHA-256,
  the registration entry still named the old one, and the workload API issued
  nothing: the start failed with "this binary matches no registration entry".
- **Another binary gets nothing.** `spire-agent api fetch x509` on the same
  socket answers `PermissionDenied: no identity issued`, because the CLI is not
  the registered workload.

### What it attests, said plainly

**The console, not each agent.** Thirty-nine analysts run in one process, and a
workload attestor checks a user, a path and a hash: at the level it looks at,
triage-aws and forecaster are the same process. Anything claiming thirty-nine
distinct SVIDs would be back to inventing identities.

So a passport for an agent with no attestation of its own now says
`{"method":"spiffe-svid","detail":"spiffe://costcrew.local/console"}` and
carries `attested: the runtime, not this agent`. That is true, it is checkable
by anybody holding the trust bundle, and it is more than "none" while being
less than "each agent proved itself".

An agent that records its OWN attestation keeps it. The runtime's identity is a
fallback for agents bound to nothing, never an overwrite of something a person
recorded.

### The effect on idryx

`bom_incomplete` went from 40 alerts to none. `stale_nhi` stayed at 32, which is
a different and also correct finding: these identities carry a creation date
and no observed activity.

### And the difference from the derivation removed the same day

Earlier today this console DERIVED an attestation from a permission list, and
that was removed as a false claim. This is also derived, and the difference is
the whole point: it is derived from a CERTIFICATE this process was issued after
something checked it, and it stops the moment the process no longer holds one.

### What it deliberately does not push

Per-agent budgets, though this console has them. TokenFuse binds an agent to a
unit through its identity map, and which credential may speak as which agent is
a decision made there, inside the perimeter the operator runs. Writing that map
from here would be this console asserting an identity binding it has no way to
verify.
