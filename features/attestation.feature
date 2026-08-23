# language: en

Feature: An identity is bound to something, or the console says it is not

  @yurii 2026-08-22
  """
  тепер додай атестацію агентам, щоб bom_incomplete зник
  """

  @claude
  """
  The honest answer to that was that the console was ALREADY claiming
  attestations for twelve of thirty-nine agents, derived from their permission
  lists. That is worse than "none": idryx stopped flagging them. Removing the
  invention made the number go UP before SPIRE made it go to zero for real.
  """

  @test:TestAnAttestationHasToCarryItsEvidence
  Scenario: A method without evidence is a word
    Given an attestation method and no evidence for it
    When it is recorded
    Then it is refused, because a method on its own can be neither checked nor
      disproved, and an identity graph that believes it stops asking

  @test:TestHiringRefusesAnAttestationWithNoEvidence
  Scenario: The same at the hire form, not only in the library
    Given a hire naming a method with an empty detail
    When it is submitted
    Then the hire is refused

  @test:TestSeedingClaimsNoAttestation
  Scenario: Seeding invents nothing
    Given a freshly seeded roster
    When its attestations are read
    Then every one is "none", because the installation has bound nothing yet
      and saying otherwise is the failure this exists to prevent
