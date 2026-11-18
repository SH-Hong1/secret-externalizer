// Command web serves the secret-externalizer user interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// version identifies the build. It is set at link time with -ldflags -X and
// is never assigned while the program runs.
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		// Standard error is the last place left to report to, so a failed
		// write has nowhere to go.
		_, _ = fmt.Fprintf(os.Stderr, "web: %v\n", err)
		os.Exit(1)
	}
}

// run parses args and executes the program. It takes its inputs and outputs
// as parameters so tests can call it without touching process state.
//
// A flag error is returned and not printed, so the caller reports it once.
// Asking for help prints the usage to stdout and is not an error.
func run(_ context.Context, args []string, _ func(string) string, stdout, _ io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		if _, err := fmt.Fprintf(stdout, "secret-externalizer web %s\n", version); err != nil {
			return fmt.Errorf("writing version: %w", err)
		}
	}
	return nil
}
