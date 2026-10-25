package syntax

import "github.com/lyth-lang/lyth/internal/source"

// Node is any syntax tree node. Every node knows its source span.
type Node interface {
	Span() source.Span
}

// Expr is an expression node.
type Expr interface {
	Node
	exprNode()
}

// Stmt is a statement node.
type Stmt interface {
	Node
	stmtNode()
}

// Decl is a top-level declaration.
type Decl interface {
	Node
	declNode()
}

// TypeExpr is a type expression.
type TypeExpr interface {
	Node
	typeNode()
}

// ---------------------------------------------------------------------------
// Files and declarations

// File is a parsed source file.
type File struct {
	Src             *source.File
	Package         *Ident // nil if the package clause is omitted
	Imports         []*ImportDecl
	Decls           []Decl
	Comments        []Comment
	BuildConstraint string // a //go:build line before the package clause, if any
	// Damaged holds the statements and declarations the parser recovered
	// from after a syntax error. What it built for them is a guess, so the
	// checker reports nothing inside them.
	Damaged []source.Span
	S       source.Span
}

// ImportDecl is `import "path" [as name]`.
type ImportDecl struct {
	Path     string
	PathSpan source.Span
	Alias    *Ident // nil, or the alias (possibly "_")
	S        source.Span
}

// FunDecl is a function or method declaration.
type FunDecl struct {
	Doc        string
	TypeParams []*TypeParam
	Recv       *Receiver // nil for plain functions
	Name       *Ident
	Params     []*Param
	Result     *ResultType // nil if no result
	Throws     bool
	ThrowsSpan source.Span
	Body       *Block // nil when ExprBody is used
	ExprBody   Expr
	S          source.Span
}

// Receiver is the receiver part of `fun *User.rename(...)` or of an
// extension function, `fun <T> []T.Filter(...)`.
type Receiver struct {
	Pointer bool
	Type    TypeExpr // the receiver type, without the pointer
	// Name and TypeArgs are set when Type is a plain name in this package's
	// scope (User, Page<T>), the form a method's receiver has.
	Name     *Ident
	TypeArgs []TypeExpr
	S        source.Span
}

// TypeParam is a type parameter `T` or `T: Constraint`.
type TypeParam struct {
	Name       *Ident
	Constraint TypeExpr
	S          source.Span
}

// Param is a function parameter.
type Param struct {
	Name     *Ident
	Type     TypeExpr
	Variadic bool
	Default  Expr // `= value`; nil if the parameter is required
	S        source.Span
}

// ResultType is a function result: one type or a parenthesized tuple.
type ResultType struct {
	Types []TypeExpr
	Tuple bool
	S     source.Span
}

// StructDecl declares a struct type.
type StructDecl struct {
	Doc        string
	Name       *Ident
	TypeParams []*TypeParam
	Implements []TypeExpr // `struct T : I1, I2`: interfaces T declares it implements
	Fields     []*Field
	S          source.Span
}

// Field is a struct field.
type Field struct {
	Doc  string
	Name *Ident
	// Embedded is a type alone on its line (ADR 0043): Name is then the
	// type's name, with the span of that name in the type.
	Embedded bool
	Type     TypeExpr
	Tag      string // raw tag text without backticks
	HasTag   bool
	TagSpan  source.Span
	S        source.Span
}

// EnumDecl is `enum Color { Red, Green, Blue }`, or a string-backed
// `enum Plan: string { Free = "free" }` (ADR 0035).
type EnumDecl struct {
	Doc     string
	Name    *Ident
	Backing TypeExpr // nil for a plain enum
	Entries []*Ident
	Values  []Expr // the value of each entry, nil where none is written
	S       source.Span
}

// InterfaceDecl declares an interface type.
type InterfaceDecl struct {
	Doc        string
	Sealed     bool // sealed interface: implemented only by declared members
	Name       *Ident
	TypeParams []*TypeParam
	Methods    []*MethodSpec
	Embeds     []TypeExpr
	S          source.Span
}

