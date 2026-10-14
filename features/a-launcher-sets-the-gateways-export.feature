# language: en

Feature: A launcher sets the gateway's export folders, and the console reads them on its own

  The AI pages read the gateway's per-call export. Until now a person had to
  save the export's folder on the connector's page and click Import every
  time, so a console a launcher started beside two gateways read nothing all
  day. The launcher can now name the folders on the command line; the
  console reads them at start and again on an interval, and the connector's
  page says what the last automatic import read or why it read nothing.

  # @test:TestTheConsoleReadsTheGatewaysExportFromFocusDir
  # @test:TestTheLaunchersFoldersAreReadAndRecordedOnTheConnector
  Scenario: The console reads the folders the launcher names
    Given a launcher starts the console with the gateway's export folder, and the OpenAI-wire door's
    When the console starts
    Then it reads both folders, each as its own door, and says so in its log
    And the connector's page holds the launcher's folders and what the import read

  # @test:TestWatchFocusReadsTheFolderAgainOnItsInterval
  Scenario: New exports are read on the interval
    Given the gateway writes a new export after the console started
    When the interval comes round
    Then the new calls are read without anybody clicking Import

  # @test:TestAnAutomaticImportOnTheGeneratedEstateSaysWhyItReadNothing
  Scenario: The generated estate is replaced only with the launcher's yes
    Given the console still holds the generated estate it starts with
    When an automatic import finds real calls without the launcher's yes to replace it
    Then nothing is imported and the connector's page says why
    And with the launcher's yes the generated estate is replaced and the journal says so

  # @test:TestCheckFocusInputRefusesAMisconfiguredLauncher
  # @test:TestAMisconfiguredFocusDirRefusesToStart
  Scenario: A misconfigured launcher stops the start
    Given a second door's folder with no main folder, a folder that is not there, one folder as both doors, or an interval of seconds
    When the console starts
    Then it refuses to start and names the mistake

  # @test:TestTheConsoleWithoutFocusDirSaysTheConnectorIsLeftToItsPage
  Scenario: Without the folders the console says the connector is left to its page
    Given the console is started with no export folder
    When it starts
    Then its log says the AI pages read the gateway only when a person imports on the connector
