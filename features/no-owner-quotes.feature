# language: en

Feature: A public repository records the owner's decisions, never his words

  @decided 2026-09-09
  """
  In any public repository there is no verbatim quote of the owner, no
  provenance marker carrying his name, and no sentence saying that he said,
  asked, decided or wanted something. A decision is still recorded, as
  @decided with its date and a paraphrase, and that line is never edited
  afterwards. His name as author, maintainer or copyright holder is not
  covered.
  """

  @decided 2026-10-08
  """
  This repository is cleaned of all of it completely, and kept that way.
  """

  @test:TestNoTrackedFileQuotesOrAttributesTheOwner
  Scenario: Nothing tracked quotes the owner or names him as the one who decided
    Given every file git tracks in this repository, binary files aside
    When each is read line by line
    Then none carries the old provenance marker with his name, none carries a
      quotation in guillemets in his language, none carries Ukrainian prose
      outside a Go string literal or a testdata fixture, and none names him
      outside a line about authorship

  @test:TestTheOwnerQuoteWalkSeesEveryShapeItNames
  Scenario: Each shape the rule names is refused where it would actually appear
    Given the marker on a scenario header, in a Go comment and in a Go string,
      guillemets around Cyrillic, Ukrainian in a line comment, a block
      comment, a docstring, a stylesheet and a shell comment, and his name as
      the one who found, asked or called something
    When the rule reads each one as the file it would sit in
    Then every one is refused, so a clean tree is the rule finding nothing
      and not the rule being unable to find anything

  @test:TestTheOwnerQuoteWalkLeavesTheseAlone
  Scenario: Authorship, addresses and test data are not quotes
    Given his name as copyright holder or author, his name and the marker's
      shape inside a URL, a lower-case username in test data, Cyrillic as
      hostile input in a Go string or a testdata fixture, a paraphrased
      decision, and English words in guillemets
    When the rule reads each one
    Then none is refused
