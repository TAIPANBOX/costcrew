package web

import (
	"net"
	"os"
	"strings"
)

// LoopbackListen reports whether addr listens only on the loopback interface
// (invariant 110). An empty host (":8321") listens on every interface, and a
// name other than localhost is not known to be loopback, so neither is. An
// address the console cannot read as host:port is not loopback either: the
// answer is yes only when the address proves it.
func LoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// InsecureHTTPEnvDefault backs -insecure-http's default with
// COSTCREW_INSECURE_HTTP, the way BehindTLSEnvDefault backs -behind-tls: the
// same three words, after trimming and lower-casing, and anything else is off,
// so a misspelt value does not switch plain HTTP on.
func InsecureHTTPEnvDefault() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COSTCREW_INSECURE_HTTP"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}
