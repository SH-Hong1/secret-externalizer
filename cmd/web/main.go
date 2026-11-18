// Command web serves the secret-externalizer user interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

// version identifies the build and is overridden at link time.
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "web: %v\n", err)
		os.Exit(1)
	}
}

// run parses args and executes the program. It takes its inputs and outputs
// as parameters so tests can call it without touching process state.
func run(_ context.Context, args []string, _ func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		if _, err := fmt.Fprintf(stdout, "secret-externalizer web %s\n", version); err != nil {
			return fmt.Errorf("writing version: %w", err)
		}
	}
	return nil
}
