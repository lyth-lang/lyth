package main

// lyth guide, lyth init, and lyth fix: how AI coding agents (and people)
// learn the installed version of Lyth. The guide is embedded in the binary,
// and projects only point at it: the AGENTS.md of every project made so far
// and the Claude Code skill in skills/lyth run `lyth guide`, `lyth check`,
// and `lyth fix`, so those commands and the guide topics must keep working
// in every later version.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lyth-lang/lyth/docs"
	"github.com/lyth-lang/lyth/internal/build"
)

// guideTopics are the pages lyth guide <topic> prints, by name. The guide
// (docs/guide/writing-lyth.md) lists each of them.
var guideTopics = []struct{ name, about, file string }{
	{"tour", "the language by example", "guide/tour.md"},
	{"go", "Lyth for Go programmers: each feature next to the Go it replaces", "guide/lyth-for-go-programmers.md"},
	{"cheatsheet", "Lyth syntax next to the Go it compiles to", "guide/cheatsheet.md"},
	{"interop", "Go packages, modules, and Lyth packages seen from Go", "guide/go-interop.md"},
	{"testing", "tests, benchmarks, and fuzzing", "guide/testing.md"},
	{"tooling", "the lyth command, diagnostics, editors, and builds", "guide/tooling.md"},
	{"language", "the language reference", "spec/language.md"},
	{"types", "the type system: nullability, platform types, generics", "spec/types.md"},
	{"errors", "error handling in depth", "spec/errors.md"},
	{"collections", "the lyth/coll collection functions", "design/collections.md"},
}

const guideFile = "guide/writing-lyth.md"

func cmdGuide(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		text, err := fs.ReadFile(docs.FS, guideFile)
		if err != nil {
			fmt.Fprintf(stderr, "lyth: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "This is the guide to lyth %s, the Lyth installed here.\n\n%s", version, text)
	case len(args) == 1 && args[0] == "topics":
		for _, t := range guideTopics {
			fmt.Fprintf(stdout, "%-12s %s\n", t.name, t.about)
		}
	case len(args) == 1:
		for _, t := range guideTopics {
			if t.name == args[0] {
				text, err := fs.ReadFile(docs.FS, t.file)
				if err != nil {
					fmt.Fprintf(stderr, "lyth: %v\n", err)
					return 1
				}
				stdout.Write(text)
				return 0
			}
		}
		names := make([]string, len(guideTopics))
		for i, t := range guideTopics {
			names[i] = t.name
		}
		fmt.Fprintf(stderr, "lyth: there is no guide topic %q; the topics are %s\n", args[0], strings.Join(names, ", "))
		return 2
	default:
		fmt.Fprintln(stderr, "usage: lyth guide [topics | <topic>]")
		return 2
	}
	return 0
}

// agentsMD is the AGENTS.md lyth init writes. It holds no language rules,
// only commands, so it stays true when Lyth changes.
const agentsMD = `# Notes for AI coding agents

The ` + "`.lyth`" + ` files in this module are written in Lyth, a programming language that
compiles to Go. Lyth changes between versions, and what you know about it may describe an
older one.

Before you write or change Lyth code, run ` + "`lyth guide`" + ` and read it: it is the guide to
the Lyth version installed here. Check each change with ` + "`lyth check`" + `; when it reports
older syntax, ` + "`lyth fix`" + ` rewrites it. Run the program with ` + "`lyth run`" + ` and the tests
with ` + "`lyth test ./...`" + `. Don't edit the generated Go in ` + "`.lyth/`" + `, and don't write
` + "`.go`" + ` files to get around a Lyth error.
`

// claudeMD makes Claude Code, which reads CLAUDE.md, read AGENTS.md too.
const claudeMD = "@AGENTS.md\n"

const helloLyth = `func main() {
    println("Hello, Lyth!")
}
`

// modulePathFor turns a directory name into a module path go mod init
// accepts: "my proj 2" becomes "my-proj-2".
func modulePathFor(dir string) string {
	var b strings.Builder
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_', r == '~':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	path := strings.Trim(b.String(), "-.")
	if path == "" {
		return "app"
	}
	return path
}

// cmdInit makes the current directory a Lyth module, or prepares an
// existing Go module: go.mod (through go mod init), main.lyth when there is
// no Lyth code yet, AGENTS.md and CLAUDE.md for coding agents, and .lyth/ in
// .gitignore. Files that exist are kept; an existing go.mod's go directive
// is raised to the version Lyth needs.
func cmdInit(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: lyth init [module-path]")
		return 2
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real // as the module root is
	}
	mod, err := build.FindModule(wd)
	root := wd
	switch {
	case err == nil:
		root = mod.Root
		if len(args) == 1 && args[0] != mod.Path {
			fmt.Fprintf(stderr, "lyth: %s is already in module %s (%s)\n", wd, mod.Path, filepath.Join(root, "go.mod"))
			return 1
		}
		if !raiseGoVersion(root, stdout, stderr, true) || !declareLythVersion(root, stdout, stderr, true) {
			return 1
		}
	default:
		path := modulePathFor(filepath.Base(wd))
		if len(args) == 1 {
			path = args[0]
		}
		cmd := exec.Command("go", "mod", "init", path)
		cmd.Dir, cmd.Stdout, cmd.Stderr = wd, stdout, stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(stderr, "lyth: go mod init %s failed; give a module path: lyth init example.com/app\n", path)
			return 1
		}
		// Go 1.26's go mod init writes the previous release (go 1.25.0).
		if !raiseGoVersion(wd, stdout, stderr, false) || !declareLythVersion(wd, stdout, stderr, false) {
			return 1
		}
	}
	created := func(name string) { fmt.Fprintf(stdout, "lyth: created %s\n", name) }
	writeNew := func(path, content string) bool {
		if _, err := os.Stat(path); err == nil {
			return false
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintf(stderr, "lyth: %v\n", err)
			return false
		}
		rel, _ := filepath.Rel(wd, path)
		created(rel)
		return true
	}
	if !hasLyth(root) {
		writeNew(filepath.Join(wd, "main.lyth"), helloLyth)
	}
	writeNew(filepath.Join(root, "AGENTS.md"), agentsMD)
	writeNew(filepath.Join(root, "CLAUDE.md"), claudeMD)
	if err := ignoreGenerated(root); err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return 0
}

