# language: en

Feature: What this console puts on the shared bus is something the estate can read

  This console is a guest producer of the estate's event bus. The envelope it
  writes is agent-passport's, and that contract's `severity` is a CLOSED enum:
  info, low, medium, high, critical. A value outside it is not a stylistic
  choice, because a consumer that validates refuses the whole line and the
  event this console wrote is then in nobody's record.

  Found on 2026-08-28 while reading what registering this console in the
  estate's own registry would take, which is what that registry's preamble
  asks for: a row there is a claim checked against the producer's code.

  # @test:TestEverySeverityThisConsoleEmitsIsOneTheEnvelopeAllows
  Scenario: A severity the shared envelope does not carry is refused here
    Given the emitter that writes this console's events
    When it is handed a severity outside the envelope's closed enum
    Then it refuses rather than writing a line every validating consumer drops

  # @test:TestEveryGuardBandEmitsASeverityTheEnvelopeAllows
  Scenario: Every band of the monthly guard reports a severity the estate accepts
    Given three analysts past their monthly guard, one in each band
    When the guard check runs
    Then each event carries the severity its band means, and all three are
      values the shared envelope allows
