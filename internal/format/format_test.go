package format

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var cases = []struct{ name, in, want string }{
	{"indentation", "func main() {\nif true {\nprintln(1)\n}\n}\n", "func main() {\n    if true {\n        println(1)\n    }\n}\n"},
	{"spaces", "func f(a:int,b:int):int{return a+b}\n", "func f(a: int, b: int): int { return a+b }\n"},
	{"operators", "func f(): bool {\n    x:=1\n    y = x==2&&x<3||!z\n    return x*2 + 1 > 0\n}\n",
		"func f(): bool {\n    x := 1\n    y = x == 2 && x < 3 || !z\n    return x*2 + 1 > 0\n}\n"},
	{"unary", "func f() {\n    a := -1\n    b := *p\n    c := &T()\n    d := [-1, -2]\n    -2\n}\n",
		"func f() {\n    a := -1\n    b := *p\n    c := &T()\n    d := [-1, -2]\n    -2\n}\n"},
	{"types", "var m: map[string]*User = [:]\nvar xs: []*int?\nvar ch: <-chan int\nvar out: chan<- int\nfunc f(xs: ...int, p: *[]int) {}\n",
		"var m: map[string]*User = [:]\nvar xs: []*int?\nvar ch: <-chan int\nvar out: chan<- int\nfunc f(xs: ...int, p: *[]int) {}\n"},
	{"literals", "func f() {\n    a := []int{ 1,2 }\n    b := map[string]int{\"a\":1}\n    c := [ 1, 2 ]\n    d := [\"k\": 1]\n    e := struct{}{}\n}\n",
		"func f() {\n    a := []int{1, 2}\n    b := map[string]int{\"a\": 1}\n    c := [1, 2]\n    d := [\"k\": 1]\n    e := struct{}{}\n}\n"},
	{"lambdas", "func f() {\n    xs.Map {it*2}\n    g := {a,b->a+b}\n    xs.Filter { x ->\n    x > 1\n    }\n}\n",
		"func f() {\n    xs.Map { it*2 }\n    g := { a, b -> a+b }\n    xs.Filter { x ->\n        x > 1\n    }\n}\n"},
	{"slices and generics", "func Max[T:cmp.Ordered](a: T, b: T): T = a\nfunc f() {\n    a := xs[1:len(xs)-1]\n    b := Max[int](1, 2)\n    c := self[:n]\n}\n",
		"func Max[T: cmp.Ordered](a: T, b: T): T = a\nfunc f() {\n    a := xs[1:len(xs)-1]\n    b := Max[int](1, 2)\n    c := self[:n]\n}\n"},
	{"declarations", "struct Circle:Shape{R: float64}\ntype Grid [3]int\ntype Set[T: comparable] map[T]struct{}\ntype Handler (Ctx) -> unit\nenum Plan: string { Free = \"free\" }\n",
		"struct Circle : Shape { R: float64 }\ntype Grid [3]int\ntype Set[T: comparable] map[T]struct{}\ntype Handler (Ctx) -> unit\nenum Plan: string { Free = \"free\" }\n"},
	{"labels and ranges", "func f() {\n    outer@ for i in 0 ..< n {\n        for j in 1..n { break@outer }\n    }\n}\n",
		"func f() {\n    outer@ for i in 0..<n {\n        for j in 1..n { break@outer }\n    }\n}\n"},
	{"errors and nulls", "func f(): int throws {\n    x := g() ?  ?: 0\n    y := h()!!\n    z := u ?. Name\n    w := a as? T\n    v := k catch(err){ 0 }\n    return x\n}\n",
		"func f(): int throws {\n    x := g()? ?: 0\n    y := h()!!\n    z := u?.Name\n    w := a as? T\n    v := k catch (err) { 0 }\n    return x\n}\n"},
	{"continuations", "func f(): int =\nlongName +\nother\nfunc g() {\n    names := users\n    .Filter { it.Ok }\n    .Map { it.Name }\n}\n",
		"func f(): int =\n    longName +\n    other\nfunc g() {\n    names := users\n        .Filter { it.Ok }\n        .Map { it.Name }\n}\n"},
	{"multi-line header", "func f() {\n    if a &&\n    b {\n    run()\n    }\n}\n", "func f() {\n    if a &&\n        b {\n        run()\n    }\n}\n"},
	{"calls across lines", "func f() {\n    u := User(\n    Name = \"a\",\n    Age = 3,\n    )\n}\n", "func f() {\n    u := User(\n        Name = \"a\",\n        Age = 3,\n    )\n}\n"},
	{"blank lines", "\n\nimport \"fmt\"\n\n\n\nfunc main() {\n\n    fmt.Println()\n\n}\n\n\n", "import \"fmt\"\n\nfunc main() {\n\n    fmt.Println()\n\n}\n"},
	{"comments", "func f() {\n// leading\n    a := 1 // one\n    bb := 22 // two\n    /* block */ c := 3\n}\n",
		"func f() {\n    // leading\n    a := 1   // one\n    bb := 22 // two\n    /* block */ c := 3\n}\n"},
	{"comment before closing brace", "func f() {\n    a()\n// trailing note\n}\n", "func f() {\n    a()\n    // trailing note\n}\n"},
	{"tags", "struct User {\n    ID: int64 `db:\"id\"`\n    Name: string `db:\"name\"` // shown\n}\n",
		"struct User {\n    ID: int64    `db:\"id\"`\n    Name: string `db:\"name\"` // shown\n}\n"},
	{"raw strings and templates", "func f() {\n    q := `SELECT *\n  FROM t   `\n    s := \"a ${ x+1 } b\"\n}\n",
		"func f() {\n    q := `SELECT *\n  FROM t   `\n    s := \"a ${ x+1 } b\"\n}\n"},
	{"in as a name", "func stage(in: <-chan int) {\n    double := { in: int -> in * 2 }\n    x := in\n    b := 1 in xs\n    c := x !in xs\n}\n",
		"func stage(in: <-chan int) {\n    double := { in: int -> in * 2 }\n    x := in\n    b := 1 in xs\n    c := x !in xs\n}\n"},
	{"when and else", "func f(x: int): string = when x {\n1, 2 -> \"low\"\nin 3..9 -> \"mid\"\nelse -> {\n\"high\"\n}\n}\n",
		"func f(x: int): string = when x {\n    1, 2 -> \"low\"\n    in 3..9 -> \"mid\"\n    else -> {\n        \"high\"\n    }\n}\n"},
	{"directives", "//go:embed a.txt\nvar s: string\n//go:noinline\nfunc f() {}\n", "//go:embed a.txt\nvar s: string\n//go:noinline\nfunc f() {}\n"},
	{"crlf", "func main() {\r\n  x := `a\r\nb`\r\n}\r\n", "func main() {\n    x := `a\nb`\n}\n"},
}

