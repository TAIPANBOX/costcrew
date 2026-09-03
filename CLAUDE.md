# CLAUDE.md, working instructions for costcrew

## Read before you change anything

`parity/README.md` says what the parity gate measures now and what it no
longer does. `docs/stack-connection.md` says what this console writes into the
estate's shared records and what it deliberately does not.

## What this is

A FinOps analyst console in which a crew of agents does the work, and a person
reviews it. Rewritten from a Python version, now finished: the Python original
was deleted 2026-08-25, and `parity/captures/golden` is the only surviving
record of its HTTP surface (see `parity/README.md` for what that does and
does not prove).

Single static binary, pure-Go SQLite (`modernc.org/sqlite`), no build step, no
runtime to install, no network. Money is integer cents at every stored total.
One reader keeps a PER-CALL amount in a finer integer subunit first
(`money.Micros`, millionths of a unit, in `ai_calls.billed_microusd`) because
an LLM call is routinely worth a few tenths of a cent and a column that
rounds each one to cents before summing loses every one of them; the daily
total it derives is still rounded to cents, once, never per call. See
`money.Micros` and invariant 25.

## What this repository contributes, declared and proved

`components.json` at the root, and `internal/manifest` proves it. Same two
buckets as TAIPANBOX/vouchryx, where the shape was worked out, and the same
reason for being here rather than in estate-gates: the only thing that knows
which binaries a repository builds is the repository, so a component that was
FORGOTTEN is invisible from outside by construction.

Seven binaries and six of them are tools no deployment installs, which is
exactly why the estate's own registry could not be the place this is written.

**This one is configured by FLAGS**, unlike its neighbours. The two environment
variables it reads are not how a deployment wires it, so `checked.flags` is the
field that carries the surface.

**A `declared` entry with no `why` is refused.** Four of them here say things no
check can: that this console enforces nothing, that the event file NAME is the
integration because genaryx keys a read offset off the stem, that the whole
integration is off unless `-stack-events` is given, and that `-stack-passports`
and `-stack-owner` are a PAIR the console refuses to start without both of.
That last one was found by TAIPANBOX/stack-up starting it, not by reading it: a
dependency BETWEEN two flags is invisible to a reader of `flag.String` call
sites.

**The health check does not follow redirects, and that is the whole of whether
it checks anything.** Every guarded route here answers 303 to `/login`, and
`/login` is public and returns 200, so a following client turns "you may not be
here" into agreement. Measured: with `http.Get`, declaring `/board` as the
health path passed.

## Gates

```sh
go test ./...                        # 534 tests, 20 packages
./scripts/gates-have-teeth.sh        # 68 cases; needs a clean tree; ~90s (6m39s @measured 2026-09-03 under this session's own heavy concurrent load, several other agents' builds and parity crawls running throughout)
./scripts/features-are-bound.sh      # 114 scenarios, both directions
./scripts/roles-are-bound.sh         # internal/crew/roles.yaml against the code and the roster, both ways
./parity/gate-has-teeth.sh parity/captures/golden
gofmt -l . && go vet ./...
staticcheck ./...                    # CI runs it, pinned at 2026.2.1, and refused PR #19 on two findings the list above never asked for; a staticcheck built for an older Go cannot read this module, so on such a machine CI is the only place it runs
```

Counts follow the suite, measured on `feat/c5-rightsizing-from-provider-recommendations`,
branched from `main` at #28's own merge (602fa25), no rebase since
(test count by `go test ./... -list '.*' | grep -c '^Test'` -- test FUNCTIONS, not
subtests, the convention PR #21, #23 and this file's own `internal/manifest` gate
already use, not `-v | grep -c '^--- PASS'`, which over-counts anything using
`t.Run`; packages by `go list -f '{{if or .TestGoFiles .XTestGoFiles}}...'`;
`grep -c '^run_case ' scripts/gates-have-teeth.sh` -- the trailing space
matters: without it the pattern also matches the function's own
`run_case() {`, one line above every real invocation, and over-counts by
one, which is what every "N cases" figure in this block was doing until
coordinator review of PR #34 caught it here 2026-09-03. Independently the
same bug, and the same fix: B6B-SPEC.md's own invariant 32 on `main`
already found it for `feat/one-metered-call-path`, and its own words are
that the script's own summary line, `teeth: N passed, N failed`, "is the
count that was actually true both before and after" -- that line comes
from an actual run, not a grep against the source, and is the one number
in this whole block that was never wrong; `grep -rc Scenario: features/*.feature`;
2026-09-03); nothing here keeps them current automatically, so they lag whichever
branch last updated them by hand (B1a's own merge left this block at 282/48/59
while its own PR body reported 301/52/70). Invariants 1, 2 and 5's own route
counts (48/30/30 -> 50/34/34) were also re-measured while touching this file for
B5, since the new /cadence routes are directly what moved them.

C5 re-measured the same way, with the SAME grep bug this file carried until
today: 515/68/105 was stated at the time as `main`'s own numbers at #28's
merge, but the true `run_case` count there (corrected grep) is 67, not 68 --
515/67/105 is what the three commands, run correctly, actually give at
602fa25. Only invariant 1's own route count had drifted for an unrelated
reason, 50 stated against 56 actual GET routes in `server.go` at that same
commit, unnoticed since B5 because nothing that landed between the two
touched a route AND this file in the same PR. 515/67/105 -> 532/68/114 is
C5's own original delta (17 tests: 9 in `internal/connectors`, 5 in
`internal/deliver`, 3 in `internal/web`; one `run_case`, C5-SPEC.md's own
named mutant; nine scenarios, `features/rightsizing.feature`); 56 -> 57 is
the one route this branch adds.

Re-measured again 2026-09-03 after coordinator review of PR #34: two more
tests (10 in `internal/connectors`, 4 in `internal/web`, both up by one),
without adding a route, a scenario or a `run_case` (the existing
"rightsizing: rank by current cost" case is retargeted at its new location
instead of a second one being added -- invariant 33's own gate list says
where). 532/68/114 -> 534/68/114, and the harness itself, run on the
resulting clean committed tree, printed `teeth: 68 passed, 0 failed` --
the one number in this whole block that is `@measured` rather than
`@claude`'s arithmetic on top of a grep.

The gates in this repo are Go tests rather than shell scripts, so
`gates-have-teeth.sh` mutates the PRODUCT and requires the test to go red.
Read its header before adding a case: two of its properties exist because Go
makes a silent pass easy, and one because a mutation that does not compile
fails `go test` with the same exit code as a caught fault.

## Hard invariants

Each one carries how it is held today. `(gate: ...)`, `(test: ...)`,
`(partly gated: ...)` or `(not enforced)`, and use the weakest one that is
true. An invariant with no check, written as though it had one, is worse than
an absent invariant.

1. **No route answers a stranger.** Every route turns an unauthenticated
   caller away, with seven exceptions listed in `publicRoutes`, each carrying
   its reason in the source. They are not one kind of thing: `/login`,
   `/logout`, `/healthz` and `/static/` genuinely answer anybody; `/signup` is
   open only while nobody can administer the installation (invariant 10); and
   `/calendar` and `/stats` are aliases that redirect to a guarded page.
   *(gate: `TestEveryRouteRequiresASession`, which walks all 57 GET routes
   registered in `server.go`. Its regexp is anchored to the registration and
   not to the string `HandleFunc`, because it once fired on a route named in a
   COMMENT, and a gate that fires on prose gets deleted the first week.)*

