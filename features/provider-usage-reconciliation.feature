# language: en

Feature: The provider's own bill, reconciled against the gateway's rows

  # The ask, paraphrased: the agents share one provider API key, so which
  # agent spent what comes only from the gateway's per-call rows and never
  # from the provider. What keeps that honest is a reconciliation: the
  # provider's own usage per model per day against the sum of the gateway's
  # rows for the same model and day, with the gap reported as its own line
  # and never silently absorbed. Readers for the Anthropic Admin API and the
  # OpenAI organization usage and costs API, each able to read a saved export
  # from a folder so it runs offline, a dry-run test, and a page and a CSV.

  @test:TestAnthropicUsageFolderIsRead
  Scenario: Anthropic's own usage and cost reports are read from a folder
    Given a folder holding Anthropic's usage report and cost report as JSON,
      the cost in cents as a decimal string, over two pages
    When the anthropic-usage connector imports it
    Then the cost per model per day is stored exactly, to the micro-dollar,
      with half a micro-dollar rounded once, and the tokens per model, key
      and token type beside it

  @test:TestOpenAIUsageFolderIsRead
  Scenario: OpenAI's own usage and costs are read from a folder
    Given a folder holding OpenAI's completions usage and costs as JSON, the
      cost as a number of dollars, one of them written with an exponent
    When the openai-usage connector imports it
    Then the cost per model per day is stored exactly, and a line item that
      is not "model, token type" is kept whole as its own row

  @test:TestBothUsageConnectorsAreBuiltAndFree
  Scenario: The catalogue says what the two connectors are and what they cost
    Given the connector catalogue
    When a person reads the anthropic-usage and openai-usage entries
    Then both are built, both feed provider_usage, both say they are not
      metered, and neither asks the console for a secret

  @test:TestProviderUsageTestDescribesAndWritesNothing
  Scenario: A dry-run test says what an import would do and writes nothing
    Given a configured folder of reports
    When a person presses Test
    Then the result names the files, days, models and totals it would write,
      and the store holds no provider usage row afterwards

  @test:TestProviderUsageImportTwiceChangesNothing
  @test:TestALaterFileReplacesADayRatherThanAddingToIt
  Scenario: Importing again never counts a day twice
    Given a folder that was already imported
    When it is imported again, or a newer report of the same day is added
    Then each day's figures are replaced, never added to, and the newer
      file's figures are the ones kept

  @test:TestProviderUsageHostileInput
  @test:TestOpenAIHostileInput
  @test:TestAnUnknownFieldNeverChangesAKnownOne
  @test:TestDecimalMicrosIsExactAndBounded
  @test:TestEveryFieldShapeIsRefusedWithItsOwnReason
  Scenario: A hostile report is refused by name and never reaches the store
    Given a report that is truncated, has a field of the wrong type, a huge
      number, an exponent that would never finish, a duplicate key, a byte
      that is not UTF-8, a page
      repeated by a pagination loop, or a field the reader does not know
    When it is imported beside a good report
    Then the bad file is refused by name with its reason, nothing it carries
      is stored, an unknown field is ignored without changing a known one,
      and the good file still lands

  @test:TestProviderUsageFolderBoundaries
  Scenario: The folder is bounded and a link is not followed
    Given a folder with an oversized file, a symlink, unrelated files and two
      copies of the same report
    When it is imported
    Then the oversized file is refused by size, the link is not read, the
      unrelated files are counted and not read, and the copies count once

  @test:TestProviderUsageNeverWritesCharges
  Scenario: The provider's figure is a check, never a second copy of the money
    Given an imported provider report
    When the charges are read
    Then no charge was written by it

  @test:TestReconcileSetsTheProviderBesideTheGatewaySum
  @test:TestTheReconciliationPageShowsTheGapAndTheStatus
  Scenario: Each day and model is reconciled and given a status
    Given the provider's cost per model per day and the gateway's per-call
      rows for the same provider
    When the reconciliation is read
    Then each day and model shows the provider's amount, the gateway's sum,
      the difference, and whether they matched, the gateway is under, the
      gateway is over, or the provider's report is missing for that day

  @test:TestTheGapIsNeverAbsorbed
  @test:TestTheReconciliationCSVCarriesEveryRowAndTheGapLine
  Scenario: The gap is its own line and is never absorbed
    Given a row whose gap is within the tolerance and a model only the
      provider knows about
    When the reconciliation is read, on the page or as a CSV
    Then the matched row still prints its gap, the provider-only model is a
      row of its own, and the window's whole gap is a line of its own

  @test:TestReconcileToleranceBoundary
  Scenario: The tolerance is stated and exact at its edge
    Given the default tolerance of one cent or half a percent of the
      provider's figure, whichever is larger
    When a gap lands exactly on it, or one micro-dollar past it
    Then the first is matched and the second is not

  @test:TestAProviderZeroIsNotProviderMissing
  Scenario: A provider that billed nothing is not a provider that was not read
    Given one day the provider's report covered with nothing in it, and one
      day it was never read for
    When the gateway has spend on both
    Then the first is gateway over and the second is provider missing

  @test:TestReconcileReadsOnlyThisProvidersUnblockedCalls
  @test:TestReconcileScopesByKeyAndWorkspace
  Scenario: Only the same provider, key and scope are compared
    Given gateway rows for another provider and blocked calls, and provider
      rows for keys and workspaces the gateway does not use
    When the reconciliation is read with the connector's key and scope set
    Then only the same provider's unblocked calls and the stated keys and
      scopes are summed, and the page says where a provider's cost report
      cannot be narrowed by key

  @test:TestReconcileOnAFreshStoreIsEmptyAndCreatesNothing
  @test:TestTheReconciliationPageSaysWhenThereIsNothingToReconcile
  Scenario: With nothing imported the page says so and writes nothing
    Given a console with neither side imported
    When the reconciliation is opened
    Then it says there is nothing to reconcile, and reading it created no table

  @test:TestAnthropicFetchWritesWhatTheConsoleReads
  @test:TestOpenAIFetchSendsABearerAndRepeatedGroupBy
  Scenario: The fetcher reads the provider with an admin key and the console reads what it wrote
    Given an admin key in the fetcher's environment and a provider answering
      over several pages
    When costcrew-usage fetches a window of days
    Then it sends only GET requests with the key in the provider's own
      header, follows every page, writes private files, and the console's
      reader imports them

  @test:TestAPaginationLoopIsStoppedAndNothingIsWritten
  @test:TestAPageCapStopsAProviderThatNeverEnds
  @test:TestAResponseTheConsoleWouldRefuseIsNotWritten
  @test:TestAnOversizedResponseIsRefused
  Scenario: A provider that misbehaves is stopped and nothing half-checked is written
    Given a provider that repeats a page cursor, never stops, answers with a
      shape the console would refuse, or answers too much
    When costcrew-usage fetches from it
    Then it stops, says why, and writes nothing for that report

  @test:TestARefusedKeyIsReportedForScopeAndNeverEchoed
  @test:TestARedirectIsNotFollowedSoTheKeyGoesNowhereElse
  Scenario: The admin key never leaves the request it was meant for
    Given a key the provider refuses and echoes back, or a provider that
      redirects to another host
    When costcrew-usage fetches
    Then a refusal is reported as a key that may lack admin scope with the
      key itself redacted, and the redirect is not followed

  @test:TestDryRunFetchesChecksAndWritesNothing
  @test:TestTheFlagsAreCheckedBeforeAnyRequest
  Scenario: A dry run writes nothing and bad flags never reach the network
    Given a dry run, a missing key, plain http to another machine, or a
      window that is too long or in the future
    When costcrew-usage starts
    Then a dry run fetches and checks and writes nothing, and every bad flag
      is refused before any request is made
