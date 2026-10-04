# language: en

Feature: The crew's live model calls go through the estate's gateway, named

  TokenFuse is a drop-in proxy that speaks the Anthropic Messages API and,
  since 2026-09-07, the OpenAI chat-completions API through a second
  gateway process (one process forwards one wire shape). It
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

  # @test:TestAnEngineNoConfiguredGatewayFrontsRefusesTheRunBeforeTheFirstCall
  Scenario: An engine no configured gateway fronts is refused, never sent direct
    Given a run with a gateway configured and some tasks on openrouter or
      bedrock, which that gateway does not front
    When the run starts
    Then it is refused before the first call, naming how many tasks are on
      which engine and the setting that would give them a route, because a
      run pointed at a metering gateway that kept spending outside it, with
      only a note beside the bill, is the fault this scenario replaces

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

  # @test:TestAnOpenRouterTaskIsNeverSentDirectWhileAGatewayIsConfigured
  Scenario: An openrouter task is never sent to openrouter.ai while a gateway is configured
    Given a gateway configured that fronts only the Anthropic wire, and a
      task on the openrouter engine
    When the task runs
    Then nothing is sent to openrouter.ai and nothing to the Anthropic
      gateway, the task is refused with the reason, and no money is
      reserved or booked

  # @test:TestAnOpenRouterTaskThroughTheOpenAIGatewayIsChargedItsSettlementPerRound
  Scenario: An openrouter task goes through the gateway that fronts the OpenAI wire
    Given -gateway-openai pointing at a gateway whose upstream is OpenRouter
    When a task on the openrouter engine runs two rounds
    Then both rounds are posted to that gateway's /v1/chat/completions with
      the same run id, agent id and budget headers an Anthropic round
      carries, and the task is charged the gateway's settlement of each
      round, summed

  # @test:TestCallOpenRouterCarriesTheGatewaysSettlementOnItsResult
  Scenario: The settlement on the OpenAI door is the charge
    Given a single-shot openrouter call through the OpenAI-shaped gateway
    When the gateway answers with its three settlement headers
    Then the result carries the settled cost, the run's total and the price
      basis, and the charge is the settlement and not the caller's own price

  # @test:TestA402FromTheOpenAIGatewayStopsTheRunAsARefusal
  Scenario: A budget refusal from the OpenAI-shaped gateway stops the run
    Given an openrouter task whose call the OpenAI-shaped gateway refuses
      with 402
    When the task runs
    Then it is a refusal that stops the run, the whole reservation comes
      back, and the gateway's own reason is printed

  # @test:TestHostileSettlementHeadersOnTheOpenAIDoorNeverBecomeACharge
  Scenario: A hostile settlement on the OpenAI door is not a charge
    Given settlement headers that are signed, not numbers, duplicated, above
      the cap or a megabyte long, on an openrouter call through the gateway
    When the response is read
    Then nothing panics and none of them becomes a charge

  # @test:TestACallWithNoGatewayRouteForItsEngineIsRefusedBeforeAnyRequest
  Scenario: With any gateway configured, a call with no route is refused before any request
    Given only an OpenAI-shaped gateway and an anthropic call, or only an
      Anthropic-shaped gateway and an openrouter call
    When the call is made
    Then it is refused by type before any request is built, and nothing
      reaches any host

  # @test:TestABedrockCallWithAGatewayConfiguredIsRefused
  Scenario: Bedrock has no gateway route and is refused when a gateway is on
    Given both gateways configured and a bedrock call
    When the call is made
    Then it is refused, because bedrock speaks neither wire TokenFuse fronts

  # @test:TestWithNoGatewayAtAllEveryEngineStillGoesDirect
  Scenario: With no gateway configured at all, nothing changes
    Given neither -gateway nor -gateway-openai
    When any engine's call is routed
    Then it goes direct exactly as before, with none of the x-fuse headers

  # @test:TestLiveOpenRouterWithOnlyAnAnthropicGatewayRefusesBeforeTheStoreOpens
  Scenario: The bench refuses an openrouter -live with no gateway that fronts it
    Given the bench's -live with the openrouter engine and only -gateway set
    When the bench starts
    Then it refuses before the store opens, naming -gateway-openai

  # @test:TestAskPlanForAnOpenRouterSupervisorGoesThroughTheOpenAIGateway
  Scenario: The supervisor's planning call on openrouter goes through the OpenAI-shaped gateway
    Given a supervisor hired onto openrouter and -gateway-openai configured
    When the supervisor's plan is asked for
    Then the call is posted to that gateway under the supervisor's own agent
      id and the gateway's settlement is what the month's spend books

  # @test:TestAnAnthropicTaskWithOnlyAnOpenAIGatewayIsRefusedAndNeverGoesDirect
  Scenario: An anthropic task with only an OpenAI-shaped gateway is refused too
    Given -gateway-openai set and -gateway not, and a task on the anthropic
      engine
    When the task runs
    Then nothing is sent to api.anthropic.com and the task is refused with
      the reason