// MethodSpec is a method in an interface.
type MethodSpec struct {
	Doc        string
	Name       *Ident
	Params     []*Param
	Result     *ResultType
	Throws     bool
	ThrowsSpan source.Span
	S          source.Span
}

// PropDecl is a top-level `val`/`var`.
type PropDecl struct {
	Doc     string
	Mutable bool
	Name    *Ident
	Type    TypeExpr // may be nil
	Value   Expr
	S       source.Span
}

// ConstDecl is a `const`, at package level or in a function body.
type ConstDecl struct {
	Doc   string
	Name  *Ident
	Type  TypeExpr // may be nil
	Value Expr
	S     source.Span
}

// TypeAliasDecl is `type Name = Type` (spelled `typealias Name = Type`
// before ADR 0037).
type TypeAliasDecl struct {
	Doc  string
	Name *Ident
	Type TypeExpr
	S    source.Span
}

// TypeDefDecl is `type Name[T] Underlying`, a Go defined type whose
// underlying type is not a struct or interface body (ADR 0037).
type TypeDefDecl struct {
	Doc        string
	Name       *Ident
	TypeParams []*TypeParam
	Type       TypeExpr
	S          source.Span
}

// BadDecl is a declaration that could not be parsed.
type BadDecl struct{ S source.Span }

func (d *FunDecl) Span() source.Span       { return d.S }
func (d *StructDecl) Span() source.Span    { return d.S }
func (d *InterfaceDecl) Span() source.Span { return d.S }
func (d *EnumDecl) Span() source.Span      { return d.S }
func (d *PropDecl) Span() source.Span      { return d.S }
func (d *ConstDecl) Span() source.Span     { return d.S }
func (d *BadDecl) Span() source.Span       { return d.S }
func (d *TypeAliasDecl) Span() source.Span { return d.S }
func (d *TypeDefDecl) Span() source.Span   { return d.S }
func (d *ImportDecl) Span() source.Span    { return d.S }
func (f *File) Span() source.Span          { return f.S }
func (r *Receiver) Span() source.Span      { return r.S }
func (p *Param) Span() source.Span         { return p.S }
func (t *TypeParam) Span() source.Span     { return t.S }
func (r *ResultType) Span() source.Span    { return r.S }
func (f *Field) Span() source.Span         { return f.S }
func (m *MethodSpec) Span() source.Span    { return m.S }

func (*FunDecl) declNode()       {}
func (*StructDecl) declNode()    {}
func (*InterfaceDecl) declNode() {}
func (*EnumDecl) declNode()      {}
func (*PropDecl) declNode()      {}
func (*ConstDecl) declNode()     {}
func (*ConstDecl) stmtNode()     {}
func (*BadDecl) declNode()       {}
func (*TypeAliasDecl) declNode() {}
func (*TypeDefDecl) declNode()   {}

// ---------------------------------------------------------------------------
// Type expressions

// NamedType is `Name`, `pkg.Name`, or either with type arguments.
type NamedType struct {
	Pkg  *Ident // nil if unqualified
	Name *Ident
	Args []TypeExpr
	S    source.Span
}

// PointerType is `*T`.
type PointerType struct {
	Elem TypeExpr
	S    source.Span
}

// NullableType is `T?`.
type NullableType struct {
	Elem TypeExpr
	S    source.Span
}

// SliceType is `[]T`.
type SliceType struct {
	Elem TypeExpr
	S    source.Span
}

// ArrayType is `[N]T`, or `[...]T` in a typed literal (Len is nil).
type ArrayType struct {
	Len  Expr
	Elem TypeExpr
	S    source.Span
}

// MapType is `map[K]V`.
type MapType struct {
	Key, Value TypeExpr
	S          source.Span
}

// ChanDir is the direction of a channel type.
type ChanDir int

const (
	ChanBoth    ChanDir = iota // chan T
	ChanSend                   // chan<- T
	ChanReceive                // <-chan T
)

// ChanType is `chan T`, `chan<- T`, or `<-chan T`.
type ChanType struct {
	Dir  ChanDir
	Elem TypeExpr
	S    source.Span
}

