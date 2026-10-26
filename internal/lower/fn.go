package lower

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"slices"
	"strconv"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// resultShape describes how the function being lowered returns.
type resultShape struct {
	results []types.Type // success results (throws) or all declared results
	throws  bool         // an extra error result follows the results
	rawErr  bool         // the last declared result is the error (raw tuple)
}

// fnLower lowers one function or lambda body.
type fnLower struct {
	l           *fileLowerer
	info        *check.Info
	stmts       *[]ast.Stmt
	shape       resultShape
	locals      map[*check.VarSym]string
	taken       map[string]bool // Go names that temporaries must avoid
	reserved    map[string]bool // Go names that user locals must avoid
	plain       map[string]bool // names of identifiers other than catch binders
	live        map[string]int  // Go names of the binders whose handler is being lowered
	errNames    *[]string       // names for error variables, preferred first (see errVar)
	temps       map[string]int
	declCount   map[string]int // Lyth declarations per name in the function
	recvName    string
	initializer bool
	parent      *fnLower
	casts       map[*check.VarSym]*castBinding // smart casts served by a binding, see typetest.go
	breaks      *[]*breakCtx                   // enclosing loops and switches, innermost last
	labels      map[string]bool                // Go labels used in this function
	pre         map[syntax.Expr]ast.Expr       // operands already evaluated into temporaries (deferred calls)
	// assignedFields names the fields the function assigns (x.f = ...), so
	// that reading resp.Body at a defer and at exit is known to agree.
	assignedFields map[string]bool
	// deferErr is the Go name of the function's error result when deferred
	// code may set it (defer f()?, throw in defer { }); inDefer marks the
	// lowerer of a deferred block, where ? and throw set it.
	deferErr string
	inDefer  bool
}

// breakCtx is an enclosing Go statement that an unlabeled break exits.
type breakCtx struct {
	loop  bool
	name  string     // the loop's Lyth label, if any
	label *ast.Ident // a loop's label, created when a break inside a switch must exit the loop
}

func newFnLower(l *fileLowerer, sig *types.Signature, lambda bool) *fnLower {
	fl := &fnLower{l: l, info: l.info, locals: map[*check.VarSym]string{}, taken: map[string]bool{},
		reserved: map[string]bool{}, plain: map[string]bool{}, live: map[string]int{}, errNames: &[]string{},
		temps: map[string]int{}, declCount: map[string]int{},
		casts: map[*check.VarSym]*castBinding{}, breaks: &[]*breakCtx{}, labels: map[string]bool{},
		pre: map[syntax.Expr]ast.Expr{}, assignedFields: map[string]bool{}}
	fl.setShape(sig)
	for name := range l.importNames {
		fl.reserved[name] = true
		fl.taken[name] = true
	}
	for name := range supportPkgs {
		fl.reserved[name] = true
		fl.taken[name] = true
	}
	return fl
}

func (fl *fnLower) setShape(sig *types.Signature) {
	fl.shape = resultShape{results: sig.Results, throws: sig.Throws}
	if !sig.Throws {
		if n := len(sig.Results); n > 1 && types.IsErrorType(sig.Results[n-1]) {
			fl.shape.rawErr = true
		}
	}
}

// child creates a lowerer for a nested lambda that shares naming state.
func (fl *fnLower) child(sig *types.Signature) *fnLower {
	c := &fnLower{l: fl.l, info: fl.info, locals: fl.locals, taken: fl.taken, reserved: fl.reserved,
		plain: fl.plain, live: fl.live, errNames: fl.errNames,
		temps: fl.temps, declCount: fl.declCount, recvName: fl.recvName, parent: fl, casts: fl.casts,
		breaks: &[]*breakCtx{}, labels: map[string]bool{}, pre: fl.pre, assignedFields: fl.assignedFields}
	c.setShape(sig)
	return c
}

// collectNames records every identifier used in a declaration so generated
// temporaries never capture or shadow them, and the package-level names
// the body refers to so user locals never shadow those.
func (fl *fnLower) collectNames(n syntax.Node) {
	syntax.Walk(n, func(x syntax.Node) {
		switch x := x.(type) {
		case *syntax.AssignStmt:
			targets := []syntax.Expr{x.Lhs}
			if tup, ok := syntax.Unparen(x.Lhs).(*syntax.TupleExpr); ok {
				targets = tup.Elems // a.Body, n = x, 1
			}
			for _, t := range targets {
				if sel, ok := syntax.Unparen(t).(*syntax.SelectorExpr); ok {
					fl.assignedFields[sel.Sel.Name] = true
				}
			}
		case *syntax.Ident:
			fl.taken[x.Name] = true
			if !fl.binderIdent(x) {
				fl.plain[x.Name] = true
			}
			if _, ok := fl.info.Defs[x]; ok {
				fl.declCount[x.Name]++
			}
			switch s := fl.info.Uses[x].(type) {
			case *check.FuncSym, *check.ConstSym, *check.TypeSym:
				fl.reserved[goNameOf(s)] = true
			case *check.VarSym:
				if s.Kind == check.GlobalVar {
					fl.reserved[s.GoName] = true
				}
			}
		case *syntax.WhenExpr:
			for _, br := range x.Branches {
				for _, c := range br.Conds {
					if t := fl.info.TypeTests[c]; t != nil {
						collectTypeNames(t, fl.reserved)
					}
				}
			}
		case *syntax.CallExpr:
			// Default values expand at this call site: locals must not
			// shadow the names they use.
			if ci := fl.info.Calls[x]; ci != nil {
				for _, d := range ci.Defaults {
					fl.reserveDefaultNames(d)
				}
			}
		}
		if e, ok := x.(syntax.Expr); ok {
			if t := fl.info.TypeOf(e); t != nil {
				collectTypeNames(t, fl.reserved)
			}
			if t := fl.info.TypeTests[e]; t != nil {
				collectTypeNames(t, fl.reserved)
			}
		}
	})
}

// reserveDefaultNames reserves the package-level names and package
// qualifiers a default value refers to.
func (fl *fnLower) reserveDefaultNames(d check.DefaultArg) {
	syntax.Walk(d.Expr, func(n syntax.Node) {
		switch x := n.(type) {
		case *syntax.Ident:
			switch s := d.Info.Uses[x].(type) {
			case *check.FuncSym, *check.ConstSym, *check.TypeSym:
				fl.reserved[goNameOf(s)] = true
			case *check.VarSym:
				if s.Kind == check.GlobalVar {
					fl.reserved[s.GoName] = true
				}
			case *check.PkgSym:
				if s.Pkg != nil {
					fl.reserved[s.Pkg.Name] = true
				}
			}
		case syntax.Expr:
			if t := d.Info.TypeOf(x); t != nil {
				collectTypeNames(t, fl.reserved)
			}
		}
	})
}

// collectTypeNames adds the Go names of Lyth-declared types inside t.
func collectTypeNames(t types.Type, out map[string]bool) {
	switch t := t.(type) {
	case *types.Named:
		if t.Decl != nil {
			out[t.Decl.GoName] = true
		}
		for _, a := range t.Args {
			collectTypeNames(a, out)
		}
	case *types.Nullable:
		collectTypeNames(t.Elem, out)
	case *types.Platform:
		collectTypeNames(t.Elem, out)
	case *types.Pointer:
		collectTypeNames(t.Elem, out)
	case *types.Slice:
		collectTypeNames(t.Elem, out)
	case *types.Array:
		collectTypeNames(t.Elem, out)
	case *types.Map:
		collectTypeNames(t.Key, out)
		collectTypeNames(t.Elem, out)
	case *types.Chan:
		collectTypeNames(t.Elem, out)
	case *types.Signature:
		for _, p := range t.Params {
			collectTypeNames(p, out)
		}
		for _, r := range t.Results {
			collectTypeNames(r, out)
		}
	case *types.Result:
		for _, r := range t.Raw {
			collectTypeNames(r, out)
		}
	case *types.Tuple:
		for _, e := range t.Elems {
			collectTypeNames(e, out)
		}
	}
}

