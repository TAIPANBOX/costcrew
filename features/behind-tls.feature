# language: en

Feature: A cookie is marked Secure when a TLS proxy is what a browser actually talks to

  @claude 2026-09-16
  """
  The documented deployment (cmd/costcrew/main.go's own -addr help text: put a
  proxy in front for TLS) means this process only ever sees plain HTTP on
  loopback. The session cookie's Secure bit was read from r.TLS != nil alone,
  which is nil on every request in exactly that shape, so the cookie was never
  Secure in the deployment this binary recommends. -behind-tls is the
  operator's explicit statement that such a proxy is there.
  """

  @test:TestLoginOverPlainHTTPIsSecureWhenBehindTLS
  Scenario: -behind-tls marks the session cookie Secure even over plain HTTP
    Given the console is started with -behind-tls
    When somebody logs in over a plain HTTP connection to this process
    Then the session cookie it issues carries Secure

  @test:TestLoginOverPlainHTTPStaysInsecureWithoutBehindTLS
  Scenario: without the flag, nothing changes
    Given the console is started without -behind-tls
    When somebody logs in over a plain HTTP connection to this process
    Then the session cookie it issues does not carry Secure, exactly as
      before this flag existed

  @test:TestEveryCookieCarriesSecureUnderTheFlag
  Scenario: every cookie carries Secure under the flag, not only the session one
    Given the console is started with -behind-tls
    When signing up, logging in and logging out each set a cookie
    Then every one of those cookies carries Secure

  @test:TestEveryCookieGoesThroughOneSecurePosture
  Scenario: a future cookie cannot be added without the flag reaching it
    Given internal/web's own source
    When it is searched for every place that calls http.SetCookie directly
    Then there is exactly one, inside the single helper that decides Secure,
      so a second one added later would be a cookie -behind-tls never reaches
