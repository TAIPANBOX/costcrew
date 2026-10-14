# language: en

Feature: A fallback-priced amount is shown apart from spend, and personal data let through is named

  The gateway prices a call from its price book, and a model the book has no
  price for at its fallback rate, which can be many times the real price. Its
  scanner can also find personal data in a call and let the call through. The
  gateway's export names both on each row. The console keeps a fallback-priced
  amount out of spend and shows it apart, names the kinds and counts of
  personal data let through, never the values, and says when an older export
  does not report either.

  # @test:TestThePriceBasisIsKeptAndAFallbackIsLeftOutOfTheLedger
  # @test:TestAFallbackEstimateIsNeverInAnAgentsOrAModelsCost
  # @test:TestTheMonthIsSplitByPriceBasis
  Scenario: A fallback-priced amount never reaches spend
    Given a gateway export with calls priced from the book and calls priced at the fallback rate
    When the export is imported
    Then the daily ledger, every agent's and model's cost leave the fallback amounts out
    And the month is counted by price basis

  # @test:TestTheAIPageShowsFallbackPricedSpendApart
  # @test:TestARunsFallbackEstimateIsKeptOutOfItsSpendAndItsShares
  Scenario: The pages show the fallback amount apart
    Given a month with fallback-priced calls
    When a person opens the AI spend page, the runs page and the reconciliation
    Then each shows the fallback-priced amount beside spend, never inside it
    And a model priced only at the fallback rate takes no share of a run's spend

  # @test:TestTheAIPageSaysThePriceBasisIsNotReportedByAnOlderGateway
  # @test:TestAnExportWithoutThePriceBasisKeepsEveryCallInTheLedger
  Scenario: An older export says the price basis is not reported
    Given an export from a gateway version without the price basis column
    When a person opens the AI spend page
    Then it says the price basis is not reported by this gateway version, and counts those calls as spend as before

  # @test:TestAMonthOfOnlyFallbackPricedCallsIsStillAMonthOfRealCalls
  Scenario: A month of fallback-priced calls only is still a month of real calls
    Given a month whose every call was priced at the fallback rate
    When the console decides which month the AI pages show
    Then it shows that month and not the generated estate

  # @test:TestThePIIFindingsAreKeptWhenTheExportCarriesThem
  # @test:TestPersonalDataLetThroughIsCountedByKindAgentAndRun
  # @test:TestTheAIPageNamesThePersonalDataLetThrough
  Scenario: Personal data let through is named by kind and count
    Given calls in which the gateway's scanner found personal data and let them through
    When a person opens the AI spend page and the runs page
    Then each names the kinds and counts by month, agent and run, never the values
    And an older export without the column is said not to report it

  # @test:TestHostilePriceBasisAndPIIFindingsAreRefusedByName
  Scenario: A hostile basis or finding is refused by name, without printing a value
    Given an export whose price basis or findings carry a control character, a formula, a value or a bad count
    When the export is imported
    Then that row is refused by name, and a refused finding's value is never printed

  # @test:TestAPriceBasisExportIsKeptOverAnOlderCopyOfTheSameCalls
  Scenario: A re-export keeps the copy that carries the basis and the findings
    Given the same calls exported with and without the two columns in one folder
    When the folder is imported
    Then each call is counted once and keeps its basis and its findings

  # @test:TestTheReconciliationMarksTheFallbackPricedPartOfTheGatewaySum
  # @test:TestAISpendSectionKeepsAFallbackEstimateOutOfTheTotalAndNamesPII
  Scenario: The reconciliation and the analyst are told
    Given a month with fallback-priced calls and personal data let through
    When the provider's bill is reconciled and the AI spend analyst's packet is built
    Then the reconciliation marks the fallback-priced part of the gateway's sum
    And the packet leaves the fallback amount out of its total and names the personal data by kind and count
