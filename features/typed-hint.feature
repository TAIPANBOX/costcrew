# language: en

# @decided 2026-10-07: before an analyst works an anomaly, the console asks
# typryx for a typed hint, the anomaly's class and its probability. Only the
# template's fields are sent; the answer is kept with the backend that
# produced it (jev, own-model or off) and shown as a suggestion labelled with
# its source; it goes into the triage packet as a hint the analyst may
# disagree with; it never decides anything. With typryx unset nothing
# changes. A timeout or a refusal is no hint, with a reason, and never holds
# detection. The bus records which backend answered, never the fields.

Feature: A typed hint from typryx before an analyst works an anomaly

  @test:TestOnlyTheTemplatesFieldsLeave
  @test:TestTheTemplateDecidesWhichFieldsLeave
  @test:TestRecentChangesAreCappedAtTenAndScopedToTheDeskAndService
  Scenario: Only the fields the template names leave the console
    Given an anomaly with a team, an agent that caused it and a registered change
    And typryx's triage.anomaly_class template names anomaly and recent_changes
    When the console asks typryx about it
    Then the state that leaves holds exactly anomaly and recent_changes
    And neither the team nor the agent appears anywhere in what was sent
    And a template naming only one field gets only that field

  @test:TestATemplateNamingAFieldThisConsoleDoesNotSendGetsNoHint
  Scenario: A template that asks for a team or an owner gets nothing
    Given a template that names a field outside the console's closed vocabulary, such as team, caused_by or owner
    When the console would ask typryx
    Then nothing is sent at all
    And the anomaly gets no hint, with a reason naming the field

  @test:TestEachBackendIsRecordedWithItsDataMode
  Scenario: The answer is kept with the data mode that produced it
    Given typryx answers from its jev, openai-logprobs or stub backend
    When the hint is recorded
    Then it is kept as jev, own-model or off, with the class, its probability and the model
    And an answer from a backend nobody defined is not kept as a hint

  @test:TestTheAnomalyPageShowsTheHintLabelledWithItsSource
  Scenario: The anomaly page shows the hint as a suggestion, labelled with its source
    Given an anomaly with a recorded hint
    When a person opens the anomaly page
    Then the page shows the suggested cause and its probability
    And it names the source: Jev hosted by TypeSafe AI, the operator's own model, or off and not a model's judgement
    And it says the suggestion changed nothing

  @test:TestTheTriagePacketCarriesTheHintAsASuggestion
  @test:TestTheRunnersPacketCarriesTheHintAndAnAnswerIsNotAskedAgain
  Scenario: The analyst reads the hint as one it may disagree with
    Given an anomaly with a recorded hint
    When the packet for the task on that anomaly is built
    Then it carries the hint after the anomaly, labelled a suggestion and not a finding
    And it tells the analyst it may disagree, and that nothing was decided because of it

  @test:TestAHintDecidesNothing
  @test:TestSaveHintNeverMovesTheAnomaly
  @test:TestALiveRunAsksTypryxBeforeTheTaskIsPriced
  Scenario: A hint decides nothing
    Given anomalies that are open and being worked, a task and an open option on one of them
    When typryx answers about both with high confidence
    Then every table is unchanged except the hint columns
    And no anomaly moves state, gains an owner or a reason, and no option is applied

  @test:TestWithTypryxUnsetTheTriagePacketIsByteIdentical
  @test:TestTheMigrationAloneChangesNoPacket
  @test:TestWithoutAHintTheAnomalyPageIsByteIdentical
  @test:TestWithoutTypryxTheConsoleAddsNoHintColumn
  Scenario: With typryx unset, nothing changes
    Given a console started without -typryx-url
    When it seeds, detects and serves
    Then the triage packets are byte for byte the ones recorded before typryx existed
    And the anomaly page is byte for byte what it was
    And the store gains no hint column

  @test:TestATimeoutIsNoHintWithAReason
  @test:TestUnreachableIsNoHintWithAReason
  @test:TestANoHintIsReportedWithItsReason
  @test:TestANoHintReasonReachesThePacket
  @test:TestTheAnomalyPageSaysWhyThereIsNoHintAndEscapesIt
  @test:TestATypryxWithoutTheTemplateIsNoHint
  @test:TestATemplateThatIsNotAChoiceIsNoHint
  Scenario: A timeout or a refusal is no hint, with a reason
    Given typryx is down, slow, refuses the ask, or does not serve the template
    When the console asks
    Then the anomaly gets no hint and a reason that says which of those happened
    And the page and the packet say why there is no hint

  @test:TestATypryxThatHangsNeverHoldsTheConsolesStart
  @test:TestAPassStopsAfterThreeAsksTypryxNeverAnswered
  @test:TestATypryxThatHangsCostsBoundedTime
  @test:TestACancelledPassStopsAtOnce
  Scenario: Typryx never holds detection or the console's start
    Given a typryx that never answers
    When the console starts with -typryx-url pointing at it
    Then detection finishes and the console answers while the ask is still waiting
    And the pass stops after three asks typryx never answered, leaving the rest for the next start

  @test:TestTheBusRecordsTheBackendNeverTheFields
  @test:TestAStartWithTypryxStoresHintsAndReportsTheBackend
  Scenario: The bus records which backend answered, never the fields
    Given the console is on the shared bus
    When typryx answers about an anomaly
    Then anomaly_hinted names the backend, the model, the class, its probability and the names of the fields sent
    And no value of any field that was sent appears on the bus

  @test:TestHostileAnswersAreNoHintNeverAGuess
  @test:TestAHostileModelNameIsDroppedNotShown
  @test:TestARedirectIsNotFollowed
  Scenario: An answer that is not exactly a choice answer is no hint
    Given typryx's answer is malformed, oversized, outside the template's options, not the most probable class, or a redirect
    When the console reads it
    Then no hint is recorded and the reason never echoes what typryx sent

  @test:TestTheKeyTravelsInItsHeaderAndNowhereElse
  @test:TestNormalizeURL
  @test:TestTheURLAndTheKeyComeFromTheirEnvironmentTwins
  @test:TestABadTypryxURLIsRefusedBeforeTheStoreOpens
  Scenario: The key is read from the environment and travels only in its header
    Given COSTCREW_TYPRYX_KEY is set
    When the console asks typryx
    Then the key is in X-Typryx-Key on every request and never in a body
    And a -typryx-url carrying credentials, a query or a fragment is refused before anything starts

  @test:TestTypryxIsAskedFromTheConsolesStartNeverFromAPage
  @test:TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork
  Scenario: Typryx is the console's second door, opened at start and never by a page
    Given the console's own source
    When its imports are walked
    Then only internal/deliver and internal/typryx build outbound requests
    And nothing the web server imports reaches internal/typryx

  @test:TestADryRunAsksTypryxNothing
  @test:TestALiveRunWithNoCeilingAsksTypryxNothing
  Scenario: The runner asks only on a live run
    Given the runner with -typryx-url
    When it runs without -live, or with -live and no ceiling
    Then typryx is asked nothing

  @test:TestTheBenchHidingPacketCarriesNoHint
  Scenario: The bench never sees a hint
    Given an anomaly with a hint asked with the change registry in its state
    When the bench builds a packet with the driver hidden
    Then the hint is not in it

  @test:TestAnAnswerIsKeptAndNeverAskedAgain
  @test:TestEnsureHintColumnsIsSafeToRunTwice
  @test:TestAStoreWithoutTheHintColumnsHasNoHintAndRefusesAWrite
  @test:TestHintSourceNamesEachDataMode
  Scenario: One answered ask per anomaly
    Given an anomaly typryx has already answered
    When a later pass runs, or a later ask fails
    Then the anomaly is not asked again and the answer is kept
