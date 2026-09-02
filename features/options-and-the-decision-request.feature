# language: en

Feature: An analyst offers options; the supervisor decides what it can; the owner is asked what is left

  @yurii 2026-09-02
  """
  він має давати на вибір якісь певні рішення, які він вважає за потрібне
  спочатку супервайзеру, тобто головному агенту, а вже той має запитувати
  юзера, користувача, власника цих агентів, що робити далі.
  """

  @yurii 2026-09-02
  """
  супервайзер питає власника тільки тоді, коли він сам не може вирішити це
  питання, тобто, що стосується безпосередньо взаємодії людей або прийняття
  якихось ключових рішень, а не щоразу, коли в агента виникають якісь спірні
  моменти.
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