func TestFormat(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Source(c.name+".lyth", []byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
			again, err := Source(c.name+".lyth", got)
			if err != nil || !bytes.Equal(again, got) {
				t.Errorf("not idempotent: %v\n%s", err, again)
			}
		})
	}
}

func TestSyntaxErrors(t *testing.T) {
	if _, err := Source("bad.lyth", []byte("func main() {\n")); err == nil || !strings.Contains(err.Error(), "bad.lyth:") {
		t.Errorf("err = %v, want a syntax error with a position", err)
	}
}

// TestCorpus formats every Lyth file of the test suites and the examples:
// the result must keep the syntax tree (Source checks it), and formatting
// it again must change nothing.
func TestCorpus(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../tests/testdata", "../../examples", "../../std", "../../bench"} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lyth") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) < 100 {
		t.Fatalf("found only %d files", len(files))
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Source(f, src)
		if err != nil {
			if strings.Contains(err.Error(), "would change") {
				t.Errorf("%v", err)
			}
			continue // a test of syntax errors
		}
		again, err := Source(f, out)
		if err != nil || !bytes.Equal(again, out) {
			t.Errorf("%s: formatting is not idempotent (%v)", f, err)
		}
	}
}

// TestExamplesFormatted keeps the examples and the standard library in the
// layout lyth fmt gives them, the layout users copy.
func TestExamplesFormatted(t *testing.T) {
	for _, dir := range []string{"../../examples", "../../std"} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".lyth") {
				return nil
			}
			src, _ := os.ReadFile(p)
			out, err := Source(p, src)
			if err != nil {
				t.Errorf("%v", err)
			} else if !bytes.Equal(src, out) {
				t.Errorf("%s is not formatted: run lyth fmt", p)
			}
			return nil
		})
	}
}
