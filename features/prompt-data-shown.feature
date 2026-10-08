# language: en

Feature: The console says what a model is sent, and whether a plan request was paid for

  The installation's prompt-data setting decides what a model reads, and under
  masked or aggregates the goal a person types for a sprint is never sent. A
  person typing that goal should see that on the page, and a person whose
  request to the supervisor came back refused should know whether that call
  cost anything.

  # @test:TestTheEnginesAndPlanPagesShowThePromptDataMode
  Scenario: The engines page and the sprint plan show the prompt-data setting
    Given the console runs with any prompt-data setting
    When a person opens the engines page or the sprint plan
    Then the setting is named and the sentence the model is shown is printed
    And under masked or aggregates the page says plainly that the goal typed for the sprint is withheld

  # @test:TestAPaidAnswerThatFailedValidationIsNotCalledARefusedCall
  Scenario: A paid call whose answer was refused says it was paid
    Given the supervisor was asked to plan and the gateway answered
    When the answer fails the checks against the plan and the roster
    Then the page says the call was made and paid for, with the amount booked
    And it does not call it a refusal before the call

  # @test:TestARefusalBeforeTheCallSaysNothingWasSpent
  # @test:TestACallThatFailedSaysNothingWasBooked
  Scenario: A request refused before the call, or a call that failed, says nothing was spent
    Given the supervisor was asked to plan
    When the request is refused before any call, or the call fails at the gateway
    Then the page says which of the two happened and that nothing was spent or booked
