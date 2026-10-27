// Package check implements Lyth's name resolution, type checking, and flow
// analyses. It records its results in Info side tables, which the lowering
// stage consumes; the syntax tree itself is never modified.
package check

import (
	"go/constant"
	gotypes "go/types"

	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// Symbol is anything a name can resolve to.
type Symbol interface {
	Name() string
	DeclSpan() source.Span
}

// VarKind classifies variables.
type VarKind int

const (
	LocalVar VarKind = iota
	ParamVar
	ReceiverVar
	GlobalVar
	LoopVar
	CatchVar
)

// VarSym is a variable: local, parameter, receiver, loop variable, or global.
type VarSym struct {
	name     string
	Type     types.Type
	Mutable  bool
	Kind     VarKind
	Span     source.Span
	Used     bool
	Pub      bool
	GoName   string // globals only; locals are named by the lowering stage
	Pkg      *Package
	Decl     *syntax.PropDecl // globals only
	state    int              // globals: 0 unresolved, 1 resolving, 2 resolved
	Implicit bool             // compiler-introduced (it, self)
	// NotReassigned is set for a local its function never assigns after
	// declaring it (see reassignedNames); such a local smart-casts.
	NotReassigned bool
	// Rebinds is the parameter a local declared by ctx := f(ctx) replaces
	// (ADR 0039). InPlace is set when the parameter is declared in the same
	// scope, the function's outermost block, where Go's := assigns it.
	Rebinds *VarSym
	InPlace bool
	// FreshName asks the lowering for a Go name of its own: the local
	// replaces a parameter that a closure created earlier captured, which
	// Go's ctx, cancel := ... would assign under the closure.
	FreshName bool
	// owner is the function or lambda that declares a parameter or local,
	// and captured records that a nested function literal used it.
	owner    *funcCtx
	captured bool
	gen      int // assignments so far, for smart casts of reassigned locals (flow.go)
	// loopForgot records that a loop, not an assignment, last ended the
	// variable's smart casts.
	loopForgot bool
	// rawCall is the fallible call a value was destructured from, which
	// makes the value null when the call fails; "" otherwise.
	rawCall string
}

func (v *VarSym) Name() string          { return v.name }
func (v *VarSym) DeclSpan() source.Span { return v.Span }

// ConstSym is a constant.
type ConstSym struct {
	name   string
	Type   types.Type
	Val    constant.Value
	Span   source.Span
	Pub    bool
	GoName string
	Pkg    *Package
	Decl   *syntax.ConstDecl
	state  int
}

func (c *ConstSym) Name() string          { return c.name }
func (c *ConstSym) DeclSpan() source.Span { return c.Span }

// FuncSym is a Lyth function or method.
type FuncSym struct {
	name   string
	GoName string
	Pub    bool
	Pkg    *Package
	Sig    *types.Signature
	Decl   *syntax.FunDecl
	Recv   *types.TypeDecl // methods only
	// RecvArgs are the type parameters of a generic receiver, as the method
	// names them (fun Page<T>.First()).
	RecvArgs []types.Type
	// ExtRecv is the receiver type of an extension function, which Go
	// receives as the first parameter.
	ExtRecv types.Type
	PtrRecv bool
	Span    source.Span
	IsMain  bool
	IsInit  bool // func init: not in the package scope, and there may be several
	// IsTest marks a function that go test runs (TestXxx, BenchmarkXxx,
	// FuzzXxx, ExampleXxx, TestMain) declared in a _test.lyth file.
	IsTest bool

	tscope      *Scope     // the type parameters of a generic function, if any
	infer       inferState // result type inferred from an expression body
	inferCycle  bool       // the inference depends on itself (reported)
	bodyChecked bool       // the body was checked early, to infer the result
}

// inferState tracks the result inference of an unexported expression-bodied
// function: fun double(x: Int) = x * 2.
type inferState int

const (
	inferNone    inferState = iota // the result type is declared
	inferPending                   // to be inferred from the body
	inferRunning                   // the body is being checked
	inferDone
)

func (f *FuncSym) Name() string          { return f.name }
func (f *FuncSym) DeclSpan() source.Span { return f.Span }

// TypeSym is a named type: a Lyth declaration or a basic/builtin type.
type TypeSym struct {
	name    string
	Type    types.Type // *types.Named for Lyth declarations, *types.Basic etc. for builtins
	Decl    *types.TypeDecl
	Generic string // "Map", "Chan", "SendChan", "ReceiveChan" for builtin generics; "List" to explain []T
	Span    source.Span
	Pkg     *Package
	syn     syntax.Decl
	// Alias is set for type aliases (type Name = Type); Type is resolved lazily.
	Alias     *syntax.TypeAliasDecl
	AliasPub  bool
	GoName    string
	aliasBusy bool
	tscope    *Scope // the type parameters of a generic type
	// AliasParams are a generic alias's type parameters (ADR 0047): its Type
	// mentions them, and each use substitutes its arguments.
	AliasParams []*types.TypeParam
}

func (t *TypeSym) Name() string          { return t.name }
func (t *TypeSym) DeclSpan() source.Span { return t.Span }

// PkgSym is an imported package name.
type PkgSym struct {
	name   string
	Pkg    *Package
	Import *syntax.ImportDecl
	Used   bool
}

func (p *PkgSym) Name() string          { return p.name }
func (p *PkgSym) DeclSpan() source.Span { return p.Import.S }

// GoObj is a declaration from a Go package, resolved through a qualifier.
type GoObj struct {
	Obj gotypes.Object
	Pkg *Package
}

func (g *GoObj) Name() string          { return g.Obj.Name() }
func (g *GoObj) DeclSpan() source.Span { return source.Span{} }

// BuiltinKind enumerates builtin functions.
type BuiltinKind int

const (
	BuiltinNone BuiltinKind = iota
	BuiltinLen
	BuiltinCap
	BuiltinAppend
	BuiltinCopy
	BuiltinDelete
	BuiltinMake
	BuiltinNew
	BuiltinPanic
	BuiltinRecover
	BuiltinMin
	BuiltinMax
	BuiltinClear
	BuiltinClose
	BuiltinPrint
	BuiltinPrintln
	BuiltinComplex
	BuiltinReal
	BuiltinImag
)

// BuiltinSym is a builtin function.
type BuiltinSym struct {
	name string
	Kind BuiltinKind
}

func (b *BuiltinSym) Name() string          { return b.name }
func (b *BuiltinSym) DeclSpan() source.Span { return source.Span{} }

// MemberSym is a receiver member reached through implicit `this`.
type MemberSym struct {
	Recv   *VarSym
	Field  *types.Field
	Method *types.Method
	GoName string
	Type   types.Type
}

func (m *MemberSym) Name() string {
	if m.Field != nil {
		return m.Field.Name
	}
	return m.Method.Name
}
func (m *MemberSym) DeclSpan() source.Span {
	if m.Field != nil {
		return m.Field.Span
	}
	return m.Method.Span
}

// ---------------------------------------------------------------------------
// Scopes

// ScopeKind classifies scopes.
type ScopeKind int

const (
	UniverseScope ScopeKind = iota
	PackageScope
	FileScope
	FuncScope
	BlockScope
)

// Scope maps names to symbols and carries smart-cast narrowings.
type Scope struct {
	parent *Scope
	kind   ScopeKind
	syms   map[string]Symbol
	order  []Symbol
	narrow map[*VarSym]narrowEntry
	fn     *funcCtx // set on function scopes
}

func newScope(parent *Scope, kind ScopeKind) *Scope {
	return &Scope{parent: parent, kind: kind, syms: map[string]Symbol{}}
}

// Replace makes sym the symbol of its name in s from now on, hiding the one
// declared before (a parameter replaced by ctx := f(ctx), ADR 0039).
func (s *Scope) Replace(sym Symbol) {
	s.syms[sym.Name()] = sym
	s.order = append(s.order, sym)
}

// Insert adds a symbol; it returns the existing symbol if the name is taken.
func (s *Scope) Insert(sym Symbol) Symbol {
	if old, ok := s.syms[sym.Name()]; ok {
		return old
	}
	s.syms[sym.Name()] = sym
	s.order = append(s.order, sym)
	return nil
}

// LookupLocal returns the symbol declared directly in s.
func (s *Scope) LookupLocal(name string) Symbol { return s.syms[name] }

// narrowed returns the smart-cast type of v visible from s in the function
// fn. A smart cast of a variable that is never reassigned holds in lambdas
// too; one of a reassigned local holds only in its own function and until
// the variable's next assignment (flow.go).
func (s *Scope) narrowed(v *VarSym, fn *funcCtx) (types.Type, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if e, ok := sc.narrow[v]; ok {
			if e.flow && (e.gen != v.gen || e.fn != fn) {
				return nil, false
			}
			return e.t, true
		}
	}
	return nil, false
}

// staleNarrow reports whether a smart cast of v recorded in s or its parents
// was ended by an assignment or a loop.
func (s *Scope) staleNarrow(v *VarSym) bool {
	for sc := s; sc != nil; sc = sc.parent {
		if e, ok := sc.narrow[v]; ok {
			return e.flow && e.gen != v.gen
		}
	}
	return false
}

func (s *Scope) setNarrow(v *VarSym, t types.Type) {
	if s.narrow == nil {
		s.narrow = map[*VarSym]narrowEntry{}
	}
	e := narrowEntry{t: t}
	if !v.Settled() {
		e.flow, e.gen, e.fn = true, v.gen, v.owner
	}
	s.narrow[v] = e
}

// Symbols returns the symbols of s in declaration order.
func (s *Scope) Symbols() []Symbol { return s.order }
