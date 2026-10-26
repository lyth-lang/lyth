package tests

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/golang"
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

// The generated Go imports packages the Lyth source does not (context, net,
// io/fs for these lambdas' Go signatures). A warm cache loads only the
// imported packages completely, so validation must complete the others.
func TestGoTypeCacheDependencyImports(t *testing.T) {
	root := writeModule(t, "cachedeps", map[string]string{
		"p/p.lyth": `import "net/http"

func Transport(dials: *int): *http.Transport {
    base := (http.DefaultTransport as *http.Transport).DialContext
    return &http.Transport(DialContext = { ctx, network, addr ->
        *dials = *dials + 1
        base(ctx, network, addr)
    })
}
`,
		"w/w.lyth": `import "path/filepath"

func Count(dir: string): int throws {
    n := 0
    filepath.WalkDir(dir) { _, _, _ ->
        n++
        return
    }?
    return n
}
`,
	})
	if compileModule(t, root, false).TypesCached() {
		t.Fatal("first build cannot be served from the cache")
	}
	second := compileModule(t, root, false)
	if !second.TypesCached() {
		t.Fatal("second build should load Go types from the cache")
	}
	if ok, err := second.GoBuild("", []string{"./..."}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(second.Sink.All()))
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

// TestModTidyKeepsTestOnlyModules checks that lyth mod tidy keeps modules
// imported only by tests: testify from a _test.lyth file and uuid from a Go
// _test.go file beside the Lyth code. Before stubs covered _test.lyth files,
// tidy removed testify and the tests stopped compiling. Modules come from the
// local module cache (GOPROXY=off semantics); the test skips without them.
func TestModTidyKeepsTestOnlyModules(t *testing.T) {
	root := writeModule(t, "tidytest", map[string]string{
		"calc.lyth": "package tidytest\n\nfunc Add(a: int, b: int): int = a + b\n",
		"calc_test.lyth": `package tidytest

import (
    "testing"
    "github.com/stretchr/testify/assert"
)

func TestAdd(t: *testing.T) {
    assert.Equal(t, 3, Add(1, 2))
}
`,
		"id_test.go": `package tidytest

import (
	"testing"

	"github.com/google/uuid"
)

func TestID(t *testing.T) {
	if uuid.Nil.String() == "" || Add(1, 1) != 2 {
		t.Fatal("unexpected")
	}
}
`,
	})
	// As after go get github.com/stretchr/testify/assert@v1.11.1.
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module tidytest\n\ngo 1.26\n\nrequire github.com/stretchr/testify v1.11.1\n"), 0o644)
	modCache, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	proxy := "GOPROXY=file://" + filepath.ToSlash(filepath.Join(strings.TrimSpace(string(modCache)), "cache", "download"))
	var tidyOut bytes.Buffer
	if _, err := build.ModTidy(&build.Config{WorkDir: root, Stdout: &tidyOut, Stderr: &tidyOut}, []string{proxy, "GOFLAGS=-mod=mod", "GOSUMDB=off"}); err != nil {
		t.Skipf("testify or uuid not available offline: %v\n%s", err, tidyOut.String())
	}
	gomod, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	for _, want := range []string{"github.com/stretchr/testify v1.11.1", "github.com/google/uuid"} {
		if !strings.Contains(string(gomod), want) {
			t.Errorf("lyth mod tidy dropped %s:\n%s", want, gomod)
		}
	}
	if code, out := testModule(t, root, "-v"); code != 0 || !strings.Contains(out, "--- PASS: TestAdd") || !strings.Contains(out, "--- PASS: TestID") {
		t.Errorf("tests after lyth mod tidy (exit %d):\n%s", code, out)
	}
}

// TestLythTestCover checks lyth test -cover and -coverprofile. The cover tool
// reads generated files from disk (it ignores -overlay), so they exist
// beside the sources only during the run; a file lyth gen -in-tree wrote
// there is restored. The profile names .lyth files with Lyth positions, and
// go tool cover -html and lyth cover -func (build.CoverFunc) read it.
func TestLythTestCover(t *testing.T) {
	root := writeModule(t, "covered", map[string]string{
		"calc/calc.lyth": `import "strconv"

func Add(a: int, b: int): int = a + b

// Parse reads a number.
func Parse(s: string): int throws {
    n := strconv.Atoi(s)?
    if n < 0 {
        throw strconv.ErrRange
    }
    return n * 2
}

func Sign(n: int): string = when {
    n < 0 -> "neg"
    n == 0 -> "zero"
    else -> "pos"
}
`,
		"calc/calc_test.lyth": `import "testing"

func TestCalc(t: *testing.T) throws {
    if Add(1, 2) != 3 || Sign(1) != "pos" {
        t.Fatal("wrong")
    }
    Parse("4")?
}
`,
		"calc/calc_go_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestFromGo(t *testing.T) { _ = Add(1, 1) }\n",
		"util/util.lyth":       "func Twice(s: string): string = s + s\n",
	})
	// util has no tests and no in-tree file; calc has an in-tree file from
	// lyth gen -in-tree, which must be left as it was.
	s := compileModule(t, root, true)
	if _, err := s.WriteInTree(); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "util", "util.lyth.go"))
	os.Remove(filepath.Join(root, "calc", "calc.lyth_test.go"))
	inTree := filepath.Join(root, "calc", "calc.lyth.go")
	before, err := os.ReadFile(inTree)
	if err != nil {
		t.Fatal(err)
	}

	code, out := testModule(t, root, "-cover", "-coverprofile=c.out")
	if code != 0 || !strings.Contains(out, "coverage:") || strings.Contains(out, "build failed") {
		t.Fatalf("lyth test -cover failed (exit %d):\n%s", code, out)
	}
	if after, _ := os.ReadFile(inTree); !bytes.Equal(before, after) {
		t.Errorf("the in-tree file of lyth gen was not restored:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(root, "util", "util.lyth.go")); err == nil {
		t.Errorf("the generated file written for the cover tool was not removed")
	}

	data, err := os.ReadFile(filepath.Join(root, "c.out"))
	if err != nil {
		t.Fatal(err)
	}
	calc := filepath.Join(root, "calc", "calc.lyth")
	src, _ := os.ReadFile(calc)
	lines := strings.Split(string(src), "\n")
	text := func(sl, sc, el, ec int) string { // a block's Lyth text (single-line blocks)
		if sl != el || sl > len(lines) || ec-1 > len(lines[sl-1]) {
			return ""
		}
		return lines[sl-1][sc-1 : ec-1]
	}
	got := map[string]int{} // single-line block text -> count
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n")[1:] {
		// file:sl.sc,el.ec stmts count; a Windows file name has a colon too.
		i := strings.LastIndex(line, ":")
		file, rest := line[:i], line[i+1:]
		if file == filepath.Join(root, "util", "util.lyth") {
			continue // untested, listed with zero counts
		}
		if file != calc {
			t.Errorf("profile names %s, want %s: %s", file, calc, line)
			continue
		}
		var sl, sc, el, ec, stmts, count int
		if _, err := fmt.Sscanf(rest, "%d.%d,%d.%d %d %d", &sl, &sc, &el, &ec, &stmts, &count); err != nil {
			t.Fatalf("bad profile line %q: %v", line, err)
		}
		if sl < 1 || el > len(lines) || el < sl {
			t.Errorf("block outside calc.lyth (%d lines): %s", len(lines), line)
		}
		got[text(sl, sc, el, ec)] = count
	}
	// The error path of ? is its own block, uncovered: Parse never failed.
	for block, want := range map[string]int{"a + b": 1, "n := strconv.Atoi(s)": 1, "?": 0, "throw strconv.ErrRange": 0, "return n * 2": 1, `"neg"`: 0, `"pos"`: 1} {
		if c, ok := got[block]; !ok || c != want {
			t.Errorf("block %q: count %d (found %v), want %d\n%s", block, c, ok, want, data)
		}
	}

	html := exec.Command("go", "tool", "cover", "-html=c.out", "-o", "c.html")
	html.Dir = root
	if out, err := html.CombinedOutput(); err != nil {
		t.Errorf("go tool cover -html failed: %v\n%s", err, out)
	} else if page, _ := os.ReadFile(filepath.Join(root, "c.html")); !strings.Contains(string(page), `<span class="cov0" title="0">?</span>`) {
		t.Errorf("the HTML report does not mark the untested error path of ?:\n%s", page)
	}
	var funcs bytes.Buffer
	if err := build.CoverFunc(root, filepath.Join(root, "c.out"), &funcs, nil); err != nil {
		t.Fatal(err)
	}
	// Go 1.26's cover tool counts n, err := ... and its error check as two
	// statements, Go 1.27's as one, so Parse is 4 of 6 or 3 of 5.
	parse := "60.0%"
	if _, minor, err := golang.GoVersion(root); err == nil && minor < 27 {
		parse = "66.7%"
	}
	calcFile := filepath.FromSlash("calc/calc.lyth")
	for _, want := range []string{calcFile + ":3:", "Add", "100.0%", calcFile + ":6:", "Parse", parse, "Sign", "total:"} {
		if !strings.Contains(funcs.String(), want) {
			t.Errorf("lyth cover -func output lacks %q:\n%s", want, funcs.String())
		}
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

// TestEmbeddingAcrossPackages embeds a Go struct shaped like gRPC's
// generated Unimplemented server, whose interface has an unexported method
// only embedding can provide, and a Lyth struct of another package whose
// promoted members keep their Lyth types (ADR 0043).
func TestEmbeddingAcrossPackages(t *testing.T) {
	files := map[string]string{
		"pb/pb.go": `package pb

import (
	"context"
	"errors"
)

type HelloRequest struct{ Name string }

type HelloReply struct{ Message string }

type GreeterServer interface {
	SayHello(context.Context, *HelloRequest) (*HelloReply, error)
	SayBye(context.Context, *HelloRequest) (*HelloReply, error)
	mustEmbedUnimplementedGreeterServer()
}

type UnimplementedGreeterServer struct{}

func (UnimplementedGreeterServer) SayHello(context.Context, *HelloRequest) (*HelloReply, error) {
	return nil, errors.New("method SayHello not implemented")
}

func (UnimplementedGreeterServer) SayBye(context.Context, *HelloRequest) (*HelloReply, error) {
	return nil, errors.New("method SayBye not implemented")
}

func (UnimplementedGreeterServer) mustEmbedUnimplementedGreeterServer() {}

func Register(s GreeterServer) GreeterServer { return s }
`,
		"models/models.lyth": `struct Base {
    ID: int64
    Note: string?
}

func Base.Label(): string = "#${self.ID}"
`,
		"main.lyth": `import "context"
import "fmt"
import "emb/models"
import "emb/pb"

struct server {
    pb.UnimplementedGreeterServer
    greeting: string
}

func *server.SayHello(ctx: context.Context, r: *pb.HelloRequest): *pb.HelloReply throws =
    &pb.HelloReply(Message = "${self.greeting}, ${r.Name}")

struct Item {
    models.Base
    Name: string
}

func main() throws {
    s := pb.Register(&server(greeting = "hi"))
    reply := s.SayHello(context.Background(), &pb.HelloRequest(Name = "ada"))?
    fmt.Println(reply.Message)
    s.SayBye(context.Background(), &pb.HelloRequest(Name = "ada")) catch (err) { fmt.Println(err) }
    it := Item(Base = models.Base(ID = 3), Name = "pen")
    fmt.Println(it.Label(), it.ID, it.Note ?: "no note")
}
`,
	}
	root := writeModule(t, "emb", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"emb"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	want := "hi, ada\nmethod SayBye not implemented\n#3 3 no note"
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("output %q, want %q, err %v", out, want, err)
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

type WrapLate = Wrap[late]

type LateCache = lib.Cache[late]

type LateGoCache = golib.Cache[late]

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

type BadCache = lib.Cache[bad]

type BadGoCache = golib.Cache[bad]

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

// TestGoPackagesBetweenLythPackages builds a module where Lyth code imports
// Go packages of the same module that import Lyth packages (as generated
// sqlc code or a package not yet converted does), directly and through
// another Go package. The Lyth types keep their declarations through the
// Go packages: enums are exhaustive in when, optionals stay optional, and
// values flow back into Lyth functions.
func TestGoPackagesBetweenLythPackages(t *testing.T) {
	files := map[string]string{
		"domain/domain.lyth": `enum Status { Pending, Paid }

struct Money {
    Cents: int64
    Note: string?
}

func Money.Label(): string = "${self.Cents}c"

func NewMoney(c: int64): Money = Money(Cents = c, Note = "new")

struct Page[T] {
    Items: []T
}
`,
		"gen/gen.go": `package gen

import (
	"fmt"

	"brg/domain"
)

type Order struct {
	Status domain.Status
	Total  domain.Money
	Prev   *domain.Money
}

func New(c int64) Order {
	return Order{Status: domain.StatusPaid, Total: domain.NewMoney(c)}
}

func Describe(m domain.Money) string { return fmt.Sprintf("%d/%s", m.Cents, m.Label()) }

func Pages() domain.Page[Order] { return domain.Page[Order]{Items: []Order{New(1), New(2)}} }
`,
		"report/report.go": `package report

import "brg/gen"

func Total(os []gen.Order) int64 {
	var n int64
	for _, o := range os {
		n += o.Total.Cents
	}
	return n
}
`,
		"main.lyth": `import "fmt"
import "brg/domain"
import "brg/gen"
import "brg/report"

func cents(m: domain.Money): int64 = m.Cents

func main() {
    o := gen.New(5)
    label := when o.Status {
        Pending -> "pending"
        Paid -> "paid"
    }
    fmt.Println(label, o.Status == domain.Status.Paid, cents(o.Total), o.Total.Label())
    fmt.Println(o.Total.Note ?: "none", o.Prev?.Cents ?: -1)
    m: domain.Money = o.Total
    fmt.Println(gen.Describe(m), gen.Describe(domain.Money(Cents = 7)))
    page := gen.Pages()
    fmt.Println(len(page.Items), report.Total(page.Items))
}
`,
	}
	root := writeModule(t, "brg", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"brg"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	want := "paid true 5 5c\nnew -1\n5/5c 7/7c\n2 3"
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("output %q, want %q, err %v", out, want, err)
	}
}

// TestLythGenerate runs the //go:generate lines of .lyth files, which go
// generate cannot see: with go generate's variables, quoting, -command
// aliases, and a failing command that stops its package only.
func TestLythGenerate(t *testing.T) {
	files := map[string]string{
		"tools/stamp/main.go": `package main

import (
	"os"
	"strings"
)

func main() {
	out := os.Args[1]
	data := strings.Join(os.Args[2:], " ") + " " + os.Getenv("GOFILE") + ":" + os.Getenv("GOLINE") + " " + os.Getenv("GOPACKAGE") + "\n"
	os.WriteFile(out, []byte(data), 0o644)
}
`,
		"app/app.lyth": `//go:generate go run gen/tools/stamp app.txt "two words" $DOLLAR
//go:generate -command stamp go run gen/tools/stamp
// A later line of the file uses the alias:
//go:generate stamp alias.txt via alias

package app

func Answer(): int = 42
`,
		"app/app_test.lyth": `//go:generate go run gen/tools/stamp test.txt from a test file

import "testing"

func TestAnswer(t: *testing.T) {
    if Answer() != 42 {
        t.Fatal("wrong answer")
    }
}
`,
		"broken/broken.lyth": `//go:generate go run gen/tools/missing
//go:generate go run gen/tools/stamp never.txt

func main() {}
`,
	}
	root := writeModule(t, "gen", files)
	var stderr bytes.Buffer
	cfg := &build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: &stderr}
	if err := build.Generate(cfg, []string{"./app"}, build.GenerateOptions{}); err != nil {
		t.Fatalf("lyth generate: %v\n%s", err, stderr.String())
	}
	for name, want := range map[string]string{
		"app.txt":   "two words $ app.lyth:1 app\n",
		"alias.txt": "via alias app.lyth:4 app\n",
		"test.txt":  "from a test file app_test.lyth:1 app\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, "app", name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	stderr.Reset()
	err := build.Generate(cfg, []string{"./..."}, build.GenerateOptions{})
	if err == nil || !strings.Contains(stderr.String(), "broken.lyth:1: running \"go\"") {
		t.Errorf("a failing generator: err %v, stderr:\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "broken", "never.txt")); err == nil {
		t.Error("the directive after a failing one ran")
	}
}

// TestGoDirectiveTooOld reports a module whose go directive predates what
// Lyth needs (per-iteration loop variables, new(expr)) at the go line,
// instead of silently sharing loop variables or failing in go build.
func TestGoDirectiveTooOld(t *testing.T) {
	root := writeModule(t, "old", map[string]string{"main.lyth": "func main() {}\n"})
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module old\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := build.Compile(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	ds := s.Sink.All()
	if len(ds) != 1 || ds[0].Code != diag.EToolchain || !strings.Contains(ds[0].Message, "go.mod declares go 1.21; Lyth needs go 1.26") ||
		ds[0].Span.StartPos().Line != 3 {
		t.Fatalf("diagnostics:\n%s", render(ds))
	}
}

// TestBuildConstraints checks only the files the build includes: per-GOOS
// files declaring the same function, and //go:build tag pairs, as go build
// selects them; -tags switches the pair.
func TestBuildConstraints(t *testing.T) {
	files := map[string]string{
		"main.lyth":         "func main() {\n    println(platform(), mode())\n}\n",
		"plat_linux.lyth":   "func platform(): string = \"linux\"\n",
		"plat_darwin.lyth":  "func platform(): string = \"darwin\"\n",
		"plat_windows.lyth": "func platform(): string = \"windows\"\n",
		"plat_other.lyth":   "//go:build !linux && !darwin && !windows\n\nfunc platform(): string = \"other\"\n",
		"mode_dev.lyth":     "//go:build dev\n\nfunc mode(): string = \"dev\"\n",
		"mode_prod.lyth":    "//go:build !dev\n\nfunc mode(): string = \"prod\"\n",
		"ignored_tool.lyth": "//go:build ignore\n\nfunc main() {}\n",
	}
	root := writeModule(t, "plat", files)
	for _, tags := range [][]string{nil, {"dev"}} {
		cfg := &build.Config{WorkDir: root, Tags: tags, Stdout: os.Stdout, Stderr: os.Stderr}
		s, err := build.Compile(cfg, build.Target{})
		if err != nil || s.Sink.HasErrors() {
			t.Fatalf("tags %v: %v\n%s", tags, err, render(s.Sink.All()))
		}
		bin := filepath.Join(root, "plat.bin")
		if ok, err := s.GoBuild(bin, []string{"plat"}, nil); err != nil || !ok {
			t.Fatalf("build: %v\n%s", err, render(s.Sink.All()))
		}
		out, _ := exec.Command(bin).Output()
		mode := "prod"
		if tags != nil {
			mode = "dev"
		}
		want := runtime.GOOS
		if want != "linux" && want != "darwin" && want != "windows" {
			want = "other"
		}
		if got := strings.TrimSpace(string(out)); got != want+" "+mode {
			t.Errorf("tags %v: output %q, want %q", tags, got, want+" "+mode)
		}
	}
}

// TestTestOnlyPackages runs a directory that holds only _test.lyth files
// (end-to-end tests of other packages), as go test runs one of _test.go
// files, and leaves it out of builds.
func TestTestOnlyPackages(t *testing.T) {
	root := writeModule(t, "e2e", map[string]string{
		"calc/calc.lyth": calcLyth,
		"e2e/e2e_test.lyth": `import "testing"
import "e2e/calc"

func TestAcross(t: *testing.T) {
    if calc.Add(1, 1) != 2 {
        t.Fatal("Add")
    }
}
`,
	})
	code, out := testModule(t, root, "-v")
	if code != 0 || !strings.Contains(out, "--- PASS: TestAcross") {
		t.Fatalf("go test (exit %d):\n%s", code, out)
	}
	s := compileModule(t, root, false)
	for _, p := range s.Targets {
		if strings.HasSuffix(p.Dir.ImportPath, "/e2e") {
			t.Errorf("a build compiles the test-only package %s", p.Dir.ImportPath)
		}
	}
}

// TestWorkspaceModule checks a module in a go.work workspace: the go command
// rejects -modfile there, so the standard library (lyth/coll) comes in
// through a generated go.work that also uses the workspace's modules.
func TestWorkspaceModule(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	files := map[string]string{
		"go.work":      "go 1.26\n\nuse (\n\t./app\n\t./util\n)\n",
		"app/go.mod":   "module example.com/app\n\ngo 1.26\n\nrequire example.com/util v0.0.0\n",
		"util/go.mod":  "module example.com/util\n\ngo 1.26\n",
		"util/util.go": "package util\n\nfunc Double(n int) int { return n * 2 }\n",
		"app/main.lyth": `import "lyth/coll"
import "example.com/util"

func main() {
    println([1, 2, 3].Map { util.Double(it) }.Filter { it > 2 })
}
`,
	}
	for name, content := range files {
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(ws, "app")
	s := compileModule(t, root, false)
	if s.WorkFile == "" {
		t.Fatal("the session did not use a workspace file")
	}
	bin := filepath.Join(t.TempDir(), "app.bin")
	if ok, err := s.GoBuild(bin, []string{"example.com/app"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "[4 6]" {
		t.Fatalf("got %q, %v", out, err)
	}
}

// TestModVendor checks lyth mod vendor: the packages .lyth files import are
// vendored, which go mod vendor alone cannot see, and the module then builds
// from vendor/.
func TestModVendor(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	files := map[string]string{
		"dep/go.mod":     "module example.com/dep\n\ngo 1.26\n",
		"dep/words/w.go": "package words\n\nfunc Hello() string { return \"hello\" }\n",
		"app/go.mod":     "module example.com/app\n\ngo 1.26\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n",
		"app/main.lyth":  "import \"example.com/dep/words\"\n\nfunc main() {\n    println(words.Hello())\n}\n",
	}
	for name, content := range files {
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(ws, "app")
	if _, err := build.ModVendor(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, nil); err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(filepath.Join(root, "vendor", "modules.txt"))
	if err != nil || !strings.Contains(string(list), "example.com/dep/words") {
		t.Fatalf("vendor/modules.txt: %v\n%s", err, list)
	}
	s := compileModule(t, root, false)
	bin := filepath.Join(t.TempDir(), "app.bin")
	if ok, err := s.GoBuild(bin, []string{"example.com/app"}, nil); err != nil || !ok {
		t.Fatalf("build from vendor/ failed: %v\n%s", err, render(s.Sink.All()))
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("got %q, %v", out, err)
	}
}
