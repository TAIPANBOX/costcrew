# language: en

Feature: A deliverable says whether a person's money bought it

  @yurii 2026-08-24
  """
  А якщо я додам тимчасові API ключі від Claude, від Anthropic, від
  OpenRouter... з реальним підключенням агентів і так далі.
  """

  @yurii 2026-08-24
  """
  То давай ми їй передамо ці два ключі, шо я тобі дав від OpenRouter і від
  Claude... шоб в неї був повноцінний варіант запуску разом з ключами від
  OpenRouter і від Claude.
  """

  The estate ships 279 generated drafts so that a new installation has
  something to review. A live run adds real ones, and after the first full
  run 63 of them sat among 342 with the same author, the same state and the
  same shape, with nothing to tell them apart. Two kinds of thing under one
  heading is the fault this console exists to catch elsewhere.

  @test:TestARunnerDeliverableIsMarkedLive
  Scenario: What a model wrote says a model wrote it
    Given an analyst with a task and a priced engine
    When the runner records what came back from the call
    Then the deliverable is marked live
    And it is a draft, because only a person's stamp publishes

  @test:TestTheTaskPageShowsWhichDeliverableWasWrittenLive
  Scenario: A reader can see which one was real
    Given one generated draft and one a model wrote, on the same task
    When the task page is opened
    Then exactly one of them is marked as written live

  @test:TestASubCentCallStillLandsOnTheTask
  Scenario: A fraction of a cent still cost something
    Given a call that cost a tenth of a cent
    When it is recorded against the task
    Then the task's spend goes up, because rounding it to nothing is how a
      bill grows out of a column of zeroes

  @test:TestTheCeilingHoldsUnderConcurrency
  Scenario: Four calls in flight cannot walk past one ceiling
    Given a ceiling that fits three calls
    When twenty are attempted at once and none of them settles
    Then no more than three are let through, because each reserves its worst
      case before it starts rather than checking a total the others have not
      yet added to

  @test:TestAFailedCallReturnsItsReservation
  Scenario: A call that failed costs nothing
    Given a call that reserved its worst case and then failed
    When the run continues
    Then the whole reservation is back, because a failed call that keeps its
      reservation shrinks the ceiling every time something goes wrong

  @test:TestAnthropicIsAskedForAnAnswerRatherThanReasoning
  Scenario: The model is asked for the deliverable, not for its reasoning
    Given a call with a budget of 1200 tokens
    When the request is built
    Then thinking is turned off, because a model that spends the whole budget
      reasoning reaches no answer at all, blocks the task, and is billed in
      full either way
