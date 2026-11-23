# language: en

Feature: An empty export folder reads nothing, and no spend replaces nothing

  @decided 2026-11-23: a launcher wires the gateway's export folders before
  any export is written, and on machines without an OpenAI-wire gateway; an
  empty folder there must not stop the main door's calls. And the launcher's
  yes to replacing the generated estate is spent only when real spend
  arrives, so an export of refused calls cannot wipe the estate, leave the
  ledger empty, have the estate seeded again at the next start and wiped
  again at the next import.

  @test:TestAnEmptyOpenAIDoorFolderDoesNotBlockTheMainDoor
  Scenario: An empty second door's folder does not stop the main door
    Given the main folder holds an export and the OpenAI-wire door's folder
      exists and is empty
    When the gateway export is imported
    Then the main door's calls land

  @test:TestAnEmptyMainFolderReadsAsNoExportYet
  Scenario: An empty folder reads as no export yet
    Given a console still holding the generated estate, and an empty folder
    When the gateway export is imported
    Then nothing is read, nothing is refused, and the sentence names the
      folder as holding no export yet

  @test:TestAMissingDoorFolderIsStillAnError
  Scenario: A folder that is not there is still refused
    Given the OpenAI-wire door is set to a folder that does not exist
    When the gateway export is imported
    Then the import is refused naming that door, and nothing lands

  @test:TestReplaceGeneratedKeepsTheEstateWhenNoCallIsSpend
  Scenario: An export with no spend in it does not replace the generated estate
    Given a console holding the generated estate, the yes to replace it, and
      an export whose every call the gateway blocked
    When the gateway export is imported
    Then the blocked calls land, the generated charges and tasks stay,
      nothing is journaled as replaced, and the sentence says the estate was
      kept

  @test:TestReplaceGeneratedStillReplacesOnceSpendLands
  Scenario: An export with spend in it replaces the generated estate
    Given a console holding the generated estate and the yes to replace it
    When an export with real spend is imported
    Then the generated charges are gone

  @test:TestSeedDoesNotReseedOverImportedCalls
  Scenario: A store holding imported calls is not seeded again
    Given a store whose ledger is empty and which holds imported gateway calls
    When the console starts
    Then the generated estate is not seeded

  @test:TestSeedStillSeedsAFreshStore
  Scenario: A fresh store is still seeded
    Given a store with no charges and no imported calls
    When the console starts
    Then the generated estate is seeded

  @test:TestAnEmptyMainFolderDoesNotBlockTheOpenAIDoor
  Scenario: An empty main folder does not stop the second door
    Given the main folder exists and is empty and the OpenAI-wire door's
      folder holds an export
    When the gateway export is imported
    Then the OpenAI-wire door's calls land

  @test:TestReplaceGeneratedKeepsTheEstateForFallbackPricedCalls
  Scenario: Calls priced at the gateway's fallback rate do not replace the estate
    Given a console holding the generated estate, the yes to replace it, and
      an export whose every call was priced at the gateway's fallback rate
    When the gateway export is imported
    Then the calls land and the generated charges stay

  @test:TestSeedReseedsAStoreHoldingOnlyCallsThatWereNotSpend
  Scenario: A store an older build wiped is seeded again once
    Given a store whose ledger is empty and whose imported calls were all
      blocked or priced at the fallback rate
    When the console starts
    Then the generated estate is seeded again
