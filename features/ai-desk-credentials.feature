# language: en

Feature: The AI page tells a credential from an agent and says why a call was blocked

  From TokenFuse 1.7.0 a call the gateway refused for identity is filed under
  the credential that sent it, and every blocked call carries the gateway's
  reason. The AI page and the AI desk's analyst read both, the pages point at
  the gateway connector instead of saying no gateway reader exists, and an
  agent the gateway named is never linked to a crew card this console does
  not have.

  # @test:TestTheAIPageNamesACredentialAndTheReasonForEachBlock
  # @test:TestACredentialRowIsMarkedAndItsBlocksCarryTheirReason
  Scenario: A row filed under a credential reads as a credential, with the reason for its block
    Given a gateway export from TokenFuse 1.7.0 with a call refused for identity
    When a person opens the AI spend page
    Then that call's row is labelled a credential, not an agent
    And it says the call was refused for identity and nothing was reserved or spent
    And an agent's budget refusal still says the reserved amount is not in the export

  # @test:TestBlockedByReasonCountsEveryReasonAndNamesAnOlderExport
  # @test:TestTheAIPageNamesACredentialAndTheReasonForEachBlock
  Scenario: Blocked calls are counted by the gateway's reason
    Given a month of blocked calls from the gateway
    When a person opens the AI spend page
    Then a table counts the blocked calls by reason and says what each reason means
    And calls from an export that carried no reason are counted as such

  # @test:TestTheAIPageNotesAMonthMixingExportsFromBeforeAndAfter170
  # @test:TestExportMixNoteOnlyWhenAMonthHoldsBoth
  Scenario: A month mixing old and new exports says so
    Given a month holding calls from an export written before TokenFuse 1.7.0 and calls from a later one
    When a person opens the AI spend page
    Then the page says the older calls were settled before reasoning tokens were counted as output
    And it says nothing of the kind when the month holds only one kind

  # @test:TestAISpendSectionCarriesTheBlockReason
  Scenario: The AI spend analyst is told why each call was blocked
    Given a month of blocked calls with the gateway's reasons
    When the AI spend analyst's task packet is built
    Then it counts the blocked calls by reason and marks a credential as not an agent

  # @test:TestNothingSaysNoGatewayReaderExists
  Scenario: The pages point at the gateway connector rather than saying there is none
    Given nothing has been imported from the gateway yet
    When a person reads the AI page, the KPI page or an AI anomaly at team grain
    Then each points at the TokenFuse connector that would name the agent
    And a cloud desk's anomaly says its bill never names an agent

  # @test:TestACausedByAgentOffTheRosterIsNotLinkedToAStaffCard
  Scenario: An agent the gateway named is not linked to a card that does not exist
    Given an anomaly caused by an agent the gateway named, which is not on the crew roster
    When a person opens the anomaly list or the anomaly
    Then the agent is named without a link
    And an anomaly caused by a crew analyst still links to that analyst's card