// declareLocal assigns a Go name to a Lyth local.
func (fl *fnLower) declareLocal(v *check.VarSym) string {
	if v == nil {
		return "_"
	}
	if name, ok := fl.locals[v]; ok {
		return name
	}
	if v.Rebinds != nil {
		// ctx := f(ctx) (ADR 0039) keeps the parameter's name, as Go's
		// ctx, cancel := ... does, unless a closure created before it
		// captured the parameter: then Go's line would change what the
		// closure sees, so the local gets a name of its own.
		name := fl.declareLocal(v.Rebinds)
		if v.FreshName {
			fl.taken[name] = true
			name = fl.alloc(name)
		}
		fl.locals[v] = name
		return name
	}
	name := v.Name()
	if name == "_" {
		return "_"
	}
	if fl.reserved[name] || check.IsGoReserved(name) {
		base := name + "_"
		name = base
		for i := 2; fl.taken[name] || fl.reserved[name]; i++ {
			name = fmt.Sprintf("%s%d", base, i)
		}
		fl.taken[name] = true
	}
	fl.locals[v] = name
	return name
}

// temp allocates a fresh temporary name. A base that names something the
// function refers to (a function, a type) falls back to "v" so temporaries
// never read like calls, and so does "err", which names error variables
// (errVar).
func (fl *fnLower) temp(base string) string {
	if base == "" || base == "err" || check.IsGoReserved(base) || fl.reserved[base] {
		base = "v"
	}
	return fl.alloc(base)
}

// alloc returns base, or base with the first free numeric suffix, and
// marks the name taken.
func (fl *fnLower) alloc(base string) string {
	name := base
	for fl.taken[name] || fl.reserved[name] {
		fl.temps[base]++
		name = fmt.Sprintf("%s%d", base, fl.temps[base]+1)
	}
	fl.taken[name] = true
	return name
}

// binderIdent reports whether id declares or refers to a catch binder.
func (fl *fnLower) binderIdent(id *syntax.Ident) bool {
	s := fl.info.Defs[id]
	if s == nil {
		s = fl.info.Uses[id]
	}
	v, ok := s.(*check.VarSym)
	return ok && v.Kind == check.CatchVar
}

// errVar returns the name for the error variable of a ? or catch expansion:
// "err", as in hand-written Go, unless an identifier other than a catch
// binder uses that name, or the binder of a handler being lowered has it;
// then the first free alternative (err2, ...). Expansions share the name,
// and so do binders named err (see catchBinder): each assignment is checked
// right away, and a later := in the same block redeclares the variable.
func (fl *fnLower) errVar() string {
	names := fl.errNames
	for i := 0; ; i++ {
		if i == len(*names) {
			if i == 0 && !fl.plain["err"] && !fl.reserved["err"] {
				*names = append(*names, "err")
				fl.taken["err"] = true
			} else {
				*names = append(*names, fl.alloc("err"))
			}
		}
		if name := (*names)[i]; fl.live[name] == 0 {
			return name
		}
	}
}

func (fl *fnLower) emit(s ast.Stmt, span source.Span) {
	fl.l.mark(s, span)
	setPos(s, fl.l.pos(span))
	*fl.stmts = append(*fl.stmts, s)
}

func (fl *fnLower) emitSynth(s ast.Stmt, span source.Span) {
	markTree(fl.l, s, span)
	setPos(s, fl.l.pos(span))
	*fl.stmts = append(*fl.stmts, s)
}

// emitSynthShallow emits glue whose own node (and condition) is synthetic
// while nested user statements keep their exact attribution.
func (fl *fnLower) emitSynthShallow(s ast.Stmt, span source.Span) {
	fl.l.markSynth(s, span)
	if ifs, ok := s.(*ast.IfStmt); ok {
		markTree(fl.l, ifs.Cond, span)
	}
	setPos(s, fl.l.pos(span))
	*fl.stmts = append(*fl.stmts, s)
}

// markTree marks a synthetic subtree with span, keeping existing marks.
func markTree(l *fileLowerer, n ast.Node, span source.Span) {
	ast.Inspect(n, func(x ast.Node) bool {
		if x != nil {
			l.markSynth(x, span)
		}
		return true
	})
}

// sub returns a lowerer writing into a fresh statement list.
func (fl *fnLower) sub() (*fnLower, *[]ast.Stmt) {
	var stmts []ast.Stmt
	c := *fl
	c.stmts = &stmts
	return &c, &stmts
}

// ---------------------------------------------------------------------------
// Bodies

func (fl *fnLower) funcBody(d *syntax.FunDecl, fi *check.FuncInfo) *ast.BlockStmt {
	var stmts []ast.Stmt
	fl.stmts = &stmts
	if d.Body != nil {
		fl.castScope(func() {
			for _, s := range d.Body.Stmts {
				fl.stmt(s)
			}
		})
	} else if d.ExprBody != nil {
		if len(fi.Sym.Sig.Results) == 0 && !fl.info.PassThrough[d.ExprBody] {
			fl.exprStmt(d.ExprBody)
		} else {
			fl.returnStmt([]syntax.Expr{d.ExprBody}, d.ExprBody.Span())
		}
	}
	end := d.S
	if d.Body != nil {
		end = d.Body.Rbrace
	}
	fl.finish(&stmts, end)
	b := &ast.BlockStmt{List: stmts}
	if d.Body != nil {
		b.Lbrace, b.Rbrace = fl.l.pos(d.Body.Lbrace), fl.l.pos(d.Body.Rbrace)
	}
	return b
}

// finish appends the implicit success return of throwing Unit functions and
// a safety panic where Go cannot see that a body terminates.
func (fl *fnLower) finish(stmts *[]ast.Stmt, span source.Span) {
	if goTerminates(*stmts) {
		return
	}
	switch {
	case fl.shape.throws && len(fl.shape.results) == 0:
		r := &ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("nil")}}
		markTree(fl.l, r, span)
		*stmts = append(*stmts, r)
	case len(fl.shape.results) > 0:
		p := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{strLit("unreachable")}}}
		markTree(fl.l, p, span)
		*stmts = append(*stmts, p)
	}
}

// goTerminates reports whether a statement list ends in a terminating
// statement as defined by the Go specification.
func goTerminates(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	return terminating(stmts[len(stmts)-1])
}

func terminating(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.GOTO || s.Tok == token.FALLTHROUGH
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	case *ast.BlockStmt:
		return goTerminates(s.List)
	case *ast.IfStmt:
		if s.Else == nil {
			return false
		}
		return goTerminates(s.Body.List) && terminating(s.Else)
	case *ast.ForStmt:
		return s.Cond == nil && !hasBreak(s.Body)
	case *ast.LabeledStmt:
		if f, ok := s.Stmt.(*ast.ForStmt); ok {
			return f.Cond == nil && !hasBreak(f.Body) && !hasLabeledBreak(f.Body, s.Label.Name)
		}
		return terminating(s.Stmt)
	case *ast.SwitchStmt:
		return switchTerminates(s.Body)
	case *ast.TypeSwitchStmt:
		return switchTerminates(s.Body)
	case *ast.SelectStmt:
		// Go: every clause, the default included, ends in a terminating
		// statement and none breaks out of the select.
		for _, cc := range s.Body.List {
			body := cc.(*ast.CommClause).Body
			if !goTerminates(body) || hasBreak(&ast.BlockStmt{List: body}) {
				return false
			}
		}
		return true
	}
	return false
}

func switchTerminates(body *ast.BlockStmt) bool {
	hasDefault := false
	for _, cc := range body.List {
		clause := cc.(*ast.CaseClause)
		if clause.List == nil {
			hasDefault = true
		}
		if !goTerminates(clause.Body) || hasBreakList(clause.Body) {
			return false
		}
	}
	return hasDefault
}

func hasBreakList(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		if hasBreak(s) {
			return true
		}
	}
	return false
}

// hasLabeledBreak reports whether n contains `break label`.
func hasLabeledBreak(n ast.Node, label string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.BranchStmt:
			if x.Tok == token.BREAK && x.Label != nil && x.Label.Name == label {
				found = true
			}
		case *ast.FuncLit:
			return false
		}
		return !found
	})
	return found
}

// hasBreak reports whether n contains an unlabeled break that targets the
// enclosing statement (not nested loops or switches).
func hasBreak(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.BranchStmt:
			if x.Tok == token.BREAK && x.Label == nil {
				found = true
			}
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
			return x == n
		}
		return !found
	})
	return found
}

// ---------------------------------------------------------------------------
// Statements

func (fl *fnLower) block(b *syntax.Block) *ast.BlockStmt {
	sub, stmts := fl.sub()
	fl.castScope(func() {
		for _, s := range b.Stmts {
			sub.stmt(s)
		}
	})
	blk := &ast.BlockStmt{List: *stmts, Lbrace: fl.l.pos(b.Lbrace), Rbrace: fl.l.pos(b.Rbrace)}
	if !b.Lbrace.IsValid() {
		blk.Lbrace, blk.Rbrace = fl.l.pos(b.S), fl.l.endPos(b.S)
	}
	fl.l.mark(blk, b.S)
	return blk
}

