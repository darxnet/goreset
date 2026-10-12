// Command goreset writes a Reset method for the struct types named by -type
// into <type>_reset.gen.go in the current package.
//
//	//go:generate go tool goreset -type=Request,Response
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/darxnet/goreset"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is main without the process: it returns the exit code.
func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("goreset", flag.ContinueOnError)
	flags.SetOutput(stderr)

	typeFlag := flags.String("type", "", "comma-separated names of the struct types to generate Reset for")
	dirFlag := flags.String("dir", ".", "directory of the package")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	say := func(format string, a ...any) {
		_, _ = fmt.Fprintf(stderr, format+"\n", a...)
	}

	if *typeFlag == "" {
		say("goreset: -type is required")
		flags.Usage()

		return 2
	}

	pkg, err := goreset.Load(*dirFlag)
	if err != nil {
		say("%v", err)

		return 1
	}

	for typeName := range strings.SplitSeq(*typeFlag, ",") {
		typeName = strings.TrimSpace(typeName)
		if typeName == "" {
			continue
		}

		res, err := pkg.Analyze(typeName)
		if err != nil {
			say("%v", err)

			return 1
		}

		name := filepath.Join(*dirFlag, goreset.FileName(typeName))

		if err := os.WriteFile(name, res.Source, 0o600); err != nil {
			say("goreset: write %s: %v", name, err)

			return 1
		}

		say("goreset: wrote %s", name)

		if !res.Bulk {
			say("goreset: %s is reset field by field; for one assignment:", typeName)

			for _, hint := range res.Hints {
				say("  - %s", hint)
			}
		}
	}

	return 0
}