// FuncType is `(A, B) -> R [throws]`.
type FuncType struct {
	Params []TypeExpr
	Result *ResultType // nil means Unit
	Throws bool
	S      source.Span
}

// BadType is a type that could not be parsed.
type BadType struct{ S source.Span }

func (t *NamedType) Span() source.Span    { return t.S }
func (t *PointerType) Span() source.Span  { return t.S }
func (t *NullableType) Span() source.Span { return t.S }
func (t *SliceType) Span() source.Span    { return t.S }
func (t *MapType) Span() source.Span      { return t.S }
func (t *ChanType) Span() source.Span     { return t.S }
func (t *ArrayType) Span() source.Span    { return t.S }
func (t *FuncType) Span() source.Span     { return t.S }
func (t *BadType) Span() source.Span      { return t.S }

func (*NamedType) typeNode()    {}
func (*PointerType) typeNode()  {}
func (*NullableType) typeNode() {}
func (*SliceType) typeNode()    {}
func (*MapType) typeNode()      {}
func (*ChanType) typeNode()     {}
func (*ArrayType) typeNode()    {}
func (*FuncType) typeNode()     {}
func (*BadType) typeNode()      {}

// ---------------------------------------------------------------------------
// Statements

// Block is `{ stmts }`.
type Block struct {
	Stmts  []Stmt
	Lbrace source.Span
	Rbrace source.Span
	S      source.Span
}

// LocalDecl is `x := e`, `a, b := e`, `x: T = e`, or `x: T`.
type LocalDecl struct {
	Mutable     bool
	Define      bool // declared with :=, which may also assign names already declared in the same scope
	Names       []*Ident
	Destructure bool     // several names
	Type        TypeExpr // only for single-name declarations
	Value       Expr     // may be nil for `var x: T`
	S           source.Span
}

// AssignStmt is `lhs op rhs`.
type AssignStmt struct {
	Op     Kind // ASSIGN or a compound assignment
	Lhs    Expr
	Rhs    Expr
	OpSpan source.Span
	S      source.Span
}

// IncDecStmt is `x++` or `x--`.
type IncDecStmt struct {
	X   Expr
	Inc bool
	S   source.Span
}

// ExprStmt is an expression used as a statement.
type ExprStmt struct {
	X Expr
}

// ForStmt is `for (x in e)` or `for ((a, b) in e)`.
type ForStmt struct {
	Label *Ident   // `outer@ for ...`; nil if unlabeled
	Names []*Ident // one or two
	Iter  Expr
	Body  *Block
	S     source.Span
}

// WhileStmt is `while (cond) body`.
type WhileStmt struct {
	Label *Ident // `outer@ while ...`; nil if unlabeled
	Cond  Expr
	Body  *Block
	S     source.Span
}

// SelectStmt is `select { ... }`: it waits until one of its channel
// operations can proceed.
type SelectStmt struct {
	Cases []*SelectCase
	S     source.Span
}

// SelectCase is `ch.receive() -> body`, `val v = ch.receive() -> body`,
// `val (v, ok) = ch.receive() -> body`, `ch.send(x) -> body`, or
// `else -> body`.
type SelectCase struct {
	Names   []*Ident // bindings of a receive; nil if none
	Mutable bool     // var instead of val
	Op      Expr     // the channel operation; nil for else
	Else    bool
	Body    *Block
	S       source.Span
}

// DeferStmt is `defer call` or `defer { block }`.
type DeferStmt struct {
	Call Expr   // nil if Body is set
	Body *Block // nil if Call is set
	S    source.Span
}

// GoStmt is `go call` or `go { block }`.
type GoStmt struct {
	Call Expr
	Body *Block
	S    source.Span
}

// BadStmt is a statement that could not be parsed.
type BadStmt struct{ S source.Span }