func (fl *fnLower) stmt(s syntax.Stmt) {
	switch s := s.(type) {
	case *syntax.LocalDecl:
		fl.localDecl(s)
	case *syntax.ConstDecl:
		fl.localConst(s)
	case *syntax.StructDecl:
		fl.localStruct(s)
	case *syntax.AssignStmt:
		fl.assignStmt(s)
	case *syntax.IncDecStmt:
		x := fl.expr(s.X)
		tok := token.INC
		if !s.Inc {
			tok = token.DEC
		}
		fl.emit(&ast.IncDecStmt{X: x, Tok: tok, TokPos: fl.l.pos(s.S)}, s.S)
	case *syntax.ExprStmt:
		fl.exprStmt(s.X)
	case *syntax.Block:
		b := fl.block(s)
		fl.emit(b, s.S)
	case *syntax.ForStmt:
		fl.forStmt(s)
	case *syntax.WhileStmt:
		fl.whileStmt(s)
	case *syntax.SelectStmt:
		fl.selectStmt(s)
	case *syntax.DeferStmt:
		fl.deferGo(s.Call, s.Body, false, s.S)
	case *syntax.GoStmt:
		fl.deferGo(s.Call, s.Body, true, s.S)
	}
}

func (fl *fnLower) localDecl(s *syntax.LocalDecl) {
	vars := fl.info.Locals[s]
	if s.Destructure {
		fl.destructure(s, vars)
		return
	}
	if len(vars) == 0 {
		return
	}
	v := vars[0]
	if s.Value == nil {
		name := fl.declareLocal(v)
		spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(v.Type)}
		fl.emit(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, TokPos: fl.l.pos(s.S), Specs: []ast.Spec{spec}}}, s.S)
		fl.useIfUnused(v, name, s.S)
		return
	}
	value := syntax.Unparen(s.Value)
	if v.Rebinds != nil && v.InPlace && !v.FreshName {
		// name := f(name) in the function's outermost block: Go has no new
		// variable to declare there, so the parameter is assigned.
		name := fl.declareLocal(v)
		fl.exprInto(s.Value, ast.NewIdent(name), v.Type)
		fl.useIfUnused(v, name, s.S)
		return
	}
	if fl.typeTestDecl(s, v, value) {
		return
	}
	conv := fl.info.Implicit[s.Value]
	if conv == check.ConvNilIface {
		// val e: Error? = p declares e and assigns p unless it is null.
		src := fl.stash(s.Value, fl.exprRaw(s.Value))
		name := fl.declareLocal(v)
		fl.nilIfaceInto(s.Value, src, name)
		fl.useIfUnused(v, name, s.S)
		return
	}
	if conv == check.ConvNone && fl.intoTargetForm(value) {
		name := fl.declareLocal(v)
		if try, ok := value.(*syntax.TryExpr); ok && fl.simpleTry(try) {
			fl.tryInto(try, []string{name}, true, s.S)
			fl.useIfUnused(v, name, s.S)
			return
		}
		if c, ok := value.(*syntax.CatchExpr); ok && fl.simpleCatch(c) {
			fl.catchInto(c, name, true, s.S)
			fl.useIfUnused(v, name, s.S)
			return
		}
		spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(v.Type)}
		fl.emit(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, TokPos: fl.l.pos(s.S), Specs: []ast.Spec{spec}}}, s.S)
		fl.exprInto(value, ast.NewIdent(name), v.Type)
		fl.useIfUnused(v, name, s.S)
		return
	}
	x := fl.exprAs(s.Value, v.Type)
	name := fl.declareLocal(v)
	if needsExplicitType(fl.info, s.Value, v.Type) {
		spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(v.Type), Values: []ast.Expr{x}}
		if _, isNull := value.(*syntax.NullLit); isNull {
			spec.Values = nil // var user *User, not var user *User = nil
		}
		fl.emit(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, TokPos: fl.l.pos(s.S), Specs: []ast.Spec{spec}}}, s.S)
	} else {
		fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, TokPos: fl.l.pos(s.S), Rhs: []ast.Expr{x}}, s.S)
	}
	fl.useIfUnused(v, name, s.S)
}

// intoTargetForm reports whether an initializer is lowered by assigning into
// the declared variable from inside generated control flow.
func (fl *fnLower) intoTargetForm(e syntax.Expr) bool {
	switch e := e.(type) {
	case *syntax.IfExpr, *syntax.WhenExpr:
		return true
	case *syntax.ElvisExpr:
		return true
	case *syntax.TryExpr:
		return fl.simpleTry(e)
	case *syntax.CatchExpr:
		return true
	}
	return false
}

func (fl *fnLower) useIfUnused(v *check.VarSym, name string, span source.Span) {
	if fl.info.Unused[v] && name != "_" {
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_")}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(name)}}, span)
	}
}

func (fl *fnLower) destructure(s *syntax.LocalDecl, vars []*check.VarSym) {
	value := syntax.Unparen(s.Value)
	// a, b := f(a, b), all replacing parameters in the outermost block, has
	// no new variable for Go's := (ADR 0039): it assigns.
	define := false
	for _, v := range vars {
		if v != nil && v.Name() != "_" && !(v.Rebinds != nil && v.InPlace && !v.FreshName) {
			define = true
		}
	}
	if try, ok := value.(*syntax.TryExpr); ok && fl.simpleTry(try) {
		names := make([]string, len(vars))
		for i, v := range vars {
			names[i] = fl.declareLocal(v)
		}
		fl.tryInto(try, names, define, s.S)
		for i, v := range vars {
			if v != nil {
				fl.useIfUnused(v, names[i], s.S)
			}
		}
		return
	}
	if c, ok := value.(*syntax.CatchExpr); ok && fl.simpleCatch(c) {
		// a, b := call() catch { ... }
		names := make([]string, len(vars))
		for i, v := range vars {
			names[i] = fl.declareLocal(v)
		}
		fl.catchIntoNames(c, names, define, s.S)
		for i, v := range vars {
			if v != nil {
				fl.useIfUnused(v, names[i], s.S)
			}
		}
		return
	}
	var rhs []ast.Expr
	if tup, ok := value.(*syntax.TupleExpr); ok {
		rhs = fl.exprList(tup.Elems, nil) // a, b := x, y
	} else {
		rhs = []ast.Expr{fl.expr(s.Value)}
	}
	var lhs []ast.Expr
	allBlank := true
	for _, v := range vars {
		name := fl.declareLocal(v)
		if name != "_" {
			allBlank = false
		}
		lhs = append(lhs, ast.NewIdent(name))
	}
	tok := token.DEFINE
	if allBlank || !define {
		tok = token.ASSIGN
	}
	fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: tok, TokPos: fl.l.pos(s.S), Rhs: rhs}, s.S)
	for i, v := range vars {
		if v != nil {
			fl.useIfUnused(v, lhs[i].(*ast.Ident).Name, s.S)
		}
	}
}

func (fl *fnLower) assignStmt(s *syntax.AssignStmt) {
	if lhs, ok := syntax.Unparen(s.Lhs).(*syntax.TupleExpr); ok {
		fl.tupleAssign(s, lhs)
		return
	}
	if id, ok := syntax.Unparen(s.Lhs).(*syntax.Ident); ok && id.Name == "_" {
		x := fl.expr(s.Rhs)
		lhs := []ast.Expr{ast.NewIdent("_")}
		if r, ok := fl.info.TypeOf(s.Rhs).(*types.Result); ok {
			lhs = nil
			for range r.Raw {
				lhs = append(lhs, ast.NewIdent("_"))
			}
		}
		fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, TokPos: fl.l.pos(s.S), Rhs: []ast.Expr{x}}, s.S)
		return
	}
	exprs := fl.exprList([]syntax.Expr{s.Lhs, s.Rhs}, []bool{true, false})
	tok := assignToken(s.Op)
	rhs := exprs[1]
	if s.Op == syntax.ASSIGN {
		rhs = fl.convertTo(s.Rhs, rhs)
	}
	fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{exprs[0]}, Tok: tok, TokPos: fl.l.pos(s.S), Rhs: []ast.Expr{rhs}}, s.S)
}

