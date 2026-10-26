// Command lyth compiles Lyth source code to Go and drives the Go toolchain.
//
// Usage:
//
//	lyth build [-o output] [packages] [-- go build flags]
//	lyth run [file.lyth | package] [-- program arguments]
//	lyth check [packages]
//	lyth gen [-stdout | -in-tree] [packages]
//	lyth vet [packages]
//	lyth test [packages] [go test flags]
//	lyth fix [packages]
//	lyth fmt [-l] [-d] [-stdin] [paths]
//	lyth generate [-run re] [-skip re] [-n] [-v] [-x] [packages]
//	lyth init [module-path]
//	lyth guide [topics | topic]
//	lyth lsp
//	lyth version
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/lsp"
)

// version is set by release builds (-ldflags "-X main.version=0.1.0"); a
// `go install ...@v0.1.0` build reports its module version instead. Local
// builds keep devVersion (their pseudo-version says nothing useful).
var version = devVersion

const devVersion = "0.6.0-dev"

var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok && version == devVersion && releaseVersion.MatchString(info.Main.Version) {
		version = info.Main.Version[1:]
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `lyth is the Lyth compiler: all of Go, in fewer lines.

Usage:
  lyth build [-o output] [packages] [-- go build flags]   compile packages with the Go toolchain
  lyth run [file.lyth | package] [-- arguments]           build and run a program
  lyth check [packages]                                   type-check without building
  lyth gen [-stdout | -in-tree] [packages]                emit the generated Go code
  lyth vet [packages]                                     run go vet on generated code
  lyth test [packages] [go test flags]                    run tests (_test.lyth and _test.go files)
  lyth cover -func=c.out | -html=c.out                    show a coverage profile of Lyth code
  lyth fix [packages]                                     rewrite older Lyth syntax and Kotlin and Go spellings
  lyth fmt [-l] [-d] [paths]                              format .lyth files in Lyth's canonical layout
  lyth generate [-run re] [-n] [-v] [-x] [packages]       run the //go:generate lines of .lyth files
  lyth mod tidy                                           go mod tidy that sees imports in .lyth files
  lyth mod vendor                                         go mod vendor that sees imports in .lyth files
  lyth init [module-path]                                 create a Lyth module, with AGENTS.md for coding agents
  lyth guide [topics | topic]                             the guide to writing this version of Lyth
  lyth lsp [-no-vet]                                      run the language server on stdin and stdout
  lyth version                                            print the compiler version

Common flags:
  -v                     verbose diagnostics (include original Go toolchain messages)
  -diagnostics=json      print diagnostics as JSON lines
  -no-line-directives    omit //line directives from generated code
  -tags=a,b              build tags
  -timings               print compilation phase durations
`

type common struct {
	verbose  bool
	jsonDiag bool
	noLines  bool
	timings  bool
	tags     string
	tests    bool // include _test.lyth files (check, vet, gen, test)
	fixing   bool // lyth fix: don't suggest lyth fix
}

func (c *common) register(fs *flag.FlagSet) {
	fs.BoolVar(&c.verbose, "v", false, "verbose diagnostics")
	diagFmt := fs.String("diagnostics", "text", "diagnostic format: text or json")
	fs.BoolVar(&c.noLines, "no-line-directives", false, "omit //line directives")
	fs.BoolVar(&c.timings, "timings", false, "print compilation phase durations")
	fs.StringVar(&c.tags, "tags", "", "comma-separated build tags")
	_ = diagFmt
}

func (c *common) parseDiag(fs *flag.FlagSet) {
	if f := fs.Lookup("diagnostics"); f != nil && f.Value.String() == "json" {
		c.jsonDiag = true
	}
}

func (c *common) config(stdout, stderr io.Writer) *build.Config {
	wd, _ := os.Getwd()
	cfg := &build.Config{WorkDir: wd, Verbose: c.verbose, NoLineDirectives: c.noLines, Timings: c.timings, Tests: c.tests, Stdout: stdout, Stderr: stderr}
	if c.tags != "" {
		cfg.Tags = strings.Split(c.tags, ",")
	}
	return cfg
}

// splitArgs separates arguments after "--".
func splitArgs(args []string) ([]string, []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "lyth version %s\n", version)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "build":
		return cmdBuild(rest, stdout, stderr)
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "check":
		return cmdCheck(rest, stdout, stderr)
	case "gen":
		return cmdGen(rest, stdout, stderr)
	case "vet":
		return cmdVet(rest, stdout, stderr)
	case "test":
		return cmdTest(rest, stdout, stderr)
	case "cover":
		return cmdCover(rest, stdout, stderr)
	case "fix":
		return cmdFix(rest, stdout, stderr)
	case "generate":
		return cmdGenerate(rest, stdout, stderr)
	case "fmt":
		return cmdFmt(rest, os.Stdin, stdout, stderr)
	case "init":
		return cmdInit(rest, stdout, stderr)
	case "guide":
		return cmdGuide(rest, stdout, stderr)
	case "lsp":
		return cmdLSP(rest, os.Stdin, stdout, stderr)
	case "mod":
		if len(rest) == 1 && (rest[0] == "tidy" || rest[0] == "vendor") {
			var c common
			run := build.ModTidy
			if rest[0] == "vendor" {
				run = build.ModVendor
			}
			sink, err := run(c.config(stdout, stderr), nil)
			if sink != nil && len(sink.All()) > 0 {
				diag.Render(stderr, sink.All(), diag.RenderOptions{Color: isTerminal(stderr)})
			}
			if err != nil {
				fmt.Fprintf(stderr, "lyth: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Fprintln(stderr, "usage: lyth mod tidy | lyth mod vendor")
		return 2
	}
	if strings.HasSuffix(cmd, ".lyth") {
		return cmdRun(args, stdout, stderr)
	}
	fmt.Fprintf(stderr, "lyth: unknown command %q\n\n%s", cmd, usage)
	return 2
}

// printTimings writes phase durations when -timings is set.
func printTimings(s *build.Session, c *common, stderr io.Writer) {
	if s == nil || !c.timings {
		return
	}
	var total time.Duration
	for _, t := range s.Timings {
		fmt.Fprintf(stderr, "lyth: %-26s %8.1fms\n", t.Phase, float64(t.Duration.Microseconds())/1000)
		total += t.Duration
	}
	fmt.Fprintf(stderr, "lyth: %-26s %8.1fms\n", "total", float64(total.Microseconds())/1000)
}

// reported counts diagnostics already printed per session, so later
// stages print only new ones.
var reported = map[*build.Session]int{}

// report prints new diagnostics and returns true if there were errors.
func report(s *build.Session, c *common, stderr io.Writer) bool {
	if s == nil {
		return false
	}
	all := s.Sink.Since(reported[s])
	reported[s] = s.Sink.Len()
	if len(all) == 0 {
		return s.Sink.HasErrors()
	}
	if c.jsonDiag {
		diag.RenderJSON(stderr, all)
	} else {
		diag.Render(stderr, all, diag.RenderOptions{Color: isTerminal(stderr), Verbose: c.verbose})
		if n := countErrors(all); n > 0 {
			word := "errors"
			if n == 1 {
				word = "error"
			}
			fmt.Fprintf(stderr, "\n%d %s\n", n, word)
		}
		if n := countFixable(all); n > 0 && !c.fixing {
			fmt.Fprintf(stderr, "lyth fix rewrites %d of them: they are older Lyth syntax, or Kotlin or Go spellings\n", n)
		}
	}
	return s.Sink.HasErrors()
}

// countFixable counts the diagnostics lyth fix would settle.
func countFixable(ds []*diag.Diagnostic) int {
	fixable := map[*diag.Edit]bool{}
	for _, e := range build.ApplicableFixes(ds) {
		fixable[e] = true
	}
	n := 0
	for _, d := range ds {
		for _, e := range d.Fixes() {
			if fixable[e] {
				n++
				break
			}
		}
	}
	return n
}

func countErrors(ds []*diag.Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == diag.Error {
			n++
		}
	}
	return n
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// target interprets positional arguments.
func target(args []string) build.Target {
	if len(args) == 1 && strings.HasSuffix(args[0], ".lyth") {
		return build.Target{File: args[0]}
	}
	return build.Target{Patterns: args}
}

func compile(c *common, args []string, stdout, stderr io.Writer) (*build.Session, bool) {
	s, err := build.Compile(c.config(stdout, stderr), target(args))
	if report(s, c, stderr) {
		return s, false
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return s, false
	}
	return s, true
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.tests = true
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c.parseDiag(fs)
	s, ok := compile(&c, fs.Args(), stdout, stderr)
	printTimings(s, &c, stderr)
	if !ok {
		return 1
	}
	return 0
}

func cmdBuild(args []string, stdout, stderr io.Writer) int {
	pos, extra := splitArgs(args)
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	out := fs.String("o", "", "output file or directory")
	race := fs.Bool("race", false, "enable data race detection")
	if err := fs.Parse(pos); err != nil {
		return 2
	}
	if *race {
		extra = append([]string{"-race"}, extra...)
	}
	c.parseDiag(fs)
	s, ok := compile(&c, fs.Args(), stdout, stderr)
	if !ok {
		return 1
	}
	var pkgs []string
	if s.Mod.Synthetic {
		pkgs = []string{"."}
	} else {
		for _, p := range s.Targets {
			pkgs = append(pkgs, p.Dir.ImportPath)
		}
	}
	output := *out
	if output != "" && !filepath.IsAbs(output) {
		wd, _ := os.Getwd()
		dir := strings.HasSuffix(output, "/") || strings.HasSuffix(output, string(filepath.Separator))
		output = filepath.Join(wd, output)
		if dir {
			output += string(filepath.Separator) // -o bin/ writes bin/<name>, as go build does
		}
	}
	if output == "" && len(s.MainPackages()) == 1 && len(pkgs) == 1 {
		wd, _ := os.Getwd()
		name := filepath.Base(s.MainPackages()[0].Dir.Dir)
		if s.Mod.Synthetic {
			name = strings.TrimSuffix(filepath.Base(fs.Arg(0)), ".lyth")
		}
		output = filepath.Join(wd, name+s.ExeSuffix())
	}
	okBuild, err := s.GoBuild(output, pkgs, extra)
	printTimings(s, &c, stderr)
	if report(s, &c, stderr) || !okBuild {
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return 0
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	race := fs.Bool("race", false, "enable data race detection")
	ldflags := fs.String("ldflags", "", "flags for the Go linker (go build -ldflags)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var goFlags []string
	if *race {
		goFlags = append(goFlags, "-race")
	}
	if *ldflags != "" {
		goFlags = append(goFlags, "-ldflags="+*ldflags)
	}
	c.parseDiag(fs)
	rest := fs.Args()
	var tgt []string
	var progArgs []string
	if len(rest) > 0 && rest[0] != "--" {
		tgt, progArgs = rest[:1], rest[1:]
	} else {
		progArgs = rest
	}
	if len(progArgs) > 0 && progArgs[0] == "--" {
		progArgs = progArgs[1:]
	}
	s, ok := compile(&c, tgt, stdout, stderr)
	if !ok {
		return 1
	}
	mains := s.MainPackages()
	if len(mains) == 0 {
		fmt.Fprintln(stderr, "lyth: no main package to run (declare func main())")
		return 1
	}
	pkg := mains[0].Dir.ImportPath
	if len(mains) > 1 {
		if len(tgt) == 0 {
			var names []string
			for _, m := range mains {
				names = append(names, m.Dir.Rel)
			}
			sort.Strings(names)
			fmt.Fprintf(stderr, "lyth: several main packages (%s); choose one: lyth run <dir>\n", strings.Join(names, ", "))
			return 1
		}
		wd, _ := os.Getwd()
		dir, _ := filepath.Abs(filepath.Join(wd, tgt[0]))
		for _, m := range mains {
			if m.Dir.Dir == dir {
				pkg = m.Dir.ImportPath
			}
		}
	}
	if s.Mod.Synthetic {
		pkg = "."
	}
	code, err := s.GoRun(pkg, progArgs, goFlags)
	if report(s, &c, stderr) {
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return code
}

func cmdGen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.tests = true
	c.register(fs)
	toStdout := fs.Bool("stdout", false, "print generated code to standard output")
	inTree := fs.Bool("in-tree", false, "write generated files next to their .lyth sources")
	force := fs.Bool("force", false, "with -stdout, print generated code even if it fails validation (debugging)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c.parseDiag(fs)
	cfg := c.config(stdout, stderr)
	cfg.InTree = *inTree
	cfg.SparseLineDirectives = true
	s, err := build.Compile(cfg, target(fs.Args()))
	if report(s, &c, stderr) {
		if *force && *toStdout && s != nil {
			for _, p := range s.Packages {
				for _, g := range p.Generated {
					fmt.Fprintf(stdout, "// ---- %s/%s\n%s", p.Dir.Rel, g.Name, g.Code)
				}
			}
		}
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	switch {
	case *inTree:
		files, err := s.WriteInTree()
		for _, f := range files {
			fmt.Fprintln(stdout, f)
		}
		if err != nil {
			fmt.Fprintf(stderr, "lyth: %v\n", err)
			return 1
		}
	case *toStdout:
		// lyth gen -stdout x.lyth prints x.lyth.go; a package, all its files.
		only := ""
		if t := target(fs.Args()); t.File != "" {
			only = genName(filepath.Base(t.File))
		}
		for _, p := range s.Packages {
			if p.Dir.Std {
				continue // the standard library is the toolchain's, not the module's code
			}
			for _, g := range p.Generated {
				if only != "" && g.Name != only {
					continue
				}
				fmt.Fprintf(stdout, "// ---- %s/%s\n%s", p.Dir.Rel, g.Name, g.Code)
			}
		}
	default:
		fmt.Fprintf(stdout, "generated Go code is in %s\n", s.GenDir)
	}
	return 0
}

func cmdVet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.tests = true
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c.parseDiag(fs)
	s, ok := compile(&c, fs.Args(), stdout, stderr)
	if !ok {
		return 1
	}
	var pkgs []string
	for _, p := range s.Targets {
		pkgs = append(pkgs, p.Dir.ImportPath)
	}
	okVet, err := s.GoVet(pkgs)
	if report(s, &c, stderr) || !okVet {
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return 0
}

// goTestValueFlags are go test flags whose value may follow as a separate
// argument (-run TestX). lyth test must know them to tell values from
// package patterns.
var goTestValueFlags = map[string]bool{
	"bench": true, "benchtime": true, "blockprofile": true, "blockprofilerate": true, "count": true,
	"covermode": true, "coverpkg": true, "coverprofile": true, "cpu": true, "cpuprofile": true,
	"exec": true, "fuzz": true, "fuzzminimizetime": true, "fuzztime": true, "gcflags": true,
	"ldflags": true, "list": true, "memprofile": true, "memprofilerate": true, "mutexprofile": true,
	"mutexprofilefraction": true, "o": true, "outputdir": true, "p": true, "parallel": true,
	"run": true, "shuffle": true, "skip": true, "timeout": true, "trace": true, "vet": true,
}

// cmdTest compiles the packages with their tests and runs go test. Flags
// other than Lyth's own (-diagnostics, -no-line-directives, -tags, -timings)
// go to go test, so lyth test -v -run TestX ./... works like go test.
func cmdTest(args []string, stdout, stderr io.Writer) int {
	var c common
	var patterns, goArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			goArgs = append(goArgs, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			patterns = append(patterns, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		next := func() string {
			if hasValue || i+1 >= len(args) {
				return value
			}
			i++
			return args[i]
		}
		switch name {
		case "diagnostics":
			c.jsonDiag = next() == "json"
		case "no-line-directives":
			c.noLines = true
		case "timings":
			c.timings = true
		case "tags":
			c.tags = next()
		default:
			goArgs = append(goArgs, a)
			if !hasValue && goTestValueFlags[name] && i+1 < len(args) {
				i++
				goArgs = append(goArgs, args[i])
			}
		}
	}
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	c.tests = true
	s, err := build.Compile(c.config(stdout, stderr), build.Target{Patterns: patterns})
	printTimings(s, &c, stderr)
	if report(s, &c, stderr) {
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	code, err := s.GoTest(patterns, goArgs)
	if err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return code
}

// cmdCover shows a coverage profile written by lyth test. go tool cover
// -func parses every file of the profile as Go, so lyth computes -func
// itself; other modes (-html) go to go tool cover, which reads .lyth files
// as plain text.
func cmdCover(args []string, stdout, stderr io.Writer) int {
	var profile, output string
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if (name == "func" || name == "o") && !hasValue && i+1 < len(args) {
			i++
			value = args[i]
		}
		switch name {
		case "func":
			profile = value
		case "o":
			output = value
		}
	}
	if profile == "" {
		if len(args) == 0 {
			fmt.Fprintln(stderr, "usage: lyth cover -func=c.out [-o file] | lyth cover -html=c.out [-o file]")
			return 2
		}
		cmd := exec.Command("go", append([]string{"tool", "cover"}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode()
			}
			fmt.Fprintf(stderr, "lyth: %v\n", err)
			return 1
		}
		return 0
	}
	w := stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			fmt.Fprintf(stderr, "lyth: %v\n", err)
			return 1
		}
		defer f.Close()
		w = f
	}
	wd, _ := os.Getwd()
	if err := build.CoverFunc(wd, profile, w, nil); err != nil {
		fmt.Fprintf(stderr, "lyth: %v\n", err)
		return 1
	}
	return 0
}

// cmdGenerate runs the //go:generate directives of .lyth files, which go
// generate cannot see.
func cmdGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runRE := fs.String("run", "", "run only the directives that match this regular expression")
	skipRE := fs.String("skip", "", "skip the directives that match this regular expression")
	dry := fs.Bool("n", false, "print the commands but do not run them")
	verbose := fs.Bool("v", false, "print the names of packages and files as they are processed")
	trace := fs.Bool("x", false, "print the commands as they run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := build.GenerateOptions{DryRun: *dry, Verbose: *verbose, Trace: *trace}
	for _, re := range []struct {
		src string
		dst **regexp.Regexp
	}{{*runRE, &opts.Run}, {*skipRE, &opts.Skip}} {
		if re.src == "" {
			continue
		}
		r, err := regexp.Compile(re.src)
		if err != nil {
			fmt.Fprintf(stderr, "lyth generate: %v\n", err)
			return 2
		}
		*re.dst = r
	}
	var c common
	if err := build.Generate(c.config(stdout, stderr), fs.Args(), opts); err != nil {
		fmt.Fprintf(stderr, "lyth generate: %v\n", err)
		return 1
	}
	return 0
}

// cmdLSP serves the Language Server Protocol on stdin and stdout for
// editors. go vet runs on save unless -no-vet is given or LYTH_LSP_NO_VET is set.
func cmdLSP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lsp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noVet := fs.Bool("no-vet", false, "do not run go vet when a file is saved")
	fs.Bool("stdio", true, "use stdin and stdout (the only transport)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	vet := !*noVet && os.Getenv("LYTH_LSP_NO_VET") == ""
	if err := lsp.Run(stdin, stdout, lsp.Options{Version: version, Vet: vet}); err != nil {
		fmt.Fprintf(stderr, "lyth lsp: %v\n", err)
		return 1
	}
	return 0
}

// genName is the name of the Go file generated from a .lyth file: x.lyth.go,
// and x.lyth_test.go for x_test.lyth.
func genName(lyth string) string {
	if base, ok := strings.CutSuffix(lyth, "_test.lyth"); ok {
		return base + ".lyth_test.go"
	}
	return lyth + ".go"
}
