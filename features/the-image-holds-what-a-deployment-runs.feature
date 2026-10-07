# language: en

Feature: The image holds every binary a deployment needs, from a base nobody can move

  # @claude 2026-10-07
  # costcrew#75, from the appliance proving run of 2026-09-17: the image shipped
  # the console and the runner, while the documentation named two more binaries
  # a deployment needs to close the loop (pushing decided budgets to the
  # gateway, and the identity plane's source). A machine with no Go toolchain
  # could not run them. The ask was both binaries in the image, with the README
  # saying how each is invoked from the launcher's compose file.
  #
  # The second half is supply chain: the two base images were named by tag, and
  # a tag can be repointed under an operator without anyone choosing that.

  @test:TestTheImageCarriesTheEnforceAndIdryxSourceBinaries
  Scenario: the image carries the enforce and idryx-source binaries
    Given the image build in the Dockerfile
    When its runtime stage is read
    Then costcrew, costcrew-run, costcrew-enforce and costcrew-idryxsource
      are all copied into it

  @test:TestTheDockerfileShipsExactlyTheBinariesTheManifestSaysItDoes
  Scenario: the manifest and the Dockerfile cannot disagree about the image
    Given components.json marks which components ship in the image
    When the Dockerfile copies a binary the manifest does not mark, or the
      manifest marks one the Dockerfile does not copy
    Then the test names that binary and fails

  @test:TestTheDockerfileComparisonSeesEachWayTheyCanDisagree
  Scenario: every way the two lists can differ is seen
    Given Dockerfiles written for the purpose, each differing from the manifest
      in one way: a copy dropped, a build dropped, a binary copied but not
      declared, built but never copied, copied under another name, built from
      another package, or nothing named at all
    When each is compared with the manifest
    Then each difference is reported, and the matching pair reports none

  @test:TestEveryBaseImageIsPinnedByDigest
  Scenario: both base images are named by digest
    Given the Dockerfile's FROM lines
    When each image reference is read
    Then each one carries a sha256 digest, not a tag alone

  @test:TestADigestPinIsRecognisedOnlyWhenItIsThere
  Scenario: a tag with no digest is not mistaken for a pin
    Given a FROM line naming only a tag, and another naming a digest
    When the pins are checked
    Then only the tag-only reference is reported as unpinned
