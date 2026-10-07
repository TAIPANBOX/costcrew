# language: en

Feature: Every gateway call says whose money it spends, so the crew's owner reaches the control plane

  @claude 2026-10-07
  costcrew#73, measured on the appliance proving run of 2026-09-17: the
  crew's calls carried the run id, the agent id, the budget and (when one
  existed) the parent run, and never the person the agent belongs to. The
  control plane folds an owner from the first user:// entry of the
  x-fuse-on-behalf-of chain, so a crew run reached its owner view as
  "unassigned" while the roster named an owner for every agent. The ask is
  that the runner send that chain on every call, with the owner as the root
  and the analyst after it, and refuse a task whose analyst has no owner
  rather than send an empty chain. The words below paraphrase the issue; no
  quote of anybody is in them. The same ask is extended here to the bench and
  to the console's own planning call, which spend through the same door.

  @test:TestTheChainIsTheOwnersUserRootThenTheAnalystsAgent
  Scenario: The chain is the owner as a person, then the analyst as the actor
    Given an analyst whose owner on the roster is alice, on the installation
      gcp.taipanbox.local
    When a call is made for that analyst through the gateway
    Then x-fuse-on-behalf-of reads the user alice at that installation, a
      comma, and the analyst's own agent id at the same installation, root first

  @test:TestEveryRoundOfAnAnthropicTaskCarriesTheAnalystsOwner
  @test:TestEveryRoundOfAnOpenRouterTaskCarriesTheAnalystsOwner
  Scenario: Every round of a task carries the owner, on either wire
    Given a task that asks for a tool before it answers, so it makes two calls
    When the gateway that fronts the engine's wire sees them
    Then both requests carry the owner chain, on the Anthropic wire and on the
      OpenAI-shaped one

  @test:TestTheToolLoopAndDeliverCallSendTheSameFuseHeaders
  Scenario: The tool loop and the one door send the same set of headers
    Given the tool loop's own request and deliver.Call's request, built for the
      same call and sent to a gateway that records them
    When the two recorded header sets are compared
    Then they are identical, the owner chain included, on both wires, because
      a second copy of the header code is how this one was missed

  @test:TestWithAGatewayAnOwnerlessAnalystsTaskIsRefusedWhenItIsPriced
  @test:TestAnUnclaimedRosterOwnerIsNoOwner
  Scenario: A task whose analyst has no owner is refused when it is priced
    Given a gateway is configured and an analyst has no owner, or only the
      placeholder a roster carries when no owner was ever named
    When the task is priced, in a dry run or a live one
    Then the verdict names the analyst and says it has no owner, and the task
      is not priced to run; with no gateway configured nothing is refused

  @test:TestAnAnalystWithNoOwnerIsRefusedBeforeAnyCall
  @test:TestARunSkipsAnOwnerlessAnalystsTaskAndStillRunsTheOthers
  Scenario: Nothing is sent for an analyst with no owner, and the rest of the run goes on
    Given a run with one task whose analyst has an owner and one whose analyst has none
    When the run is made against a gateway that records its requests
    Then the ownerless analyst's task never reaches the gateway, nothing is
      reserved for it, and the other task runs

  @test:TestHostileOwnersCannotForgeOrBreakAChain
  @test:TestAHostileOwnerStillGivesTheGatewayExactlyTwoEntries
  @test:TestAChainTheGatewayWouldSilentlyIgnoreIsRefusedHere
  Scenario: An owner name cannot add, replace or forge a chain entry
    Given an account name holding commas, spaces, control bytes, non-ASCII
      text, a second user:// root or a forged agent, or one thousands of bytes long
    When it is made the root of the chain
    Then the header the gateway splits has exactly two entries, the first a
      user:// root that decodes back to the owner, the second the analyst; a
      chain longer than the gateway would silently ignore is refused here

  @test:TestAGatewayCallWithNoOwnerChainIsRefusedBeforeAnyRequest
  Scenario: The one door refuses a gateway call that names nobody
    Given a gateway call with a run and an agent but no owner chain
    When it reaches the door that every call passes
    Then it is refused on both wires before any request exists, and the
      refusal names the agent

  @test:TestLiveSendsTheAnalystsOwnerOnEveryCase
  @test:TestLiveRefusesBeforeAnyCallWhenACasesAnalystHasNoOwner
  Scenario: The bench names the owner too, and refuses whole when one case cannot
    Given a live bench run through a gateway
    When each case is scored
    Then each request carries the owner chain of that case's analyst, and a run
      with an ownerless analyst among its cases is refused before the first
      call is made, not after the cases before it were billed

  @test:TestThePlanAskNamesTheAskingPersonAsTheRoot
  @test:TestAHostileUsernameCannotReshapeThePlanAsksChain
  Scenario: The console's planning call names the person who asked, then the supervisor
    Given a person who is not the roster's owner asks the supervisor for a plan
    When the console makes the call through the gateway
    Then the chain reads that person as the root and the supervisor as the
      agent, and a username holding commas still gives exactly two entries
