# language: en

Feature: Demo mode spends nothing

  @decided 2026-11-23: the README and the docs say what the code does. The
  console's accounts package has always promised that in demo mode nobody
  spends the owner's model budget, whatever their role, and nothing held
  that promise: the setting only printed a line at start, and the
  supervisor's plan-ask, the one request the console can spend on, still
  reached the gateway.

  @test:TestDemoModeRefusesThePlanAskBeforeAnyCall
  Scenario: With demo mode on, the supervisor's plan-ask is refused before any call
    Given a console with a TokenFuse gateway configured and COSTCREW_DEMO set
    When the installation's admin asks the supervisor to plan a sprint
    Then the ask is refused before it is priced or sent, the page says demo
      mode refused it, and the gateway is never reached
