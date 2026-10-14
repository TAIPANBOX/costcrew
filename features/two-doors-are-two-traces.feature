# language: en

Feature: One blocked call on each gateway door is two calls

  The main gateway and the OpenAI-wire gateway each write their own trace and
  their own export, so one call can appear in both. Two rows that share an
  identity across the two doors are two calls that each gateway refused, not
  one call exported twice. The console keeps one row per door, whichever folder
  it reads first.

  # @test:TestOneBlockedCallOnEachDoorIsTwoCalls
  Scenario: The same blocked call exported by both doors is kept once per door
    Given a blocked call exported by the main gateway and the same blocked call exported by the OpenAI-wire gateway
    When the connector imports both folders, in either order
    Then the console keeps one row for each door
    And the import says one row came through each door
    And a second import leaves both rows where they were
