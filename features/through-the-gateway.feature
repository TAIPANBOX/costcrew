# language: en

Feature: The crew's live model calls go through the estate's gateway, named

  TokenFuse is a drop-in proxy that speaks the Anthropic Messages API. It
  refuses a call with no run id, meters what it answers, and can refuse a
  call that would cross a budget it was told. Item B6 of the plan: when
  -gateway is set, the Anthropic route posts through it instead of
  api.anthropic.com, carrying the run id and the analyst's identity on every
  call, so TokenFuse's trace and the estate's own bus name the same agent for
  the same work.

  # @test:TestAGatewayCallCarriesTheAnalystsIdentity
  Scenario: A call carries the analyst's name
    Given a runner pointed at a gateway, with the estate bus also configured
    When it makes a call for one task
    Then the request carries the run id, the analyst's agent id and a budget,
      and the agent id is the exact one the bus event for that same call
      carries, because a trace and a bus that could name two different agents
      for one call is the fault this console exists to catch in other
      people's data

  # @test:TestA402FromTheGatewayReturnsTheReservationAndNamesTheBudget
  Scenario: A refusal by the gateway returns the reservation
    Given a call whose worst case is within the run's own ceiling
    When the gateway answers 402 because it would cross the budget it was told
    Then the run stops the way it stops on its own ceiling check, the whole
      reservation comes back, and the sentence printed says the GATEWAY
      refused it and names the budget and what was already spent

  # @test:TestA402WithANonJSONBodyStillProducesAReadableRefusal
  Scenario: A malformed refusal still reads as a refusal
    Given a 402 whose body is not the documented JSON shape
    When the runner reads it
    Then it still stops the run with a readable sentence, never a panic and
      never a silently swallowed error, because the gateway's body is input
      from a process this runner does not control

  # @test:TestEnginesTheGatewayCannotFrontAreCalledDirectAndSaidSo
  Scenario: An engine the gateway cannot front is called direct and said so
    Given a run with -gateway set and some tasks on openrouter or bedrock
    When the run starts
    Then those calls still go straight to their own host, and the run prints
      one line naming how many, because TokenFuse speaks the Anthropic
      Messages API and nothing OpenAI-shaped yet, and that must be said
      rather than happen quietly

  # @test:TestWithNoGatewayTheRequestGoesToAnthropicDirectly
  Scenario: With no gateway, nothing changes
    Given -gateway is never set
    When the Anthropic route builds its request
    Then it is built for api.anthropic.com exactly as before, with none of
      the x-fuse-* headers on it at all

  # @test:TestAGatewayRequestCarriesTheThreeHeadersAndNeverInventsAParent
  Scenario: A parent run id is sent only when the runner actually has one
    Given a gateway call with no parent run configured
    When the request is built
    Then x-fuse-parent-run-id is absent rather than invented, and
      x-fuse-outcome is never sent at all, because this step has nothing
      worth reporting there yet

  # @test:TestNormalizeGatewayRefusesANonHTTPURL
  Scenario: -gateway accepts only http or https
    Given a -gateway value that is not an http(s) URL
    When the runner starts
    Then it refuses before any call is attempted, rather than surfacing as a
      confusing dial error on the first task

  @measured 2026-09-02, ghcr.io/taipanbox/tokenfuse:v0.4.1 stub via docker, `tokenfuse focus-export`
  """
  One task, through the real container: the gateway's own FOCUS trace named
  ResourceId and x_agent_id agent://gcp.taipanbox.local/partner-gcp,
  SubAccountId and x_run_id crew-1788359462, billed 0.052500 USD on the
  stub's fixed 1000/500 tokens, x_blocked false, x_parent_run_id and
  x_outcome both empty exactly as this step leaves them. A second call at a
  higher token cap, same 0.05 ceiling, hit the gateway's own budget check and
  came back 402, which the runner reported as "the gateway refused this
  call: per-run budget exceeded" and returned the whole reservation.

  The stub answers every call with a fixed {"stub":true,"usage":{...}}, no
  content array at all, which this runner's own response reader (unrelated
  to this change) reads as no deliverable rather than as free text. So this
  particular call wrote no bus line: saveDraft never runs on a call this
  runner itself did not accept. The agent id TokenFuse recorded on its own
  side is the exact string the same code path writes to the bus, which
  TestAGatewayCallCarriesTheAnalystsIdentity checks against a server that
  does answer with text, on every run of the suite.
  """

  # @test:TestGatewayBudgetUSDIsTheTighterOfCeilingAndTaskGuard
  Scenario: The budget named is the tighter of the run and the task
    Given a run ceiling and a task guard that differ
    When the header for one call is built
    Then it names whichever of the two is smaller, because sending the wider
      one would let the gateway wave through a call this runner's own
      reservation would already have refused