// raiseGoVersion raises the go directive of root's go.mod to the version
// Lyth needs (build.MinGoMinor), saying so when announce is set. It reports
// false after an error.
func raiseGoVersion(root string, stdout, stderr io.Writer, announce bool) bool {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return true
	}
	v, _ := build.GoDirective(string(data))
	if !build.GoVersionBelow(v, build.MinGoMinor) {
		return true
	}
	want := fmt.Sprintf("1.%d", build.MinGoMinor)
	cmd := exec.Command("go", "mod", "edit", "-go="+want)
	cmd.Dir, cmd.Stderr = root, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "lyth: go.mod declares go %s; Lyth needs go %s: go mod edit -go=%s failed\n", v, want, want)
		return false
	}
	if announce {
		fmt.Fprintf(stdout, "lyth: raised the go version in go.mod from %s to %s, which Lyth needs\n", v, want)
	}
	return true
}

// declareLythVersion adds the //lyth:version line to root's go.mod when it
// has none, declaring the version this lyth compiles (ADR 0054), saying so
// when announce is set. A line it has is kept: only lyth fix -lang raises
// it. It reports false after an error.
func declareLythVersion(root string, stdout, stderr io.Writer, announce bool) bool {
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	if v, _, _ := build.LythDirective(string(data)); v != "" {
		return true
	}
	if _, err := build.SetLythDirective(path, build.LangVersion); err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return false
	}
	if announce {
		fmt.Fprintf(stdout, "lyth: go.mod now declares the Lyth version the module is written for (//lyth:version %s)\n", build.LangVersion)
	}
	return true
}

// hasLyth reports whether the module at root has a .lyth file.
func hasLyth(root string) bool {
	found := errors.New("found")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".lyth") {
			return found
		}
		return nil
	})
	return err == found
}

// ignoreGenerated adds .lyth/, where builds put the generated Go, to the
// module's .gitignore.
func ignoreGenerated(root string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if l := strings.TrimSpace(line); l == ".lyth" || l == ".lyth/" || l == "/.lyth/" || l == "/.lyth" {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(path, append(data, ".lyth/\n"...), 0o644)
}

// cmdFix rewrites older, Kotlin, and Go spellings of Lyth constructs, the
// edits the compiler marks as fixes, and reports what is left.
func cmdFix(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fix", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var c common
	c.tests = true
	c.fixing = true
	c.register(flags)
	lang := flags.String("lang", "", "raise the Lyth version go.mod declares (//lyth:version) to `version`, and apply the fixes up to it")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	c.parseDiag(flags)
	if *lang != "" && !raiseLythVersion(*lang, stdout, stderr) {
		return 1
	}
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

// raiseLythVersion is lyth fix -lang: it raises the //lyth:version line of
// the current module's go.mod to v before the fixes are applied, so the
// compilation that finds them uses v's rules.
func raiseLythVersion(v string, stdout, stderr io.Writer) bool {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return false
	}
	mod, err := build.FindModule(wd)
	if err != nil {
		fmt.Fprintf(stderr, "lyth: -lang needs a module: %v\n", err)
		return false
	}
	old, err := build.SetLythDirective(filepath.Join(mod.Root, "go.mod"), v)
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return false
	}
	switch old {
	case v:
	case "":
		fmt.Fprintf(stdout, "lyth: go.mod now declares Lyth %s\n", v)
	default:
		fmt.Fprintf(stdout, "lyth: raised the Lyth version in go.mod from %s to %s\n", old, v)
	}
	return true
}
