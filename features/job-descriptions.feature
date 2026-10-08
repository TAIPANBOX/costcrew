# language: en

Feature: The crew decides according to its job descriptions

  @decided 2026-09-02
  """
  The agents decide everything according to their job descriptions, and those
  descriptions are written out clearly, for the supervisor and for the FinOps
  agents alike, so that both follow them closely.
  """

  @decided 2026-09-02
  """
  An analyst offers options to the supervisor, and the supervisor asks the
  owner only when it cannot decide the matter itself: where people deal with
  each other directly, or where a key decision is to be made.
  """

  @decided 2026-09-02
  """
  An agent does not buy anything, does not change infrastructure, and least of
  all negotiates with a vendor on its own.
  """

  @test:TestARoleCannotDecideAClassItDoesNotOwn
  Scenario: An analyst decides what its description lists
    Given an investigator, whose job description lists anomaly.explain among
      what it decides alone, and does not list period.close
    When it is asked whether it may decide anomaly.explain, and separately
      whether it may decide period.close
    Then it may decide anomaly.explain alone
    And it may not decide period.close, because that class belongs to the
      owner, and it is told so rather than merely refused

  @test:TestRolesAreBound
  Scenario: The supervisor asks the owner only for what the description hands up
    Given the supervisor's job description and its hands_to_owner list
    When that list is checked against every decision class the owner owns
    Then the two sets are exactly equal, plus the two named conditions: a
      data-quality halt that has lasted past T.stale_days, and a question
      two analysts answered differently on the same evidence

  @test:TestARoleCannotDecideAClassItDoesNotOwn
  Scenario: The crew never buys, changes or negotiates
    Given purchase, infra.change and vendor.negotiate, the three classes
      section 1 of the job descriptions owns to nobody in the crew
    When any link in the crew is asked whether it may decide one of them,
      the owner link included
    Then it may not: each is only ever recorded as an option inside a
      recommendation, never a decision the console applies
