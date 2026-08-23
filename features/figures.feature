# language: en

Feature: The figures answer to each other

  @yurii 2026-08-22
  """
  Пройдись по всім вкладкам, по всім агентам, по всім дескам, по всім
  командам... щоб там було нормальне наповнення, логічне, щоб не було просто
  тупих цифр, а щоб вони якось між собою відображали дійсність. Тобто, щоб
  можна було по одному показнику перевірити далі в команді, скільки загальна
  сума чогось там, наприклад... Щоб це не були просто відокремлені цифри,
  взяті з нікуди.
  """

  @test:TestOwnersAndCrewAgreeOnUnboundAgents
  Scenario: One number cut two ways gives one answer
    Given agents whose identity is bound to nothing
    When the owners page and the crew page both count them
    Then they report the same number
    And that number is not zero, because the seeded roster records no
      attestation for any agent, and a reassuring zero would mean the count
      is measuring something other than its label

  @test:TestPagesRenderTheSameTwice
  Scenario: The same estate renders the same way twice
    Given a store nothing has written to
    When a page is rendered again
    Then it is byte for byte what it was
    And this is checked on sixteen pages that group something, because a
      table whose rows moved reads as new data rather than as a bug

  @test:TestAIUnitsAreOrderedTheSameEveryCall
  Scenario: Rows are ordered by something, never by a map
    Given the AI desk built from the same generated series
    When it is asked for its rows twenty times
    Then the order is identical every time

  @test:TestATransferSplitsTheSpendAtTheHandover
  Scenario: A figure that moves says where it went
    Given an owner who has handed every agent away
    When the owners page is read
    Then she is still on it, against what she authorised, with no agents
      beside it
