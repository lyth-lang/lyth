// Package tests holds end-to-end suites that compile Lyth programs with the
// real Go toolchain:
//
//	testdata/run/*.lyth    programs that must build and print the expected output
//	testdata/check/*.lyth  programs with inline // ERROR expectations
//	testdata/gen/*.lyth    generated Go compared byte-for-byte with testdata/gen/*.go.golden
//
// Run with -update to rewrite golden files.
package tests

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/emit"
)

var update = flag.Bool("update", false, "update golden files")

// newModule creates a temporary module with one package directory per
// test file and returns the module root and the directory for each file.
func newModule(t *testing.T, files []string, modPath string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+modPath+"\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".lyth")
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), data, 0o644); err != nil {
			t.Fatal(err)
		}
		dirs[f] = dir
	}
	return root, dirs
}

func render(diags []*diag.Diagnostic) string {
	var b bytes.Buffer
	diag.Render(&b, diags, diag.RenderOptions{Verbose: true})
	return b.String()
}

// expectations parses trailing "// Output:" blocks and "// Exit: N".
type runExpect struct {
	output   string
	exit     int
	stderr   []string
	hasOuput bool
}

func parseRunExpect(src string) runExpect {
	var e runExpect
	lines := strings.Split(src, "\n")
	inOutput := false
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "// Output:":
			inOutput = true
			e.hasOuput = true
			continue
		case strings.HasPrefix(t, "// Exit:"):
			e.exit, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(t, "// Exit:")))
			inOutput = false
			continue
		case strings.HasPrefix(t, "// Stderr:"):
			e.stderr = append(e.stderr, strings.TrimSpace(strings.TrimPrefix(t, "// Stderr:")))
			inOutput = false
			continue
		}
		if inOutput {
			if !strings.HasPrefix(t, "//") {
				inOutput = false
				continue
			}
			line := strings.TrimPrefix(t, "//")
			line = strings.TrimPrefix(line, " ")
			out = append(out, line)
		}
	}
	e.output = strings.Join(out, "\n")
	return e
}

func TestRun(t *testing.T) {
	files, _ := filepath.Glob("testdata/run/*.lyth")
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no run tests")
	}
	root, dirs := newModule(t, files, "lythtest")
	cfg := &build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin") + string(filepath.Separator)
	var pkgs []string
	for _, f := range files {
		pkgs = append(pkgs, "lythtest/"+filepath.Base(dirs[f]))
	}
	ok, err := s.GoBuild(bin, pkgs, nil)
	if err != nil || !ok {
		t.Fatalf("go build failed: %v\n%s", err, render(s.Sink.All()))
	}
	// Generated code must pass go vet.
	if ok, err := s.GoVet(pkgs); err != nil || !ok {
		t.Errorf("go vet failed on generated code: %v\n%s", err, render(s.Sink.All()))
	}
	// Generated code must be gofmt-idempotent.
	for _, p := range s.Packages {
		for _, g := range p.Generated {
			formatted, err := format.Source(g.Code)
			if err != nil {
				t.Errorf("%s: gofmt failed: %v", g.Name, err)
			} else if !bytes.Equal(formatted, g.Code) {
				t.Errorf("%s/%s is not gofmt-clean:\n%s", p.Dir.Rel, g.Name, diffLines(string(g.Code), string(formatted)))
			}
			checkLineDirectives(t, p.Dir.Rel+"/"+g.Name, g)
		}
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			want := parseRunExpect(string(src))
			cmd := exec.Command(filepath.Join(root, "bin", filepath.Base(dirs[f])))
			cmd.Dir = dirs[f]
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != want.exit {
				t.Errorf("exit code %d, want %d\nstderr:\n%s", code, want.exit, stderr.String())
			}
			got := strings.TrimRight(stdout.String(), "\n")
			if want.hasOuput && got != want.output {
				t.Errorf("output mismatch\n--- got\n%s\n--- want\n%s", got, want.output)
			}
			for _, s := range want.stderr {
				if !strings.Contains(stderr.String(), s) {
					t.Errorf("stderr does not contain %q:\n%s", s, stderr.String())
				}
			}
		})
	}
}