func (s *Block) Span() source.Span      { return s.S }
func (s *LocalDecl) Span() source.Span  { return s.S }
func (s *AssignStmt) Span() source.Span { return s.S }
func (s *IncDecStmt) Span() source.Span { return s.S }
func (s *ExprStmt) Span() source.Span   { return s.X.Span() }
func (s *ForStmt) Span() source.Span    { return s.S }
func (s *WhileStmt) Span() source.Span  { return s.S }
func (s *SelectStmt) Span() source.Span { return s.S }
func (s *SelectCase) Span() source.Span { return s.S }
func (s *DeferStmt) Span() source.Span  { return s.S }
func (s *GoStmt) Span() source.Span     { return s.S }
func (s *BadStmt) Span() source.Span    { return s.S }

func (*Block) stmtNode()      {}
func (*LocalDecl) stmtNode()  {}
func (*AssignStmt) stmtNode() {}
func (*IncDecStmt) stmtNode() {}
func (*ExprStmt) stmtNode()   {}
func (*ForStmt) stmtNode()    {}
func (*WhileStmt) stmtNode()  {}
func (*SelectStmt) stmtNode() {}
func (*DeferStmt) stmtNode()  {}
func (*GoStmt) stmtNode()     {}
func (*BadStmt) stmtNode()    {}

// ---------------------------------------------------------------------------
// Expressions

// Ident is an identifier.
type Ident struct {
	Name string
	S    source.Span
}

// BasicLit is an INT, FLOAT, RUNE, STRING, or RAWSTRING literal.
type BasicLit struct {
	Kind  Kind
	Raw   string // source text
	Value string // decoded value for strings and runes
	S     source.Span
}

// TemplatePart is a text segment or an interpolated expression.
type TemplatePart struct {
	Text string // used when X is nil
	X    Expr
	S    source.Span
}

// TemplateLit is a string with ${...} parts.
type TemplateLit struct {
	Parts []*TemplatePart
	S     source.Span
}

// BoolLit is `true` or `false`.
type BoolLit struct {
	Value bool
	S     source.Span
}

// NullLit is `null`.
type NullLit struct{ S source.Span }

// ThisExpr is `this`.
type ThisExpr struct{ S source.Span }

// ParenExpr is `(x)`.
type ParenExpr struct {
	X Expr
	S source.Span
}

// TupleExpr is `(a, b, ...)`, valid only as either side of an assignment and
// as the value of a destructuring declaration.
type TupleExpr struct {
	Elems []Expr
	S     source.Span
}

// ListLit is `[a, b, c]`.
type ListLit struct {
	Type  TypeExpr // []T{...} or [N]T{...}; nil for [a, b]
	Elems []Expr
	S     source.Span
}

// MapLit is `[k: v, ...]`, `[:]`, or `map[K]V{k: v, ...}`.
type MapLit struct {
	Type   TypeExpr // map[K]V{...}; nil for [k: v]
	Keys   []Expr
	Values []Expr
	S      source.Span
}

// UnaryExpr is a prefix operation: - + ! ^ & * <-.
type UnaryExpr struct {
	Op     Kind
	X      Expr
	OpSpan source.Span
	S      source.Span
}

// BinaryExpr is a binary operation.
type BinaryExpr struct {
	Op     Kind
	X, Y   Expr
	OpSpan source.Span
	S      source.Span
}

// ElvisExpr is `x ?: y`.
type ElvisExpr struct {
	X, Y   Expr
	OpSpan source.Span
	S      source.Span
}

// RangeExpr is `lo..hi` or `lo..<hi`.
type RangeExpr struct {
	Lo, Hi    Expr
	Exclusive bool
	S         source.Span
}

// Arg is a call argument.
type Arg struct {
	Name   *Ident // non-nil for named arguments
	Value  Expr
	Spread bool // trailing ...
	S      source.Span
}

// CallExpr is `f(args) [trailing lambda]`.
type CallExpr struct {
	Fun      Expr
	Args     []*Arg
	Trailing *LambdaExpr
	Lparen   source.Span
	Rparen   source.Span
	S        source.Span
}

// InstantiateExpr is `Name<T, U>` in expression position.
type InstantiateExpr struct {
	X    Expr
	Args []TypeExpr
	S    source.Span
}

// SelectorExpr is `x.name` or `x?.name`.
type SelectorExpr struct {
	X      Expr
	Sel    *Ident
	Safe   bool
	OpSpan source.Span
	S      source.Span
}

