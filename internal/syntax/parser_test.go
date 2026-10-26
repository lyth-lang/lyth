package syntax

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

func parse(t *testing.T, src string) (*File, *diag.Sink) {
	t.Helper()
	sink := &diag.Sink{}
	f := Parse(source.NewFile("t.lyth", "t.lyth", []byte(src)), sink)
	return f, sink
}

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, sink := parse(t, src)
	if sink.HasErrors() {
		var b strings.Builder
		diag.Render(&b, sink.All(), diag.RenderOptions{})
		t.Fatalf("unexpected syntax errors:\n%s", b.String())
	}
	return f
}

func parseExprDump(t *testing.T, src string) string {
	t.Helper()
	sink := &diag.Sink{}
	x := ParseExpr(source.NewFile("t.lyth", "t.lyth", []byte(src)), sink)
	if sink.HasErrors() {
		var b strings.Builder
		diag.Render(&b, sink.All(), diag.RenderOptions{})
		t.Fatalf("ParseExpr(%q):\n%s", src, b.String())
	}
	return sexpr(x)
}

// sexpr renders an expression as a compact s-expression for precedence tests.
func sexpr(e Expr) string {
	switch e := e.(type) {
	case *Ident:
		return e.Name
	case *BasicLit:
		return e.Raw
	case *BoolLit:
		if e.Value {
			return "true"
		}
		return "false"
	case *NullLit:
		return "null"
	case *ParenExpr:
		return sexpr(e.X)
	case *BinaryExpr:
		return "(" + e.Op.String() + " " + sexpr(e.X) + " " + sexpr(e.Y) + ")"
	case *UnaryExpr:
		return "(" + e.Op.String() + " " + sexpr(e.X) + ")"
	case *ElvisExpr:
		return "(?: " + sexpr(e.X) + " " + sexpr(e.Y) + ")"
	case *SelectorExpr:
		op := "."
		if e.Safe {
			op = "?."
		}
		return "(" + op + " " + sexpr(e.X) + " " + e.Sel.Name + ")"
	case *CallExpr:
		var parts []string
		for _, a := range e.Args {
			s := sexpr(a.Value)
			if a.Name != nil {
				s = a.Name.Name + "=" + s
			}
			parts = append(parts, s)
		}
		s := "(call " + sexpr(e.Fun)
		if len(parts) > 0 {
			s += " " + strings.Join(parts, " ")
		}
		if e.Trailing != nil {
			s += " {lambda}"
		}
		return s + ")"
	case *TryExpr:
		return "(? " + sexpr(e.X) + ")"
	case *NotNullExpr:
		return "(!! " + sexpr(e.X) + ")"
	case *CatchExpr:
		b := ""
		if e.Binder != nil {
			b = e.Binder.Name
		}
		return "(catch " + sexpr(e.X) + " " + b + ")"
	case *IndexExpr:
		return "(index " + sexpr(e.X) + " " + sexpr(e.Index) + ")"
	case *RangeExpr:
		op := ".."
		if e.Exclusive {
			op = "..<"
		}
		return "(" + op + " " + sexpr(e.Lo) + " " + sexpr(e.Hi) + ")"
	case *JumpExpr:
		return "(jump)"
	case *TemplateLit:
		var parts []string
		for _, p := range e.Parts {
			if p.X != nil {
				parts = append(parts, "${"+sexpr(p.X)+"}")
			} else {
				parts = append(parts, p.Text)
			}
		}
		return "(tmpl " + strings.Join(parts, "|") + ")"
	case *InstantiateExpr:
		return "(inst " + sexpr(e.X) + ")"
	case *IsExpr:
		return "(is " + sexpr(e.X) + ")"
	case *AsExpr:
		if e.Safe {
			return "(as? " + sexpr(e.X) + ")"
		}
		return "(as " + sexpr(e.X) + ")"
	case *TypeOperand:
		return "(type)"
	case *ListLit:
		if e.Type != nil {
			return fmt.Sprintf("(list %s %d)", e.Type.Span().Text(), len(e.Elems))
		}
		return "(list)"
	case *MapLit:
		if e.Type != nil {
			return fmt.Sprintf("(map %s %d)", e.Type.Span().Text(), len(e.Keys))
		}
		return "(map)"
	case *LambdaExpr:
		return "{lambda}"
	case *IfExpr:
		return "(if " + sexpr(e.Cond) + ")"
	}
	return "?"
}

