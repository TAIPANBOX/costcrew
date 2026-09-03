# language: en

Feature: Month-end close, written by the chargeback analyst

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  A chargeback analyst's last three days of the month: reconcile, allocate,
  freeze, send the statements, answer the arguments.

  @test:TestClosePackSectionAppearsOnAChargebackTaskNamingAPeriod
  Scenario: The close pack is written from figures
    Given the chargeback analyst's own task, its title naming a period
    When the packet is built
    Then the close pack section carries each team's direct, allocated and
      loaded cents, the coverage, and the unallocated total, straight from
      the same allocation the console renders on its own pages

  @test:TestAllocationRuleWithNoTargetIsRefused
  @test:TestAllocationRuleWithAValidTargetIsSaved
  Scenario: A rule proposal names its target or is refused
    Given an allocation.rule option in the chargeback analyst's own
      deliverable
    When it is saved without a target, and then with one naming a rule id, a
      method and a share
    Then the target-less option is refused whole, with the reason, and
      nothing is written; the well-formed one is saved and carries its
      target through

  @test:TestApplyPeriodCloseClosesTheOpenPeriod
  Scenario: The close is a person's stamp
    Given a period.close option carried to an owner
    When the owner applies it
    Then the period freezes under that owner's own name, never the
      supervisor's, and never on its own

  @test:TestApplyPeriodCloseQueuesOneReporterTaskPerTeam
  Scenario: Statements go to teams only after the close, never sent by this console
    Given a period with teams on desks that have a reporter analyst
    When period.close is applied
    Then one showback-narration task per team is queued for that desk's
      reporter, and this console sends nothing itself
