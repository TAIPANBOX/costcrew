# language: en

Feature: A thinking model's reasoning is counted as tokens the run used

  A model that reasons before it answers generates tokens nobody reads, and
  they are billed and they count against a run's limits like any others. Two
  vendors that speak the same OpenAI-shaped wire report them differently: one
  puts them inside the completion count, the other leaves them out of it and
  only the total carries them. Measured on a Vertex AI endpoint, a short answer
  reported 59 completion tokens, 560 reasoning tokens, 14 prompt tokens and a
  total of 633. Counting the completion figure alone made the run look nine
  tenths cheaper than it was, in tokens and in the runner's own price.

  # @test:TestALocalCallCountsAThinkingModelsReasoningAsOutput
  # @test:TestAnOpenRouterCallCountsAThinkingModelsReasoningAsOutput
  Scenario: Reasoning left out of the completion count is still counted
    Given a server whose usage block reports a total larger than the prompt
      and the completion together
    When a call on either OpenAI-shaped engine reads it
    Then the output counted is everything after the prompt, 619 tokens for
      the measured answer and not 59

  # @test:TestOpenAIUsageOutputTokens
  # @test:TestAnOpenRouterCallCountsAThinkingModelsReasoningAsOutput
  Scenario: Reasoning already inside the completion count is not counted twice
    Given a server that reports the reasoning inside the completion count, so
      the total is exactly the prompt plus the completion
    When the call reads it
    Then the output counted is the completion count, unchanged

  # @test:TestOpenAIUsageOutputTokens
  # @test:TestALocalCallCountsAThinkingModelsReasoningAsOutput
  Scenario: A total that does not add up never lowers the count
    Given a server that sends no total, or a total smaller than its own parts,
      or smaller than the prompt, or below zero
    When the call reads it
    Then the output counted is the completion count, never less and never a
      number that wrapped around

  # @test:TestTheToolLoopsRoundCountsAThinkingModelsReasoningAsOutput
  # @test:TestTheTokenCeilingCountsAThinkingModelsReasoning
  Scenario: A run's token ceiling sees the reasoning
    Given a task on the operator's own engine, under a token ceiling, whose
      model reasons before it answers
    When the runner runs it live
    Then the tokens the ceiling counts, the runner's own price and the output
      tokens on the tool-call event all include the reasoning, and the next
      task is refused when the reasoning used the room it needed
