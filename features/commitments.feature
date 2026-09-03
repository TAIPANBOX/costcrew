# language: en

Feature: The commitment analyst's case is measured, not generated

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @yurii 2026-09-02
  """
  він має сам не купувати
  """

  @test:TestCommitmentColumnsFillTheCommitmentsTable
  @test:TestCoverageIsCommittedOverEligiblePerDeskAndMonth
  @test:TestUtilisationIsUsedOverCommittedPerCommitment
  Scenario: Coverage and utilisation come from the file
    Given a FOCUS file carrying the CommitmentDiscount* columns and a
      ChargeCategory=Purchase row naming a commitment's id, type, status,
      quantity and unit
    When it is imported
    Then the commitment is kept in the store's own commitments table, the
      import summary says how many rows carried the columns, and coverage
      (committed spend over eligible spend, per desk) and utilisation (used
      over committed, per commitment) are computed from those rows rather
      than from the generated waterline

  @test:TestExpiryCalendarListsWithinNinetyDaysNotBeyond
  @test:TestCommitmentsSectionListsAnExpiryWithinNinetyDays
  Scenario: The calendar says what expires
    Given real commitments with different expiry dates, one inside ninety
      days of today and one well beyond it
    When the expiry calendar is built
    Then the one expiring within ninety days is listed and the one beyond
      it is not, and the same calendar reaches the commitment analyst's own
      task packet

  @test:TestBreakEvenMonthsForAKnownFixture
  @test:TestCommitmentsSectionAppears
  Scenario: The case for finance ends in a purchase option a person decides
    Given a commitment's own monthly price and the on-demand run rate it
      would cover, both real figures from the store
    When the commitment analyst's packet is built
    Then it carries coverage, utilisation, the expiry calendar and, for
      each candidate, buy-or-wait with the break-even in months, so the
      analyst's own deliverable can end in a purchase option -- the class
      its job description hands up, never one it decides alone -- for a
      person to act on

  @test:TestApplyingPurchaseHasNoSideEffect
  @test:TestPurchaseRowsAreNeverCountedAsUsage
  Scenario: Nothing buys
    Given a purchase option, whether carried to the owner by the supervisor's
      own pass or answered directly, and a Purchase row in an imported file
    When the option is applied, and separately when the file is imported
    Then applying the option writes no side effect -- purchase is owned by
      nobody in the crew, so no link, the owner included, may ever decide it
      -- and the Purchase row itself is never counted as usage; the purchase
      itself never happens in this console