// tupleAssign lowers (a, b) = (x, y) to Go's a, b = x, y and (a, b) = f()
// to a, b = f(); Go evaluates every operand before assigning, like Lyth.
func (fl *fnLower) tupleAssign(s *syntax.AssignStmt, lhs *syntax.TupleExpr) {
	if fl.multiFallibleExpr(s.Rhs, len(lhs.Elems)) {
		// a, b = call()? or a, b = call() catch { ... }: bind the success
		// values to fresh variables, then assign them as Go would. The
		// calls inside the targets run first (xs[i()], ys[j()] = f()?).
		xs := fl.exprList(lhs.Elems, trues(len(lhs.Elems)))
		var spills []ast.Stmt
		for i, el := range lhs.Elems {
			xs[i] = fl.spillTarget(el, xs[i], &spills)
		}
		*fl.stmts = append(*fl.stmts, spills...)
		names := fl.multiFallible(s.Rhs, len(lhs.Elems))
		var dst, src []ast.Expr
		for i, n := range names {
			if id, ok := xs[i].(*ast.Ident); ok && id.Name == "_" {
				continue
			}
			dst, src = append(dst, xs[i]), append(src, ast.NewIdent(n))
		}
		if len(dst) > 0 {
			fl.emit(&ast.AssignStmt{Lhs: dst, Tok: token.ASSIGN, TokPos: fl.l.pos(s.S), Rhs: src}, s.S)
		}
		return
	}
	var ops []syntax.Expr
	var lvalue []bool
	for _, el := range lhs.Elems {
		ops = append(ops, el)
		lvalue = append(lvalue, true)
	}
	rhs, isTuple := syntax.Unparen(s.Rhs).(*syntax.TupleExpr)
	if isTuple {
		ops = append(ops, rhs.Elems...)
	} else {
		ops = append(ops, s.Rhs)
	}
	for len(lvalue) < len(ops) {
		lvalue = append(lvalue, false)
	}
	xs := fl.exprList(ops, lvalue)
	n := len(lhs.Elems)
	right := xs[n:]
	if isTuple {
		for i, el := range rhs.Elems {
			right[i] = fl.convertTo(el, right[i])
		}
	}
	fl.emit(&ast.AssignStmt{Lhs: xs[:n], Tok: token.ASSIGN, TokPos: fl.l.pos(s.S), Rhs: right}, s.S)
}

func assignToken(k syntax.Kind) token.Token {
	switch k {
	case syntax.ADD_ASSIGN:
		return token.ADD_ASSIGN
	case syntax.SUB_ASSIGN:
		return token.SUB_ASSIGN
	case syntax.MUL_ASSIGN:
		return token.MUL_ASSIGN
	case syntax.QUO_ASSIGN:
		return token.QUO_ASSIGN
	case syntax.REM_ASSIGN:
		return token.REM_ASSIGN
	case syntax.AND_ASSIGN:
		return token.AND_ASSIGN
	case syntax.OR_ASSIGN:
		return token.OR_ASSIGN
	case syntax.XOR_ASSIGN:
		return token.XOR_ASSIGN
	case syntax.SHL_ASSIGN:
		return token.SHL_ASSIGN
	case syntax.SHR_ASSIGN:
		return token.SHR_ASSIGN
	case syntax.AND_NOT_ASSIGN:
		return token.AND_NOT_ASSIGN
	}
	return token.ASSIGN
}

// exprStmt lowers an expression used as a statement.
func (fl *fnLower) exprStmt(e syntax.Expr) {
	switch x := e.(type) {
	case *syntax.JumpExpr:
		fl.jump(x)
		return
	case *syntax.IfExpr:
		fl.ifStmt(x, nil, nil)
		return
	case *syntax.WhenExpr:
		fl.whenStmt(x, nil, nil)
		return
	case *syntax.TryExpr:
		fl.tryInto(x, nil, false, x.S)
		return
	case *syntax.CatchExpr:
		fl.catchInto(x, "", false, x.S)
		return
	case *syntax.ElvisExpr:
		// `x ?: return ...` as a statement: an early exit.
		if xt := fl.info.TypeOf(x.X); xt != nil && !types.MayBeNull(xt) && !types.IsNilable(xt) && !isCollection(xt) {
			fl.exprStmt(x.X) // never null (the checker warned)
			return
		}
		v := fl.expr(x.X)
		sub, stmts := fl.sub()
		sub.exprStmt(x.Y)
		cond := &ast.BinaryExpr{X: v, Op: token.EQL, Y: ast.NewIdent("nil")}
		fl.emit(&ast.IfStmt{If: fl.l.pos(x.S), Cond: cond, Body: &ast.BlockStmt{List: *stmts}}, x.S)
		return
	case *syntax.ParenExpr:
		fl.exprStmt(x.X)
		return
	}
	g := fl.expr(e)
	switch g := g.(type) {
	case *ast.CallExpr:
		fl.emit(&ast.ExprStmt{X: g}, e.Span())
	case *ast.UnaryExpr:
		if g.Op == token.ARROW {
			fl.emit(&ast.ExprStmt{X: g}, e.Span()) // <-ch
			break
		}
		fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_")}, Tok: token.ASSIGN, Rhs: []ast.Expr{g}}, e.Span())
	case *ast.Ident:
		// A value already materialized into a temporary (e.g. a safe call
		// whose result is unused): nothing to evaluate.
	default:
		fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_")}, Tok: token.ASSIGN, Rhs: []ast.Expr{g}}, e.Span())
	}
}

func (fl *fnLower) jump(j *syntax.JumpExpr) {
	switch j.Kind {
	case syntax.JumpReturn:
		fl.returnStmt(j.Values, j.S)
	case syntax.JumpThrow:
		err := fl.expr(j.Values[0])
		for _, s := range fl.failStmts(err) {
			if fl.inDefer {
				// err = e is the throw's own line
				ast.Inspect(s, func(n ast.Node) bool {
					fl.l.mark(n, j.S)
					return true
				})
			}
			fl.emit(s, j.S)
		}
	case syntax.JumpBreak, syntax.JumpContinue:
		tok := token.BREAK
		if j.Kind == syntax.JumpContinue {
			tok = token.CONTINUE
		}
		br := &ast.BranchStmt{Tok: tok, TokPos: fl.l.pos(j.S)}
		if label := fl.jumpLabel(tok, j.Label); label != nil {
			br.Label = ast.NewIdent(label.Name)
		}
		fl.emit(br, j.S)
	}
}

// jumpLabel returns the Go label a break or continue needs, or nil when the
// plain statement already targets the right loop. Go's break inside a
// switch exits the switch, while Lyth's break always exits a loop; and
// break@outer / continue@outer name a loop explicitly.
func (fl *fnLower) jumpLabel(tok token.Token, name *syntax.Ident) *ast.Ident {
	stack := *fl.breaks
	target := -1
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].loop && (name == nil || stack[i].name == name.Name) {
			target = i
			break
		}
	}
	if target < 0 {
		return nil
	}
	// The plain statement suffices when nothing it would stop at lies in
	// between: for break, any loop or switch; for continue, any loop.
	plain := true
	for _, ctx := range stack[target+1:] {
		if tok == token.BREAK || ctx.loop {
			plain = false
		}
	}
	if plain {
		return nil
	}
	return fl.labelFor(stack[target])
}

// labelFor returns the Go label of a loop, allocating one unique in the
// function (the Lyth label's name, or "loop").
func (fl *fnLower) labelFor(ctx *breakCtx) *ast.Ident {
	if ctx.label == nil {
		base := ctx.name
		if base == "" {
			base = "loop"
		} else if check.IsGoReserved(base) {
			base += "_"
		}
		name := base
		for i := 2; fl.labels[name]; i++ {
			name = fmt.Sprintf("%s%d", base, i)
		}
		fl.labels[name] = true
		ctx.label = ast.NewIdent(name)
	}
	return ctx.label
}

// breakable lowers the body of a loop or switch with ctx on the break stack.
func (fl *fnLower) breakable(ctx *breakCtx, f func()) {
	*fl.breaks = append(*fl.breaks, ctx)
	f()
	*fl.breaks = (*fl.breaks)[:len(*fl.breaks)-1]
}

// loopBody lowers a loop body; emitLoop then labels the loop if needed.
func (fl *fnLower) loopBody(label *syntax.Ident, b *syntax.Block) (*ast.BlockStmt, *breakCtx) {
	ctx := &breakCtx{loop: true}
	if label != nil {
		ctx.name = label.Name
	}
	var body *ast.BlockStmt
	fl.breakable(ctx, func() { body = fl.block(b) })
	return body, ctx
}

