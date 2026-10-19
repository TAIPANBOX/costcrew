package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
)

// A console that another machine can reach serves plain HTTP only when the
// operator has either put TLS in front of it (-behind-tls) or said, by name,
// that plain HTTP is intended (-insecure-http). Loopback needs neither.
func TestAnAddressAnotherMachineCanReachNeedsTLSOrTheExplicitFlag(t *testing.T) {
	cases := []struct {
		name      string
		addr      string
		behindTLS bool
		insecure  bool
		wantErr   bool
	}{
		{"loopback needs nothing", "127.0.0.1:8321", false, false, false},
		{"loopback with the TLS flag starts", "127.0.0.1:8321", true, false, false},
		{"every interface, no flag, refused", "0.0.0.0:8321", false, false, true},
		{"every interface behind TLS starts", "0.0.0.0:8321", true, false, false},
		{"every interface, plain HTTP on purpose, starts", "0.0.0.0:8321", false, true, false},
		{"an empty host listens everywhere, refused", ":8321", false, false, true},
		{"a name that is not loopback, refused", "costcrew.example:8321", false, false, true},
	}
	for _, c := range cases {
		err := checkListenAddress(c.addr, c.behindTLS, c.insecure)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, want an error = %v", c.name, err, c.wantErr)
			continue
		}
		if err != nil && (!strings.Contains(err.Error(), "-behind-tls") || !strings.Contains(err.Error(), "-insecure-http")) {
			t.Errorf("%s: the refusal %q does not name both -behind-tls and -insecure-http", c.name, err)
		}
	}
}

// The start-up log says where the -prompt-data setting came from, so an
// operator reading it can tell a chosen mode from the default.
func TestPromptDataSourceSaysWhereTheSettingCameFrom(t *testing.T) {
	newSet := func(args ...string) *flag.FlagSet {
		fs := flag.NewFlagSet("costcrew", flag.ContinueOnError)
		fs.String("prompt-data", deliver.PromptDataEnvDefault(), "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return fs
	}

	t.Setenv(deliver.PromptDataEnv, "")
	if got := promptDataSource(newSet()); !strings.Contains(got, "the default") {
		t.Errorf("neither flag nor environment set: source %q does not say it is the default", got)
	}

	if got := promptDataSource(newSet("-prompt-data", "full")); !strings.Contains(got, "command line") {
		t.Errorf("flag set: source %q does not say it came from the command line", got)
	}

	t.Setenv(deliver.PromptDataEnv, "aggregates")
	if got := promptDataSource(newSet()); !strings.Contains(got, deliver.PromptDataEnv) || strings.Contains(got, "the default") {
		t.Errorf("environment set, flag not: source %q does not name %s", got, deliver.PromptDataEnv)
	}

	if got := promptDataSource(newSet("-prompt-data", "masked")); !strings.Contains(got, "command line") {
		t.Errorf("flag and environment both set: source %q, want the command line to win", got)
	}
}
