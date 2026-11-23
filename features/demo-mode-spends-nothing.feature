# language: en

Feature: Demo mode spends nothing

  @decided 2026-11-23: the README and the docs say what the code does. The
  console's accounts package has always promised that in demo mode nobody
  spends the owner's model budget, whatever their role, and nothing held
  that promise: the setting only printed a line at start, and the
  supervisor's plan-ask still reached the gateway, the cadence switch could
  turn the clock-driven crew on, costcrew-run -live spent, and a hosted
  typryx was asked at start.

  @test:TestDemoModeRefusesThePlanAskBeforeAnyCall
  Scenario: With demo mode on, the supervisor's plan-ask is refused before any call
    Given a console with a TokenFuse gateway configured and COSTCREW_DEMO set
    When the installation's admin asks the supervisor to plan a sprint
    Then the ask is refused before it is priced or sent, the page says demo
      mode refused it, and the gateway is never reached

  @test:TestDemoModeRefusesSwitchingTheCadenceOn
  Scenario: With demo mode on, the clock-driven crew cannot be switched on
    Given a console with COSTCREW_DEMO set
    When its admin turns the cadence switch on
    Then the switch stays off and the page says demo mode refused it, and
      turning it off still works

  @test:TestDemoModeRefusesALiveRun
  Scenario: With demo mode on, costcrew-run refuses to spend
    Given COSTCREW_DEMO is set
    When costcrew-run is started with -live
    Then it is refused by name before the store is opened

  @test:TestDemoModeAsksTypryxNothing
  Scenario: With demo mode on, typryx is not asked
    Given a console started with -typryx-url and COSTCREW_DEMO set
    When it starts
    Then typryx is never called
