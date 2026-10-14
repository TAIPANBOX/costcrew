# language: en

Feature: A call's cost is rounded to the nearest micro-unit, never truncated

  The crew prices every call twice: the worst case it reserves before the
  call, and what the call cost from its tokens after it. Both are tokens at
  a price per million, and both must be the exact figure rounded once, never
  a float a hair under it cut down by a micro-unit.

  # @test:TestACallsCostIsRoundedNeverTruncated
  Scenario: A call's worst case and its real cost are the exact figure, rounded
    Given a call of 1,000 tokens in at 3 per million and 500 out at 15 per million
    When its worst case and its real cost are priced
    Then each is 10,500 micro-units, not 10,499
    And a fraction of a micro-unit rounds to the nearest
