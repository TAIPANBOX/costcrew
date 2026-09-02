# language: en

Feature: The console reads what an agent actually spent

  @measured 2026-09-02
  """
  tokenfuse-focus-2026-09-02.sh, beside the fixture in
  ~/Development/go-to-market-2026-09/fixtures: ghcr.io/taipanbox/tokenfuse:v0.4.1
  with TOKENFUSE_ALLOW_STUB=1, five calls through /v1/messages from three
  agents in three runs, one carrying x-fuse-outcome: case_resolved, one
  escalated, one refused by a budget of one microdollar, then
  `tokenfuse focus-export --traces /data --out /data/focus.csv`. What is
  real: the 26-column shape, the agent id, the run id, the outcome tags, the
  blocked row. What is not: every amount, because the stub provider meters a
  fixed 1000/500 tokens. fixtures/README.md there carries the rest.
  """

  @yurii 2026-09-01
  """
  в мене задача, щоб ми могли встановлювати наші сервіси як на CostCrew, так
  і на довільні агенти, які вже працюють в клієнта, наприклад, на AWS
  Bedrock, на GCP, і на інші
  """

  This is the first reader the connector registry has ever held: a folder of
  FOCUS 1.2-style CSV files as `tokenfuse focus-export` writes them, with the
  gateway's own extension columns naming an agent and a run on every row. The
  scenarios below are about the console reading what an agent already
  running at a client actually spent, which is what the quote above asks
  for; the shape here is the one the cloud FOCUS readers (AWS, Azure, GCP)
  will reuse.

  @test:TestTokenFuseFocusIsRead
  Scenario: A FOCUS export lands on the AI desk with an agent on every row
    Given a folder tokenfuse focus-export wrote to, five calls from three
      agents, one of them blocked
    When it is imported
    Then the blocked call is recorded at zero and the other four are summed
      into three daily charges rows by model
    And the agent with the largest billed share that day is attributed to
      it with confidence gateway-header

  @test:TestImportIsIdempotent
  Scenario: Reading the same folder twice changes nothing the second time
    Given a folder already imported once
    When it is imported again
    Then the row counts and the charges sums are exactly what they were

  @test:TestGeneratedEstateIsNotMixed
  Scenario: A generated estate is never mixed with real money
    Given a store still holding the estate this console generates for a
      fresh install
    When real data is imported without being told to replace it
    Then the import is refused and nothing is written
    And with -replace-generated the generated rows are gone, the real ones
      are there, and the replacement is in the journal

  @test:TestA200MegabyteFileStaysBounded
  Scenario: A large export is streamed rather than loaded whole
    Given a 200 MB file built from one row repeated
    When it is imported
    Then the live heap after importing it grows by kilobytes, not by
      megabytes

  @test:TestTheAIPageReadsARealImport
  Scenario: The AI page reads what was actually spent, not the fixture
    Given a real import on this store
    When the AI page is opened
    Then it reads the store rather than the generated world, with a table of
      what each agent spent
    And it says whether its action count came from outcomes the calling
      agents tagged themselves or from the fixed ratio, because those are
      different kinds of number and the page does not blur them together

  @test:TestAgentAttributionKPIBecomesComputedAfterAnImport
  Scenario: The AI spend attribution KPI stops being permanently blocked
    Given a store with no real AI spend
    When the KPI library is asked what share of AI spend is attributed to an
      agent
    Then it is blocked, naming why
    But once a connector like tokenfuse-focus has written real spend with an
      agent on it, the same KPI reports a real number instead