// IndexExpr is `x[i]`.
type IndexExpr struct {
	X     Expr
	Index Expr
	S     source.Span
}

// SliceExpr is `x[lo:hi]`.
type SliceExpr struct {
	X      Expr
	Lo, Hi Expr // may be nil
	S      source.Span
}

// TryExpr is postfix `x?`.
type TryExpr struct {
	X      Expr
	OpSpan source.Span
	S      source.Span
}

// NotNullExpr is postfix `x!!`.
type NotNullExpr struct {
	X      Expr
	OpSpan source.Span
	S      source.Span
}

// CatchExpr is `x catch (err) { ... }`.
type CatchExpr struct {
	X         Expr
	Binder    *Ident   // may be nil
	Type      TypeExpr // `catch (e: T)`: handles only errors of type T; nil catches all
	Body      *Block
	CatchSpan source.Span
	S         source.Span
}

// IfExpr is `if (cond) then [else else]`. Branches are always blocks; a
// single-statement branch is wrapped in a synthetic block.
type IfExpr struct {
	Cond Expr
	Then *Block
	Else *Block // nil if absent; an `else if` is a block holding one IfExpr
	S    source.Span
}

// WhenCondKind classifies a when-branch condition.
type WhenCondKind int

const (
	WhenExprCond WhenCondKind = iota
	WhenIs
	WhenNotIs
	WhenIn
	WhenNotIn
)

// WhenCond is one condition of a when branch.
type WhenCond struct {
	Kind WhenCondKind
	X    Expr
	Type TypeExpr
	S    source.Span
}

// WhenBranch is `conds -> body` or `else -> body`.
type WhenBranch struct {
	Conds []*WhenCond
	Else  bool
	Body  *Block
	S     source.Span
}

// WhenExpr is a when expression or statement.
type WhenExpr struct {
	Subject  Expr   // may be nil
	Bind     *Ident // `when (val x = e)`
	Branches []*WhenBranch
	S        source.Span
}

// LambdaParam is a lambda parameter.
type LambdaParam struct {
	Name *Ident
	Type TypeExpr // may be nil
	S    source.Span
}

// LambdaExpr is `{ params -> body }`.
type LambdaExpr struct {
	Params   []*LambdaParam
	HasArrow bool
	Body     *Block
	S        source.Span
}

// JumpKind classifies jump expressions.
type JumpKind int

const (
	JumpReturn JumpKind = iota
	JumpThrow
	JumpBreak
	JumpContinue
)

// JumpExpr is `return [e, ...]`, `throw e`, `break`, or `continue`. Jumps are
// expressions of type Nothing so they can appear after `?:` and in branches.
type JumpExpr struct {
	Kind   JumpKind
	Label  *Ident // `break@outer`, `continue@outer`; nil if unlabeled
	Values []Expr
	S      source.Span
}

// AsExpr is `x as T` or `x as? T`.
type AsExpr struct {
	X    Expr
	Type TypeExpr
	Safe bool
	S    source.Span
}

// IsExpr is `x is T` or `x !is T`.
type IsExpr struct {
	X    Expr
	Type TypeExpr
	Not  bool
	S    source.Span
}

// InExpr is `x in y` or `x !in y`.
type InExpr struct {
	X, Y Expr
	Not  bool
	S    source.Span
}

// TypeOperand is a type used where an expression is expected, as in
// `make([]Int, 10)`, `[]Byte(s)`, or `new(Int)`.
type TypeOperand struct {
	Type TypeExpr
	S    source.Span
}

// EmptyStructType is Go's struct{}, the type of values that carry no data
// (chan struct{}, map[K]struct{}).
type EmptyStructType struct{ S source.Span }

// EmptyStructLit is Go's struct{}{}, the value of type struct{}.
type EmptyStructLit struct{ S source.Span }

func (t *EmptyStructType) Span() source.Span { return t.S }
func (*EmptyStructType) typeNode()           {}
func (e *EmptyStructLit) Span() source.Span  { return e.S }
func (*EmptyStructLit) exprNode()            {}

