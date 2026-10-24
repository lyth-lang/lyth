package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lyth-lang/lyth/internal/build"
)

// cmdFix rewrites older, Kotlin, and Go spellings of Lyth constructs, the
// edits the compiler marks as fixes, and reports what is left.
func cmdFix(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fix", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var c common
	c.tests = true
	c.fixing = true
	c.register(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	c.parseDiag(flags)
	files, s, err := build.Fix(c.config(stdout, stderr), target(flags.Args()))
	wd, _ := os.Getwd()
	for _, f := range files {
		if rel, err := filepath.Rel(wd, f); err == nil && !strings.HasPrefix(rel, "..") {
			f = rel
		}
		fmt.Fprintf(stdout, "fixed %s\n", f)
	}
	if report(s, &c, stderr) {
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return 0
}
