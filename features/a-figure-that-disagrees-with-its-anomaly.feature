# language: en

Feature: The supervisor judges an anomaly's option by the anomaly's own figure

  @decided 2026-10-17
  """
  An option tied to an anomaly is judged on the excess the anomaly recorded,
  never on the figure the analyst wrote into its options block. An option whose
  figure disagrees with the recorded excess is not applied automatically: it is
  carried to the owner, with the reason written on the decision.
  """

  @test:TestAnOptionFiguredDifferentlyFromItsAnomalyIsCarriedNotApplied
  Scenario: A dismissal on a figure that disagrees with the anomaly is carried
    Given an anomaly whose recorded excess is 1,040.00
    And an analyst's option to dismiss it, written with a figure of 10.00
    When the supervisor's pass runs
    Then the option is carried to the owner, not applied
    And the anomaly stays open

  @test:TestTheThresholdReadsTheAnomaliesRecordedExcessNotTheOption
  Scenario: A small figure cannot pass the threshold for a large recorded excess
    Given an anomaly whose recorded excess is 4,000.00, over T.anomaly
    And an analyst's option to explain it, written with a figure of 1,000.00
    When the supervisor's pass runs
    Then the option is carried, because the recorded excess is over T.anomaly

  @test:TestTheThresholdReadsTheAnomaliesRecordedExcessNotTheOption
  Scenario: A figure that agrees with a large recorded excess is still carried
    Given an anomaly whose recorded excess is 4,000.00, over T.anomaly
    And an analyst's option to explain it, written with a figure of 4,000.00
    When the supervisor's pass runs
    Then the option is carried, because the figure is over T.anomaly

  @test:TestTheThresholdReadsTheAnomaliesRecordedExcessNotTheOption
  Scenario: A figure that agrees with a small recorded excess is applied
    Given an anomaly whose recorded excess is 2,400.00, under T.anomaly
    And an analyst's option to explain it, written with a figure of 2,400.00
    When the supervisor's pass runs
    Then the option is applied

  @test:TestTheRecordedFigureIsTheMagnitudeOfAnExcessThatFell
  Scenario: A dismissal of a fall is applied when its figure is the fall's size
    Given an anomaly whose recorded excess is a fall of 1,040.00
    And an analyst's option to dismiss it, written with a figure of 1,040.00
    When the supervisor's pass runs
    Then the option is applied

  @test:TestTheRecordedFigureIsTheMagnitudeOfAnExcessThatFell
  Scenario: A dismissal of a fall is carried when its figure understates the fall
    Given an anomaly whose recorded excess is a fall of 1,040.00
    And an analyst's option to dismiss it, written with a figure of 10.00
    When the supervisor's pass runs
    Then the option is carried to the owner, not applied

  @test:TestAgreementIsExactToTheCent
  Scenario: A figure equal to the recorded excess to the cent is applied
    Given an anomaly whose recorded excess is 1,040.00
    And an analyst's option to dismiss it, written with a figure of 104000 cents
    When the supervisor's pass runs
    Then the option is applied

  @test:TestAgreementIsExactToTheCent
  Scenario: A figure one cent over the recorded excess is carried
    Given an anomaly whose recorded excess is 1,040.00
    And an analyst's option to dismiss it, written with a figure of 104001 cents
    When the supervisor's pass runs
    Then the option is carried to the owner, not applied

  @test:TestAgreementIsExactToTheCent
  Scenario: A figure one cent under the recorded excess is carried
    Given an anomaly whose recorded excess is 1,040.00
    And an analyst's option to dismiss it, written with a figure of 103999 cents
    When the supervisor's pass runs
    Then the option is carried to the owner, not applied

  @test:TestAnOptionWithNoFigureOnAnAnomalyTaskIsCarried
  Scenario: An option that wrote no figure does not pass the threshold
    Given an anomaly whose recorded excess is 1,040.00
    And an analyst's option to dismiss it, written with no figure
    When the supervisor's pass runs
    Then the option is carried to the owner, not applied

  @test:TestTheCarriedDecisionSaysWhyItsFigureWasNotApplied
  Scenario: The owner is told why the figure was not applied
    Given an option carried because its figure disagrees with its anomaly
    When the owner's decision request is read
    Then it says the figure does not match the anomaly's recorded excess
    And it gives both figures
