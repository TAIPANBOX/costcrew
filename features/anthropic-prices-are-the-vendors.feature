# language: en

Feature: The Anthropic prices the console reserves with are the vendor's current prices

  The ask, 2026-10-13: bring the built-in price tables to the providers'
  current official prices. The console held anthropic/claude-sonnet-5 at
  3 / 15 and anthropic/claude-opus-5 at 15 / 75 per million tokens, both
  marked as never checked against the vendor. Anthropic's pricing page lists
  2 / 10 and 5 / 25, so a worst case priced for either engine was two to three
  times what the call can cost, and a ceiling refused work that would have
  fitted.

  # @test:TestTheAnthropicRowsAreTheVendorsListRates
  Scenario: Sonnet 5 and Opus 5 are priced at the vendor's list rates
    Given a task on the anthropic engine
    When the console prices its worst case before any call is made
    Then Sonnet 5 is priced at 2 per million input and 10 per million output tokens
    And Opus 5 at 5 and 25

  # @test:TestNoPriceRowIsLeftUnverified
  Scenario: No price the console relies on is marked as unchecked
    When a person reads the price table the runner prints before a live run
    Then every row says when it was written down and where it came from
    And no row says it was never checked against the vendor
