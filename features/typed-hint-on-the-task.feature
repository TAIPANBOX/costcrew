# language: en

Feature: The typed hint reaches the task it was meant for, with what was sent

  typryx's typed hint is a suggestion recorded on an anomaly. The analyst works
  a task, not the anomaly page, so the hint belongs on the task too, linked
  back to the anomaly. The anomaly's panel names typryx's own answer id and
  which fields left, and it never says typryx saw anything when nothing was
  sent. With typryx not configured, nothing about it appears anywhere.

  # @test:TestTheTaskPageShowsTheHintOnItsAnomaly
  Scenario: The task page shows the hint on the anomaly it came from
    Given an anomaly carrying a typed hint from typryx
    And a task opened from that anomaly
    When a person opens the task
    Then the suggested cause, its probability and its source are shown, with a link to the anomaly
    And it says the hint is a suggestion, never a decision

  # @test:TestTheAnomalyPanelNamesTheAnswerAndTheFieldsSent
  # @test:TestAPassRecordsWhatLeftForTypryx
  Scenario: The anomaly's panel says which answer this is and what was sent
    Given typryx answered about an anomaly
    When a person opens the anomaly
    Then the panel names typryx's answer id, the fields that left, and how many typryx held back

  # @test:TestANoHintRowWhereNothingWasSentDoesNotSayTypryxSawFields
  Scenario: A row where nothing was sent does not say typryx saw anything
    Given typryx could not be reached when the anomaly was asked about
    When a person opens the anomaly
    Then the panel says nothing about the anomaly left for typryx
    And it does not say typryx saw the template's fields

  # @test:TestWithTypryxOffTheTaskPageHasNoHintPanel
  Scenario: With typryx off the task page says nothing about it
    Given a console with no typryx configured
    When a person opens a task that came from an anomaly
    Then the page does not mention typryx