func (fl *fnLower) emitLoop(loop ast.Stmt, ctx *breakCtx, span source.Span) {
	if ctx.label == nil {
		fl.emit(loop, span)
		return
	}
	fl.l.mark(loop, span)
	setPos(loop, fl.l.pos(span))
	fl.emit(&ast.LabeledStmt{Label: ctx.label, Stmt: loop}, span)
}

// failResults returns the Go results for returning err from the current
// function: zero values for the other results, then err.
// failStmts returns the statements that fail the function with err: a
// return, or in a deferred block, the assignment of the function's named
// error result while it is nil (the first error wins), then a return from
// the deferred function.
func (fl *fnLower) failStmts(err ast.Expr) []ast.Stmt {
	if !fl.inDefer {
		return []ast.Stmt{&ast.ReturnStmt{Results: fl.failResults(err)}}
	}
	set := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(fl.deferErr)}, Tok: token.ASSIGN, Rhs: []ast.Expr{err}}
	return []ast.Stmt{
		&ast.IfStmt{Cond: nilCmp(ast.NewIdent(fl.deferErr), token.EQL), Body: &ast.BlockStmt{List: []ast.Stmt{set}}},
		&ast.ReturnStmt{},
	}
}

func (fl *fnLower) failResults(err ast.Expr) []ast.Expr {
	var out []ast.Expr
	results := fl.shape.results
	if fl.shape.rawErr {
		results = results[:len(results)-1]
	}
	for _, r := range results {
		out = append(out, fl.l.zeroValue(r))
	}
	return append(out, err)
}

func (fl *fnLower) returnStmt(values []syntax.Expr, span source.Span) {
	if len(values) == 1 && fl.info.PassThrough[values[0]] {
		// return f(): the call's results, error included, as they are.
		fl.emit(&ast.ReturnStmt{Return: fl.l.pos(span), Results: []ast.Expr{fl.expr(values[0])}}, span)
		return
	}
	if len(values) == 1 && len(fl.shape.results) == 1 && fl.info.Implicit[values[0]] == check.ConvNilIface {
		// return p for an interface result: null is a nil interface.
		//
		//	if p == nil {
		//		return nil
		//	}
		//	return p
		v := values[0]
		src := fl.stash(v, fl.exprRaw(v))
		fl.emitSynth(&ast.IfStmt{Cond: nilCmp(cloneIdentExpr(src), token.EQL),
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: fl.successResults(ast.NewIdent("nil"))}}}}, v.Span())
		val := cloneIdentExpr(src)
		if optionalValue(fl.info.TypeOf(v)) {
			val = &ast.StarExpr{X: val}
		}
		fl.emit(&ast.ReturnStmt{Return: fl.l.pos(span), Results: fl.successResults(val)}, span)
		return
	}
	if len(values) == 1 && len(fl.shape.results) == 1 {
		// return if/when: each branch returns its value.
		switch x := syntax.Unparen(values[0]).(type) {
		case *syntax.IfExpr, *syntax.WhenExpr:
			if fl.info.UsedAsValue[x] {
				fl.exprInto(x, returnTarget, fl.shape.results[0])
				return
			}
		}
	}
	var results []ast.Expr
	if len(values) > 0 {
		exprs := fl.exprList(values, nil)
		for i, v := range values {
			var want types.Type
			if i < len(fl.shape.results) {
				want = fl.shape.results[i]
			}
			results = append(results, fl.convertTo(v, fl.adaptConst(v, exprs[i], want)))
		}
	}
	if fl.shape.throws {
		results = append(results, ast.NewIdent("nil"))
	}
	fl.emit(&ast.ReturnStmt{Return: fl.l.pos(span), Results: results}, span)
}

// successResults returns the results of a successful return of v, with the
// nil error of a throws function.
func (fl *fnLower) successResults(v ast.Expr) []ast.Expr {
	if fl.shape.throws {
		return []ast.Expr{v, ast.NewIdent("nil")}
	}
	return []ast.Expr{v}
}

// ---------------------------------------------------------------------------
// if / when as statements and into targets

// ifStmt lowers an if. When target is non-nil, each branch assigns its value
// to target instead of discarding it.
func (fl *fnLower) ifStmt(e *syntax.IfExpr, target ast.Expr, targetType types.Type) {
	if is, ok := syntax.Unparen(e.Cond).(*syntax.IsExpr); ok && fl.info.TypeTests[is] != nil {
		fl.ifTypeTest(e, is, target, targetType)
		return
	}
	stmt := &ast.IfStmt{If: fl.l.pos(e.S)}
	if ok := fl.ifMapTest(stmt, e.Cond); ok != "" {
		stmt.Body = fl.branch(e.Then, target, targetType)
		fl.release(ok)
	} else {
		stmt.Cond = fl.expr(e.Cond)
		stmt.Body = fl.branch(e.Then, target, targetType)
	}
	fl.emitIf(stmt, fl.elseOf(stmt, e, target, targetType), e.S)
}

// branch lowers a branch block, assigning its value to target if given.
func (fl *fnLower) branch(b *syntax.Block, target ast.Expr, targetType types.Type) *ast.BlockStmt {
	sub, stmts := fl.sub()
	valueExpr := fl.info.BlockValues[b]
	fl.castScope(func() {
		for i, s := range b.Stmts {
			if target != nil && valueExpr != nil && i == len(b.Stmts)-1 {
				if es, ok := s.(*syntax.ExprStmt); ok && es.X == valueExpr {
					sub.exprInto(valueExpr, target, targetType)
					continue
				}
			}
			sub.stmt(s)
		}
	})
	blk := &ast.BlockStmt{List: *stmts, Lbrace: fl.l.pos(b.Lbrace), Rbrace: fl.l.pos(b.Rbrace)}
	if !b.Lbrace.IsValid() {
		blk.Lbrace, blk.Rbrace = fl.l.pos(b.S), fl.l.endPos(b.S)
	}
	return blk
}

// exprInto lowers e and assigns its value to target, using control flow
// directly for if/when/elvis/catch so no temporaries are needed.
func (fl *fnLower) exprInto(e syntax.Expr, target ast.Expr, targetType types.Type) {
	if target == returnTarget {
		fl.returnInto(e, targetType)
		return
	}
	switch x := syntax.Unparen(e).(type) {
	case *syntax.IfExpr:
		fl.ifStmt(x, target, targetType)
		return
	case *syntax.WhenExpr:
		fl.whenStmt(x, target, targetType)
		return
	case *syntax.ElvisExpr:
		fl.elvisInto(x, target, targetType)
		return
	case *syntax.CatchExpr:
		if id, ok := target.(*ast.Ident); ok && fl.simpleCatch(x) && fl.info.Implicit[e] == check.ConvNone {
			fl.catchInto(x, id.Name, false, x.S)
			return
		}
	case *syntax.JumpExpr:
		fl.jump(x)
		return
	}
	if t := fl.info.TypeOf(e); t == types.Nothing {
		fl.exprStmt(e)
		return
	}
	v := fl.exprAs(e, targetType)
	fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{v}}, e.Span())
}

// returnTarget is the target of if/when values that are returned: branches
// return their values instead of assigning a variable.
var returnTarget = &ast.Ident{Name: "<return>"}

// returnInto lowers a branch value of a returned if/when.
func (fl *fnLower) returnInto(e syntax.Expr, targetType types.Type) {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.IfExpr:
		fl.ifStmt(x, returnTarget, targetType)
		return
	case *syntax.WhenExpr:
		fl.whenStmt(x, returnTarget, targetType)
		return
	case *syntax.JumpExpr:
		fl.jump(x)
		return
	}
	if fl.info.TypeOf(e) == types.Nothing {
		fl.exprStmt(e)
		return
	}
	fl.returnStmt([]syntax.Expr{e}, e.Span())
}

