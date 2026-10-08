# language: en

Feature: An analyst offers options; the supervisor decides what it can; the owner is asked what is left

  @decided 2026-09-02
  """
  An analyst offers a choice of the decisions it thinks right, first to the
  supervisor, the lead agent, and the supervisor then asks the user who owns
  these agents what to do next.
  """

  @decided 2026-09-02
  """
  The supervisor asks the owner only when it cannot decide the matter itself:
  where people deal with each other directly, or where a key decision is to be
  made, and not every time an agent meets a disputed point.
  """

  @test:TestADeliverableEndsInOptionsTheRoleMayName
  Scenario: An analyst offers options, never an action
    Given an investigator whose deliverable ends in a fenced options block
      naming anomaly.explain, a class its own job description lists
    When the deliverable is saved
    Then the option is stored open, and nothing about the anomaly has changed:
      the deliverable proposes, and only a later stamp disposes

  @test:TestAnOptionOutsideTheRoleIsRefusedAndReturned
  Scenario: A class outside the role's own vocabulary is refused whole
    Given the same investigator, whose deliverable instead names period.close,
      a class its job description lists under neither decides_alone nor
      hands_up
    When the deliverable is saved
    Then it is saved without its options, the task is returned to the
      analyst with the reason, and the refusal is journaled

  @test:TestTheSupervisorDecidesItsOwnClassesAndCarriesTheRest
  Scenario: The supervisor decides what its description allows
    Given a sprint whose posted deliverables carry one driver.recurring
      option and one period.close option
    When the supervisor's pass runs
    Then driver.recurring is applied as the supervisor's own act, because its
      job description decides that class alone, and period.close is carried
      into a decision request, because it is not

  @test:TestOnlyTheOwnersStampAppliesAKeyDecision
  Scenario: The owner is asked only for key decisions, and only the owner answers
    Given a decision request carrying a key decision addressed to one owner
    When an operator who is not that owner tries to apply it, and then the
      owner does
    Then the operator who is not the owner is refused and applies nothing,
      the owner's own stamp is what applies it, and posting an ordinary
      deliverable never applies an option by itself

  @test:TestARefusalNeedsAReason
  Scenario: A refusal carries a reason
    Given the owner of a carried option, deciding to refuse it
    When the owner submits the refusal with no reason, and then with one
    Then the empty refusal changes nothing, and only the one with a reason is
      recorded

  @test:TestADecisionRequestAsksOncePerOwnerPerSprint
  Scenario: One decision request per owner per sprint, not one per option
    Given a second option carried to an owner who already has an open
      decision request in the same sprint
    When the supervisor's pass runs again
    Then the same decision request is written to, not duplicated: the owner
      still has exactly one request for that sprint

  @test:TestOnlyOneAlternativeOfOneDeliverableIsApplied
  Scenario: Options in one deliverable are a choice, and only one is ever applied
    Given one deliverable carrying two alternative options for the same
      task, ranked by saving so one clearly comes first
    When the supervisor's pass applies the top-ranked option, because its
      job description decides that class alone
    Then the other option of the same deliverable is marked not_chosen,
      never applied, and never silently dropped

  @test:TestAnOptionAboveTAnomalyIsCarriedNotApplied
  Scenario: A figure above T.anomaly is a key decision, even for a class the supervisor could otherwise decide alone
    Given a deliverable's option whose figure is over the roles.yaml
      T.anomaly threshold, in a class the supervisor's own job description
      would normally decide by itself
    When the supervisor's pass runs
    Then the option is carried to the owner instead of applied: the figure
      alone makes it a key decision

  # The owner is asked rarely: only for key decisions and for anything that
  # reaches people. The supervisor's job description gives it option.select
  # for options inside the analysts' own classes, so choosing among an
  # analyst's options is the supervisor's own act while the figure is small.
  # @decided 2026-10-04 (applying the 2026-09-02 rule to analyst-owned classes)

  @test:TestTheSupervisorSelectsAnAnalystClassOptionWithinTAnomaly
  Scenario: The supervisor selects among an analyst's own options without asking the owner
    Given a deliverable whose option is in a class the analyst link owns,
      recommendation.rightsizing, with a figure inside the roles.yaml
      T.anomaly threshold
    When the supervisor's pass runs
    Then the supervisor applies it as its own act, decided_by supervisor,
      and no decision request is written to the owner for it

  @test:TestTheSupervisorsSelectionOfAnAnalystClassCarriesItsSideEffect
  Scenario: Selecting an analyst's option does what the option says, as the supervisor
    Given a deliverable whose option dismisses an open anomaly, inside
      T.anomaly
    When the supervisor's pass runs
    Then the anomaly is dismissed, the option records the supervisor as who
      decided it, and the owner is not asked

  @test:TestAnAnalystClassOptionOverTAnomalyIsCarried
  Scenario: A figure above T.anomaly still goes to the owner, whoever owns the class
    Given an analyst-class option whose figure is one cent over T.anomaly
    When the supervisor's pass runs
    Then the option is carried to its owner as a decision request and
      nothing is applied

  @test:TestAnAnalystClassOptionExactlyAtTAnomalyIsApplied
  Scenario: A figure exactly at T.anomaly is inside it
    Given an analyst-class option whose figure equals T.anomaly
    When the supervisor's pass runs
    Then the supervisor applies it, the same boundary its own classes have

  @test:TestOwnerAndNobodyClassOptionsStayCarriedWithinTAnomaly
  Scenario: What reaches people or commits money is never the supervisor's to select
    Given small options in classes the owner holds (period.close,
      budget.set) and in classes nobody in the crew decides (purchase,
      infra.change, vendor.negotiate)
    When the supervisor's pass runs
    Then every one is carried to the owner and none is applied

  @test:TestSelectingAnAnalystOptionResolvesItsHandsUpAlternative
  Scenario: Selecting one alternative resolves the others of the same choice
    Given one deliverable offering a rightsizing option and, as the
      alternative, a purchase
    When the supervisor selects the rightsizing option
    Then the purchase is marked not_chosen, never applied and never asked
      about separately

  @test:TestAContradictedAnalystOptionIsStillCarriedToTheOwner
  Scenario: Two analysts who disagree are the owner's one question, not the ranking's
    Given two deliverables naming different causes for the same anomaly,
      both inside T.anomaly
    When the supervisor's pass runs
    Then neither is applied, both are carried in one decision request, and
      the anomaly stays open

  @test:TestASecondOptionOnAnAnomalyTheSupervisorAlreadyDecidedIsCarried
  Scenario: An anomaly is settled once per pass
    Given one deliverable dismissing an anomaly and another explaining the
      same anomaly, both inside T.anomaly
    When the supervisor's pass runs
    Then the dismissal is applied, the explanation is carried to its owner,
      and the pass finishes instead of aborting on the closed anomaly

  @test:TestSupervisorMaySelectReadsOptionSelectFromTheJobDescription
  Scenario: The supervisor's authority over analyst options is what roles.yaml says
    Given roles.yaml listing option.select in the supervisor's decides_alone
    When option.select is taken out of that list
    Then the supervisor may no longer select analyst-class options, and its
      own classes remain its own
