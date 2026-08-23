# language: en

Feature: The card says everything about one agent

  @yurii 2026-08-22
  """
  треба для кожного агента зробити щось наподобі паспорта агента, або як ми в
  нашому стеку робили в Genaryx, робили Agent 360... щоб коли заходиш на
  нього, щоб була вся інформація про нього.
  """

  @yurii 2026-08-23
  """
  зроби подієву половину Agent 360
  """

  @test:TestMostCardsShowWhereTheAgentStopped
  Scenario: The card is not empty on almost every agent
    Given the estate as it is seeded
    When each of the thirty-nine cards is opened
    Then at least two thirds carry something under "Where it stopped"
    And none of them is missing the panel entirely

  @test:TestTheStopsPanelDoesNotNeedTheEventStream
  Scenario: It reads the board, not the event stream
    Given no agent-event stream at all
    When a card is opened
    Then it still shows where that agent stopped
    And the events panel says plainly that the stream is empty, so a reader
      can tell which record each panel is showing

  @test:TestTheStopsPanelAgreesWithTheBoard
  Scenario: What the panel says is what the board holds
    Given an agent with returned artifacts and blocked tasks
    When its card is read
    Then the counts and the cost are the board's own figures

  @test:TestAStopWithNoReasonSaysSo
  Scenario: A stop with no reason is named as such
    Given a return or a block with an empty reason
    When the card is read
    Then it says no reason was recorded, rather than showing a blank cell
      that reads as nothing having gone wrong
