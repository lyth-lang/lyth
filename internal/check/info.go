package check

import (
	"go/constant"
	gotypes "go/types"

	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// Mode describes what an expression denotes.
type Mode int

const (
	ModeInvalid Mode = iota
	ModeValue
	ModeNoValue // a call that returns nothing
	ModeType
	ModePackage
	ModeBuiltin
)

// TypeAndValue records an expression's type and, for constants, its value.
type TypeAndValue struct {
	Mode  Mode
	Type  types.Type
	Value constant.Value
}

// ConversionKind is an implicit conversion inserted at a use site.
type ConversionKind int

const (
	ConvNone     ConversionKind = iota
	ConvBox                     // T -> T? for value types: new(x)
	ConvUnbox                   // smart-cast optional value read as T: *x
	ConvAssert                  // smart cast by a type test: x.(T), see Info.Asserts
	ConvNilIface                // T? or *T? to a nullable interface: null is a nil interface, see Info.IfaceTargets
	ConvReuse                   // smart-cast optional value used as T? again: the original box, x
)

// CallKind classifies call expressions.
type CallKind int

const (
	CallFunc CallKind = iota
	CallMethod
	CallConstruct
	CallConvert
	CallBuiltin
	CallPrelude
)

// PreludeKind enumerates prelude methods on built-in types.
type PreludeKind int

const (
	PreludeNone PreludeKind = iota
	PreludeToInt
	PreludeToInt64
	PreludeToFloat64
	PreludeToBool
	PreludeWrap
	PreludeWrapFailure // f().wrap("ctx") on a call that can fail: the error, wrapped when it fails
	PreludeSend        // ch.send(v): ch <- v
	PreludeReceive     // ch.receive(): <-ch
	PreludeClose       // ch.close(): close(ch)
)

// CallInfo describes a resolved call.
type CallInfo struct {
	Kind     CallKind
	Sig      *types.Signature // resolved (instantiated) signature
	Builtin  BuiltinKind
	Prelude  PreludeKind
	Target   types.Type    // construction/conversion target type
	Fields   []string      // construction: Go field name per argument, in source order
	Args     []syntax.Expr // all arguments in source order (trailing lambda last)
	TypeArgs []types.Type  // inferred type arguments of a generic call
	// ExplicitTypeArgs reports that Go cannot infer TypeArgs from the
	// arguments (a type parameter occurs only in the results), so the
	// lowering writes them.
	ExplicitTypeArgs bool
	Safe             bool // x?.m(): result is null when x is null
	TryFirst         bool // x?.m() on a fallible x: propagate first
	// Ext is set for a call of an extension function: x.Filter(...) is the
	// Go call coll.Filter(x, ...).
	Ext *ExtCall
	// ArgParams maps each argument (in Args order) to its parameter index
	// when named arguments or default values are involved; nil otherwise.
	ArgParams []int
	// Defaults are the parameters a call leaves to their default values.
	Defaults []DefaultArg
}

// ExtCall describes the extension function a selector names.
type ExtCall struct {
	Func *FuncSym
	Pkg  *Package // the package declaring it
	Bind bindings // type parameters bound by the receiver
	// Addr passes the receiver's address (a pointer receiver on a value);
	// Deref passes the value a pointer receiver points to.
	Addr, Deref bool
}

// Conformance is an interface a struct declares it implements. Ptr reports
// that only the pointer type implements it (pointer-receiver methods).
type Conformance struct {
	Iface types.Type
	Ptr   bool
	Span  source.Span // the interface in the declaration
}

// DefaultArg is a parameter filled with its default value at a call site.
// The expression belongs to the declaring package, whose Info describes it.
type DefaultArg struct {
	Param int
	Expr  syntax.Expr
	Info  *Info
}

// InKind classifies membership tests (`x in y`).
type InKind int

const (
	InRange  InKind = iota // y is a range a..b or a..<b
	InList                 // y is a List: slices.Contains
	InArray                // y is an array: slices.Contains over a slice of it
	InMap                  // y is a Map: key presence
	InString               // y is a String: substring (or rune) containment
)

// InInfo describes a membership test.
type InInfo struct {
	Kind  InKind
	Rune  bool              // InString with a Rune element
	Range *syntax.RangeExpr // InRange
	// Optional is the element type of a list of boxed optionals ([]int?),
	// searched by content.
	Optional types.Type
}

// SelKind classifies selector expressions.
type SelKind int

const (
	SelField SelKind = iota
	SelMethod
	SelPkgMember
	SelPrelude
	SelExtension // an extension function; Selection.Obj is an *ExtCall
)

// Selection describes a resolved selector.
type Selection struct {
	Kind     SelKind
	GoName   string
	Type     types.Type // field type, method signature, or member type
	Recv     types.Type // receiver type (before auto-deref)
	PtrRecv  bool
	Safe     bool // ?. on a nullable receiver
	TryFirst bool // ?. on a fallible receiver: (x?).name
	Prelude  PreludeKind
	Obj      any
}

// TryInfo describes a `?` or `catch` expression.
type TryInfo struct {
	Success []types.Type // success value types
	Raw     []types.Type // all Go components (success + error)
	Binder  *VarSym      // catch only
	// CatchType is the error type a typed catch handles (`catch (e: T)`);
	// nil for ? and for a catch of all errors.
	CatchType types.Type
	// For catch: the expression yielding the handler's value (nil if the block diverges).
	ValueExpr syntax.Expr
}

// LambdaInfo describes a lambda.
type LambdaInfo struct {
	Sig      *types.Signature
	Params   []*VarSym
	Fallible bool
	// ResultExpr is the trailing expression statement used as the lambda's
	// value, or nil.
	ResultExpr syntax.Expr
	Unused     []*VarSym
}

// ForKind classifies for loops.
type ForKind int

const (
	ForRange ForKind = iota // a..b or a..<b
	ForSlice
	ForArray
	ForMap
	ForString
	ForChan
	ForFunc // a Go iterator function (iter.Seq, iter.Seq2)
)

// ForInfo describes a for loop.
type ForInfo struct {
	Kind ForKind
	Vars []*VarSym
}

// FuncInfo describes a checked function body.
type FuncInfo struct {
	Sym    *FuncSym
	Recv   *VarSym
	Params []*VarSym
	// Results are the named results, locals of the body (ADR 0047).
	Results []*VarSym
	Unused  []*VarSym
	// DeferErr says a deferred call or block of the function may fail it
	// (defer f.Close()?, throw in defer { }): Go names its error result.
	DeferErr bool
}

// Info holds all results of type checking a package.
type Info struct {
	Types      map[syntax.Expr]TypeAndValue
	TypeExprs  map[syntax.TypeExpr]types.Type
	Defs       map[*syntax.Ident]Symbol
	Uses       map[*syntax.Ident]Symbol
	Calls      map[*syntax.CallExpr]*CallInfo
	Selections map[*syntax.SelectorExpr]*Selection
	Implicit   map[syntax.Expr]ConversionKind
	Tries      map[syntax.Expr]*TryInfo // *TryExpr and *CatchExpr
	Lambdas    map[*syntax.LambdaExpr]*LambdaInfo
	Fors       map[*syntax.ForStmt]*ForInfo
	Funcs      map[*syntax.FunDecl]*FuncInfo
	Locals     map[*syntax.LocalDecl][]*VarSym
	// BlockValues maps a block used as a value (if/when branch, catch handler)
	// to the expression that provides the value.
	BlockValues map[*syntax.Block]syntax.Expr
	// UsedAsValue records if/when expressions whose value is used.
	UsedAsValue map[syntax.Expr]bool
	// Unused lists locals that are never read (the generator emits `_ = x`).
	Unused map[*VarSym]bool
	// Asserts records the target type of ConvAssert conversions.
	Asserts map[syntax.Expr]types.Type
	// IfaceTargets records the interface type of ConvNilIface conversions.
	IfaceTargets map[syntax.Expr]types.Type
	// Conforms lists, per struct declaration, the interfaces it declares it
	// implements (struct T : I), in declaration order.
	Conforms map[*syntax.StructDecl][]Conformance
	// PassThrough marks fallible calls returned as they are by a throws
	// function (return f() lowers to Go's return f()).
	PassThrough map[syntax.Expr]bool
	// Ins describes membership tests, keyed by the InExpr or WhenCond.
	Ins map[syntax.Node]*InInfo
	// FuncInsts records the type arguments of explicitly instantiated
	// generic functions (errors.AsType<*NotFound>).
	FuncInsts map[*syntax.InstantiateExpr][]types.Type
	// ValueInsts records the type arguments inferred for a generic function
	// used as a value outside a call's arguments (S(f = cmp.Compare)), which
	// the lowering writes: cmp.Compare[int].
	ValueInsts map[syntax.Expr][]types.Type
	// IndexInsts maps an index expression that is an instantiation (f[int])
	// to the instantiation the checker read it as.
	IndexInsts map[*syntax.IndexExpr]*syntax.InstantiateExpr
	// TypeTests records the tested type of `is`/`as` expressions and of
	// `is` conditions in when branches.
	TypeTests map[syntax.Node]types.Type
	// SelectVars lists the bindings of select branches that receive into
	// variables, in declaration order (nil for _).
	SelectVars map[*syntax.SelectCase][]*VarSym
	// Entries maps enum entries used as values (Color.Red, or Red in a when
	// over a Color) to the entry.
	Entries map[syntax.Expr]*types.EnumEntry
	// FallibleDefers marks the defer statements that may fail the function:
	// defer f()?, or a deferred block with ? or throw.
	FallibleDefers map[*syntax.DeferStmt]bool
	// Exhaustive records when expressions over an enum or sealed interface
	// that cover every entry or member without an else branch; the value is
	// the subject's type. The generated switch gets a panicking default.
	Exhaustive map[*syntax.WhenExpr]types.Type
}

func newInfo() *Info {
	return &Info{
		Types:        map[syntax.Expr]TypeAndValue{},
		TypeExprs:    map[syntax.TypeExpr]types.Type{},
		Defs:         map[*syntax.Ident]Symbol{},
		Uses:         map[*syntax.Ident]Symbol{},
		Calls:        map[*syntax.CallExpr]*CallInfo{},
		Selections:   map[*syntax.SelectorExpr]*Selection{},
		Implicit:     map[syntax.Expr]ConversionKind{},
		Tries:        map[syntax.Expr]*TryInfo{},
		Lambdas:      map[*syntax.LambdaExpr]*LambdaInfo{},
		Fors:         map[*syntax.ForStmt]*ForInfo{},
		Funcs:        map[*syntax.FunDecl]*FuncInfo{},
		Locals:       map[*syntax.LocalDecl][]*VarSym{},
		BlockValues:  map[*syntax.Block]syntax.Expr{},
		UsedAsValue:  map[syntax.Expr]bool{},
		Unused:       map[*VarSym]bool{},
		Asserts:      map[syntax.Expr]types.Type{},
		IfaceTargets: map[syntax.Expr]types.Type{},
		TypeTests:    map[syntax.Node]types.Type{},
		FuncInsts:    map[*syntax.InstantiateExpr][]types.Type{},
		ValueInsts:   map[syntax.Expr][]types.Type{},
		IndexInsts:   map[*syntax.IndexExpr]*syntax.InstantiateExpr{},
		Ins:          map[syntax.Node]*InInfo{},
		PassThrough:  map[syntax.Expr]bool{},
		Conforms:     map[*syntax.StructDecl][]Conformance{},
		SelectVars:   map[*syntax.SelectCase][]*VarSym{},
		Entries:      map[syntax.Expr]*types.EnumEntry{},
		Exhaustive:   map[*syntax.WhenExpr]types.Type{},

		FallibleDefers: map[*syntax.DeferStmt]bool{},
	}
}

// TypeOf returns the recorded type of an expression, or nil.
func (i *Info) TypeOf(e syntax.Expr) types.Type {
	if tv, ok := i.Types[e]; ok {
		return tv.Type
	}
	return nil
}

// Package is a Lyth package or a Go package as seen by the checker.
type Package struct {
	Path  string
	Name  string
	Lyth  bool
	Go    *gotypes.Package // Go: loaded types. Lyth: shadow package.
	Scope *Scope           // Lyth packages only
	Files []*syntax.File
	Info  *Info
	// Globals, functions, and types in declaration order (Lyth packages).
	Globals []Symbol
	Funcs   []*FuncSym
	Types   []*TypeSym
	// Extensions are the package's extension functions by name; Go has one
	// function per name, so a name has at most one receiver type.
	Extensions map[string]*FuncSym
	// GoImports lists Go and Lyth import paths used by the package's files.
	Imports map[string]*Package
	HasMain bool
}
