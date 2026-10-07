package main

import (
	"os"
	"strings"
	"testing"
)

// main builds its listener through web.NewHTTPServer, which owns the timeouts.
// A literal http.Server here is how three of the four came to be unset: it set
// ReadHeaderTimeout and nothing else, and no test could see what was missing.
func TestTheConsoleServesThroughTheServerThatOwnsItsTimeouts(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	if !strings.Contains(code, "web.NewHTTPServer(") {
		t.Errorf("main.go does not build its server with web.NewHTTPServer")
	}
	if strings.Contains(code, "http.Server{") {
		t.Errorf("main.go builds a literal http.Server, whose timeouts nothing checks")
	}
}
