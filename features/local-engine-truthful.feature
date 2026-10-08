# language: en

Feature: The console tells the truth about a model the organisation hosts itself

  A model on the organisation's own server is called by costcrew-run, never
  by this console, so the console's environment says nothing about whether it
  is set up. Its flags belong to costcrew-run, the engines page counts what it
  lists, the forms name engines rather than print their ids, and a local run,
  which no vendor bills, shows the tokens it used rather than a zero against a
  money guard.

  # @test:TestCheckReadsTheLocalEndpointFromTheEnvironment
  # @test:TestTheEnginesPageCountsItsCatalogueAndLeavesLocalToTheRunner
  Scenario: The local engine's readiness is left to costcrew-run
    Given the console's own environment names a model server
    When a person opens the engines page
    Then the local engine is not shown as ready on that evidence
    And its entry says costcrew-run decides, from its own flags

  # @test:TestTheLocalHowTextNamesCostcrewRunsFlags
  Scenario: Setting up the local engine names costcrew-run's flags
    When a person reads how to set up the local engine
    Then it names the server address, the model name and how many tasks are sent at once, one by default
    And it says these are costcrew-run's flags, not the console's

  # @test:TestTheEnginesPageCountsItsCatalogueAndLeavesLocalToTheRunner
  # @test:TestTheCatalogueCountsItself
  Scenario: The engines page counts what it lists
    When a person opens the engines page
    Then the number of answers and of engines it states are the ones it lists

  # @test:TestTheHireAndRebriefFormsNameTheEngines
  Scenario: The hire and re-brief forms name the engines
    When a person hires or re-briefs an analyst
    Then each engine is offered by its name, with its id beside it

  # @test:TestEachTaskRecordsTheTokensItsCallsUsed
  # @test:TestALocalTaskShowsTokensAgainstTheRunCeiling
  # @test:TestTheLocalAgentCardShowsTheTokensItsWorkUsed
  Scenario: A local run shows the tokens it used, not a zero against a money guard
    Given an analyst on the organisation's own model ran a task under a token ceiling
    When a person opens the task or the analyst's card
    Then the tokens the work used are shown, with the run's token ceiling on the task
    And the task does not show nothing spent against a per-task guard

  # @test:TestLiveMarkTitleSaysWhatWroteIt
  # @test:TestALocalTaskShowsTokensAgainstTheRunCeiling
  Scenario: A deliverable written on the organisation's own model is not marked as a real key
    Given a deliverable written live by an analyst on the local engine or a paid-for assistant
    When a person hovers its live mark
    Then the mark says what wrote it, not that a real key was used
