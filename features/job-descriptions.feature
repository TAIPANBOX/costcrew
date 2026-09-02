# language: en

Feature: The crew decides according to its job descriptions

  @yurii 2026-09-02
  """
  Вони мають вирішувати це все згідно своїх посадових інструкцій. І бажано,
  щоб ці посадові інструкції чітко були виписані, що для супервайзера, що для
  Фінопс-агента, щоб вони також чітко дотримувались.
  """

  @yurii 2026-09-02
  """
  Аналітик пропонує варіанти супервайзеру, а супервайзер питає власника
  тільки тоді, коли він сам не може вирішити це питання, тобто, що
  стосується безпосередньо взаємодії людей або прийняття якихось ключових
  рішень.
  """

  @yurii 2026-09-02
  """
  він має сам не купувати, змінювати інфраструктуру, і тим більше переговори
  з вендером проводити він сам особі не може.
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