// checkLineDirectives checks that, in build output, every generated line
// that starts with Lyth code reports that code's Lyth line to the Go
// toolchain (debuggers, profilers, vet, and coverage file names): //line
// directives are emitted wherever the Lyth line changes.
func checkLineDirectives(t *testing.T, name string, g *emit.Result) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, g.Name, g.Code, parser.SkipObjectResolution)
	if err != nil {
		return // reported by the gofmt check
	}
	// Physical lines; token.File.Line applies the directives.
	line := func(p token.Pos) int { return fset.PositionFor(p, false).Line }
	inString := map[int]bool{} // continuation lines of multi-line strings
	reported := map[int]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			for l := line(lit.Pos()) + 1; l <= line(lit.End()); l++ {
				inString[l] = true
			}
		}
		if n == nil || !n.Pos().IsValid() {
			return true
		}
		l := line(n.Pos())
		if reported[l] || inString[l] || l >= len(g.Map.LineToSrc) || g.Map.LineToSrc[l] == 0 {
			return true
		}
		reported[l] = true
		if got := fset.PositionFor(n.Pos(), true).Line; got != g.Map.LineToSrc[l] {
			t.Errorf("%s:%d reports Lyth line %d, want %d", name, l, got, g.Map.LineToSrc[l])
		}
		return true
	})
}

// errorRE matches inline expectations: // ERROR E0201 "message substring".
// Warnings must all be expected in a file with a WARN expectation or a
// "// Warnings: none" line; elsewhere they are ignored.
var errorRE = regexp.MustCompile(`//\s*(ERROR|WARN)\s+([EW]\d{4})(?:\s+"([^"]*)")?`)

var noWarningsRE = regexp.MustCompile(`(?m)^// Warnings: none$`)

type expectedDiag struct {
	line int
	code string
	msg  string
	warn bool
	seen bool
}

func TestCheck(t *testing.T) {
	files, _ := filepath.Glob("testdata/check/*.lyth")
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no check tests")
	}
	root, dirs := newModule(t, files, "lythcheck")
	cfg := &build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{})
	if err != nil && (s == nil || !s.Sink.HasErrors()) {
		t.Fatal(err)
	}
	byFile := map[string][]*diag.Diagnostic{}
	for _, d := range s.Sink.All() {
		if d.Span.File == nil {
			t.Errorf("diagnostic without position: %s %s", d.Code, d.Message)
			continue
		}
		byFile[d.Span.File.Path] = append(byFile[d.Span.File.Path], d)
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			var want []*expectedDiag
			for i, line := range strings.Split(string(src), "\n") {
				for _, m := range errorRE.FindAllStringSubmatch(line, -1) {
					want = append(want, &expectedDiag{line: i + 1, code: m[2], msg: m[3], warn: m[1] == "WARN"})
				}
			}
			path := filepath.Join(dirs[f], filepath.Base(f))
			got := byFile[path]
			for _, d := range got {
				line := d.Span.StartPos().Line
				matched := false
				for _, w := range want {
					if !w.seen && w.line == line && w.code == string(d.Code) && strings.Contains(diagText(d), w.msg) {
						w.seen, matched = true, true
						break
					}
				}
				if !matched && (d.Severity == diag.Error || hasWarnExpect(want) || noWarningsRE.Match(src)) {
					t.Errorf("unexpected %s at line %d: %s\n%s", d.Code, line, d.Message, render([]*diag.Diagnostic{d}))
				}
			}
			for _, w := range want {
				if !w.seen {
					t.Errorf("missing %s at line %d (%q)", w.code, w.line, w.msg)
				}
			}
		})
	}
}

// diagText joins all human-readable parts of a diagnostic for matching.
func diagText(d *diag.Diagnostic) string {
	parts := []string{d.Message, d.Label}
	parts = append(parts, d.Notes...)
	for _, h := range d.Hints {
		parts = append(parts, h.Message)
	}
	for _, r := range d.Related {
		parts = append(parts, r.Label)
	}
	return strings.Join(parts, " | ")
}

func hasWarnExpect(ws []*expectedDiag) bool {
	for _, w := range ws {
		if w.warn {
			return true
		}
	}
	return false
}

func TestGenGolden(t *testing.T) {
	files, _ := filepath.Glob("testdata/gen/*.lyth")
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no gen tests")
	}
	root, dirs := newModule(t, files, "lythgen")
	cfg := &build.Config{WorkDir: root, InTree: true, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	for _, p := range s.Packages {
		for _, g := range p.Generated {
			generated[p.Dir.Dir] = append(generated[p.Dir.Dir], g.Code...)
		}
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			got := generated[dirs[f]]
			golden := strings.TrimSuffix(f, ".lyth") + ".go.golden"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run go test ./tests -run TestGenGolden -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("generated code differs from %s\n%s", golden, diffLines(string(want), string(got)))
			}
		})
	}
}

func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(w), len(g))
	shown := 0
	for i := 0; i < n && shown < 20; i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d:\n  want: %s\n  got:  %s\n", i+1, wl, gl)
			shown++
		}
	}
	return b.String()
}