2. **A viewer may read and export, and may write nothing.**
   *(gate: `TestAViewerCannotWrite`, all 34 write routes against a real viewer
   session with a real CSRF token. It requires THE role refusal by its wording,
   not any refusal: accepting any message made it pass on 26 of 27 routes whose
   handlers happened to complain about something else first.)*

3. **Managing accounts is an admin's job, and it is a higher bar than acting.**
   An operator may spend the budget and may not hand anybody else the ability
   to.
   *(gate: `TestAnOperatorCannotEscalateThroughAccounts`, which checks the
   DATABASE rather than the redirect, because a handler that writes and then
   complains would pass a redirect check.)*

4. **Only the agent's owner, or an admin, manages an agent.** Remove,
   transfer, re-brief and state, plus the re-brief page itself. An admin is
   included because somebody has to clean up after a person who has left, and
   that is exactly when the owner cannot act.
   *(gate: nine tests in `internal/web/ownership_test.go`. One of them,
   `TestTheOwnerAndAnAdminCanStillManage`, exists because every other test here
   proves somebody is REFUSED and a fix that refused everybody would pass all
   of them.)*

5. **Every write route refuses a request without a valid CSRF token.** The
   check is in five places, not one: `s.checked` and four handlers with their
   own copy.
   *(gate: `TestEveryWriteRouteChecksCSRF`, 34 routes with an empty token and
   with a wrong one, requiring the CSRF wording specifically. Until
   2026-08-23 this was one route of thirty-three.)*

6. **Spend is attributed by the owner recorded ON the charge**, never by who
   owns the agent today. Otherwise an agent changing hands rewrites history for
   two people: the new owner's total jumps by an amount they never authorised
   and the previous owner's drops by the same.
   *(gate: `TestSpendByOwnerReadsTheChargeNotTheRoster` and
   `TestATransferSplitsTheSpendAtTheHandover`. A transfer moves the charges on
   OPEN work and leaves closed work with whoever authorised it; moving
   everything would mean there is no history at all.)*

7. **The same estate renders the same way every time.** Nondeterminism here
   does not look like a bug, it looks like a table whose rows moved, which a
   reader takes for new data.
   *(gate: `TestAIUnitsAreOrderedTheSameEveryCall` and
   `TestPagesRenderTheSameTwice`. The second scales its repeat count to the row
   count, because comparing two renders of a four-row table is close to a coin
   toss: ranging a Go map is a rotation from a random start, not a uniform
   shuffle, and three repeats caught a real fault only 17 times in 20.)*

8. **A right this console can grant has an explanation, and an explanation
   has a right.** Both directions, because the second rots quietly.
   *(gate: `TestEveryGrantableRightIsExplained`,
   `TestNoExplanationOutlivesItsRight`, `TestNoSkillGrantsARetiredRight`. The
   third guards the way a retired right comes back, which is not the roster but
   `RightsFor` deriving rights from skills on the next hire.)*

9. **An attestation is never invented.** A method with no evidence is a word:
   it can be neither checked nor disproved, and an identity graph that believes
   it stops asking about an agent bound to nothing. This console once derived
   attestations for 12 of 39 agents from their permission lists, which was
   worse than `none`, because idryx then stopped flagging them.
   *(gate: `ValidAttestation` plus `TestAnAttestationHasToCarryItsEvidence`,
   `TestHiringRefusesAnAttestationWithNoEvidence`,
   `TestSeedingClaimsNoAttestation`.)*

10. **Registration is open while nobody can administer this installation**,
    not while the users table is empty. The estate seeds five owner accounts
    with unusable passwords, and counting ACCOUNTS made a brand-new
    installation refuse the first person who opened it.
    *(gate: `TestSignupIsOpenUntilThereIsAnAdmin`.)*

11. **Starting the console twice changes nothing the second time.** Every
    startup step is a migration or a backfill and every one runs on every
    start.
    *(gate: `TestBackfillMandateConverges`, `TestSeedOwnersIsIdempotent`,
    `TestSeedOwnersDoesNotUndoAPlacementAPersonMade`,
    `TestEnsureOwnershipHistoryIsSafeToRunAgain`. The first exists because
    every start reported filling in a mandate for two analysts and never had:
    SQLite counts the rows an UPDATE MATCHED, not the rows whose values
    differ.)*

12. **The journal is a chain, and the audit page says where it broke rather
    than that it is broken.**
    *(gate: `TestAnEditedEntryStillBreaksTheChain`,
    `TestAWholeNumberInAnEntryDoesNotBreakTheChain`,
    `TestTheAuditPageVerifiesTheChain`. The second is there because JSON
    round-tripping turns an int64 into a float64 and the writer and the
    verifier then disagree about the same bytes.)*

13. **Figures reconcile across pages.** One number cut two ways must give one
    answer. Four separate disagreements have been found this way, and each
    looked entirely plausible from one side.
    *(partly gated: `TestOwnersAndCrewAgreeOnUnboundAgents` and the
    reconciliation tests in `internal/world`. There is no gate that walks every
    pair of pages showing the same figure, and there could not easily be one:
    the pairs are known to people and not to the code.)*

14. **Every scenario in `features/` names a test, and every name is real.**
    The scenarios are Yurii's own words, quoted above each one, and are not
    derived from the code: a scenario written by reading what was built only
    proves it can be described.
    *(gate: `scripts/features-are-bound.sh`, reachable from the suite as
    `TestFeatureBindingsHold`. It checks the POINTER in both directions and
    deliberately not whether the test asserts what the scenario says, which
    nothing mechanical can. There is no BDD runner: three runners with three
    step-definition styles across the estate would cost more surface than the
    readability they buy, and that is my call rather than his.)*

15. **Every gate can go red.** A new gate is not finished until
    `scripts/gates-have-teeth.sh` has a case that plants its fault.
    *(gate: `scripts/gates-have-teeth.sh` itself, checked by hand on
    2026-08-23: gutting an assertion reports TOOTHLESS, a dirty tree is
    refused, an already-red gate is UNJUDGEABLE rather than judged, and a kill
    restores the tree.)*

16. **A deliverable says whether a person's money bought it.** The estate
    ships 279 generated drafts; a live run adds real ones to the same table,
    with the same author, the same state and the same shape. After the first
    full run 63 real ones sat among 342 and nothing could tell them apart.
    `docs/live-agents.md` named this before the runner existed and the runner
    was built without it anyway.
    *(gate: `TestARunnerDeliverableIsMarkedLive` and
    `TestTheTaskPageShowsWhichDeliverableWasWrittenLive`, one for the writer and
    one for the reader, because a marker no page displays is not a marker. The
    column defaults to `fixture`, which is the safe direction: a row whose
    provenance is unknown must not read as evidence of a real call. The SPEND is
    named too, since 2026-08-24, on all three pages that show a mixed cost:
    `TestTheCrewPageSaysWhatOfItsFigureIsReal`,
    `TestTheCrewCostKPISaysWhatIsRealMoney`,
    `TestTheAgentCardSaysWhatOfItsCostIsReal`. One function, `crew.RealMoney`,
    writes the sentence for all three, because three copies of one fact is how
    two pages come to disagree about it. The card reads `LiveSpendBy`, per
    analyst, and its test gives a second agent 0.99 of live spend and requires
    that it not appear on the first agent's card.
    `TestTheKPISaysNothingAboutMoneyNobodySpent` holds the other direction: a
    console where no agent has run says nothing about real money.)*