func TestPrecedence(t *testing.T) {
	tests := []struct{ src, want string }{
		{"a + b * c", "(+ a (* b c))"},
		{"a * b + c", "(+ (* a b) c)"},
		{"a + b | c", "(| (+ a b) c)"},
		{"a < b == c < d", "(== (< a b) (< c d))"},
		{"a || b && c", "(|| a (&& b c))"},
		{"a ?: b ?: c", "(?: a (?: b c))"},
		{"a ?: b + c", "(?: a (+ b c))"},
		{"a == b ?: c", "(== a (?: b c))"},
		{"-a.b", "(- (. a b))"},
		{"!x!!", "(! (!! x))"},
		{"&User(name = n)", "(& (call User name=n))"},
		{"*p.x", "(* (. p x))"},
		{"f(x)?.name", "(?. (call f x) name)"}, // the checker reinterprets ?. on fallible calls
		{"a?.b?.c", "(?. (?. a b) c)"},
		{"load(id) catch (e) { 0 }", "(catch (call load id) e)"},
		{"x + load() catch { 0 }", "(+ x (catch (call load) ))"},
		{"xs[i + 1]", "(index xs (+ i 1))"},
		{"0..<n", "(..< 0 n)"},
		{"a..b + 1", "(.. a (+ b 1))"},
		{`"Hi ${name}!"`, "(tmpl Hi |${name}|!)"},
		{"run { it }", "(call run {lambda})"},
		{"app.get(\"/\") { c -> c }", "(call (. app get) \"/\" {lambda})"},
		{"a < b", "(< a b)"},
		{"f(a < b, c > d)", "(call f (< a b) (> c d))"},
		{"make([]int, 10)", "(call make (type) 10)"},
		{"make(map[string]int)", "(call make (type))"},
		{"make(chan int, 1)", "(call make (type) 1)"},
		{"[]byte(s)", "(call (type) s)"},
		{"f([], x)", "(call f (list) x)"},
		{"Box[int](1)", "(call (index Box int) 1)"},
		{"[]int{1, 2}", "(list []int 2)"},
		{"[]int{}", "(list []int 0)"},
		{"[3]int{1}", "(list [3]int 1)"},
		{"[...]string{\"a\", \"b\"}", "(list [...]string 2)"},
		{"map[string]int{\"a\": 1, \"b\": 2,}", "(map map[string]int 2)"},
		{"[][]int{[1], []int{2}}", "(list [][]int 2)"},
		{"f([]int{1}) { it }", "(call f (list []int 1) {lambda})"},
		{"run { it }", "(call run {lambda})"},
		{"[4]byte(b)", "(call (type) b)"},
		{"[x] == xs", "(== (list) xs)"},
		{"Pair[int, string](1)", "(call (inst Pair) 1)"},
		{"f[[]int](x)", "(call (index f (type)) x)"},
		{"x is string", "(is x)"},
		{"x as? T", "(as? x)"},
		{"x as T", "(as x)"},
		{"a ?: return", "(?: a (jump))"},
		{"f(g()?, h())", "(call f (? (call g)) (call h))"},
	}
	for _, tt := range tests {
		if got := parseExprDump(t, tt.src); got != tt.want {
			t.Errorf("%s\n got: %s\nwant: %s", tt.src, got, tt.want)
		}
	}
}

