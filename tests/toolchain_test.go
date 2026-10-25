package tests

import (
	"bytes"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/lower"
)

// writeModule creates a module with the given files (path -> content).
func writeModule(t *testing.T, modPath string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	files["go.mod"] = "module " + modPath + "\n\ngo 1.26\n"
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func compileModule(t *testing.T, root string, inTree bool) *build.Session {
	t.Helper()
	cfg := &build.Config{WorkDir: root, InTree: inTree, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPanicStackTraceShowsLythLines(t *testing.T) {
	root := writeModule(t, "trace", map[string]string{
		"app/main.lyth": `func pick(xs: []int, i: int): int {
    return xs[i]
}

func main() {
    xs := [1, 2, 3]
    println(pick(xs, 10))
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"trace/app"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected a panic")
	}
	out := stderr.String()
	for _, want := range []string{"index out of range", "app/main.lyth:2", "app/main.lyth:7"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack trace does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".lyth.go:") {
		t.Errorf("stack trace mentions generated Go files:\n%s", out)
	}
}

// The panic of !! is compiler glue before the statement; its frame must
// still show the statement's line.
func TestNullAssertionPanicShowsLythLine(t *testing.T) {
	root := writeModule(t, "nntrace", map[string]string{
		"app/main.lyth": `func get(): string? = null

func main() {
    x := 1
    y := 2
    s := get()!!
    println(s, x, y)
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"nntrace/app"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected a panic")
	}
	out := stderr.String()
	for _, want := range []string{"unexpected null (main.lyth:6:15)", "app/main.lyth:6 "} {
		if !strings.Contains(out, want) {
			t.Errorf("stack trace does not mention %q:\n%s", want, out)
		}
	}
}

func TestVetFindingsMapToLyth(t *testing.T) {
	root := writeModule(t, "vetmap", map[string]string{
		"main.lyth": `import "fmt"

func main() {
    name := "lyth"
    fmt.Printf("%d\n", name)
}
`,
	})
	s := compileModule(t, root, false)
	ok, err := s.GoVet([]string{"vetmap"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected go vet to report the format mismatch")
	}
	found := false
	for _, d := range s.Sink.All() {
		if d.Code == diag.EGoCompile && d.Span.File != nil && strings.HasSuffix(d.Span.File.Path, "main.lyth") && d.Span.StartPos().Line == 5 {
			found = true
			if !strings.Contains(d.Message, "Printf") {
				t.Errorf("unexpected message: %s", d.Message)
			}
		}
	}
	if !found {
		t.Errorf("no vet diagnostic at main.lyth:5:\n%s", render(s.Sink.All()))
	}
}

func TestOverlayBuildLeavesSourceTreeUntouched(t *testing.T) {
	root := writeModule(t, "clean", map[string]string{
		"main.lyth":     "func main() { println(\"ok\") }\n",
		"util/str.lyth": "func Shout(s: string): string = \"${s}!\"\n",
	})
	s := compileModule(t, root, false)
	if ok, err := s.GoBuild(filepath.Join(root, ".lyth", "out"), []string{"clean"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var goFiles []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if d.IsDir() && d.Name() == ".lyth" {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})
	if len(goFiles) > 0 {
		t.Errorf("overlay build wrote Go files into the source tree: %v", goFiles)
	}
}

func TestInTreeGenerationBuildsWithPlainGo(t *testing.T) {
	root := writeModule(t, "intree", map[string]string{
		"main.lyth": `import "intree/greet"

func main() {
    println(greet.Hello("go"))
}
`,
		"greet/greet.lyth": `func Hello(name: string): string = "hello, ${name}"
`,
	})
	s := compileModule(t, root, true)
	if _, err := s.WriteInTree(); err != nil {
		t.Fatal(err)
	}
	// No lyth involvement from here on: plain go build and run.
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "hello, go" {
		t.Errorf("unexpected output %q", out)
	}
	// Committed output uses bare file names in //line directives.
	data, _ := os.ReadFile(filepath.Join(root, "greet", "greet.lyth.go"))
	if !strings.Contains(string(data), "//line greet.lyth:1") || strings.Contains(string(data), root) {
		t.Errorf("in-tree output should use bare file names in //line directives:\n%s", data)
	}
}

func TestGeneratedFileNames(t *testing.T) {
	cases := map[string]string{
		"users.lyth":          "users.lyth.go",
		"users_test.lyth":     "users.lyth_test.go",
		"net_linux.lyth":      "net_linux.lyth.go",
		"net_linux_test.lyth": "net_linux.lyth_test.go",
	}
	for in, want := range cases {
		if got := lower.GoFileName(in); got != want {
			t.Errorf("GoFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestThirdPartyModule uses github.com/google/uuid: a named array type with
// methods, (T, error) parsing, package variables, and generic-free APIs.
// It resolves the module from the local module cache (GOPROXY=off) and is
// skipped when the module is not cached.
func TestThirdPartyModule(t *testing.T) {
	root := writeModule(t, "thirdparty", map[string]string{
		"main.lyth": `import "github.com/google/uuid"

func main() throws {
    id := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")
    println(id.String(), id.Version())

    parsed := uuid.Parse("not-a-uuid") catch { uuid.Nil }
    println(parsed == uuid.Nil)

    named := uuid.NewSHA1(uuid.NameSpaceDNS, []byte("lyth.dev"))
    println(named.Version(), len(named.String()))

    strict := uuid.Parse("also-bad")?
    println(strict)
}
`,
	})
	// lyth mod tidy must add the requirement even though no .go file imports
	// the module (plain go mod tidy would not see the import).
	modCache, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	proxy := "GOPROXY=file://" + filepath.ToSlash(filepath.Join(strings.TrimSpace(string(modCache)), "cache", "download"))
	var tidyOut bytes.Buffer
	if _, err := build.ModTidy(&build.Config{WorkDir: root, Stdout: &tidyOut, Stderr: &tidyOut}, []string{proxy, "GOFLAGS=-mod=mod", "GOSUMDB=off"}); err != nil {
		t.Skipf("github.com/google/uuid not available offline: %v\n%s", err, tidyOut.String())
	}
	if data, _ := os.ReadFile(filepath.Join(root, "go.mod")); !strings.Contains(string(data), "github.com/google/uuid") {
		t.Fatalf("lyth mod tidy did not add the requirement:\n%s", data)
	}
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"thirdparty"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("expected exit status 1 from the propagated parse error, got %v\nstderr: %s", err, stderr.String())
	}
	want := "f47ac10b-58cc-4372-a567-0e02b2c3d479 VERSION_4\ntrue\nVERSION_5 36\n"
	if stdout.String() != want {
		t.Errorf("output %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "error: invalid UUID length") {
		t.Errorf("stderr %q does not report the uuid error", stderr.String())
	}
}

// TestExamples builds and runs every program under examples/.
// TestExamples checks every example. Single-file examples are built and
// run. An example with its own go.mod is an application module (the todo
// API needs PostgreSQL to run), so its test suite runs through GoTest, as
// lyth test would; database-backed tests skip unless DATABASE_URL is set.
func TestExamples(t *testing.T) {
	dirs, _ := filepath.Glob("../examples/*")
	if len(dirs) == 0 {
		t.Skip("no examples")
	}
	for _, dir := range dirs {
		dir, _ := filepath.Abs(dir)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			var out bytes.Buffer
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				cfg := &build.Config{WorkDir: dir, Tests: true, Stdout: &out, Stderr: &out}
				s, err := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
				if s != nil && s.Sink.HasErrors() {
					t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range s.Packages {
					for _, g := range p.Generated {
						if formatted, err := format.Source(g.Code); err != nil || !bytes.Equal(formatted, g.Code) {
							t.Errorf("%s/%s is not gofmt-clean (%v)", p.Dir.Rel, g.Name, err)
						}
					}
				}
				code, err := s.GoTest([]string{"./..."}, []string{"-count=1"})
				if err != nil || code != 0 {
					t.Fatalf("tests failed (exit %d): %v\n%s", code, err, out.String())
				}
				return
			}
			files, _ := filepath.Glob(filepath.Join(dir, "*.lyth"))
			for _, f := range files {
				wd, _ := os.Getwd()
				cfg := &build.Config{WorkDir: wd, Stdout: &out, Stderr: &out}
				s, err := build.Compile(cfg, build.Target{File: f})
				if s != nil && s.Sink.HasErrors() {
					t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
				}
				if err != nil {
					t.Fatal(err)
				}
				mains := s.MainPackages()
				if len(mains) != 1 {
					t.Fatalf("%s: want one main package, got %d", f, len(mains))
				}
				code, err := s.GoRun(mains[0].Dir.ImportPath, nil, nil)
				if err != nil || code != 0 {
					t.Fatalf("run failed (exit %d): %v\n%s\n%s", code, err, out.String(), render(s.Sink.All()))
				}
			}
		})
	}
}

// TestTargetedBuildKeepsOtherPackages checks that building one package does
// not drop the generated code of the module's other Lyth packages.
func TestTargetedBuildKeepsOtherPackages(t *testing.T) {
	root := writeModule(t, "targeted", map[string]string{
		"a/a.lyth": "func main() { println(\"a\") }\n",
		"b/b.lyth": "func main() { println(\"b\") }\n",
	})
	compileModule(t, root, false)
	cfg := &build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{Patterns: []string{"./a"}})
	if err != nil || s.Sink.HasErrors() {
		t.Fatalf("targeted compile failed: %v\n%s", err, render(s.Sink.All()))
	}
	if len(s.Targets) != 1 || len(s.Packages) != 1 {
		t.Errorf("targeted compile should only compile ./a, got %d targets and %d packages", len(s.Targets), len(s.Packages))
	}
	data, _ := os.ReadFile(filepath.Join(root, ".lyth", "overlay.json"))
	for _, want := range []string{"a.lyth.go", "b.lyth.go"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("overlay lost %s after a targeted build:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".lyth", "gen", "b", "b.lyth.go")); err != nil {
		t.Errorf("generated code of b was removed: %v", err)
	}
}

// TestGoTypeCache checks that warm builds load Go type information from the
// persistent cache, and that editing a local Go package invalidates it.
func TestGoTypeCache(t *testing.T) {
	root := writeModule(t, "cached", map[string]string{
		"util/util.go": "package util\n\nfunc Double(x int) int { return 2 * x }\n",
		"main.lyth": `import "cached/util"
import "strings"

func main() {
    println(strings.ToUpper("x"), util.Double(21))
}
`,
	})
	first := compileModule(t, root, false)
	if first.TypesCached() {
		t.Fatal("first build cannot be served from the cache")
	}
	second := compileModule(t, root, false)
	if !second.TypesCached() {
		t.Fatal("second build should load Go types from the cache")
	}
	// Edit the local Go package: the cache must not be used, and the new
	// function must be visible.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "util", "util.go"), []byte("package util\n\nfunc Double(x int) int { return 2 * x }\n\nfunc Triple(x int) int { return 3 * x }\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.lyth"), []byte(`import "cached/util"
import "strings"

func main() {
    println(strings.ToUpper("x"), util.Double(21), util.Triple(2))
}
`), 0o644)
	third := compileModule(t, root, false)
	if third.TypesCached() {
		t.Fatal("editing a local package must invalidate the cache")
	}
	bin := filepath.Join(root, "app.bin")
	if ok, err := third.GoBuild(bin, []string{"cached"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(third.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "X 42 6" {
		t.Fatalf("output %q, err %v", out, err)
	}
}

// testModule compiles a module with its tests and runs go test on it.
func testModule(t *testing.T, root string, flags ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	cfg := &build.Config{WorkDir: root, Tests: true, Stdout: &out, Stderr: &out}
	s, err := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.GoTest([]string{"./..."}, append([]string{"-count=1"}, flags...))
	if err != nil {
		t.Fatal(err)
	}
	return code, out.String()
}

const calcLyth = `import "strconv"

func Add(a: int, b: int): int = a + b

func Parse(s: string): int throws = strconv.Atoi(s)?
`

// TestLythTest covers lyth test: Lyth tests (plain and throwing), a Go test
// file beside the Lyth code, and exclusion of tests from ordinary builds.
func TestLythTest(t *testing.T) {
	root := writeModule(t, "testing", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestAdd(t: *testing.T) {
    if Add(2, 3) != 5 {
        t.Errorf("Add(2, 3) = %d", Add(2, 3))
    }
}

func TestParse(t: *testing.T) throws {
    n := Parse("42")?
    if n != 42 {
        t.Fatalf("parse = %d", n)
    }
}

func helper(): int = 1

func ExampleAdd() {
    println(Add(2, 3))
    // Output: 5
}
`,
		"calc/calc_go_test.go": `package calc

import "testing"

func TestFromGo(t *testing.T) {
	if Add(1, helper()) != 2 {
		t.Fatal("Add from Go")
	}
}
`,
	})
	code, out := testModule(t, root, "-v")
	if code != 0 {
		t.Fatalf("go test failed (exit %d):\n%s", code, out)
	}
	for _, want := range []string{"--- PASS: TestAdd", "--- PASS: TestParse", "--- PASS: TestFromGo", "--- PASS: ExampleAdd"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// An ordinary build ignores test files, even broken ones.
	os.WriteFile(filepath.Join(root, "calc", "broken_test.lyth"), []byte("func TestBroken(t: *testing.T) { undefinedThing() }\n"), 0o644)
	compileModule(t, root, false)
}

// TestLythTestFailuresPointAtLythLines checks that t.Errorf and a thrown
// error report .lyth file lines.
func TestLythTestFailuresPointAtLythLines(t *testing.T) {
	root := writeModule(t, "failing", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestWrong(t: *testing.T) {
    t.Errorf("wrong sum %d", Add(1, 1))
}

func TestThrows(t: *testing.T) throws {
    Parse("x")?
}
`,
	})
	code, out := testModule(t, root)
	if code == 0 {
		t.Fatalf("failing tests passed:\n%s", out)
	}
	for _, want := range []string{"calc_test.lyth:4: wrong sum 2", "calc_test.lyth:7:", `parsing "x": invalid syntax`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestLythTestSignatures checks the signature rules for test functions.
func TestLythTestSignatures(t *testing.T) {
	root := writeModule(t, "sigs", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestNoParam() {}

func BenchmarkWrong(t: *testing.T) {}

func ExampleThrows() throws {}

func testHelper(x: int): int = x
`,
		"calc/ext_test.lyth": "package calc_test\n\nfunc TestExt() {}\n",
	})
	cfg := &build.Config{WorkDir: root, Tests: true, Stdout: os.Stdout, Stderr: os.Stderr}
	s, _ := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
	got := render(s.Sink.All())
	for _, want := range []string{
		"TestNoParam is run by go test, so it must be declared func TestNoParam(t: *testing.T)",
		"BenchmarkWrong is run by go test, so it must be declared func BenchmarkWrong(b: *testing.B)",
		"ExampleThrows is run by go test, so it must be declared func ExampleThrows()",
		"external test packages (package calc_test) are not supported yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "testHelper") {
		t.Errorf("testHelper is not a test function:\n%s", got)
	}
}

// TestDefaultsAcrossPackages checks that a default value written in one
// package expands correctly at a call site in another: its public names are
// qualified and its imports are added.
func TestDefaultsAcrossPackages(t *testing.T) {
	root := writeModule(t, "defaults", map[string]string{
		"db/db.lyth": `import "time"

const DefaultPort = 5432

func Connect(host: string, port: int = DefaultPort, timeout: time.Duration = 5 * time.Second): string = "${host}:${port} ${timeout}"
`,
		"main.lyth": `import "defaults/db"

func main() {
    println(db.Connect("a"), db.Connect("b", timeout = 0))
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"defaults"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "a:5432 5s b:5432 0s" {
		t.Fatalf("output %q, err %v", out, err)
	}
}

// TestExtensionsAcrossPackages calls the exported extension functions of
// another Lyth package, which lower to qualified Go calls; unexported ones are
// not visible, and two packages declaring the same extension are ambiguous.
func TestExtensionsAcrossPackages(t *testing.T) {
	root := writeModule(t, "exts", map[string]string{
		"text/text.lyth": `func string.Shout(): string = self + "!"

func []T.Second[T](): T = self[1]

func string.whisper(): string = self
`,
		"main.lyth": `import "exts/text"

func main() {
    println("hi".Shout(), [1, 2, 3].Second())
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"exts"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "hi! 2" {
		t.Fatalf("output %q, err %v", out, err)
	}
	for _, g := range s.Packages {
		for _, f := range g.Generated {
			if g.Dir.ImportPath == "exts" && !strings.Contains(string(f.Code), "text.Shout(\"hi\")") {
				t.Errorf("expected a qualified Go call text.Shout(\"hi\"):\n%s", f.Code)
			}
		}
	}

	bad := writeModule(t, "exts2", map[string]string{
		"a/a.lyth":  "func string.Shout(): string = self\n\nfunc string.quiet(): string = self\n",
		"b/b.lyth":  "func string.Shout(): string = self\n",
		"main.lyth": "import \"exts2/a\"\nimport \"exts2/b\"\n\nfunc main() {\n    println(\"x\".Shout(), \"y\".quiet())\n}\n",
	})
	cfg := &build.Config{WorkDir: bad, Stdout: io.Discard, Stderr: io.Discard}
	s2, _ := build.Compile(cfg, build.Target{})
	msgs := render(s2.Sink.All())
	for _, want := range []string{"Shout is ambiguous: packages a and b both declare it for string", "has no field or method quiet"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing diagnostic %q in:\n%s", want, msgs)
		}
	}
}

// TestTypeArgsAcrossPackages calls the generic functions of another Lyth
// package with explicit type arguments (one or several, called or as a
// value) and with inferred ones, next to its generic types and other names.
// Wrong type arguments are reported, not resolved as types.
func TestTypeArgsAcrossPackages(t *testing.T) {
	files := map[string]string{
		"lib/lib.lyth": `import "cmp"
import "errors"

interface Store {
    Get(k: int): int
}

func Zero[T](): T {
    z: T
    return z
}

func Lookup[S: Store](s: S, k: int): int = s.Get(k)

func Max2[T: cmp.Ordered](a: T, b: T): T = if a > b { a } else { b }

func Head[T](xs: []T): T throws {
    if len(xs) == 0 {
        throw errors.New("empty")
    }
    return xs[0]
}

struct Both[A, B] {
    First: A
    Second: B
}

func MakeBoth[A, B](a: A, b: B): Both[A, B] = Both(First = a, Second = b)

func Map[T, R](xs: []T, f: (T) -> R): []R {
    out := make([]R, 0, len(xs))
    for x in xs {
        out = append(out, f(x))
    }
    return out
}

struct Box[T] {
    V: T
}

func Plain(n: int): int = n * 2

var Values = [10, 20, 30]

const Limit = 3

func hidden[T](): T {
    z: T
    return z
}
`,
		"main.lyth": `import "fmt"
import "targs/lib"

struct good {
    n: int
}

func good.Get(k: int): int = self.n + k

func main() throws {
    // Explicit type arguments: one or several, called or as a value.
    fmt.Println(lib.Zero[int](), lib.Zero[string]() == "", lib.Lookup[good](good(n = 1), 2), lib.Max2[float64](1, 2.5))
    b := lib.MakeBoth[int, string](1, "x")
    fmt.Println(b.First + 1, b.Second + "!", lib.Map[int, string]([1, 2]) { "<${it}>" })
    f := lib.Zero[float64]
    g: () -> int = lib.Zero[int]
    fmt.Println(f(), g(), lib.Head[string](["a", "b"])?, lib.Head[int]([]) catch { -1 })
    // The same functions with inferred type arguments.
    fmt.Println(lib.Lookup(good(n = 5), 1), lib.MakeBoth(2, true).Second, lib.Map([1, 2]) { it * 10 }, lib.Max2(3, 4))
    // Generic types and other names of the package.
    bx := lib.Box[int](V = 4)
    both: lib.Both[string, int] = lib.Both[string, int](First = "a", Second = 2)
    fmt.Println(bx.V, both.First, lib.Values[1], lib.Limit, lib.Plain(3))
}
`,
	}
	root := writeModule(t, "targs", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"targs"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	want := "0 true 3 2.5\n2 x! [<1> <2>]\n0 0 a -1\n6 true [10 20] 4\n4 a 20 3 6"
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("output %q, err %v", out, err)
	}
	for _, g := range s.Packages {
		if g.Dir.ImportPath != "targs" {
			continue
		}
		for _, f := range g.Generated {
			for _, want := range []string{
				"lib.Zero[int]()", "lib.Lookup[good](good{n: 1}, 2)", "lib.MakeBoth[int, string](1, \"x\")",
				"f := lib.Zero[float64]", "lib.Head[int]([]int{})", "lib.Lookup(good{n: 5}, 1)", "lib.Box[int]{V: 4}",
			} {
				if !strings.Contains(string(f.Code), want) {
					t.Errorf("expected %s in the generated Go:\n%s", want, f.Code)
				}
			}
		}
	}

	files["main.lyth"] = `import "targs/lib"

struct bad {
    n: int
}

struct good {
    n: int
}

func good.Get(k: int): int = self.n + k

func main() {
    println(lib.Lookup[bad](bad(), 1))
    println(lib.Zero[int, string]())
    println(lib.MakeBoth[int](1, "x"))
    println(lib.Zero[Nope]())
    println(lib.hidden[int]())
    println(lib.Plain[int](1))
    println(lib.Plain[int, string](1))
    println(lib.Lookup[good](1, 1))
    println(lib.Zero[int](5))
}
`
	root = writeModule(t, "targs", files)
	s, err = build.Compile(&build.Config{WorkDir: root, Stdout: io.Discard, Stderr: io.Discard}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	const allArgs = "give all type arguments, or none to infer them from the call's arguments"
	wantDiags := []string{
		"14: invalid type arguments for lib.Lookup; bad does not satisfy lib.Store (missing method Get)",
		"15: lib.Zero takes 1 type argument (T), got 2; " + allArgs,
		"16: lib.MakeBoth takes 2 type arguments (A, B), got 1; " + allArgs,
		"17: undefined type Nope",
		"18: hidden is not exported by package lib",
		"19: cannot index a value of type (int) -> int",
		"20: lib.Plain is not a generic function",
		"21: type mismatch in argument s of lib.Lookup[good]",
		"22: lib.Zero[int] expects 0 arguments, but 1 argument given; signature: () -> int",
	}
	var got []string
	for _, d := range s.Sink.All() {
		got = append(got, strconv.Itoa(d.Span.StartPos().Line)+": "+strings.Join(append([]string{d.Message}, d.Notes...), "; "))
	}
	slices.Sort(got)
	slices.Sort(wantDiags)
	if !slices.Equal(got, wantDiags) {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantDiags, "\n"))
	}
}

// TestEnumsAndSealedAcrossPackages uses an enum and a sealed interface from
// another package: entries are qualified Go constants, an exhaustive when
// sees the other package's members, and only that package can add members.
func TestEnumsAndSealedAcrossPackages(t *testing.T) {
	files := map[string]string{
		"shapes/shapes.lyth": `enum Kind { Round, Square }

sealed interface Shape {
    Kind(): Kind
}

struct Circle : Shape {
    R: int
}

func Circle.Kind(): Kind = Kind.Round

struct Box : Shape {
    W: int
}

func Box.Kind(): Kind = Kind.Square
`,
		"main.lyth": `import "sealed/shapes"

func describe(s: shapes.Shape): string = when s {
    is shapes.Circle -> "circle ${s.R}"
    is shapes.Box -> "box ${s.W}"
}

func label(k: shapes.Kind): string = when k {
    Round -> "round"
    shapes.Kind.Square -> "square"
}

func main() {
    s: shapes.Shape = shapes.Box(W = 2)
    println(describe(shapes.Circle(R = 1)), describe(s), label(s.Kind()), shapes.Kind.Round)
}
`,
	}
	root := writeModule(t, "sealed", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"sealed"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "circle 1 box 2 square Round" {
		t.Fatalf("output %q, err %v", out, err)
	}

	// Another package cannot join the sealed interface.
	files["main.lyth"] = `import "sealed/shapes"

struct Tri : shapes.Shape {
    A: int
}

func Tri.Kind(): shapes.Kind = shapes.Kind.Round

func main() {}
`
	root = writeModule(t, "sealed", files)
	s, err = build.Compile(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(s.Sink.All()); !strings.Contains(got, "shapes.Shape is sealed: only types declared in package shapes can implement it") {
		t.Fatalf("missing sealed error:\n%s", got)
	}
}

// TestGenericConstraintsAcrossPackages instantiates the generic types of a
// Lyth package and of a Go package, constrained by their own interfaces, in
// fields, aliases, and signatures written before the types and methods the
// constraints need (in the same file and in the files around it), and
// constrains local generic types by the imported interfaces. Invalid type
// arguments in the same places are reported in Lyth's names.
func TestGenericConstraintsAcrossPackages(t *testing.T) {
	files := map[string]string{
		"lib/lib.lyth": `// Cache is declared before the interfaces that constrain it.
struct Cache[S: Store[int]] {
    Src: S
}

func Cache[S].Value(): int = self.Src.Get(1) * 10

struct Named[N: Namer] {
    N: N
}

func Named[N].Label(): string = "<" + self.N.Name() + ">"

func Lookup[S: Store[int]](s: S, k: int): int = s.Get(k)

interface Store[K] {
    Get(k: K): int
}

interface Namer {
    Name(): string
}
`,
		"golib/golib.go": `package golib

type Store[K any] interface{ Get(K) int }

type Cache[S Store[int]] struct{ Src S }

func (c Cache[S]) Value() int { return c.Src.Get(2) * 100 }
`,
		"a_early.lyth": `// early and its methods come before the declarations that use them.
struct early {
    base: int
}

func early.Get(k: int): int = self.base + k

func early.Name(): string = "early"
`,
		"b_uses.lyth": `import "fmt"
import "xgen/golib"
import "xgen/lib"

// Instances in fields, aliases, and signatures, of types declared in an
// earlier file (early) and a later one (late).
struct App {
    cache: lib.Cache[late]
    named: lib.Named[late]
    goCache: golib.Cache[late]
    wrap: WrapLate
    early: lib.Cache[early]
    earlyGo: golib.Cache[early]
}

typealias WrapLate = Wrap[late]

typealias LateCache = lib.Cache[late]

typealias LateGoCache = golib.Cache[late]

interface Maker {
    Make(): lib.Cache[late]
}

// Local generic types and functions constrained by imported interfaces.
struct Wrap[S: lib.Store[int]] {
    s: S
}

func Wrap[S].Value(): int = self.s.Get(3)

struct Holder[S: lib.Store[int]] {
    c: lib.Cache[S]
}

struct GoHolder[S: golib.Store[int]] {
    c: golib.Cache[S]
}

struct Pair[S: golib.Store[int], N: lib.Namer] {
    s: S
    n: N
}

func Pair[S, N].String(): string = "${self.n.Name()}:${self.s.Get(4)}"

// Generic interfaces that embed imported generic interfaces.
interface Keyed[K] {
    lib.Store[K]
    lib.Namer
}

interface GoKeyed[K] {
    golib.Store[K]
}

struct Both[S: Keyed[int], G: GoKeyed[int]] {
    s: S
    g: G
}

func Both[S, G].String(): string = "${self.s.Name()}/${self.s.Get(5)}/${self.g.Get(6)}"

func total[S: lib.Store[int]](s: S): int = s.Get(7)

func describe(c: lib.Cache[late], g: golib.Cache[late], e: LateCache): string = "${c.Value()} ${g.Value()} ${e.Value()}"

func cacheOf(l: late): lib.Cache[late] = lib.Cache(Src = l)

func newApp(l: late, e: early): App = App(
    cache = cacheOf(l),
    named = lib.Named(N = l),
    goCache = golib.Cache[late](Src = l),
    wrap = Wrap(s = l),
    early = lib.Cache(Src = e),
    earlyGo = golib.Cache[early](Src = e),
)

func lateCaches(l: late): (LateCache, LateGoCache) {
    return lib.Cache(Src = l), golib.Cache[late](Src = l)
}

func main() {
    l := late(base = 100)
    e := early(base = 10)
    app := newApp(l, e)
    fmt.Println(app.cache.Value(), app.named.Label(), app.goCache.Value(), app.wrap.Value())
    fmt.Println(app.early.Value(), app.earlyGo.Value(), lib.Lookup(e, 2), total(l))
    fmt.Println(describe(app.cache, app.goCache, app.cache))
    lc, lg := lateCaches(l)
    m: Maker = l
    fmt.Println(lc.Value(), lg.Value(), m.Make().Value())
    h := Holder[early](c = lib.Cache(Src = e))
    g := GoHolder[early](c = golib.Cache[early](Src = e))
    fmt.Println(h.c.Value(), g.c.Value(), Pair(s = e, n = l), Both(s = l, g = e))
}
`,
		"c_late.lyth": `import "xgen/lib"

// late's methods are declared before the type itself.
func late.Get(k: int): int = self.base + k

func late.Name(): string = "late"

func late.Make(): lib.Cache[late] = lib.Cache(Src = self)

struct late {
    base: int
}
`,
	}
	root := writeModule(t, "xgen", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"xgen"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	want := "1010 <late> 10200 103\n110 1200 12 107\n1010 10200 1010\n1010 10200 1010\n110 1200 late:14 late/105/16"
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("output %q, err %v", out, err)
	}

	// Type arguments that do not satisfy the imported constraints.
	delete(files, "a_early.lyth")
	delete(files, "c_late.lyth")
	delete(files, "b_uses.lyth")
	files["main.lyth"] = `import "xgen/golib"
import "xgen/lib"

struct App {
    cache: lib.Cache[bad]
    goCache: golib.Cache[bad]
    wrap: Wrap[bad]
    ptr: lib.Cache[ptrOnly]
}

typealias BadCache = lib.Cache[bad]

typealias BadGoCache = golib.Cache[bad]

struct Wrap[S: lib.Store[int]] {
    s: S
}

struct Holder[S: lib.Namer] {
    c: lib.Cache[S]
}

func use(c: lib.Cache[bad], g: golib.Cache[bad]) {}

func main() {
    println(lib.Lookup(bad(), 1))
}

struct bad {
    n: int
}

struct ptrOnly {
    n: int
}

func *ptrOnly.Get(k: int): int = k
`
	root = writeModule(t, "xgen", files)
	s, err = build.Compile(&build.Config{WorkDir: root, Stdout: io.Discard, Stderr: io.Discard}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	const missing = "bad does not satisfy lib.Store[int] (missing method Get)"
	const goMissing = "invalid type arguments for golib.Cache: bad does not satisfy golib.Store[int] (missing method Get)"
	wantDiags := []string{
		"5: invalid type arguments for Cache; " + missing,
		"6: " + goMissing,
		"7: invalid type arguments for Wrap; " + missing,
		"8: invalid type arguments for Cache; ptrOnly does not satisfy lib.Store[int] (method Get has pointer receiver)",
		"11: invalid type arguments for Cache; " + missing,
		"13: " + goMissing,
		"20: invalid type arguments for Cache; S does not satisfy lib.Store[int] (missing method Get)",
		"23: invalid type arguments for Cache; " + missing,
		"23: " + goMissing,
		"26: invalid type arguments for lib.Lookup (S = bad); " + missing,
	}
	var got []string
	for _, d := range s.Sink.All() {
		got = append(got, strconv.Itoa(d.Span.StartPos().Line)+": "+strings.Join(append([]string{d.Message}, d.Notes...), "; "))
	}
	slices.Sort(got)
	slices.Sort(wantDiags)
	if !slices.Equal(got, wantDiags) {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantDiags, "\n"))
	}
	if msgs := render(s.Sink.All()); strings.Contains(msgs, "xgen/") || strings.Contains(msgs, "xgen.") {
		t.Errorf("a Go package path reached the diagnostics:\n%s", msgs)
	}
}