17. **A run cannot walk past its ceiling by running things at once.** Each call
    reserves its worst case BEFORE it starts and settles the difference after,
    so four calls in flight cannot each pass a check against the same unspent
    balance.
    *(gate: `TestTheCeilingHoldsUnderConcurrency`, `TestAFailedCallReturnsItsReservation`.
    The first passed against a budget that ignored reservations entirely until
    it was rewritten to HOLD its reservations open: it settled each call
    immediately, so no two ever overlapped, and the one fault it existed to
    catch could not reach it.)*

18. **What the console says a run cost is what the run cost.** Cents cannot
    hold a fifth of a cent and a model call costs about that, so the rounding
    happens once, over the whole run, and the cents are handed out by largest
    remainder. Rounding per CALL recorded 0.56 for a run that billed 0.2337;
    rounding per TASK recorded the same, because the runner makes one call per
    task.
    *(gate: `TestTheLedgerDoesNotOverstateManySmallCalls`, with both faults in
    `gates-have-teeth.sh`, and `TestSettlingTheSameRunTwiceChangesNothing`
    because settling runs on every run. The test that let the second fault
    through put all 44 calls on ONE task, where per-task rounding happens to be
    right: a test can only prove what it describes.
    @measured 2026-08-24, a full run: router 0.2342, board 0.24, crew page
    3871.35 -> 3871.59.)*