func (fl *fnLower) whenStmt(e *syntax.WhenExpr, target ast.Expr, targetType types.Type) {
	switch fl.classifyWhen(e) {
	case whenTypeSwitch:
		fl.typeSwitch(e, target, targetType)
		return
	case whenChain:
		fl.whenChainStmt(e, target, targetType)
		return
	}
	sw := &ast.SwitchStmt{Switch: fl.l.pos(e.S), Body: &ast.BlockStmt{Lbrace: fl.l.pos(e.S), Rbrace: fl.l.endPos(e.S)}}
	if e.Subject != nil {
		subj := fl.expr(e.Subject)
		if e.Bind != nil {
			if v, ok := fl.info.Defs[e.Bind].(*check.VarSym); ok {
				name := fl.declareLocal(v)
				sw.Init = &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, Rhs: []ast.Expr{subj}}
				subj = ast.NewIdent(name)
			}
		}
		sw.Tag = subj
	}
	fl.breakable(&breakCtx{}, func() {
		for _, br := range e.Branches {
			cc := &ast.CaseClause{Case: fl.l.pos(br.S), Colon: fl.l.pos(br.S)}
			if !br.Else {
				for _, c := range br.Conds {
					if c.X == nil {
						continue
					}
					// classifyWhen routes conditions that need statements to an
					// if/else chain, except the first condition of a subjectless
					// when, whose statements can run before the switch.
					cc.List = append(cc.List, fl.expr(c.X))
				}
				if cc.List == nil {
					cc.List = []ast.Expr{ast.NewIdent("false")}
				}
			}
			body := fl.branch(br.Body, target, targetType)
			cc.Body = body.List
			fl.l.mark(cc, br.S)
			sw.Body.List = append(sw.Body.List, cc)
		}
	})
	fl.exhaustiveDefault(e, sw.Body)
	fl.emit(sw, e.S)
}

// exhaustiveDefault adds a panicking default clause to the switch of a when
// that covers every entry of an enum or member of a sealed interface without
// an else: Go cannot know the switch is exhaustive, and a value from outside
// the set (a converted integer, a nil interface) must not pass silently.
func (fl *fnLower) exhaustiveDefault(e *syntax.WhenExpr, body *ast.BlockStmt) {
	if p := fl.exhaustivePanic(e); p != nil {
		cc := &ast.CaseClause{Body: []ast.Stmt{p}}
		markTree(fl.l, cc, e.S)
		body.List = append(body.List, cc)
	}
}

// exhaustivePanic is the statement for a value no branch of an exhaustive
// when matches, or nil if e is not exhaustive.
func (fl *fnLower) exhaustivePanic(e *syntax.WhenExpr) ast.Stmt {
	t, ok := fl.info.Exhaustive[e]
	if !ok {
		return nil
	}
	pos := e.S.StartPos()
	msg := "lyth: no when branch for this " + types.Base(t).String() + " (" + e.S.File.Base() + ":" + strconv.Itoa(pos.Line) + ":" + strconv.Itoa(pos.Col) + ")"
	return &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{strLit(msg)}}}
}

// selectStmt lowers select to Go's select. Operands are evaluated in branch
// order before the select starts, as Go does. A break inside a branch exits
// the enclosing loop (Lyth's rule), so the select is breakable like a switch.
func (fl *fnLower) selectStmt(s *syntax.SelectStmt) {
	sel := &ast.SelectStmt{Select: fl.l.pos(s.S), Body: &ast.BlockStmt{Lbrace: fl.l.pos(s.S), Rbrace: fl.l.endPos(s.S)}}
	comms := make([]ast.Stmt, len(s.Cases))
	for i, sc := range s.Cases {
		if !sc.Else {
			comms[i] = fl.commStmt(sc)
		}
	}
	fl.breakable(&breakCtx{}, func() {
		for i, sc := range s.Cases {
			cc := &ast.CommClause{Case: fl.l.pos(sc.S), Comm: comms[i], Colon: fl.l.pos(sc.S)}
			sub, stmts := fl.sub()
			for j, v := range fl.info.SelectVars[sc] {
				if v != nil {
					sub.useIfUnused(v, fl.locals[v], sc.Names[j].S)
				}
			}
			fl.castScope(func() {
				for _, st := range sc.Body.Stmts {
					sub.stmt(st)
				}
			})
			cc.Body = *stmts
			fl.l.mark(cc, sc.S)
			sel.Body.List = append(sel.Body.List, cc)
		}
	})
	fl.emit(sel, s.S)
}

// commStmt lowers the channel operation of a select branch to a Go
// communication clause: ch <- v, <-ch, v := <-ch, or v, ok := <-ch.
func (fl *fnLower) commStmt(sc *syntax.SelectCase) ast.Stmt {
	call := syntax.Unparen(sc.Op).(*syntax.CallExpr)
	ci := fl.info.Calls[call]
	sel := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	ch := fl.expr(sel.X)
	if ci.Prelude == check.PreludeSend {
		return fl.sendStmt(call, ch, ci)
	}
	recv := &ast.UnaryExpr{Op: token.ARROW, X: ch}
	var lhs []ast.Expr
	allBlank := true
	for _, v := range fl.info.SelectVars[sc] {
		name := "_"
		if v != nil {
			name = fl.declareLocal(v)
			allBlank = false
		}
		lhs = append(lhs, ast.NewIdent(name))
	}
	if allBlank {
		return &ast.ExprStmt{X: recv}
	}
	return &ast.AssignStmt{Lhs: lhs, Tok: token.DEFINE, Rhs: []ast.Expr{recv}}
}

// ---------------------------------------------------------------------------
// Loops

func (fl *fnLower) forStmt(s *syntax.ForStmt) {
	fi := fl.info.Fors[s]
	if fi == nil {
		return
	}
	names := make([]string, len(fi.Vars))
	for i, v := range fi.Vars {
		names[i] = fl.declareLocal(v)
		if fl.info.Unused[v] {
			names[i] = "_" // Go rejects unused loop variables; Lyth only warns
		}
	}
	if r, ok := s.Iter.(*syntax.RangeExpr); ok {
		fl.rangeLoop(s, r, names)
		return
	}
	iter := fl.expr(s.Iter)
	body, ctx := fl.loopBody(s.Label, s.Body)
	rs := &ast.RangeStmt{For: fl.l.pos(s.S), X: iter, Tok: token.DEFINE, Body: body}
	switch {
	case len(names) == 2:
		rs.Key, rs.Value = ast.NewIdent(names[0]), ast.NewIdent(names[1])
	case fi.Kind == check.ForMap || fi.Kind == check.ForChan || fi.Kind == check.ForFunc:
		rs.Key = ast.NewIdent(names[0])
	default:
		rs.Key, rs.Value = ast.NewIdent("_"), ast.NewIdent(names[0])
	}
	if id, ok := rs.Value.(*ast.Ident); ok && id.Name == "_" {
		rs.Value = nil
	}
	if id, ok := rs.Key.(*ast.Ident); ok && id.Name == "_" && rs.Value == nil {
		rs.Key, rs.Tok = nil, token.ILLEGAL
	}
	fl.emitLoop(rs, ctx, s.S)
}

func (fl *fnLower) rangeLoop(s *syntax.ForStmt, r *syntax.RangeExpr, names []string) {
	name := names[0]
	lo := fl.expr(r.Lo)
	loConst := fl.info.Types[r.Lo].Value
	t := fl.info.TypeOf(r)
	isInt := types.Identical(types.Base(t), types.Int)
	// for i := range n: when the range starts at 0, is exclusive, and has type Int.
	if r.Exclusive && loConst != nil && constant.Sign(loConst) == 0 && isInt {
		hi := fl.expr(r.Hi)
		body, ctx := fl.loopBody(s.Label, s.Body)
		rs := &ast.RangeStmt{For: fl.l.pos(s.S), X: hi, Tok: token.DEFINE, Body: body}
		if name != "_" {
			rs.Key = ast.NewIdent(name)
		} else {
			rs.Tok = token.ILLEGAL
		}
		fl.emitLoop(rs, ctx, s.S)
		return
	}
	hi := fl.expr(r.Hi)
	if !fl.pure(r.Hi, hi) {
		tmp := fl.temp("n")
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{hi}}, r.S)
		hi = ast.NewIdent(tmp)
	}
	if name == "_" {
		name = fl.temp("i")
	}
	op := token.LEQ
	if r.Exclusive {
		op = token.LSS
	}
	init := ast.Stmt(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, Rhs: []ast.Expr{lo}})
	if !gotypes.Identical(gotypes.Default(constKindTypeFor(fl.info, r.Lo)), types.Lower(t)) {
		init = &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE,
			Rhs: []ast.Expr{&ast.CallExpr{Fun: fl.l.typeExpr(t), Args: []ast.Expr{lo}}}}
	}
	body, ctx := fl.loopBody(s.Label, s.Body)
	fs := &ast.ForStmt{For: fl.l.pos(s.S), Init: init,
		Cond: &ast.BinaryExpr{X: ast.NewIdent(name), Op: op, Y: hi},
		Post: &ast.IncDecStmt{X: ast.NewIdent(name), Tok: token.INC},
		Body: body}
	fl.emitLoop(fs, ctx, s.S)
}

