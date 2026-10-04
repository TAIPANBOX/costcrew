# language: en

Feature: The thresholds that bound the crew's authority are the ones the owner decided

  # @decided 2026-10-04: the owner halved the two money thresholds in
  # roles.yaml, T.anomaly from USD 5,000 to USD 2,500 and T.urgent from USD
  # 25,000 to USD 12,500, and kept the other five (T.firstpass, T.stale,
  # T.stale_days, T.untagged, T.migration) at their draft values. All seven
  # stop being drafts: each is recorded as decided on that date.

  @test:TestTAnomalyIsTwoAndAHalfThousand
  Scenario: T.anomaly is USD 2,500, and the card says the same amount the code compares
    Given roles.yaml's T.anomaly threshold
    When its figure in cents and its display text are read
    Then the figure is 250000 cents, and the text on the card is the same
      amount, so a person reading the card and the supervisor's pass cannot
      disagree

  @test:TestTUrgentIsTwelveAndAHalfThousand
  Scenario: T.urgent is USD 12,500
    Given roles.yaml's T.urgent threshold, the money at stake that lets the
      supervisor ask outside the weekly rhythm
    When its figure in cents and its display text are read
    Then the figure is 1250000 cents and the text says the same amount

  @test:TestAnOptionOfThreeThousandDollarsIsCarriedToTheOwner
  Scenario: A USD 3,000 option is no longer the supervisor's to select
    Given an analyst's deliverable offering one rightsizing option with a
      figure of USD 3,000, which was inside the draft threshold
    When the supervisor's pass runs
    Then the option is carried to its owner in a decision request and
      nothing is applied

  @test:TestTAnomalyBoundaryIsTwoThousandFiveHundredDollarsToTheCent
  Scenario: The boundary is USD 2,500 to the cent
    Given one option at exactly USD 2,500 and another one cent over
    When the supervisor's pass runs on each
    Then the option at USD 2,500 is applied by the supervisor and the one
      cent over is carried to its owner

  @test:TestASupervisorOwnedClassOverTheDecidedTAnomalyIsCarried
  Scenario: A class the supervisor owns is held to the same figure
    Given a recurring driver the supervisor would decide alone, with a
      figure of USD 3,000
    When the supervisor's pass runs
    Then it is carried to the owner rather than applied

  @test:TestEveryThresholdIsMarkedDecidedOnTheDayItWasDecided
  Scenario: All seven thresholds are recorded as decided on 2026-10-04
    Given the seven thresholds in roles.yaml
    When each one's provenance is read
    Then each begins with the decided marker and the date 2026-10-04

  @test:TestTheFiveThresholdsTheOwnerKeptKeepTheirValues
  Scenario: Deciding the other five does not change them
    Given T.firstpass, T.stale, T.stale_days, T.untagged and T.migration
    When their values are read
    Then they are 80% over two sprints, 3, 7, 10% of the desk's month and
      15%, exactly as drafted

  @test:TestProvenanceVocabulary
  Scenario: A threshold's provenance is one of three markers and nothing else
    Given the provenance vocabulary: claude, decided with a real date, and
      measured with how and date
    When markers are offered that carry the owner's name, lack a date, lack
      a how, are padded, are multi-line, hold a control character or run
      past the length cap
    Then every one is refused, and the three well-formed shapes are accepted

  @test:TestRolesAreBoundRefusesAThresholdWithAnUnrecognisedProvenance
  Scenario: The roles gate refuses a provenance outside the vocabulary
    Given roles.yaml with one threshold's provenance replaced by the
      owner's name as a marker, by a bare phrase, or by a decided with no
      date
    When scripts/roles-are-bound.sh runs
    Then it fails and says UNRECOGNISED PROVENANCE

  @test:TestRolesAreBoundRefusesAThresholdWithNoProvenance
  Scenario: The roles gate refuses a threshold that says nothing about where it came from
    Given roles.yaml with one threshold's provenance line removed
    When scripts/roles-are-bound.sh runs
    Then it fails and says UNRECOGNISED PROVENANCE

  @test:TestRolesAreBoundAcceptsAMeasuredProvenance
  Scenario: The roles gate does not refuse a measured provenance that names its how and date
    Given roles.yaml with one threshold marked measured, with a command and
      a date
    When scripts/roles-are-bound.sh runs
    Then it passes