19. **Nothing may move the sidebar under the cursor.** Three attempts, and the
    middle one made it worse, so all three are written down in the CSS itself.
    Sticky gave the panel its own scrollbar, invisible and reset by every page
    load. Static put it in the flow, where a trackpad's momentum slid the whole
    list after the finger had left it: Yurii clicked Accounts four times and got
    Desks, Crew and Budgets, 574px, 682px and 466px away. Fixed is the only one
    the page cannot touch.
    *(gate: `TestThePageCannotMoveTheSidebar` and `TestTheSidebarFitsAWindow`,
    which are budgets on the CSS because Go cannot lay out a page. @measured,
    Chrome at 1440x1020 with the page forced to 3000px and both nav.scrollTop
    and main.scrollTop forced to 400: nothing moved it, all 26 links visible,
    zero mismatches; 92 page-and-size combinations clean afterwards. Below
    936px of viewport the panel keeps an internal scroll, which is what any
    list too long for its box does; the structural answer is fewer than 26
    destinations and that is Yurii's call.)*

20. **A figure that mixes generated and real money says so.** Both themes are
    measured, not eyeballed: the marker's first border was 1.2:1 against its
    surface in light mode, which is the console's container vocabulary, not its
    state vocabulary.
    *(gate: `TestTheCrewPageSaysWhatOfItsFigureIsReal`,
    `TestTheLiveMarkerIsDrawnLikeAMarker`. @measured 2026-08-24: marker text
    6.53:1 light and 7.17:1 dark, border 4.8:1 and 5.2:1; the ten text pairs on
    the overview pass in both themes, thinnest slack 0.29 on the state chip.)*

21. **A skill on the roster is a skill this console can back with rights, and
    the hire form offers exactly that set.** `rightsForSkill` is the source of
    truth for what a skill grants; `SkillPool`, what the hire form offers, is
    its sorted keys, derived rather than written by hand a second time. Before
    this, `SkillPool` was a hand-kept list of fifteen while `rightsForSkill`
    already defined thirty-eight, so thirty of the roster's forty-five skill
    strings were never offered by the form, and nine of them (including
    `sql-readonly`, a RIGHT written where a skill goes, on the three
    investigators) had no rights entry at all: an analyst holding one was
    seeded with the figures-read floor and nothing its own mission needed.
    *(gate: `TestEverySkillOnTheRosterHasRights`,
    `TestSkillPoolIsExactlyTheSkillsWithRights`. The sustainability analyst's
    two skills were renamed onto the map's existing `carbon-accounting` and
    `sustainability-reporting` rather than adding new entries under the
    roster's old names; `RenameRetiredSkills` carries an installation seeded
    under the old names onto the new ones, once, topping up only the rights
    the new name adds.)*

22. **A connector is Built only if a reader is registered for it.** The
    package comment has said Built means "there is a reader and a test" since
    the catalogue was written; the field itself was set by hand on every
    entry regardless, and seven of ten said Built while no reader existed
    anywhere in the module for any of them. `Status` is now derived from the
    `readers` registry in exactly one place (`deriveStatus`, run from
    `init()`), so an entry can never again claim more than the registry
    backs. The registry is empty today: every entry is honestly `Documented`,
    and each of the seven that used to say otherwise now says so in its own
    Note.
    *(gate: `TestBuiltMeansAReaderExists`. `Import` still refuses a metered
    connector without confirmation FIRST, before it ever asks whether a
    reader exists: `TestImportRefusesAMeteredConnectorWithoutConfirmation`.
    Once past that gate it looks up the reader and, finding none, returns the
    same refusal it always has: `TestImportRefusesADocumentedConnector`.
    `TestCountsMatchTheCatalogue` and `TestImportCallsARegisteredReader`
    round out the four.)*

23. **A role decides what its job description lists, and nothing else.** The
    card, the prompt packet and the mission seeded onto every analyst are
    three renderings of one file, `internal/crew/roles.yaml`, not three
    hand-written copies of the same thirty-nine sentences. Before this,
    `missionFor`'s switch statement said what an analyst was FOR and nothing
    else read it, so a prompt could tell an analyst nothing about what it
    might decide alone or where a purchase, an infrastructure change or a
    vendor negotiation had to go instead. Three classes are owned by nobody
    in the crew for exactly that reason: `purchase`, `infra.change` and
    `vendor.negotiate` are never a decision the console applies, only ever an
    option a person acts on outside it.
    *(gate: `scripts/roles-are-bound.sh`, reachable from the suite as
    `TestRolesAreBound`, holding four properties both ways: every class named
    in code exists in the file and every class in the file is owned by
    exactly one link; every roster name matches exactly one role family and
    every family matches at least one roster name; every class a role
    decides alone is within what its rights back; and the supervisor's
    `hands_to_owner` is exactly the set of classes the owner owns, plus the
    two named conditions. `crew.MayDecide` and `crew.Escalates` are the two
    functions everything above reduces to, tested directly by
    `TestARoleCannotDecideAClassItDoesNotOwn`; `TestEveryRoleHasAJobDescription`
    and `TestThePromptCarriesTheJobDescription` hold the card and the prompt
    packet. `Post`, `Return` and `Approve` each ask `MayDecide` for the
    actor's link before touching the database, refusing with
    `ErrMayNotDecide` otherwise; every caller today passes "owner", so the
    refusal never fires in production, and
    `TestPostReturnApproveRefuseALinkThatMayNotDecide` is what proves the
    check is real code on that path rather than a promise standing next to
    it.)*

24. **A generated estate is never mixed with real money.** The first reader
    the registry has ever held (`tokenfuse-focus`) refuses every row while
    `charges` still holds generated ones (`provenance IS NULL`), unless the
    operator passes `-replace-generated`. With it, the generated `charges`
    (scoped to `provenance IS NULL`, so a later connector's own real rows are
    never touched by an earlier one's flag), `drivers`, `attribution` and the
    seeded `anomalies`, `tasks`, `artifacts`, `sprints`, `forecasts` and
    `chargeback` rows are removed FIRST, in one transaction, and the removal
    is journaled; the roster, accounts and connections are not on that list.
    Emptying the seeded board this way is what found a second bug: `KPIs()`'s
    first-pass-acceptance query summed `tasks` with two of its three `SUM`s
    missing the `COALESCE` the query beside it already carried, so an operator
    who used the flag and then opened `/kpis` got a 500 rather than a page
    that had never seen a task.
    *(gate: `TestGeneratedEstateIsNotMixed`, both directions: the refusal
    (nothing written, the generated rows untouched, no journal line) and the
    flag (the generated rows gone, the real ones present, a
    `generated_estate_replaced` entry in the chain). The `COALESCE` fix is
    held by every test in `internal/finops/ai_test.go` that runs `KPIs()`
    against a store carrying real rows and nothing generated.)*

25. **A per-call amount is never rounded before it is summed.** `ai_calls`
    keeps each call's own cost in `money.Micros` (millionths of a unit), not
    cents: an LLM call is routinely worth a few tenths of a cent, and a
    column that rounds every one to the nearest cent on its own loses all of
    them before they have a chance to add up. Ten calls at $0.0035 are three
    and a half cents, not zero. `deriveCharges` sums a whole day's Micros in
    SQL, exact 64-bit integer arithmetic, and rounds to Cents exactly once,
    half away from zero, the same convention `money.Parse` and `money.Bps`
    already use. The first version of this reader parsed `BilledCost`
    straight into `money.Cents` and rounded every row on its own before it
    was ever summed; a review before merge, not a test that had been
    written yet, is what caught it.
    *(gate: `TestSubCentCallsRoundHalfAwayFromZeroOnceSummed` (two calls
    round up to one cent; ten calls sum to an exact tie at 3.5 cents and
    round up to four, not down to three) and
    `TestCostIsNeverParsedThroughFloat64` ($0.000249 comes back as exactly
    249 micros through `money.ParseMicros`; a float64 round-trip of the same
    string gives 248). `internal/money`'s own `TestSubCentCallsRoundHalfAwayFromZeroOnceSummed`
    and `TestMicrosCentsIsSymmetric` hold the arithmetic in isolation.)*
26. **A tool is called only under a right the analyst holds, and a query
    reaches only the charges.** Before this, a skill on the roster was a tag
    on a card and the model calling anything on the analyst's behalf did not
    exist. `dispatch()` (`tools/run/dispatch.go`) looks a call up by name,
    refuses one this console never registered, and refuses one
    `crew.RightsFor(analyst.Skills, analyst.State)` does not cover -- named to
    the model, journaled to the shared bus as a `tool_call` event, printed to
    the console -- before the tool's own function is ever reached.
    `charges_query`, the one tool whose argument the model writes as SQL
    rather than picks from a schema, is checked independently of the right
    gate, in two layers that do not trust each other: `tablesInSQL` walks the
    statement's own FROM/JOIN structure as a first, cheap pass, and
    `refuseUnknownTables` -- added after review of PR #20 found that the
    first pass alone was not enough to trust on its own -- tokenizes EVERY
    identifier the statement contains, in every quoting form SQLite accepts
    (bare, `"double"`, `` `backtick` ``, `[bracket]`), and refuses the
    statement if any one of them is the name of a real table or view this
    database currently has (`sqlite_master`, read fresh on every call, so a
    table added next month needs no code change) that is not `charges`,
    `drivers` or `attribution`. WITH (a CTE is a derived table by another
    name and this tool's three allowed tables need none), `sqlite_*`,
    `pragma_*` and a `main.`/`temp.` schema qualification are refused
    outright, unconditionally, wherever they appear in the statement, with
    no database round trip needed to know it. The text must be one `SELECT`;
    every write keyword, `;`, `--` and `/*` are refused outright. The
    statement runs on a SECOND connection SQLite itself keeps in
    `query_only` mode on every physical connection it opens
    (`internal/store.OpenReadOnly`, not a single `PRAGMA` run once against
    whichever connection a pool happened to hand back), a 5-second deadline,
    and the result is capped at 200 rows regardless of what the statement
    itself asked for.
    *(gate: `TestAToolTheAnalystHasNoRightForIsRefused`,
    `TestAnUnknownToolIsRefused`, `TestMissingRequiredArgumentIsRefused` for
    the dispatcher; `TestChargesQueryHostileInputs` (every hostile input
    B2-SPEC.md section 3.3 names, plus PR #20 review's own list -- a derived
    table followed by a comma-continued disallowed table, every quoting
    form, `main.`, `pragma_table_info(...)`, `sqlite_schema`, a CTE, all as
    subtests, each required to name its own reason) and
    `TestChargesQueryHostileInputsNeverTouchARow` (the same inputs run
    through the full tool against a store with a canary row in `analysts`,
    requiring the row unchanged and its marker absent from every result) for
    `charges_query`; `TestOpenReadOnlyRefusesAWrite` for the read-only
    connection on its own; `TestWrapWithLimitCapsTheStatement` and
    `TestRefuseUnknownTablesCatchesARealDisallowedTable` each isolate one
    layer directly, calling it rather than the full pipeline, because an
    end-to-end test alone cannot tell "this layer works" from "a different,
    redundant layer already caught it" -- which is exactly what happened
    once already (`wrapWithLimit`'s own mutant passed
    `TestChargesQueryResultIsCappedAt200Rows` outright the first time it was
    tried) and would have happened again here: `tablesInSQL` turned out to
    already catch every hostile input this file constructs, including the
    ones PR #20's review named as the reason to add `refuseUnknownTables` at
    all -- read `tablesInSQL`'s own comment for what its predecessor claimed
    about a comma-list continuing past a derived table, tested and found
    false. `TestATableNamedCTEPassesTablesInSQLButNotTheWithBan` is the one
    case that does isolate a real gap: a CTE named `charges` shadows the
    real table, `tablesInSQL` sees only the allowed name and finds nothing
    to refuse, and only the whole-statement WITH check stops a model reading
    fabricated numbers under the real table's name.
    `scripts/gates-have-teeth.sh` plants and catches six mutants: dropping
    the table allow-list, dropping `_query_only` from the read-only
    connection's DSN, dropping the semicolon refusal, dropping the row cap,
    dropping `refuseUnknownTables`'s own check, and dropping the
    whole-statement WITH check.)*

27. **An analyst's deliverable ends in options, never an action; a stamp is
    what applies one, and options in the SAME deliverable are alternatives,
    never independent actions.** `@yurii 2026-09-02`: "він має давати на
    вибір якісь певні рішення, які він вважає за потрібне спочатку
    супервайзеру, тобто головному агенту, а вже той має запитувати юзера,
    користувача, власника цих агентів, що робити далі." A fenced `options`
    block at the end of the body (`crew.ParseOptions`) names one to three
    classes from the SAME closed vocabulary `jobDescriptionBlock` already
    shows the model (`crew.ValidClassesFor`, the writing role's own
    `decides_alone` and `hands_up`); a class outside it is refused WHOLE at
    save time -- `crew.ValidateAndSaveOptions`, called from `saveDraft` --
    the deliverable keeps its prose and loses its options, the task is
    returned (`crew.Return` with `actorLink` `"supervisor"`, the class it
    decides alone), and `option_refused` is journaled with the reason, from
    inside that function so a caller cannot forget to. Naming NO options is
    refused the same way unless `crew.AllowsNoOptions` allows it, true only
    when the writing role's WHOLE vocabulary -- `ValidClassesFor`,
    `decides_alone` and `hands_up` together -- is empty or entirely prose
    (`commentary.variance`, `commentary.showback`, `forecast.project`);
    checking `decides_alone` alone let a role with a real `hands_up` list (a
    reporter's `explainer.publish`, `message.team`) skip the block on
    nothing.

    `internal/finops.Apply` is the one table (B3-SPEC.md section 3) that
    turns an option into a side effect, reusing the plane that already owns
    it (`anomaly.Explain/Dismiss/Accept`, `finops.Freeze/Close/Reopen`,
    `estate.InsertDriver`, extracted from `Seed`'s own inline insert so a
    second caller does not copy the column order by hand); three classes
    (`allocation.rule`, `budget.set`, `explainer.publish`) are recorded only
    for now, because the generic option shape carries no rule id, team,
    month or explainer id for them to act on, and inventing one would be
    exactly `commit money`'s neighbour on the never-list, "invent a number it
    was not given" -- see the PR body. Applying one option marks every OTHER
    live option of the SAME deliverable, and every live rival option of a
    DIFFERENT deliverable answering the same `anomaly.explain` question (next
    paragraph), `not_chosen` (`crew.LiveRivalsOf`, called from inside `Apply`
    so every caller -- the supervisor's own auto-apply, the owner's web
    route -- gets this for free, with no special-casing); `not_chosen` is
    free text, no schema change, and is the only state a rival option of an
    applied one ever reaches -- there is no `dropped` state anywhere in this
    file.

    `internal/finops.Supervise` is the supervisor's pass (`-supervise` on
    the runner, needs `-sprint`; a console button on the sprint page):
    collect the sprint's posted deliverables' open options, group them by
    their OWN deliverable (a group is decided together, never as independent
    rows), rank by saving then risk, and for each deliverable's top-ranked
    option apply it as the supervisor's own act when BOTH
    `MayDecide("supervisor", class)` allows it AND its `figure_cents` is at
    or under `roles.yaml`'s own `T.anomaly` threshold
    (`crew.ThresholdFor("T.anomaly")`, read from the roles data, never a
    literal and never the writing analyst's own guard headroom -- comparing
    a cloud figure against an LLM-spend guard compares units that do not
    compare); otherwise the WHOLE deliverable's options are carried into ONE
    decision request per owner per sprint (`crew.WriteDecisionRequest`
    rewrites the existing artifact rather than duplicating it), posted
    automatically once every carried option for that owner is answered.
    Nothing is ever dropped: a figure over `T.anomaly` is a key decision
    carried to the owner even for a class the supervisor's own job
    description would otherwise decide alone, and a contradiction -- two
    DIFFERENT deliverables' `anomaly.explain` options naming a different
    cause for the SAME anomaly, with different summaries -- is exactly
    `roles.yaml`'s own `hands_to_owner_conditions`, "any question two
    analysts answer differently on the same evidence": both sides are
    carried, addressed to the higher-ranked side's owner as ONE question
    naming the other analyst (`contradictionRouting`), never two requests
    and never a coin flip. Options of ONE deliverable never contradict each
    other -- they are the SAME analyst's own alternatives, a choice, not a
    disagreement -- and options of different classes are alternatives, never
    contradictions.

    The decision request's own body lists a multi-option deliverable's
    options as "choose at most one," not as a flat list, and carries a
    contradiction's note under the option it belongs to
    (`decisionRequestBody`). Its lapse date is a promise about ITSELF, never
    enforced: `WriteDecisionRequest` sets it once and a rewrite (a second
    pass, more options carried) leaves it exactly as it was
    (`crew.ExistingLapses` reads it back so the SAME pass that rewrites the
    body renders the SAME date) -- a promise "answer by X" whose X moved
    every rerun was the false promise heraldyx once made ("eventually times
    out") and had to retract. Once today is past that date the body says so
    ("Unanswered since X") rather than still inviting an answer ("Answer by
    X") -- `isStale`, a plain string comparison, because both are
    `"2006-01-02"` -- and heraldyx's and agent-passport's own sentences for
    `decision_requested` already say the same thing about themselves: "names
    a date after which it counts as lapsed; nothing enforces that date."
    There is no sweeper.

    Only the request's own owner, or an admin, may apply or refuse a carried
    option (`internal/web/decisions.go`'s `mayAnswerFor`, the same shape
    `roster.go`'s `mayManage` already holds for an agent's own owner);
    applying one marks the deliverable's other carried options `not_chosen`
    the same way `Apply` does for the supervisor's own act (same reason
    shape, `decided_by` the owner's own username); a refusal needs a reason,
    the same argument `Return` and `anomaly.Dismiss` already make, and
    refusing marks only that one option.

    *(gate: `TestADeliverableEndsInOptionsTheRoleMayName`,
    `TestAnOptionOutsideTheRoleIsRefusedAndReturned`,
    `TestAllowsNoOptionsIsFalseForAHandsUpOnlyRole`,
    `TestTheSupervisorDecidesItsOwnClassesAndCarriesTheRest`,
    `TestOnlyOneAlternativeOfOneDeliverableIsApplied`,
    `TestWhenTheTopRankedOptionIsHandsUpTheWholeChoiceIsCarried`,
    `TestAnOptionAboveTAnomalyIsCarriedNotApplied`,
    `TestARealAnalystsGuardNeverBlocksACloudFigure`,
    `TestApplyingOneCarriedOptionMarksItsSiblingNotChosen`,
    `TestOnlyTheOwnersStampAppliesAKeyDecision`,
    `TestADecisionRequestAsksOncePerOwnerPerSprint`, `TestARefusalNeedsAReason`,
    `TestContradictingOptionsAreCarriedAsOneQuestion`,
    `TestAgreeingOptionsOnTheSameAnomalyAreNotLinked`,
    `TestOptionsWithinOneDeliverableNeverContradict`,
    `TestASecondPassKeepsTheFirstLapseDateAndMarksItStale`,
    `TestOptionsBlockHostileInputs` (not JSON, an undefined class, a negative
    figure, 50 options, a 1 MB block, a string where an integer belongs), and
    `TestAScriptTagInAnOptionSummaryRendersAsText` for the seventh hostile
    input (rendered as text because `optionView`'s fields are never wrapped
    in `template.HTML`, unlike `Rendered`). `scripts/roles-are-bound.sh`
    holds every class `internal/finops/apply.go`'s table names to
    `roles.yaml`, the same generic `// class:` scan property 1 already
    applies across `internal/` and `tools/`, extended in effect by tagging
    this file's own switch rather than by a second, redundant check.
    `scripts/gates-have-teeth.sh`'s `options: an analyst's Post applies an
    option` case is B3-SPEC.md section 6's own words for the fault it
    plants: `work.go`'s `artifactAction("post")` marking an option applied
    alongside `crew.Post`, using only `internal/crew` (already imported
    there) because the property is that POSTING touches no option at all,
    not that a specific side effect ran. Three more mutants were planted by
    hand and reverted rather than kept as permanent cases, each named with
    the test that caught it in review of this feature's first version:
    applying every alternative of one deliverable instead of just the
    top-ranked one (`TestOnlyOneAlternativeOfOneDeliverableIsApplied`),
    keying the contradiction check by ordinal instead of artifact identity so
    one deliverable's own alternatives read as a contradiction with each
    other (`TestOptionsWithinOneDeliverableNeverContradict`), and dropping
    the `T.anomaly` check so an over-threshold figure applies silently
    (`TestAnOptionAboveTAnomalyIsCarriedNotApplied`).)*

28. **A fixture driver carries its desk in `Source`, the field every reader
    filters on.** `world.Drivers()` once wrote "planted fixture, event E02"
    there while `driversSection` (the packet) filters on `Source == desk`,
    so the "Drivers on this service and desk" section was empty for every
    seeded anomaly in every live run, and no test noticed because every
    packet test planted its own driver row with the desk already right.
    `@yurii 2026-09-02` found it reading the code. The live apply path
    (`internal/finops.applyDriver`) always wrote the desk; the fixture now
    does too, and the packet is proven non-empty on the seeded estate itself.
    *(gate: `TestEveryFixtureDriverCarriesItsDesk` (every fixture driver's
    `Source` is one of the fixture's desks) and
    `TestTheSeededFixtureDriversReachThePacket` (E02's driver reaches
    `driversSection` and `packet()` after `estate.Seed`), both red on the
    old code. `@claude` 2026-09-02: both now live in
    `internal/deliver/fixture_drivers_test.go`, unrenamed, calling
    `driversSection` and `Packet(..., false)` in their new home -- B7 moved
    the packet builder there (invariant 29) after this invariant's own PR
    merged; the two names above are otherwise exactly as landed.)*

29. **A bench never hands an analyst the answer it is about to be scored
    against, and it never writes the thing it measures.** B7-SPEC.md.
    `internal/crew`'s investigator role owes "the cause, named, or 'none
    established' in those words" (`roles.yaml`'s own `owes` line); the
    generated fixture's registry knows the true cause of every planted
    driver event, which is what makes a number possible at all --
    `tools/bench` runs an analyst (or its own mock engine) on N such
    anomalies with the driver's label and kind stripped from the packet,
    scores whether the deliverable names the service, the day, the kind and
    a cause matching that label, and never inserts a task, an artifact, a
    charge or a journal row while doing it. `-live` is refused with either
    mock engine and, without it, any other engine is priced at that model's
    published rate and refused before a call, never after one.

    The packet builder moved to `internal/deliver` to make this possible at
    all: Go refuses to import a second `package main`
    (`import ".../tools/run" ... is a program, not an importable package`),
    so `tools/bench` could not otherwise build the exact packet
    `tools/run` sends. `Packet`'s new `hideDriver` boolean is the only
    behavioural change; `tools/run/packet.go`, `mandate.go`, `main.go` keep
    their old unexported names as one-line wrappers, so no other call site
    or test in that package changed.

    `call()` did NOT move, and `tools/bench` holds no caller of its own
    either, which a first version of this change got wrong: it read a key
    from the environment and spoke to the Anthropic wire directly, no
    gateway involved, exactly the second money path B6 closed by putting
    TokenFuse in `tools/run`'s own call path in the first place. Caught in
    review before merge (coordinator pass on PR #25, 2026-09-03) and
    removed rather than gated: `-live` with any engine but the two mocks
    now refuses outright, unconditionally, with the sentence "the bench is
    not wired through the TokenFuse gateway yet; a live run waits for the
    shared caller (one call path for tools/run and tools/bench, in
    internal/deliver)" -- naming the eventual fix without attempting it
    here. `tools/bench` holds no `net/http` import, no model-provider
    credential read, anywhere.
    *(gate: `TestBenchPacketHidesTheDriverLabelAndItsKind`,
    `TestBenchPacketHidesTheDriverOnTheOtherKnownCase` (`internal/deliver`,
    both real fixture cases: gcp/GKE and onprem/Batch cluster),
    `TestBenchWritesNothingToTheEstate` (`tools/bench`: every table's row
    count and the journal file itself, before and after a full scoring
    run), `TestLiveWithARealEngineRefusesUntilTheSharedCallerExists` (the
    refusal fires before the store opens) and
    `TestNoFileInThisPackageCanMakeAnHTTPRequest` (every non-test file
    under `tools/bench`, scanned for the literal substrings, the same way
    `tools/run/main_test.go`'s `TestThisBinaryCannotSpend` proves it about
    `main.go`). `TestScoreJudgesTheNamedCauseNotTheWholeBody` holds the
    companion property a hidden driver would otherwise make pointless: the
    scorer judges the EXTRACTED named cause, not whether the label's own
    words appear anywhere in the deliverable's body, so an analyst cannot
    score a match by echoing its own task description back.
    `scripts/gates-have-teeth.sh` plants and catches three mutants, named in
    B7-SPEC.md section 5: leaving the `driver:` line and the drivers
    section in a hiding-mode packet, scoring cause by substring of the
    whole deliverable instead of the named cause, and summing a run's cost
    from cents rounded per case instead of micro-dollars rounded once at
    the total (the `finest-unit-per-row-round-once-at-the-aggregate`
    principle invariant 25 already holds for `ai_calls`). `ensureSeeded`
    itself carries the same shape of fault, caught the same review pass:
    it ran detection against ANY store, seeded by this call or not, so an
    existing store (a live console's own data, or charges newer than
    whatever detection last ran) gained anomaly rows it never asked for.
    `estate.Seed`'s own return value now says whether this call created
    the charges; detection and roster DATA run only then, while both
    schemas (`crew.Schema`, `crew.RosterSchema`) are created unconditionally
    because CREATE TABLE IF NOT EXISTS adds no row, proven by
    `TestBenchDoesNotDetectAgainstAnExistingStore`: an existing store's own
    anomaly count, before and after a bench run against it, equal.)*

30. **Memory is appended last, and the cap cuts from the end, so memory is
    always what yields first, never the anomaly, the series, the drivers,
    the team's month or the last posted explanation that precede it.**
    B8-SPEC.md section 2. An analyst's packet now also carries its OWN last
    three posted deliverables on this desk (`ownHistorySection`), newest
    first, each with the task it answered, the first 240 bytes of its body
    and the fate of every option it ended in ("applied by X", "refused by X:
    reason", "not chosen (reason)", "still waiting on X" for a carried
    option, "open" otherwise); and `driversSection`'s own window widened
    from 90 days to 180 ("last six months"), capped at 24 rows, newest
    first, with a trailing "and N more" line when it is cut, so an
    unbounded registry cannot itself grow past the packet's own bound. The
    order this is held by is the WHOLE mechanism, and it is one line:
    `Packet()` appends `ownHistorySection` after every other section, and
    `BoundBytes` cuts bytes off the END of the joined sections once the 12
    KiB cap is reached, so whatever is appended last is trimmed first, and
    everything appended earlier is untouched unless that alone is not
    enough. Nothing else about the cap changed; making this order explicit,
    in the one place it is decided, is this invariant. `ownHistorySection`
    is skipped ENTIRELY, not merely trimmed, when `hideDriver` is true: a
    past posted deliverable's own option can name the very driver a bench
    run is hiding (a recurring cause explained before is exactly the case
    memory exists for), so memory of past answers on the same desk is
    itself an answer key. Coordinator review of PR #27 found both this and
    `waitingOwner` (below) reading the wrong table.
    *(gate: `TestOwnHistoryNeverCrowdsOutTheAnomalyUnderTheCap`
    (`internal/deliver`), which forces a packet over 12 KiB on a real
    anomaly and requires the anomaly section whole, the history section's
    newest entry present, its oldest entry missing, and the packet ending in
    the truncation note. `TestOwnHistoryShowsTheAnalystsOwnLastPostedDeliverable`,
    `TestOwnHistoryShowsExactlyThreeNewestFirst`,
    `TestOwnHistoryHidesAnotherAnalystsDeliverableOnTheSameDesk`,
    `TestOwnHistoryHidesTheSameAnalystsDeliverableOnAnotherDesk` and
    `TestOwnHistoryShowsTheFateOfEveryOptionState` hold the section itself;
    `TestDriversSectionReachesOneHundredTwentyDays` and
    `TestDriversSectionCapsAtTwentyFourWithAndNMore` hold the widened
    window; `TestBenchHidingModeOmitsOwnHistoryEntirely` holds the hiding
    case, planting a past option that names the current anomaly's own
    driver and requiring it absent from a hidden packet and present in a
    shown one. `scripts/gates-have-teeth.sh` plants four mutants, named in
    B8-SPEC.md section 4: own history shown for any author, not scoped to
    the analyst; an option's fate line dropped; the drivers window kept at
    90 days; and memory prepended instead of appended, so it is protected
    rather than trimmed first.)*

31. **No clock-driven run spends without the console's switch AND the
    ceiling, both a person's act.** B5-SPEC.md. The cluster ships
    `costcrew-crew` as a suspended CronJob (`stack-k8s/manifests/49-costcrew.yaml`)
    and stack-single has no routine for it at all today (`@claude`
    2026-09-03: read looking for one, per the spec's own instruction to read
    it; none exists, contrary to what a cursory read of that manifest's
    comment history might suggest -- the comment there only cites
    `costcrew-crew` as PRECEDENT for `idryx-detect`'s own shape). Flipping
    stack-k8s's `suspend: true` is a platform act and stays one; this
    invariant is about the SECOND switch, inside the store, that a routine
    firing on the platform's clock still has to clear. `tools/run -due`
    refuses unless `crew.CadenceSettings` reads `cadence.enabled` on, re-read
    a second time immediately before creating or running anything (a person
    switching it off between the two must still stop the run), and never
    creates a sprint or a task when it refuses. The ceiling it runs under is
    the SMALLER of `-ceiling` and the console's own `cadence.ceiling_cents`
    (default 0, which is off by another name), named together in every
    refusal so a person reading the log knows which one bound it. `/cadence`
    is the only writer of the switch (`crew.SetCadence`, journaled as
    `cadence_set` with the actor, the same way `budgets_set` already is); the
    runner only ever reads it. The due list itself comes from
    `crew.CadenceDue`, exported from `plan.go`'s own `proposeCadenceDue` so
    the plan, the console page and the runner route through one function and
    cannot disagree about what is due, and pricing (`deliver.WorstCaseMicros`,
    `deliver.EstimateWorstCase`) is likewise one shared formula so the
    console's preview price and the runner's own preflight price cannot
    drift apart -- `internal/web` cannot import `tools/run` to call its
    `price()` directly, the same "package main" restriction B7 already
    documents for `internal/deliver`.
    *(gate: `TestDueWithTheSwitchOffExitsTwoAndCreatesNothing`,
    `TestDueRefusesBeforeAnyCallWhenWorstExceedsTheCeiling`,
    `TestDueDryRunPrintsAndWritesNothing`, `TestDueLiveCreatesRunsAndEmitsCrewRan`,
    `TestASecondDueLiveTheSameDayIsIdempotent`,
    `TestDueRunsWhenTheCeilingExactlyEqualsTheWorstCase`,
    `TestDueRefusesWhenCadenceCeilingCentsIsZero`,
    `TestDueRefusesANegativeCeilingFlag`, `TestDueOnGarbageSettingsRefusesAsOff`,
    `TestDueRefusesWhenTheSwitchIsFlippedOffBetweenPreflightAndExecution` and
    `TestCrewRanCostSumsMicrosBeforeAnyRounding` in `tools/run`;
    `TestCadenceDueMatchesWhatProposeAlreadyProducesForCadence`,
    `TestCadenceDueNeverListsAnOnRequestAnalyst`,
    `TestCadenceSettingsDefaultsToOff`, `TestSetCadenceThenCadenceSettingsRoundTrips`,
    `TestSetCadenceRefusesANegativeCeiling` and
    `TestCadenceSettingsOnGarbageReadsAsOff` in `internal/crew`;
    `TestCadenceGETRendersTheSwitchAndTheDueList`,
    `TestCadencePOSTFlipsTheSwitchAndJournalsTheActor`,
    `TestCadencePOSTRefusesAViewer`, `TestCadencePOSTRefusesANegativeCeiling`
    and `TestCadenceShowsTheLastThreeCrewRanEvents` in `internal/web`.
    `scripts/gates-have-teeth.sh`'s `due: skip the switch check` case plants
    the mutant named in B5-SPEC.md section 7 on the SOURCE LINE both the
    preflight and the re-read share (`if !enabled {`, disabled in both
    places at once, because disabling only one leaves the other catching
    it); the other three named mutants -- comparing the worst case against
    `-ceiling` alone, rounding cost per task before summing, and emitting
    `crew_ran` before the run instead of after -- were planted by hand and
    reverted rather than kept as permanent cases, each caught by one of the
    tests above, the same shape invariant 27's own history already
    describes for this repository.)*

33. **The optimizer's own packet ranks its recommendations by saving, never
    by the size string, and names the risk a short lookback carries.**
    C5-SPEC.md (numbered 33, not 32: B6B-SPEC.md's own invariant landed on
    `main` as 32 while this branch was in flight, after this branch's fork
    point -- see the PR body's "Invariant numbering collision" section).
    The providers already publish their own rightsizing and idle
    recommendations for free, and this step's whole surface is reading
    them: three readers (`aws-rightsizing`, Cost Explorer's own Rightsizing
    Recommendations CSV; `gcp-recommender`, a Recommender export;
    `azure-advisor`, Advisor's own cost recommendations CSV, which reports
    its saving as POTENTIAL ANNUAL cost and is divided by twelve, rounded
    half away from zero, the same convention `money.Parse` already uses)
    land in one shared `recommendations` table, and `deliver.recommendationsSection`
    reads it back for the optimizer families' own packet
    (`optimizer-aws/gcp/azure/onprem`, skill `rightsizing-analysis`). No
    model is called by this step and `infra.change` never enters the apply
    table, which invariant 23 already owns to nobody in the crew; this is
    read-only, all the way through.

    Ranked by `MonthlySavingCents`, descending, ties broken by resource so
    the same estate renders the same list every time (invariant 7) rather
    than on map or file order. A row's own lookback carries the sentence
    `roles.yaml`'s optimizer family already writes for itself in its own
    `owes` text ("a monthly job looks idle to a fourteen-day window")
    whenever that lookback is fourteen days or fewer, and stays silent
    about the risk otherwise. Top ten, with a trailing "and N more".

    The comparator itself is `connectors.RankBySaving`
    (`internal/connectors/rightsizing.go`), called by BOTH
    `deliver.recommendationsSection` and web's `/rightsizing` page, which
    wants the identical order ("a person reading this page and an analyst
    reading its packet see the same list in the same order"). It did not
    start that way: coordinator review of PR #34, 2026-09-03, found that
    the page carried its own separately-maintained copy of this same
    comparator, and that `scripts/gates-have-teeth.sh`'s "rank by current
    cost" case (below) only ever mutated the `internal/deliver` copy --
    proven by planting the identical mutation directly in the page's own
    copy by hand and running `go test ./internal/web/...`, which passed
    clean, since no test in that package checked row order at all, only
    substring presence. `@claude` 2026-09-03: two callers wanting the same
    order is not a reason for two copies of a comparator, so now there is
    one, `RankBySaving`, and one gate on it protects both callers by
    construction rather than by each being separately remembered.
    *(gate: `TestRankBySavingOrdersDescendingWithResourceTiebreak` in
    `internal/connectors`, `RankBySaving`'s own direct proof, isolated from
    any DB or HTTP surface; `TestRecommendationsSectionRanksBySavingFromAFixtureImport`,
    `TestRecommendationsSectionFlagsShortLookbackNotLong`,
    `TestRecommendationsSectionCapsAtTenWithAndNMore`,
    `TestRecommendationsSectionEmptyForADeskWithNoImports`,
    `TestPacketIncludesRecommendationsOnlyForRightsizingAnalysis` in
    `internal/deliver`; `TestAWSRightsizingIsRead`, `TestGCPRecommenderIsRead`,
    `TestAzureAdvisorIsRead`, `TestAzureAnnualToMonthlyRoundsHalfAwayFromZero`,
    `TestEmptyFileIsZeroRows`, `TestImportIsIdempotentForRightsizing`,
    `TestTestDescribesRightsizingWithoutWriting`,
    `TestRecommendationsFiltersByDesk` and `TestHostileRightsizingInput`
    (an unknown header set; a negative saving; a resource id and a field
    each carrying an embedded quote or comma; a UTF-8 BOM; CRLF line
    endings; a 100 MB line, memory measured before and after) in
    `internal/connectors`; `TestTheRightsizingPageReadsARealImport`,
    `TestTheRightsizingPageStartsWithNoneImported` and
    `TestTheRightsizingPageOrdersRowsBySavingNotBySize` (the row-order
    proof the page's own gap needed) in `internal/web`, the same
    end-to-end shape `TestTheAIPageReadsARealImport` already holds for
    `tokenfuse-focus`; `/rightsizing` is also now on
    `TestPagesRenderTheSameTwice`'s own curated path list
    (`internal/web/owners_test.go`), which it was missing from before.
    That a reader earns `Built` only by being registered is invariant 22's
    own gate, `TestBuiltMeansAReaderExists`, unchanged and already proving
    it for these three entries too, because `Status` is derived from the
    `readers` map in exactly the one place invariant 22 describes.
    `scripts/gates-have-teeth.sh` plants the mutant C5-SPEC.md names by its
    own words, "rank by current cost": the comparator inside
    `RankBySaving` swapped from comparing `MonthlySavingCents` to
    comparing `Current` (the resource's own size string, e.g.
    "m5.2xlarge") -- Go allows `>` on strings, so this still compiles, and
    the list silently reorders itself alphabetically by instance type
    instead of by money; caught now by all three of
    `TestRankBySavingOrdersDescendingWithResourceTiebreak`,
    `TestRecommendationsSectionCapsAtTenWithAndNMore` and
    `TestTheRightsizingPageOrdersRowsBySavingNotBySize` (each
    `@measured` 2026-09-03 by hand, PR report has the transcripts), any one
    of which going toothless should still be caught by the other two.)*

## Decisions that have no gate yet

Written here so that "it holds" and "something holds it" stay different
sentences.

- **The console never reaches the network.** True today and checked by reading:
  the only outbound HTTP client in the repo is `internal/enforce`, which is a
  separate binary the console never calls, and the stack integrations are all
  behind flags that default to off. *(not enforced: nothing would catch a
  handler that grew a client.)*

- **Money is integer cents.** `money.Cents` is an `int64` and `Float()` exists
  only for presentation. *(not enforced: nothing stops a new float from being
  summed, and `TestFloatSumIsNotOrderIndependent` demonstrates why that
  matters rather than preventing it.)*

- **The agent-event stream is never fabricated.** Nothing derived is written
  into it: the card's history panels read the BOARD instead, because that
  stream is read by trailryx, heraldyx and idryx, and several hundred
  reconstructed lines with months-old dates would arrive at the end of an
  append-only record as though they were new. *(not enforced.)*

- **A viewer reads the account list and is served no controls.**
  `@yurii 2026-08-23`, asked directly: "лишай список акаунтів". The list is not
  a secret in a console where everyone with an account is a colleague. The
  CONTROLS are gated (`TestWhatAViewerCanRead`); the reasoning for showing the
  list is a decision, not an invariant.

## Standing rule

Anything written here that is not marked `@yurii` or `@measured` is `@claude`:
my reading, and re-checkable. Before writing that an invariant is held, open
the check and confirm it asserts what the sentence says. Invariant 5 said
"every write route" for a long time while one route of thirty-three was
covered, and the sentence was written before anybody counted.

## Conventions

- Comments say WHY, and name the failure that made the line necessary. Most of
  this codebase's comments are a record of something that went wrong once.
- A page states its own limits: what a figure does not include, what a gate
  does not cover, what a number is not comparable to.
- Report the slack, not the verdict. "It fits" once hid 18px.
