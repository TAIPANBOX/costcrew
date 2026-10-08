# language: en

Feature: Four small defects found by review and by live runs, closed

  Review sessions and live runs on 2026-10-07 and 2026-10-08 found four places
  where a rule held only by luck or by a check somewhere else: a month in a
  URL written into SQL, three file readers that listed every bad row and kept
  quiet about a link, one rule for a unit's name kept in two packages, and an
  example pseudonym a real team could draw.

  # @test:TestAHostilePeriodIsAValueNotSQL
  # @test:TestABreakdownReadsOnlyTheNamedPeriod
  # @test:TestABreakdownRefusesAColumnItWasNotWrittenFor
  Scenario: A month from the address bar is a value, never part of the query
    Given a team, desk or service page breaks spend down by month
    And the month in the address carries a quote and some SQL
    When the breakdown is read with the month check out of the way
    Then nothing comes back and no other team's spend is shown
    And the charges table is untouched

  # @test:TestEveryCSVReaderCountsRefusedRowsWholeButNamesOnlyTheFirstFew
  # @test:TestEveryCSVReaderNamesALinkItDidNotFollow
  # @test:TestARefusalTallyCountsEveryRefusalAndNamesTheFirstFew
  Scenario: The rightsizing, budget and seat readers say what they refused, briefly, and name a link they passed over
    Given a folder holding a file of a thousand bad rows, or a link to a file elsewhere
    When the rightsizing, budget recommendation or SaaS seats reader imports it
    Then every refused row is counted but only the first twenty are named
    And a link is never followed and is named in the summary as not followed

  # @test:TestTheUnitNameRuleIsWrittenOnce
  # @test:TestCheckRefusesEveryShapeANamePrintedElsewhereMustNotHave
  # @test:TestCheckHoldsTheBoundItIsGiven
  Scenario: A unit's name is judged by one rule wherever it is judged
    Given the file reader and the rule a person stamps both decide whether a unit's name is fit to print
    When either of them reads a name
    Then both ask the same single function, and that function is tested on its own

  # @test:TestTheModeLineExampleHasAShapeNoRealTokenCanTake
  # @test:TestReidentifyNeverReadsTheModeLineExampleAsAName
  Scenario: The example pseudonym shown to a model can never be a real team's
    Given a prompt under the masked or aggregates setting shows the model an example of a pseudonym
    When the model copies that example into its draft
    Then the example has a shape no real pseudonym can take
    And turning the draft back into real names leaves the example as written
