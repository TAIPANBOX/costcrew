# language: en

Feature: Each gateway door is named as its own source, or the page says there is one source

  A gateway speaks one wire, so the OpenAI wire is a second gateway, a second
  door, with its own trace, its own export and its own budgets. The export does
  not say which door a call came through. When the console is given each
  door's export folder it names every call by its door, on the engines page
  and per run; when it is given one folder it says the spend is read from one
  source. The engines page also names the doors this console's own calls go
  through.

  # @test:TestASecondDoorsFolderIsReadAsThatDoors
  # @test:TestOneFolderNamesNoSecondDoor
  Scenario: The OpenAI-wire door's folder is read as that door's
    Given the main gateway's export folder and the OpenAI-wire door's export folder are both set
    When the gateway connector imports
    Then every call is named by the folder it was read from
    And the import counts the rows through each door
    And with one folder set no door is named

  # @test:TestOneFolderSetAsBothDoorsIsRefused
  # @test:TestAnUnreadableSecondFolderIsNamedAsTheDoors
  Scenario: One folder cannot be both doors
    Given the same folder is set as both doors, by the same path or another way to it
    When the gateway connector imports
    Then the import is refused and nothing is written
    And a missing second folder is named as the OpenAI-wire door's

  # @test:TestARunThroughTwoDoorsNamesBoth
  # @test:TestTheRunsPageNamesTheDoorsWhenTheInputTellsThemApart
  Scenario: A run that came through two doors names both
    Given a run with calls through the main gateway and the OpenAI-wire door
    When a person opens the runs page
    Then the run is marked as through two doors with each door's calls and spend
    And the page says such a run spent against two ledgers

  # @test:TestARowFromBeforeTheDoorColumnIsReadAsTheMainFolders
  # @test:TestTheEnginesPageSaysOneSourceWhenTheInputDoesNotTellDoorsApart
  Scenario: One folder is one source, and the page says so
    Given the gateway's calls were read from one folder
    When a person opens the engines page
    Then it says the spend is read from one source because the export does not name the door

  # @test:TestTheEnginesPageNamesEachGatewayDoorThisConsoleCallsThrough
  # @test:TestTheEnginesPageSaysOneSourceWhenTheInputDoesNotTellDoorsApart
  Scenario: The engines page names the doors this console's own calls go through
    Given an Anthropic-wire gateway and an OpenAI-wire door are configured
    When a person opens the engines page
    Then each door is named with its flag, its wire, its host and the engines that go through it
    And the engine no door fronts is named as refused
    And a gateway address's path and query are never printed
