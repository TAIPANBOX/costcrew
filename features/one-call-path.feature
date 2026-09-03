# language: en

Feature: The runner and the bench spend through one door

  @claude 2026-09-03
  """
  the bench is not wired through the TokenFuse gateway yet; a live run waits
  for the shared caller (one call path for tools/run and tools/bench, in
  internal/deliver)
  """

  The sentence above is the bench review's own refusal (PR #25, coordinator
  review, 2026-09-03), quoted because it is the closest thing this step has
  to a mandate: B6 put the TokenFuse gateway in tools/run's own call path so
  every crew call is metered per agent, and the bench had grown a second,
  private caller with a key read from the environment and no gateway --
  exactly the hole B6 had just closed. There is no quote of Yurii's for this
  step; none is invented here, per [[gherkin-when-his-words-do-not-exist]].
  This step moves call() and everything it needs to spend correctly out of
  tools/run/live.go into internal/deliver, as one exported Call over one
  exported Gateway type, so tools/run keeps a one-line wrapper at its old
  call site and tools/bench's -live finally calls the real thing behind the
  same flag, the same validation, and the same three x-fuse-* headers.

  # @test:TestNoFileInThisPackageCanMakeAnHTTPRequest
  Scenario: The bench holds no second door
    Given every non-test file under tools/bench
    When it is scanned for net/http, a model provider's key, or os.Getenv
    Then none of them appears anywhere, because the only way this package
      can spend anything is through internal/deliver.Call

  # @test:TestLiveDotGoHoldsNoWayToMakeAnHTTPRequestAnyMore
  Scenario: The runner's own live.go holds no door of its own any more
    Given tools/run/live.go after call() and its engine bodies moved out
    When it is scanned the same way
    Then it holds none of them either, because the only way it can still
      spend anything is the one-line wrapper over the same internal/deliver.Call

  # @test:TestLiveWithGatewaySendsTheThreeFuseHeaders
  Scenario: The bench's door carries the run, the agent and the budget
    Given a bench live run scoring the fixture's two known cases, pointed at
      a fake gateway through -gateway
    When each case is scored
    Then the fake server receives x-fuse-run-id, x-fuse-agent-id and
      x-fuse-budget-usd on every request, the run id is the SAME for both
      cases and the agent id DIFFERS between them, because one bench
      invocation is one run and its two cases are two different analysts

  # @test:TestLiveWithARealEngineAndNoGatewayRefuses
  Scenario: Without the door open, the bench's spend refuses before any call
    Given -live with a real engine and no -gateway at all
    When the bench starts
    Then it refuses with one sentence naming -gateway, before the store even
      opens, because the bench's spend must be metered exactly like the
      crew's

  # @test:TestLiveRefusesANonHTTPGatewayURLBeforeTheStoreOpens
  Scenario: A gateway that is not http(s) refuses the same way in both binaries
    Given a -gateway value that is not an http(s) URL, on the bench
    When it starts a live run
    Then it refuses before the store opens, with the same wording
      TestNormalizeGatewayRefusesANonHTTPURL already holds for the runner

  # @test:TestAnEmptyRunIDRefusesBeforeTheCall
  # @test:TestAnEmptyAgentIDRefusesBeforeTheCall
  Scenario: A gateway that would meter nothing refuses too
    Given the gateway is on but this call's run id or its agent id would be
      empty
    When the call is made
    Then it refuses before any request reaches the gateway, because a call
      TokenFuse cannot attribute is not a call this console should make
      unmetered by default

  # @test:TestWithNoGatewayTheRequestGoesToAnthropicDirectly
  # @test:TestAGatewayRequestCarriesTheThreeHeadersAndNeverInventsAParent
  Scenario: The wire itself never moved
    Given the exact requests the runner built before this step, and the
      moved anthropicRequest after it
    When either is built with no gateway, and with one
    Then they are byte-for-byte the same requests: the direct endpoint with
      none of the x-fuse-* headers, or the gateway's own endpoint carrying
      exactly the three this step has always sent and never inventing a
      parent run id

  # @test:TestA400MeteringRequiredIsAPlainErrorNotAGatewayRefusal
  # @test:TestTheGatewayAnswersA5MBBodyWithoutHangingOrPanicking
  # @test:TestTheGatewayClosesTheConnectionWithNoResponse
  Scenario: A hostile gateway is read, never trusted
    Given a gateway that answers 400 metering_required, a 5 MB body, or
      simply closes the connection
    When the call reads the response
    Then it always comes back a plain, readable error inside a generous
      bound, never a panic, never a hang, and never mistaken for the one
      shape (402) that stops the whole run
