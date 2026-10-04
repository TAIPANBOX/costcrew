# language: en

Feature: An admin may answer for an owner, and it is never silent

  # @decided 2026-10-04: an admin may still apply or refuse a decision that is
  # addressed to an owner who is somebody else, kept as an emergency path (an
  # owner on leave, an owner who has left). It is no longer silent: the admin
  # must give a reason, and the option, the decision card, the journal and the
  # bus event (option_applied or option_refused) all say the answer was given
  # on behalf of owner X, with the reason and the admin's username. An owner
  # answering their own decision needs no reason and is marked as the owner.

  @test:TestAnAdminAnsweringForAnotherOwnerMustGiveAReason
  Scenario: An admin applying another owner's option without a reason is refused
    Given a carried option addressed to owner1 and an admin who is not owner1
    When the admin applies it with no reason, a blank one, or only whitespace
    Then the option stays carried and no option_applied entry is journaled

  @test:TestAnAdminRefusingForAnotherOwnerMustGiveAReasonToo
  Scenario: A refusal for another owner needs the reason for answering as well
    Given a carried option addressed to owner1 and an admin who is not owner1
    When the admin refuses it with a reason for the refusal but none for
      answering for owner1
    Then the option stays carried

  @test:TestAnAdminAnswerWithAReasonIsMarkedOnBehalfOfTheOwner
  Scenario: An admin's answer with a reason says whose it is and why
    Given the same option and an admin giving a reason
    When the admin applies it
    Then the option is applied and records the admin, the owner it was on
      behalf of and the reason, and the journal entry says the same together
      with who answered and as what

  @test:TestAnAdminRefusalWithAReasonIsMarkedOnBehalfOfTheOwner
  Scenario: An admin's refusal for an owner is marked and journaled
    Given the same option and an admin giving both the refusal's reason and
      the reason for answering
    When the admin refuses it
    Then the option is refused with the refusal's own reason, and an
      option_refused entry carries the on-behalf marking

  @test:TestAnOwnersOwnAnswerCarriesNoOnBehalfOf
  Scenario: An owner's own answer needs no reason and is marked as theirs
    Given a carried option addressed to owner1
    When owner1 applies it with no reason
    Then it is applied, nothing says on behalf of anybody, and the journal
      entry says it was answered by the owner

  @test:TestAnOwnersOwnRefusalIsJournaledAsTheOwnersAndCarriesNoOnBehalfOf
  Scenario: An owner's own refusal is journaled, as the owner's
    Given a carried option addressed to owner1
    When owner1 refuses it with a reason
    Then an option_refused entry is journaled, marked as the owner's, with
      the refusal's reason and no on-behalf marking

  @test:TestAnAdminAnsweringTheirOwnDecisionIsTheOwner
  Scenario: An admin who is the owner of the request answers as the owner
    Given a carried option addressed to an admin
    When that admin applies it
    Then no reason is needed and nothing says on behalf of anybody

  @test:TestAnOperatorWhoIsNotTheOwnerIsStillRefusedWhateverReasonTheyGive
  Scenario: Only the owner or an admin may answer, whatever reason is given
    Given a carried option addressed to owner1 and an operator who is neither
      owner1 nor an admin
    When the operator applies it with a reason
    Then it stays carried

  @test:TestAnOnBehalfReasonIsCappedPlainAndEscaped
  Scenario: A hostile reason is refused or shown as text, never as markup
    Given reasons over the cap, a megabyte long, with control characters,
      invalid text, a line separator, a text-direction override or nothing
      but zero-width characters
    When an admin answers for the owner with each
    Then every one is refused and the option stays carried, a reason of
      exactly the cap is accepted, and a reason with markup is stored as
      given and shown escaped on the task page and the decision card

  @test:TestTheReasonFieldAppearsOnlyWhenAnsweringForSomeoneElse
  Scenario: The form asks for the reason only when it is needed
    Given the decision request for owner1
    When an admin who is not owner1 opens it, and when owner1 opens it
    Then the admin's form has the reason field and says it is on owner1's
      behalf, and the owner's has neither and keeps its CSRF field

  @test:TestTheDecisionCardNamesWhoAnsweredAndForWhom
  Scenario: The decision card says who answered and for whom
    Given an option an admin answered for owner1, and one owner1 answered
    When each decision card is opened
    Then the first says the admin answered on behalf of owner owner1 with the
      reason, and the second says it was answered by the owner

  @test:TestAnOnBehalfAnswerStillNeedsTheCSRFToken
  Scenario: An answer on behalf of an owner still needs the CSRF token
    Given an admin with a good reason
    When the answer is posted with a wrong token, and with none
    Then the option stays carried

  @test:TestBothAnswerEventsReachTheBusWithTheirMarking
  Scenario: Both events reach the bus, with a valid severity and the marking
    Given a console on the shared bus
    When an admin applies one option and refuses another for owner1
    Then option_applied reaches the bus as info and option_refused as low,
      each carrying on_behalf_of, the reason and who answered

  @test:TestBehalfReasonIsRequiredCappedAndPlain
  Scenario: The reason rules are one function
    Given the rules for a reason: present, at most 500 bytes, plain text
    When sentences, markup and non-latin text, and the hostile shapes above
      are offered
    Then the first are accepted trimmed and the rest are refused

  @test:TestAnAnswerValidatesItsOwnShape
  Scenario: An answer on behalf of an owner without a reason is not a valid answer
    Given the shapes an answer can take
    When each is validated
    Then an owner's own answer and the supervisor's act are valid, an admin's
      with a reason is valid, and one with no reason, a reason for nobody, or
      the wrong mark is not

  @test:TestApplyAsRefusesAnAnswerOnBehalfWithNoReasonBeforeAnySideEffect
  Scenario: Applying is refused before it changes anything
    Given an option that dismisses an anomaly and an admin's answer with no
      reason
    When it is applied
    Then the anomaly is still open, the option is still open and nothing is
      journaled

  @test:TestApplyAsOnBehalfOfAnOwnerMarksTheOptionAndTheEvent
  Scenario: Applying on behalf of an owner marks both
    Given the same option and an admin's answer with a reason
    When it is applied
    Then the anomaly is dismissed, the option records the admin, the owner and
      the reason, and option_applied carries them

  @test:TestRefuseOptionMarksAndJournalsWhoAnsweredAndForWhom
  Scenario: Refusing records and journals who answered and for whom
    Given a carried option and either the owner's or an admin's answer
    When it is refused
    Then the option and the option_refused event carry the marking that
      answer implies, at severity low

  @test:TestRefuseOptionOnBehalfWithNoReasonChangesNothing
  Scenario: A refusal on behalf of an owner with no reason changes nothing
    Given a carried option and an admin's answer with a blank reason
    When it is refused
    Then it is still carried and nothing is journaled

  @test:TestApplyForTheSupervisorAddsNoAnswerFields
  Scenario: The supervisor's own selection is not an answer
    Given an option the supervisor selects itself
    When it is applied
    Then the event carries none of the answer fields

  @test:TestEnsureOptionBehalfAddsTheColumnsSafelyTwice
  Scenario: An installation from before reads back as answered by the owner
    Given an options table without the two columns and an answered option in it
    When the migration runs twice
    Then the option still reads, with no on-behalf mark

  @test:TestApplyAsAnOwnersOwnAnswerCarriesNoOnBehalfOf
  Scenario: Applying as the owner records the owner and nothing on behalf
    Given the same option and the owner's own answer
    When it is applied
    Then the option is decided by the owner with no on-behalf mark, and the
      event says it was answered as the owner

  @test:TestAnswerDataAddsNothingForTheSupervisor
  Scenario: Answer fields are added only for a person's answer
    Given an event's data and the supervisor's own act
    When the answer fields are added
    Then nothing is added
