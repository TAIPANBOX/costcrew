# language: en

Feature: The charge a run records is what the gateway settled, and the estimate is only the reservation

  @claude 2026-09-18
  costcrew#67, found on the appliance proving run of 2026-09-17: the runner
  priced one task's worst case at 0.0115, the gateway settled the task's one
  real call at 0.05811, and the runner printed "Spent 0.0116 of a 0.15
  ceiling" and put that figure on the board. The runner had been reporting
  its own estimate as what was spent. The words below paraphrase the issue's
  own asks; no quote of anybody is in them.

  @test:TestTheChargeRecordedIsTheGatewaysSettlement
  @test:TestTheSummaryLineReadsTheSettledTotalAndTheGatewaysOwnRunTotal
  Scenario: The board carries the gateway's settlement, not the runner's own estimate
    Given a live task whose worst case the runner priced at about a cent
    When the gateway settles the task's one real call at five times that
    Then the task's own spend, the run's total and the summary line all read
      the settlement, the board carries it rounded up once, and the estimate
      is only ever what was reserved before the call

  @test:TestParseSettlementReadsTheGatewaysOwnThreeHeaders
  @test:TestCallAnthropicCarriesTheGatewaysSettlementOnItsResult
  Scenario: The settlement is read off the gateway's own answer
    Given a metered call answered through the gateway
    When the response is read
    Then this call's own settled cost, the run's cumulative spend and whether
      the gateway priced the model from its book or from its fallback are
      all read from the response, and the fallback is named on the console
      line beside the figure it explains

  @test:TestFourTasksUnderOneRunIdAreNotOverCountedByTheCumulativeHeader
  Scenario: Four tasks under one run id are each charged their own call
    Given four tasks running at once under the one run id an invocation mints
    When the gateway answers each with its own cost and the run's running total
    Then each task is charged its own call and nothing more, the run's total
      is the sum of the four, and the cents are worked out once over the run

  @test:TestAddRoundSettlesATaskOnlyWhenEveryRoundWas
  @test:TestChargeIsTheSettlementWhenSettledAndTheCallersOwnPriceOtherwise
  Scenario: A task is settled only when every round of it was
    Given a task that made several rounds through the tool loop
    When one round comes back without a settlement
    Then the whole task is priced by the runner and said to be, rather than
      a sum that mixes the gateway's figures with the runner's own

  @test:TestHostileSettlementHeadersNeverPanicAndNeverBecomeACharge
  @test:TestAHostileSettlementFallsBackToTheRunnersOwnPriceAndSaysSo
  Scenario: A settlement the runner cannot read is not a charge
    Given a settlement header that is missing, empty, duplicated, signed,
      not a number, negative, a megabyte long or absurdly large
    When the response is read
    Then nothing panics, no charge is taken from it, the runner prices the
      call itself, and the console line says there was no settlement to read

  @test:TestWithNoGatewayTheChargeIsTheRunnersOwnPriceAndTheLineSaysSo
  Scenario: With no gateway, the runner prices its own call and says so
    Given a call made straight to a provider, with no gateway configured
    When it is recorded
    Then the charge is the runner's own price, exactly as before, and the
      line says it was priced by the runner because there was no settlement

  @test:TestASettlementAboveTheReservationStillCountsAgainstTheCeiling
  Scenario: A settlement above the reservation still counts against the ceiling
    Given a ceiling that fits two tasks at the runner's own price
    When the first task is settled by the gateway above what was reserved
    Then the second task is refused before its call, because the ceiling is
      checked against what was actually booked and not against the estimate

  @test:TestAskPlanRecordsTheGatewaysSettlementNotItsOwnPrice
  Scenario: The supervisor's own planning call is charged the same way
    Given the console's one spending route, the supervisor's planning call
    When the gateway settles it
    Then the settlement, not the console's own estimate, is what lands in
      the supervisor's spend for the month
