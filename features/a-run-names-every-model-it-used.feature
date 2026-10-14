# language: en

Feature: A run names every model it used, its share of the spend, and how it ended

  An agent can change model in the middle of a run. A view that names a run by
  its last model names it by the model that may have done the least of its
  work. The console lists each run the gateway's rows name, with every model
  it called, in the order it called them, and each one's share of the run's
  spend, says how the run ended as far as the gateway's rows show, and says
  plainly that the door a call came through is not in the export.

  # @test:TestARunThatSwitchedModelsListsEveryModelWithItsShare
  # @test:TestTheRunsPageShowsEveryModelARunUsedWithItsShare
  Scenario: A run that switched model shows both models with their shares
    Given a run that called a large model three times and then a small one twice
    When a person opens the runs page
    Then the run is marked as a model switch
    And it lists the large model first with 96% of the spend and the small one after it with 4%
    And it names the model of the last call separately

  # @test:TestARunSaysHowItEndedAsTheGatewaysRowsShow
  # @test:TestTheRunsPageShowsEveryModelARunUsedWithItsShare
  Scenario: A run says how it ended
    Given runs that were killed, stopped by their budget, refused once and went on, or spent nothing
    When a person opens the runs page
    Then each run reads as killed, stopped with the gateway's reason, refused then went on, or no spend

  # @test:TestRunsAreListedLargestSpendFirstTheSameEveryCall
  # @test:TestARunSaysHowItEndedAsTheGatewaysRowsShow
  Scenario: Runs are listed largest spend first, the same way every time
    Given a month of runs, one of them across midnight
    When the runs are read again and again
    Then they come in the same order, largest spend first, and the run across midnight is one run

  # @test:TestTheShareOfASliverIsNeverReadAsNothing
  Scenario: A sliver of a run's spend is never read as nothing
    Given a model that did a tiny part of a run's work
    When its share is shown
    Then it reads as less than a tenth of a percent, never as zero

  # @test:TestTheRunsPageShowsEveryModelARunUsedWithItsShare
  Scenario: The page says the door is not in the export
    Given runs read from one folder of the gateway's export
    When a person opens the runs page
    Then it says the export does not name the door a call came through, so every run is read from one source

  # @test:TestTheRunsPageSaysThereAreNoRunsBeforeAnImport
  Scenario: Before an import the page says where runs come from
    Given nothing has been imported from the gateway
    When a person opens the runs page
    Then it says there are no runs yet and points at the gateway connector
