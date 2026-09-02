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
runtime to install, no network. Money is integer cents everywhere.

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
go test ./...                        # 282 tests, 18 packages
./scripts/gates-have-teeth.sh        # 48 cases; needs a clean tree; ~60s
./scripts/features-are-bound.sh      # 59 scenarios, both directions
./scripts/roles-are-bound.sh         # internal/crew/roles.yaml against the code and the roster, both ways
./parity/gate-has-teeth.sh parity/captures/golden
gofmt -l . && go vet ./...
```

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
   *(gate: `TestEveryRouteRequiresASession`, which walks all 48 GET routes
   registered in `server.go`. Its regexp is anchored to the registration and
   not to the string `HandleFunc`, because it once fired on a route named in a
   COMMENT, and a gate that fires on prose gets deleted the first week.)*

2. **A viewer may read and export, and may write nothing.**
   *(gate: `TestAViewerCannotWrite`, all 30 write routes against a real viewer
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
   *(gate: `TestEveryWriteRouteChecksCSRF`, 30 routes with an empty token and
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