func constKindTypeFor(info *check.Info, e syntax.Expr) gotypes.Type {
	tv := info.Types[e]
	if tv.Value == nil {
		return types.Lower(tv.Type)
	}
	if lit, ok := syntax.Unparen(e).(*syntax.BasicLit); ok && lit.Kind == syntax.RUNE {
		return gotypes.Typ[gotypes.UntypedRune]
	}
	return constKindType(tv.Value)
}

func constKindType(v constant.Value) gotypes.Type {
	switch v.Kind() {
	case constant.Bool:
		return gotypes.Typ[gotypes.UntypedBool]
	case constant.String:
		return gotypes.Typ[gotypes.UntypedString]
	case constant.Int:
		return gotypes.Typ[gotypes.UntypedInt]
	case constant.Float:
		return gotypes.Typ[gotypes.UntypedFloat]
	case constant.Complex:
		return gotypes.Typ[gotypes.UntypedComplex]
	}
	return gotypes.Typ[gotypes.Invalid]
}

func (fl *fnLower) whileStmt(s *syntax.WhileStmt) {
	sub, stmts := fl.sub()
	cond := sub.expr(s.Cond)
	body, ctx := fl.loopBody(s.Label, s.Body)
	if len(*stmts) == 0 {
		fs := &ast.ForStmt{For: fl.l.pos(s.S), Cond: cond, Body: body}
		if id, ok := cond.(*ast.Ident); ok && id.Name == "true" {
			fs.Cond = nil
		}
		fl.emitLoop(fs, ctx, s.S)
		return
	}
	// The condition needs statements: evaluate it at the top of each iteration.
	brk := &ast.IfStmt{Cond: &ast.UnaryExpr{Op: token.NOT, X: cond}, Body: &ast.BlockStmt{List: []ast.Stmt{&ast.BranchStmt{Tok: token.BREAK}}}}
	markTree(fl.l, brk, s.Cond.Span())
	list := append(*stmts, brk)
	list = append(list, body.List...)
	fl.emitLoop(&ast.ForStmt{For: fl.l.pos(s.S), Body: &ast.BlockStmt{List: list, Lbrace: body.Lbrace, Rbrace: body.Rbrace}}, ctx, s.S)
}

// ---------------------------------------------------------------------------
// defer and go

func (fl *fnLower) deferGo(call syntax.Expr, body *syntax.Block, isGo bool, span source.Span) {
	var callExpr *ast.CallExpr
	if body != nil {
		child := fl.child(&types.Signature{})
		var stmts []ast.Stmt
		child.stmts = &stmts
		if !isGo && fl.deferErr != "" {
			// ? and throw set the function's error result; the block's own
			// error variables need other names than it.
			child.deferErr, child.inDefer = fl.deferErr, true
			fl.live[fl.deferErr]++
			defer func() { fl.live[fl.deferErr]-- }()
		}
		child.castScope(func() {
			for _, s := range body.Stmts {
				child.stmt(s)
			}
		})
		callExpr = &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: stmts}}}
	} else if t, ok := syntax.Unparen(call).(*syntax.TryExpr); ok && !isGo && fl.deferErr != "" {
		callExpr = fl.deferTry(t)
	} else if c, ok := syntax.Unparen(call).(*syntax.CatchExpr); ok {
		// Go evaluates a deferred call's function value and arguments at
		// the defer statement; the handler's closure would read them at
		// exit. Spill the ones that may change into temporaries first.
		fl.evalOperandsNow(c.X)
		child := fl.child(&types.Signature{})
		var stmts []ast.Stmt
		child.stmts = &stmts
		child.catchInto(c, "", false, c.S)
		callExpr = &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: stmts}}}
	} else {
		g := fl.expr(call)
		ce, ok := g.(*ast.CallExpr)
		if !ok {
			return
		}
		callExpr = ce
	}
	markTree(fl.l, callExpr, span)
	if isGo {
		fl.emit(&ast.GoStmt{Go: fl.l.pos(span), Call: callExpr}, span)
	} else {
		fl.emit(&ast.DeferStmt{Defer: fl.l.pos(span), Call: callExpr}, span)
	}
}

// deferTry lowers defer f()?, which makes a failure of the deferred call the
// function's error unless it is failing already, as Go code does it:
//
//	defer func() {
//		if cerr := f.Close(); cerr != nil && err == nil {
//			err = cerr
//		}
//	}()
func (fl *fnLower) deferTry(t *syntax.TryExpr) *ast.CallExpr {
	fl.evalOperandsNow(t.X)
	child := fl.child(&types.Signature{})
	var stmts []ast.Stmt
	child.stmts = &stmts
	child.deferErr, child.inDefer = fl.deferErr, true
	fl.live[fl.deferErr]++
	defer func() { fl.live[fl.deferErr]-- }()
	info := fl.info.Tries[t]
	if _, isCall := syntax.Unparen(t.X).(*syntax.CallExpr); isCall && info != nil {
		cerr := child.alloc("cerr")
		init := &ast.AssignStmt{Lhs: append(blanks(len(info.Success)), ast.NewIdent(cerr)), Tok: token.DEFINE, Rhs: []ast.Expr{child.expr(t.X)}}
		cond := &ast.BinaryExpr{X: nilCmp(ast.NewIdent(cerr), token.NEQ), Op: token.LAND, Y: nilCmp(ast.NewIdent(fl.deferErr), token.EQL)}
		set := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(fl.deferErr)}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(cerr)}}
		child.emit(&ast.IfStmt{Init: init, Cond: cond, Body: &ast.BlockStmt{List: []ast.Stmt{set}}}, t.S)
	} else {
		child.stmt(&syntax.ExprStmt{X: t}) // a typed catch chain ending in ?
	}
	return &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: stmts}}}
}

// deferResultName picks the Go name of the error result that deferred code
// sets: err, as Go code names it, when every fallible defer is a statement
// of the function's outermost block, where the err of x, err := f() is the
// result itself; retErr when a defer in an inner block could see an inner
// err instead, or when the code has a variable named err.
func (fl *fnLower) deferResultName(d *syntax.FunDecl) string {
	top := true
	syntax.Walk(d.Body, func(n syntax.Node) {
		if ds, ok := n.(*syntax.DeferStmt); ok && fl.info.FallibleDefers[ds] && !slices.Contains(d.Body.Stmts, syntax.Stmt(ds)) {
			top = false
		}
	})
	if top && !fl.plain["err"] && !fl.reserved["err"] && len(*fl.errNames) == 0 {
		*fl.errNames = append(*fl.errNames, "err")
		fl.taken["err"] = true
		return "err"
	}
	return fl.alloc("retErr")
}

// evalOperandsNow evaluates the receiver and arguments of the call e that a
// later assignment could change into temporaries, recorded in fl.pre for the
// closure that runs the call.
func (fl *fnLower) evalOperandsNow(e syntax.Expr) {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return
	}
	spill := func(x syntax.Expr, name string, copyValue bool) {
		if c := fl.info.Implicit[x]; c == check.ConvUnbox || c == check.ConvReuse {
			return // a smart-cast optional: its box never changes
		}
		if t := fl.info.TypeOf(x); fl.stableField(x) && (!copyValue || t == nil || types.IsNilable(t) || isScalar(t)) {
			// resp.Body: a field of a variable nothing assigns. A struct
			// value is still copied: its own fields may change.
			return
		}
		g := fl.expr(x)
		if id, ok := g.(*ast.Ident); ok && identOf(x) == nil {
			// x ?: 1, if/when/catch: already computed into a temporary,
			// which the closure reads.
			fl.pre[x] = id
			return
		}
		if fl.pure(x, g) && !(copyValue && fl.mutableVar(x)) {
			return
		}
		tmp := fl.temp(name)
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{g}}, x.Span())
		fl.pre[x] = ast.NewIdent(tmp)
	}
	switch f := syntax.Unparen(call.Fun).(type) {
	case *syntax.SelectorExpr:
		// A pointer or interface receiver is a value, and so is the copy a
		// value method receives; a struct receiver of a pointer method is
		// the variable itself, as in Go.
		s := fl.info.Selections[f]
		t := fl.info.TypeOf(f.X)
		_, ptr := types.Base(t).(*types.Pointer)
		switch {
		case t == nil || s == nil || fl.info.Types[f.X].Mode != check.ModeValue:
		case fl.info.Implicit[f.X] == check.ConvUnbox:
			// o.Show() on a smart-cast optional: the box never changes.
		case s.Kind == check.SelMethod && ptr && !s.PtrRecv:
			// p.Show() with a value method: Go copies *p at the defer.
			g := fl.expr(f.X)
			tmp := fl.temp("recv")
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{&ast.StarExpr{X: g}}}, f.X.Span())
			fl.pre[f.X] = ast.NewIdent(tmp)
		case s.Kind == check.SelMethod && (types.IsNilable(t) || !s.PtrRecv):
			// A value method's copy is made at the defer, even of a
			// variable whose fields change later.
			spill(f.X, "recv", !types.IsNilable(t))
		case s.Kind == check.SelField:
			// s.cb(): the function value is read at the defer.
			spill(f, "fn", false)
		}
	case *syntax.IndexExpr:
		spill(f, "fn", false) // hs[i]()
	case *syntax.Ident:
		// g(1) with a function variable g: Go evaluates g at the defer.
		if v, ok := fl.info.Uses[f].(*check.VarSym); ok && !v.Settled() {
			spill(f, "fn", false)
		}
	}
	var names []string
	if sig, ok := types.Base(fl.info.TypeOf(call.Fun)).(*types.Signature); ok {
		names = sig.ParamNames
	}
	for i, a := range call.Args {
		if a.Value != nil {
			name := "arg"
			if i < len(names) && names[i] != "" && names[i] != "_" {
				name = names[i] // defer cancel(ctx): ctx := ...
			}
			spill(a.Value, name, true)
		}
	}
}

