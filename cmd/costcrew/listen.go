package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// checkListenAddress refuses to start a console that other machines can reach
// over plain HTTP (invariant 110). Loopback needs nothing. Any other address
// needs -behind-tls (a TLS proxy in front) or -insecure-http (plain HTTP there
// is deliberate). The refusal names both, so the operator can choose.
func checkListenAddress(addr string, behindTLS, insecureHTTP bool) error {
	if web.LoopbackListen(addr) || behindTLS || insecureHTTP {
		return nil
	}
	return fmt.Errorf("-addr %s is not a loopback address, so this console would serve plain HTTP "+
		"to other machines. Put a TLS-terminating proxy in front and set -behind-tls (or "+
		"COSTCREW_BEHIND_TLS), or, if plain HTTP on this address is deliberate, set -insecure-http "+
		"(or COSTCREW_INSECURE_HTTP). Refusing to start", addr)
}

// promptDataSource says where the -prompt-data setting came from, for the
// start-up log (invariant 110): a chosen mode and the default look the same
// in the mode line, so the log has to say which it is.
func promptDataSource(fs *flag.FlagSet) string {
	onCommandLine := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt-data" {
			onCommandLine = true
		}
	})
	switch {
	case onCommandLine:
		return "-prompt-data on the command line"
	case os.Getenv(deliver.PromptDataEnv) != "":
		return deliver.PromptDataEnv + " in the environment"
	}
	return "the default: neither -prompt-data nor " + deliver.PromptDataEnv + " is set"
}