// BadExpr is an expression that could not be parsed.
type BadExpr struct{ S source.Span }

func (e *Ident) Span() source.Span           { return e.S }
func (e *BasicLit) Span() source.Span        { return e.S }
func (e *TemplateLit) Span() source.Span     { return e.S }
func (e *BoolLit) Span() source.Span         { return e.S }
func (e *NullLit) Span() source.Span         { return e.S }
func (e *ThisExpr) Span() source.Span        { return e.S }
func (e *ParenExpr) Span() source.Span       { return e.S }
func (e *TupleExpr) Span() source.Span       { return e.S }
func (e *ListLit) Span() source.Span         { return e.S }
func (e *MapLit) Span() source.Span          { return e.S }
func (e *UnaryExpr) Span() source.Span       { return e.S }
func (e *BinaryExpr) Span() source.Span      { return e.S }
func (e *ElvisExpr) Span() source.Span       { return e.S }
func (e *RangeExpr) Span() source.Span       { return e.S }
func (e *CallExpr) Span() source.Span        { return e.S }
func (e *InstantiateExpr) Span() source.Span { return e.S }
func (e *SelectorExpr) Span() source.Span    { return e.S }
func (e *IndexExpr) Span() source.Span       { return e.S }
func (e *SliceExpr) Span() source.Span       { return e.S }
func (e *TryExpr) Span() source.Span         { return e.S }
func (e *NotNullExpr) Span() source.Span     { return e.S }
func (e *CatchExpr) Span() source.Span       { return e.S }
func (e *IfExpr) Span() source.Span          { return e.S }
func (e *WhenExpr) Span() source.Span        { return e.S }
func (e *LambdaExpr) Span() source.Span      { return e.S }
func (e *JumpExpr) Span() source.Span        { return e.S }
func (e *AsExpr) Span() source.Span          { return e.S }
func (e *IsExpr) Span() source.Span          { return e.S }
func (e *InExpr) Span() source.Span          { return e.S }
func (e *TypeOperand) Span() source.Span     { return e.S }
func (e *BadExpr) Span() source.Span         { return e.S }
func (a *Arg) Span() source.Span             { return a.S }
func (p *TemplatePart) Span() source.Span    { return p.S }
func (c *WhenCond) Span() source.Span        { return c.S }
func (b *WhenBranch) Span() source.Span      { return b.S }
func (p *LambdaParam) Span() source.Span     { return p.S }

func (*Ident) exprNode()           {}
func (*TupleExpr) exprNode()       {}
func (*BasicLit) exprNode()        {}
func (*TemplateLit) exprNode()     {}
func (*BoolLit) exprNode()         {}
func (*NullLit) exprNode()         {}
func (*ThisExpr) exprNode()        {}
func (*ParenExpr) exprNode()       {}
func (*ListLit) exprNode()         {}
func (*MapLit) exprNode()          {}
func (*UnaryExpr) exprNode()       {}
func (*BinaryExpr) exprNode()      {}
func (*ElvisExpr) exprNode()       {}
func (*RangeExpr) exprNode()       {}
func (*CallExpr) exprNode()        {}
func (*InstantiateExpr) exprNode() {}
func (*SelectorExpr) exprNode()    {}
func (*IndexExpr) exprNode()       {}
func (*SliceExpr) exprNode()       {}
func (*TryExpr) exprNode()         {}
func (*NotNullExpr) exprNode()     {}
func (*CatchExpr) exprNode()       {}
func (*IfExpr) exprNode()          {}
func (*WhenExpr) exprNode()        {}
func (*LambdaExpr) exprNode()      {}
func (*JumpExpr) exprNode()        {}
func (*AsExpr) exprNode()          {}
func (*IsExpr) exprNode()          {}
func (*InExpr) exprNode()          {}
func (*TypeOperand) exprNode()     {}
func (*BadExpr) exprNode()         {}

// Unparen strips enclosing parentheses.
func Unparen(e Expr) Expr {
	for {
		p, ok := e.(*ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}