func TestParseProgram(t *testing.T) {
	src := `package users

import "fmt"
import pgx "github.com/jackc/pgx/v5"

// User is a user.
struct User {
    ID: int64
    Name: string
    Email: string?  ` + "`json:\"email,omitempty\"`" + `
    hash: string
}

interface Store {
    Find(id: int64): User? throws
    All(): []User
}

const maxUsers = 100
var greeting = "hi"

func *Repo.Find(id: int64): User? throws {
    u := self.db.get(id)?
        ?: return null
    if u.Name == "" {
        throw errors.New("empty")
    } else if u.ID < 0 {
        return null
    } else {
        println("ok")
    }
    count := 0
    count += 1
    count++
    for x in 0..<10 {
        count = count + x
    }
    for i, v in self.items { println(i, v) }
    while count > 0 { count-- }
    a, b := pair()
    label := when count {
        0, 1 -> "few"
        else -> "many"
    }
    m := ["a": 1, "b": 2]
    xs: []int = [1, 2, 3]
    f := { x: int -> x * 2 }
    defer f.Close() catch (e) { log(e) }
    go { work() }
    return u
}

func User.display(): string = "${self.Name} <${self.Email ?: "-"}>"

func main() throws {
    result := compute()
        .map { it * 2 }
        .filter { it > 2 }
    fmt.Println(result)
}
`
	f := mustParse(t, src)
	if f.Package == nil || f.Package.Name != "users" {
		t.Fatalf("package clause not parsed")
	}
	if len(f.Imports) != 2 || f.Imports[1].Alias == nil || f.Imports[1].Alias.Name != "pgx" {
		t.Fatalf("imports not parsed: %+v", f.Imports)
	}
	if len(f.Decls) != 7 {
		t.Fatalf("got %d decls, want 7", len(f.Decls))
	}
	st := f.Decls[0].(*StructDecl)
	if st.Doc != "// User is a user." || len(st.Fields) != 4 || st.Fields[3].Name.Name != "hash" || st.Fields[2].Tag != `json:"email,omitempty"` {
		t.Errorf("struct parsed incorrectly: %s", Dump(st))
	}
	fn := f.Decls[4].(*FunDecl)
	if fn.Recv == nil || !fn.Recv.Pointer || fn.Recv.Name.Name != "Repo" || !fn.Throws {
		t.Errorf("method header parsed incorrectly")
	}
	if _, ok := fn.Result.Types[0].(*NullableType); !ok {
		t.Errorf("result type should be nullable")
	}
}

// A comment after code on its line belongs to that code, not to the next declaration or field.
func TestTrailingCommentIsNotDoc(t *testing.T) {
	f := mustParse(t, `struct User {  // header
    // ID doc
    ID: int64 // id
    Name: string
}  // closing

func find() {}
`)
	st := f.Decls[0].(*StructDecl)
	if st.Fields[0].Doc != "// ID doc" || st.Fields[1].Doc != "" {
		t.Errorf("field docs = %q, %q; want %q, %q", st.Fields[0].Doc, st.Fields[1].Doc, "// ID doc", "")
	}
	if fn := f.Decls[1].(*FunDecl); fn.Doc != "" {
		t.Errorf("func doc = %q, want none", fn.Doc)
	}
}

func TestSyntaxErrors(t *testing.T) {
	tests := []struct {
		src  string
		code diag.Code
		line int
	}{
		{"func main() {\n  val x = 1\n}", diag.EUnsupportedSyntax, 2},
		{"fun main() {}", diag.EUnsupportedSyntax, 1},
		{"func f(x int) {}", diag.EUnexpectedToken, 1},
		{"func main() {\n  if x > 1 println(x)\n}", diag.EUnsupportedSyntax, 2},
		{"func main() {\n  x := (1 + \n}", diag.EUnexpectedToken, 3},
		{"func main() {\n  for i := 0; i < 3; i++ { }\n}", diag.EUnsupportedSyntax, 2},
		{"func main() {\n  for i := 0; i < 3; i += 2 { }\n}", diag.EUnsupportedSyntax, 2},
		{"func main() {\n  for (x in xs) { }\n}", diag.EUnsupportedSyntax, 2},
		{"func f[T cmp.Ordered]() {}", diag.EUnsupportedSyntax, 1},
		{"func (s *Server) Run() {}", diag.EUnsupportedSyntax, 1},
		{"struct S(val a: int)", diag.EUnexpectedToken, 1},
	}
	for _, tt := range tests {
		_, sink := parse(t, tt.src)
		all := sink.All()
		if len(all) == 0 {
			t.Errorf("%q: expected an error", tt.src)
			continue
		}
		if all[0].Code != tt.code || all[0].Span.StartPos().Line != tt.line {
			t.Errorf("%q: got %s at line %d (%s), want %s at line %d", tt.src, all[0].Code, all[0].Span.StartPos().Line, all[0].Message, tt.code, tt.line)
		}
	}
}

func TestRecoveryReportsMultipleErrors(t *testing.T) {
	src := "func a() {\n  x := = 1\n}\nfunc b() {\n  y = = 2\n}\nfunc c() { ok := 1 }\n"
	f, sink := parse(t, src)
	if sink.ErrorCount() < 2 {
		t.Errorf("expected at least 2 errors, got %d", sink.ErrorCount())
	}
	if len(f.Decls) != 3 {
		t.Errorf("expected 3 declarations after recovery, got %d", len(f.Decls))
	}
}
