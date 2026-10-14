# language: en

Feature: A call the gateway forwarded under identity warn mode is marked, with its spend

  With TOKENFUSE_IDENTITY_STRICT=warn the gateway lets a call through when the
  agent id it carries does not belong to the credential that sent it, and
  files the call and its spend under the id it claimed. From TokenFuse 1.8.0
  the export names the failed check on the row. The console reads it and
  marks such calls per agent, per team and per run, so a forwarded call never
  reads as an honest one, and it says so when an older export cannot tell.

  # @test:TestTheIdentityReasonIsKeptWhenTheExportCarriesIt
  # @test:TestAnExportWithoutTheColumnStoresNoIdentityReason
  # @test:TestTheIdentityReasonColumnIsAddedToAStoreThatPredatesIt
  Scenario: The identity check's finding is kept from an export that carries it
    Given a gateway export from TokenFuse 1.8.0 with calls forwarded under warn mode
    When the export is imported
    Then each row keeps the identity check's finding
    And the import says how many calls were forwarded under warn mode and what they cost
    And an older export without the column is stored as not reporting it

  # @test:TestAForwardedUnderWarnCallIsMarkedPerTeamAndPerRun
  # @test:TestTheAIPageMarksCallsForwardedUnderIdentityWarn
  # @test:TestWarnGroupsBreakTiesOnCallsAndThenTheKey
  Scenario: The AI page marks forwarded calls per agent, per team and per run
    Given a month with calls forwarded under identity warn mode in two teams and two runs
    When a person opens the AI spend page
    Then the page says how many calls and how much spend were forwarded
    And the agent's row, and the team's rows, carry an identity warn mark
    And a panel lists the forwarded calls by team and by run, largest spend first, with the check that failed

  # @test:TestTheAgentRowCountsItsForwardedCallsInsideItsSpend
  # @test:TestTheAIPageMarksCallsForwardedUnderIdentityWarn
  Scenario: A forwarded call is still spend, and a refusal is not a forwarded call
    Given a forwarded call and a call refused for identity in the same month
    When a person opens the AI spend page
    Then the forwarded call stays inside the agent's spend, marked
    And the refused call is not marked as forwarded

  # @test:TestTheAIPageSaysAnOlderExportCannotTellAWarnedCall
  # @test:TestAnOlderExportIsCountedAsNotReportingIdentityWarn
  # @test:TestTheAIPageSaysNothingWasForwardedWhenTheExportSaysSo
  Scenario: An export written before TokenFuse 1.8.0 is not read as honest
    Given a month imported only from an export written before TokenFuse 1.8.0
    When a person opens the AI spend page
    Then the page says those calls read as honest whether or not warn mode forwarded them
    And it says nothing was forwarded only when the export reports every call

  # @test:TestAWarnMarkedExportIsKeptOverAnOlderCopyOfTheSameCalls
  Scenario: A re-export keeps the copy that carries the mark
    Given the same calls exported by TokenFuse 1.7.0 and by 1.8.0 in one folder
    When the folder is imported, whichever file is read last
    Then each call is counted once and keeps its identity warn mark

  # @test:TestHostileIdentityReasonIsRefusedByName
  Scenario: A hostile finding is refused by name
    Given an export whose identity finding carries a control character, a formula or too many bytes
    When the export is imported
    Then that row is refused by name and the rest of the import goes on

  # @test:TestAISpendSectionNamesTheCallsForwardedUnderIdentityWarn
  Scenario: The AI spend analyst is told which spend nobody proved
    Given a month with calls forwarded under identity warn mode
    When the AI spend analyst's task packet is built
    Then it counts the forwarded calls and their spend for the whole desk, even when only totals may be sent
    And each agent line says how much of its spend was forwarded under warn mode
    And a month with nothing forwarded says nothing of it
