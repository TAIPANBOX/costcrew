# language: en

Feature: The crew can run on a model the organisation hosts itself

  An organisation that may not send billing data outside its own network has
  to be able to run the crew with nothing leaving it. The local engine calls a
  server the operator runs (Ollama, vLLM, LM Studio, the llama.cpp server:
  anything that answers POST /v1/chat/completions) at an address the operator
  typed, on a model the operator names. It needs no key, may carry one if the
  server wants it, and never sends anything to a vendor.

  Every guard in the runner is counted in money, and a model on your own
  hardware costs no vendor money, so a zero price would let a run loop
  without limit. The operator can price their own hardware, and a run that
  includes the local engine at a price of zero is refused unless it carries a
  ceiling in tokens.

  # @test:TestALocalTaskRunsTheToolLoopAndIsChargedAtTheOperatorsPrice
  Scenario: A task runs on the operator's own server and is charged at their price
    Given an analyst hired onto the local engine, a server at an address the
      operator gave and a price the operator set for their hardware
    When the runner runs the task live
    Then the request goes to that server's chat-completions route and to
      nothing else, the tool loop runs as it does on any engine, and the
      charge recorded is the tokens the server reported at the operator's
      price

  # @test:TestNoVendorHostAppearsInTheLocalRoute
  Scenario: Nothing in the local route can name a vendor
    Given the code that makes a local call
    When it is read for any vendor's host or key
    Then there is none: the only address a local call can reach is one the
      operator typed

  # @test:TestTheModelKeyIsNeverPrintedOrReturned
  Scenario: A server's key is sent as a bearer token and shown nowhere
    Given a server that wants a key, set in COSTCREW_MODEL_KEY
    When a task runs, and again when the server fails in each way it can
    Then the key travels to the server as a bearer token and appears in no
      line the runner prints and in no error it returns

  # @test:TestBothOpenAIEnginesSendTheSameRequestShape
  Scenario: The local engine and the OpenAI-wire vendor engine send the same request
    Given the same conversation and the same tools
    When a round is built for each, directly and through a gateway
    Then the bodies are identical, the route under a gateway is the same, and
      the metering headers are the same, because it is one implementation and
      not two copies that can drift

  # @test:TestALocalTaskThroughTheOpenAIGatewayIsChargedItsSettlement
  Scenario: Through the OpenAI-shaped gateway a local call is metered like any other
    Given the OpenAI-shaped gateway is set and its upstream is the local server
    When a local task runs
    Then the call goes to the gateway with the run, agent and budget headers,
      the operator's own server is not called directly, and the charge is
      what the gateway settled

  # @test:TestALocalTaskWithOnlyAnAnthropicGatewayIsRefusedAndNothingIsCalled
  Scenario: With a gateway set that does not front the OpenAI wire, a local task is refused
    Given only the Anthropic-shaped gateway is set, and a server address too
    When a local task is run
    Then it is refused before any request, naming the engine, and the
      server is not used instead

  # @test:TestALocalRunAtPriceZeroIsRefusedAtStartWithoutATokenCeiling
  Scenario: A run on a zero-priced local engine cannot start without a token ceiling
    Given tasks on the local engine priced at zero, so money cannot bound them
    When the run is started with no ceiling in tokens
    Then it is refused before the first call, naming the flag, nothing is
      contacted and no task on the board changes

  # @test:TestALocalRunAtPriceZeroRunsOnceItHasATokenCeiling
  Scenario: With a token ceiling the same run goes ahead and says how many tokens it used
    Given the same tasks and a ceiling in tokens
    When the run is started
    Then every task runs and the summary says how many tokens were used of the
      ceiling

  # @test:TestTheTokenCeilingRefusesTheNextTaskOnceTheLastOneUsedIt
  Scenario: The next task is refused once the ceiling is used up
    Given a task that used most of the token ceiling
    When the next task asks to start
    Then it is refused as a budget refusal, its reservation is returned and
      the server is not called

  # @test:TestTheWholeRunsWorstCaseOverTheTokenCeilingIsRefusedBeforeAnyCall
  Scenario: A run whose worst case is over the token ceiling never starts
    Given two tasks whose worst case together is more tokens than the ceiling
    When the run is started
    Then it is refused before the first call, and a ceiling exactly equal to
      the worst case is accepted

  # @test:TestALocalTaskIsPricedByTheOperatorsModelAndPrice
  Scenario: The operator names the model and prices their own hardware
    Given a model name and a price per million tokens given on the command line
    When the dry run prices a task on the local engine
    Then it is priced at that price on that model, a task with no model name
      is refused for want of one, and a price of zero is still a price

  # @test:TestALocalServerThatReportsNoUsageIsCountedAtTheWorstCaseNotZero
  Scenario: A server that omits its usage is counted at the worst case, not at nothing
    Given a server whose answers carry no token usage
    When a task runs
    Then the round is counted as the bytes sent in and the whole output cap
      out, charged at the operator's price, and the runner says the server
      reported nothing

  # @test:TestThePriceBasisOfALocalCallIsLocalWhateverTheGatewaySaid
  Scenario: The record says no vendor was involved
    Given a call made on the local engine, settled by a gateway or not
    When the runner tells the shared bus about it
    Then the price basis is local, so the evidence reads that no vendor price
      was involved

  # @test:TestARunWithABadModelURLFailsBeforeTheStoreIsOpened
  # @test:TestNormalizeModelURL
  Scenario: A bad server address is refused before anything is opened
    Given an address with a password in it, a scheme that is not http or https,
      or a query string
    When the runner starts
    Then it is refused before the data directory is touched, and the refusal
      does not repeat the password

  # @test:TestAnUnreachableLocalServerStopsTheRunAtStartWithOneLine
  Scenario: A server that is not there stops the run with one line naming it
    Given a server address nothing is listening on
    When a live run is started
    Then it stops before the first task with one line naming the URL, not a
      stack trace, and no task is blocked for a call that never happened

  # @test:TestAHostileLocalResponseFailsOneTaskWithABoundedMessage
  Scenario: Whatever the server says back, at most one task fails and every reservation returns
    Given a server that answers with nothing, a web page, half a document, no
      answer, an empty answer or megabytes
    When a task runs against it
    Then that task fails with a short message, it is not read as a budget
      refusal, and nothing stays reserved