// setPos gives a generated statement the layout position of its Lyth source
// line by setting the position of its first token, so go/printer keeps the
// author's blank lines between statements.
func setPos(n ast.Node, pos token.Pos) {
	if !pos.IsValid() || n == nil {
		return
	}
	switch n := n.(type) {
	case *ast.Ident:
		if !n.NamePos.IsValid() {
			n.NamePos = pos
		}
	case *ast.BasicLit:
		if !n.ValuePos.IsValid() {
			n.ValuePos = pos
		}
	case *ast.CallExpr:
		setPos(n.Fun, pos)
	case *ast.SelectorExpr:
		setPos(n.X, pos)
	case *ast.StarExpr:
		if !n.Star.IsValid() {
			n.Star = pos
		}
	case *ast.UnaryExpr:
		if !n.OpPos.IsValid() {
			n.OpPos = pos
		}
	case *ast.BinaryExpr:
		setPos(n.X, pos)
	case *ast.IndexExpr:
		setPos(n.X, pos)
	case *ast.ParenExpr:
		if !n.Lparen.IsValid() {
			n.Lparen = pos
		}
	case *ast.CompositeLit:
		if n.Type != nil {
			setPos(n.Type, pos)
		} else if !n.Lbrace.IsValid() {
			n.Lbrace = pos
		}
	case *ast.ArrayType:
		if !n.Lbrack.IsValid() {
			n.Lbrack = pos
		}
	case *ast.ExprStmt:
		setPos(n.X, pos)
	case *ast.AssignStmt:
		if len(n.Lhs) > 0 {
			setPos(n.Lhs[0], pos)
		}
		if !n.TokPos.IsValid() {
			n.TokPos = pos
		}
	case *ast.IncDecStmt:
		setPos(n.X, pos)
		if !n.TokPos.IsValid() {
			n.TokPos = pos
		}
	case *ast.ReturnStmt:
		if !n.Return.IsValid() {
			n.Return = pos
		}
	case *ast.IfStmt:
		if !n.If.IsValid() {
			n.If = pos
		}
	case *ast.ForStmt:
		if !n.For.IsValid() {
			n.For = pos
		}
	case *ast.RangeStmt:
		if !n.For.IsValid() {
			n.For = pos
		}
	case *ast.SwitchStmt:
		if !n.Switch.IsValid() {
			n.Switch = pos
		}
	case *ast.TypeSwitchStmt:
		if !n.Switch.IsValid() {
			n.Switch = pos
		}
	case *ast.LabeledStmt:
		setPos(n.Label, pos)
		if !n.Colon.IsValid() {
			n.Colon = pos
		}
	case *ast.DeclStmt:
		if gd, ok := n.Decl.(*ast.GenDecl); ok && !gd.TokPos.IsValid() {
			gd.TokPos = pos
		}
	case *ast.BranchStmt:
		if !n.TokPos.IsValid() {
			n.TokPos = pos
		}
	case *ast.DeferStmt:
		if !n.Defer.IsValid() {
			n.Defer = pos
		}
	case *ast.GoStmt:
		if !n.Go.IsValid() {
			n.Go = pos
		}
	case *ast.BlockStmt:
		if !n.Lbrace.IsValid() {
			n.Lbrace = pos
		}
	}
}

// multiFallibleExpr reports whether multiFallible lowers e.
func (fl *fnLower) multiFallibleExpr(e syntax.Expr, n int) bool {
	var info *check.TryInfo
	switch x := syntax.Unparen(e).(type) {
	case *syntax.TryExpr:
		info = fl.info.Tries[x]
	case *syntax.CatchExpr:
		info = fl.info.Tries[x]
	}
	return info != nil && len(info.Success) == n && n >= 2
}

// multiFallible lowers x? or x catch { ... } of a call with n success
// values into fresh variables and returns their names; nil when e is not
// such an expression.
func (fl *fnLower) multiFallible(e syntax.Expr, n int) []string {
	var info *check.TryInfo
	switch x := syntax.Unparen(e).(type) {
	case *syntax.TryExpr:
		info = fl.info.Tries[x]
	case *syntax.CatchExpr:
		info = fl.info.Tries[x]
	}
	if info == nil || len(info.Success) != n || n < 2 {
		return nil
	}
	names := make([]string, n)
	for i := range names {
		names[i] = fl.temp("v")
	}
	switch x := syntax.Unparen(e).(type) {
	case *syntax.TryExpr:
		fl.tryInto(x, names, true, x.S)
	case *syntax.CatchExpr:
		fl.catchIntoNames(x, names, true, x.S)
	}
	return names
}

func trues(n int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = true
	}
	return out
}

// localConst lowers a constant declared in a function body to Go's local
// const declaration.
func (fl *fnLower) localConst(d *syntax.ConstDecl) {
	k, ok := fl.info.Defs[d.Name].(*check.ConstSym)
	if !ok || k.Val == nil {
		return
	}
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(k.GoName)}, Values: []ast.Expr{fl.expr(d.Value)}}
	fl.l.mark(spec.Names[0], d.Name.S)
	if d.Type != nil {
		spec.Type = fl.l.typeExpr(k.Type)
	}
	fl.emit(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.CONST, TokPos: fl.l.pos(d.S), Specs: []ast.Spec{spec}}}, d.S)
}

// localStruct lowers a struct declared in a function body to Go's local
// type declaration.
func (fl *fnLower) localStruct(d *syntax.StructDecl) {
	ts, ok := fl.info.Defs[d.Name].(*check.TypeSym)
	if !ok || ts.Decl == nil {
		return
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.Decl.GoName), Type: fl.l.structType(ts.Decl, d)}
	fl.l.mark(spec.Name, d.Name.S)
	fl.emit(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.TYPE, TokPos: fl.l.pos(d.S), Specs: []ast.Spec{spec}}}, d.S)
}

// stableField reports whether e is a chain of field selections on a variable
// that is never reassigned, none of whose fields the function assigns: its
// value is the same at a defer and when the deferred call runs.
func (fl *fnLower) stableField(e syntax.Expr) bool {
	sel, ok := syntax.Unparen(e).(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	s := fl.info.Selections[sel]
	if s == nil || s.Kind != check.SelField || s.Safe || fl.assignedFields[sel.Sel.Name] {
		return false
	}
	if id := identOf(sel.X); id != nil {
		v, ok := fl.info.Uses[id].(*check.VarSym)
		return ok && v.Settled()
	}
	return fl.stableField(sel.X)
}

// mutableVar reports whether e names a mutable local or package variable,
// whose fields or elements may change after it is read.
func (fl *fnLower) mutableVar(e syntax.Expr) bool {
	id := identOf(e)
	if id == nil {
		return false
	}
	v, ok := fl.info.Uses[id].(*check.VarSym)
	return ok && v.Mutable && !types.IsNilable(v.Type) && !isScalar(v.Type)
}

// isCollection reports whether t is a slice or map (also a Go named one),
// which ?: treats as absent when nil.
func isCollection(t types.Type) bool {
	switch check.CollectionOf(t).(type) {
	case *types.Slice, *types.Map:
		return true
	}
	return false
}

// isScalar reports whether t is a basic type, whose value a field
// assignment cannot change.
func isScalar(t types.Type) bool {
	_, ok := types.Base(t).(*types.Basic)
	return ok
}
