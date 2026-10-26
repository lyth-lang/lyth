package lower

import (
	"go/ast"
	"go/token"
	gotypes "go/types"
	"maps"
	"strings"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// Type tests: `is`, `as`, `as?`, and the smart casts they establish.
//
// A smart-cast read of a variable is a type assertion x.(T) that cannot
// fail. Where a type test opens a region in which the cast holds (the then
// branch of `if (x is T)`, a single-type when clause, the rest of a block
// after `if (x !is T) return`), the value is bound once, as idiomatic Go
// does, and reads in the region use the binding:
//
//	if (err is *NotFound) { use(err.key) }
//	if notFound, ok := err.(*NotFound); ok { use(notFound.key) }
//
//	when (s) { is Circle -> s.r; else -> 0.0 }
//	switch s := s.(type) { case Circle: s.r; default: 0.0 }
//
// Reads outside such regions keep the explicit assertion.

// castBinding is a name bound to the asserted value of a smart-cast variable.
type castBinding struct {
	typ   types.Type
	ident *ast.Ident // the declaring identifier; renamed to _ if never read
	used  bool
}

// finish blanks a binding that no read used, so Go does not reject it.
func (b *castBinding) finish() {
	if !b.used {
		b.ident.Name = "_"
	}
}

// castFor returns the binding that serves the smart-cast read e, if any.
func (fl *fnLower) castFor(e syntax.Expr) *castBinding {
	var v *check.VarSym
	switch x := e.(type) {
	case *syntax.Ident:
		v, _ = fl.info.Uses[x].(*check.VarSym)
	case *syntax.ThisExpr:
		v = fl.recvVar
	}
	if v == nil {
		return nil
	}
	if b := fl.casts[v]; b != nil && types.Identical(b.typ, fl.info.Asserts[e]) {
		return b
	}
	return nil
}

// withCast runs f with b serving the smart casts of v to b.typ.
func (fl *fnLower) withCast(v *check.VarSym, b *castBinding, f func()) {
	prev, had := fl.casts[v]
	fl.casts[v] = b
	f()
	if had {
		fl.casts[v] = prev
	} else {
		delete(fl.casts, v)
	}
}

// castScope runs f, the lowering of a block, and ends the bindings that
// early-exit type tests in the block added.
func (fl *fnLower) castScope(f func()) {
	saved := maps.Clone(fl.casts)
	f()
	for v, b := range fl.casts {
		if saved[v] != b {
			b.finish()
		}
	}
	clear(fl.casts)
	maps.Copy(fl.casts, saved)
}

// castVar returns the variable a type test of x smart-casts: an immutable
// variable named by x (the checker narrows only those).
func (fl *fnLower) castVar(x syntax.Expr) *check.VarSym {
	switch e := syntax.Unparen(x).(type) {
	case *syntax.Ident:
		if v, ok := fl.info.Uses[e].(*check.VarSym); ok && v.Settled() && v.Kind != check.ReceiverVar {
			return v
		}
	case *syntax.ThisExpr:
		return fl.recvVar // self is read-only
	}
	return nil
}

// bindingName names the value of an assertion to t as idiomatic Go does:
// the type's name in lower camel case (circle for Circle), else its
// initials (nf for a private notFound type, s for string).
func (fl *fnLower) bindingName(t types.Type) string {
	base := typeBase(t)
	for _, name := range []string{lowerFirst(base), initials(base)} {
		if name != "" && !fl.taken[name] && !fl.reserved[name] && !check.IsGoReserved(name) {
			fl.taken[name] = true
			return name
		}
	}
	return fl.temp(initials(base))
}

// initials returns the lower-cased first letters of the words of a camel
// case name (NotFound -> nf, HTTPError -> he).
func initials(name string) string {
	var out []rune
	prevUpper := false
	for i, r := range name {
		upper := r >= 'A' && r <= 'Z'
		if i == 0 || upper && !prevUpper {
			out = append(out, r|0x20)
		}
		prevUpper = upper
	}
	return string(out)
}

// release frees temporaries whose Go scope has ended (an if statement's
// init), so sibling statements can reuse names like ok. User identifiers
// are never released: they are collected before lowering.
func (fl *fnLower) release(names ...string) {
	for _, n := range names {
		if n != "_" {
			delete(fl.taken, n)
		}
	}
}

// typeAssign builds `name, ok := x.(T)`.
func (fl *fnLower) typeAssign(name *ast.Ident, ok string, x ast.Expr, t types.Type) *ast.AssignStmt {
	return &ast.AssignStmt{Lhs: []ast.Expr{name, ast.NewIdent(ok)}, Tok: token.DEFINE,
		Rhs: []ast.Expr{&ast.TypeAssertExpr{X: x, Type: fl.l.typeExpr(t)}}}
}

// ifTypeTest lowers `if (x is T)` and `if (x !is T)` to a comma-ok
// assertion that binds the asserted value for the region where it holds.
func (fl *fnLower) ifTypeTest(e *syntax.IfExpr, is *syntax.IsExpr, target ast.Expr, targetType types.Type) {
	t := fl.info.TypeTests[is]
	x := fl.expr(is.X)
	v := fl.castVar(is.X)
	bind := &castBinding{typ: t, ident: ast.NewIdent("_")}
	if v != nil {
		bind.ident.Name = fl.bindingName(t)
	}
	ok := fl.temp("ok")
	assign := fl.typeAssign(bind.ident, ok, x, t)
	stmt := &ast.IfStmt{If: fl.l.pos(e.S), Init: assign, Cond: ast.NewIdent(ok)}
	region := func(f func()) {
		if v != nil {
			fl.withCast(v, bind, f)
		} else {
			f()
		}
	}
	var after []ast.Stmt
	if !is.Not {
		region(func() { stmt.Body = fl.branch(e.Then, target, targetType) })
		bind.finish()
		// The init's names are not read in the else branch; free them.
		fl.release(bind.ident.Name, ok)
		after = fl.elseOf(stmt, e, target, targetType)
		fl.emitIf(stmt, after, e.S)
		return
	}
	stmt.Cond = &ast.UnaryExpr{Op: token.NOT, X: stmt.Cond}
	stmt.Body = fl.branch(e.Then, target, targetType)
	switch {
	case e.Else != nil:
		region(func() { after = fl.elseOf(stmt, e, target, targetType) })
		bind.finish()
		fl.release(bind.ident.Name, ok)
	case v != nil && goTerminates(stmt.Body.List):
		// if (x !is T) return: the cast holds for the rest of the block, so
		// the assertion moves before the if and the enclosing castScope
		// ends the binding.
		stmt.Init = nil
		fl.emitSynth(assign, is.S)
		fl.casts[v] = bind
	default:
		bind.finish()
		fl.release(bind.ident.Name, ok)
	}
	fl.emitIf(stmt, after, e.S)
}

// elseOf lowers the else branch of e into stmt, chaining `else if`. When
// the if's value is returned and the then branch returns, the else branch
// follows the if instead, in Go's early-return style; the caller emits the
// returned statements after the if.
func (fl *fnLower) elseOf(stmt *ast.IfStmt, e *syntax.IfExpr, target ast.Expr, targetType types.Type) (after []ast.Stmt) {
	if e.Else == nil {
		return nil
	}
	els := fl.branch(e.Else, target, targetType)
	if outdentable(target, stmt.Body, e.Else) {
		return els.List
	}
	if len(els.List) == 1 && len(e.Else.Stmts) == 1 {
		if inner, ok := els.List[0].(*ast.IfStmt); ok {
			stmt.Else = inner
			return nil
		}
	}
	stmt.Else = els
	return nil
}

// outdentable reports whether the else branch els of an if whose value is
// returned can follow the if: the then branch returns and els declares no
// variables that would move into the enclosing scope.
func outdentable(target ast.Expr, then *ast.BlockStmt, els *syntax.Block) bool {
	if target != returnTarget || !goTerminates(then.List) {
		return false
	}
	for _, st := range els.Stmts {
		switch st := st.(type) {
		case *syntax.LocalDecl:
			return false
		case *syntax.ExprStmt:
			if w, ok := syntax.Unparen(st.X).(*syntax.WhenExpr); ok && w.Bind != nil {
				return false
			}
		}
	}
	return true
}

// emitIf emits an if statement and the statements that follow it.
func (fl *fnLower) emitIf(stmt *ast.IfStmt, after []ast.Stmt, span source.Span) {
	fl.emit(stmt, span)
	*fl.stmts = append(*fl.stmts, after...)
}

// isExpr lowers `x is T` in other positions to the ok result of a comma-ok
// assertion.
func (fl *fnLower) isExpr(e *syntax.IsExpr) ast.Expr {
	x := fl.expr(e.X)
	ok := fl.temp("is" + upperFirst(typeBase(fl.info.TypeTests[e])))
	fl.emitSynth(fl.typeAssign(ast.NewIdent("_"), ok, x, fl.info.TypeTests[e]), e.S)
	if e.Not {
		return &ast.UnaryExpr{Op: token.NOT, X: ast.NewIdent(ok)}
	}
	return ast.NewIdent(ok)
}

// asExpr lowers `x as T` to a type assertion, which panics on failure like
// Kotlin's ClassCastException, and `x as? T` to a comma-ok assertion that
// yields null on failure.
func (fl *fnLower) asExpr(e *syntax.AsExpr) ast.Expr {
	x := fl.expr(e.X)
	t := fl.info.TypeTests[e]
	if !e.Safe {
		return &ast.TypeAssertExpr{X: x, Type: fl.l.typeExpr(t)}
	}
	if types.IsNilable(types.Base(t)) {
		// Nil-able targets need no box: the failed assertion's zero value
		// is null.
		res := fl.temp(lowerFirst(typeBase(t)))
		fl.emitSynth(fl.typeAssign(ast.NewIdent(res), "_", x, t), e.S)
		return ast.NewIdent(res)
	}
	res := fl.temp("v")
	fl.boxedAs(e, x, t, res, true)
	return ast.NewIdent(res)
}

// boxedAs emits `x as? T` for a value type T into the variable res:
//
//	var res *T
//	if t, ok := x.(T); ok { res = new(t) }
func (fl *fnLower) boxedAs(e *syntax.AsExpr, x ast.Expr, t types.Type, res string, synth bool) {
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(res)}, Type: fl.l.typeExpr(types.MakeNullable(t))}
	decl := &ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}
	if synth {
		fl.emitSynth(decl, e.S)
	} else {
		fl.emit(decl, e.S)
	}
	val, ok := fl.bindingName(t), fl.temp("ok")
	ifs := &ast.IfStmt{
		Init: fl.typeAssign(ast.NewIdent(val), ok, x, t),
		Cond: ast.NewIdent(ok),
		Body: &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(res)}, Tok: token.ASSIGN,
			Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{ast.NewIdent(val)}}}}}},
	}
	fl.emitSynth(ifs, e.S)
	fl.release(val, ok)
}

// typeTestDecl lowers declarations initialized by a type test straight into
// the declared variable:
//
//	val ok = x is T        ->  _, ok := x.(T)
//	val p = x as? *T       ->  p, _ := x.(*T)
//	val c = x as? Circle   ->  var c *Circle; if ...
func (fl *fnLower) typeTestDecl(s *syntax.LocalDecl, v *check.VarSym, value syntax.Expr) bool {
	t := fl.info.TypeTests[value]
	if t == nil || !gotypes.Identical(types.Lower(v.Type), types.Lower(fl.info.TypeOf(value))) {
		return false
	}
	switch value := value.(type) {
	case *syntax.IsExpr:
		if value.Not {
			return false
		}
		x := fl.expr(value.X)
		fl.emit(fl.typeAssign(ast.NewIdent("_"), fl.declareLocal(v), x, t), s.S)
	case *syntax.AsExpr:
		if !value.Safe {
			return false
		}
		x := fl.expr(value.X)
		name := fl.declareLocal(v)
		if !types.IsNilable(types.Base(t)) {
			fl.boxedAs(value, x, t, name, false)
			break
		}
		fl.emit(fl.typeAssign(ast.NewIdent(name), "_", x, t), s.S)
	default:
		return false
	}
	fl.useIfUnused(v, fl.declareLocal(v), s.S)
	return true
}

// typeBase names a type for temporaries (*pgconn.PgError -> PgError).
func typeBase(t types.Type) string {
	switch b := types.Base(t).(type) {
	case *types.Pointer:
		return typeBase(b.Elem)
	case *types.Named:
		if b.Decl != nil {
			return b.Decl.Name
		}
		if b.Go != nil {
			return b.Go.Obj().Name()
		}
	case *types.Basic:
		return b.String()
	case *types.AnyType:
		return "Any"
	}
	return "T"
}

// whenKind classifies how a when statement lowers.
type whenKind int

const (
	whenSwitch     whenKind = iota // expression switch
	whenTypeSwitch                 // every condition is `is T`: type switch
	whenChain                      // if/else chain (mixed tests, conditions needing statements)
)

func (fl *fnLower) classifyWhen(e *syntax.WhenExpr) whenKind {
	if e.Subject != nil && boxedOptional(fl.info.TypeOf(e.Subject)) {
		// A Go switch compares *T with T; the chain compares contents.
		return whenChain
	}
	allIs, anyTest := true, false
	for i, br := range e.Branches {
		for j, c := range br.Conds {
			switch c.Kind {
			case syntax.WhenIs:
				anyTest = true
			case syntax.WhenNotIs, syntax.WhenIn, syntax.WhenNotIn:
				anyTest, allIs = true, false
			default:
				if isNull(c.X) && e.Subject != nil && types.IsInterface(fl.info.TypeOf(e.Subject)) {
					continue // case nil: in a type switch
				}
				allIs = false
				// A Go case expression cannot run statements first; any
				// condition but the first one evaluated would run them
				// eagerly, so such whens become if/else chains.
				if (i > 0 || j > 0 || e.Subject != nil) && fl.needsStmts(c.X) {
					return whenChain
				}
			}
		}
	}
	switch {
	case anyTest && allIs && e.Subject != nil:
		return whenTypeSwitch
	case anyTest:
		return whenChain
	}
	return whenSwitch
}

func isNull(e syntax.Expr) bool {
	_, ok := syntax.Unparen(e).(*syntax.NullLit)
	return ok
}

// needsStmts reports whether lowering x may emit statements. It is a
// conservative syntactic test (lowering has naming side effects, so it is
// not run speculatively); a false positive only costs the switch form.
func (fl *fnLower) needsStmts(x syntax.Expr) bool {
	found := false
	var visit func(n syntax.Node)
	visit = func(n syntax.Node) {
		if found || n == nil {
			return
		}
		switch n := n.(type) {
		case *syntax.LambdaExpr:
			return // its statements stay inside the function literal
		case *syntax.TryExpr, *syntax.ElvisExpr, *syntax.CatchExpr, *syntax.IfExpr, *syntax.WhenExpr,
			*syntax.IsExpr, *syntax.NotNullExpr, *syntax.JumpExpr:
			found = true
			return
		case *syntax.AsExpr:
			if n.Safe {
				found = true
				return
			}
		case *syntax.SelectorExpr:
			if n.Safe {
				found = true
				return
			}
		case *syntax.CallExpr:
			if fl.info.Tries[n] != nil {
				found = true
				return
			}
		case *syntax.InExpr:
			// Map tests always need a statement; other tests spill impure
			// operands into temporaries.
			if in := fl.info.Ins[n]; in == nil || in.Kind == check.InMap || !trivialExpr(fl.info, n.X) || !trivialRight(fl.info, in, n.Y) {
				found = true
				return
			}
		}
		if e, ok := n.(syntax.Expr); ok {
			switch fl.info.Implicit[e] {
			case check.ConvBox, check.ConvNilIface:
				found = true // boxing may need a temporary; a nil check needs an if
				return
			}
		}
		syntax.Children(n, visit)
	}
	visit(x)
	return found
}

// whenCastVar returns the variable that `is` clauses of e smart-cast.
func (fl *fnLower) whenCastVar(e *syntax.WhenExpr) *check.VarSym {
	if e.Bind != nil {
		v, _ := fl.info.Defs[e.Bind].(*check.VarSym)
		return v
	}
	return fl.castVar(e.Subject)
}

// usesVar reports whether any branch of e reads v.
func (fl *fnLower) usesVar(e *syntax.WhenExpr, v *check.VarSym) bool {
	found := false
	for _, br := range e.Branches {
		syntax.Walk(br.Body, func(n syntax.Node) {
			if id, ok := n.(*syntax.Ident); ok && fl.info.Uses[id] == v {
				found = true
			}
		})
	}
	return found
}

// typeSwitch lowers `when (x) { is A -> ...; is B, C -> ...; else -> ... }`
// to a Go type switch. A when on an immutable local rebinds it, the Go idiom
// `switch x := x.(type)`, so single-type clauses read it with its case type.
func (fl *fnLower) typeSwitch(e *syntax.WhenExpr, target ast.Expr, targetType types.Type) {
	subj := fl.expr(e.Subject)
	v := fl.whenCastVar(e)
	var name *ast.Ident
	used := false // whether any clause reads the switch variable
	if v != nil {
		switch {
		case e.Bind != nil || v.Kind != check.GlobalVar:
			// The switch variable replaces v in every clause, under v's name.
			name = ast.NewIdent(fl.declareLocal(v))
			used = fl.usesVar(e, v)
		case v.Pkg == nil || v.Pkg.Path == fl.l.pkg.Path:
			// So does a package-level val of this package (it is immutable).
			name = ast.NewIdent(v.GoName)
			used = fl.usesVar(e, v)
		default:
			// Another package's variable keeps its qualified name; casts
			// read a new one.
			name = ast.NewIdent(fl.bindingName(fl.info.TypeOf(e.Subject)))
		}
	}
	sw := &ast.TypeSwitchStmt{Switch: fl.l.pos(e.S), Body: &ast.BlockStmt{Lbrace: fl.l.pos(e.S), Rbrace: fl.l.endPos(e.S)}}
	for _, br := range e.Branches {
		cc := &ast.CaseClause{Case: fl.l.pos(br.S), Colon: fl.l.pos(br.S)}
		if !br.Else {
			for _, c := range br.Conds {
				if c.Kind != syntax.WhenIs && isNull(c.X) {
					cc.List = append(cc.List, ast.NewIdent("nil"))
					continue
				}
				cc.List = append(cc.List, fl.l.typeExpr(fl.info.TypeTests[c]))
			}
		}
		lower := func() {
			fl.breakable(&breakCtx{}, func() { cc.Body = fl.branch(br.Body, target, targetType).List })
		}
		if name != nil && len(cc.List) == 1 && br.Conds[0].Kind == syntax.WhenIs {
			b := &castBinding{typ: fl.info.TypeTests[br.Conds[0]], ident: name}
			fl.withCast(v, b, lower)
			used = used || b.used
		} else {
			lower()
		}
		fl.l.mark(cc, br.S)
		sw.Body.List = append(sw.Body.List, cc)
	}
	fl.exhaustiveDefault(e, sw.Body)
	assert := &ast.TypeAssertExpr{X: subj}
	if used {
		sw.Assign = &ast.AssignStmt{Lhs: []ast.Expr{name}, Tok: token.DEFINE, Rhs: []ast.Expr{assert}}
	} else {
		sw.Assign = &ast.ExprStmt{X: assert}
	}
	fl.emit(sw, e.S)
}

// whenChainStmt lowers a when that a Go switch cannot express to an
// if/else chain. The subject is evaluated once; each condition is evaluated
// only when the branches before it did not match, as in Kotlin.
func (fl *fnLower) whenChainStmt(e *syntax.WhenExpr, target ast.Expr, targetType types.Type) {
	var subj ast.Expr
	if e.Subject != nil {
		x := fl.expr(e.Subject)
		switch v := fl.whenCastVar(e); {
		case e.Bind != nil && v != nil:
			name := fl.declareLocal(v)
			fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}, e.Subject.Span())
			subj = ast.NewIdent(name)
		case isIdentExpr(x):
			subj = x
		default:
			name := fl.temp("v")
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}, e.Subject.Span())
			subj = ast.NewIdent(name)
		}
	}
	fl.chainFrom(e, 0, subj, target, targetType)
}

func isIdentExpr(x ast.Expr) bool {
	_, ok := x.(*ast.Ident)
	return ok
}

// chainFrom emits the if statement for branch i of e; later branches go in
// its else.
func (fl *fnLower) chainFrom(e *syntax.WhenExpr, i int, subj ast.Expr, target ast.Expr, targetType types.Type) {
	br := e.Branches[i]
	if br.Else {
		*fl.stmts = append(*fl.stmts, fl.branch(br.Body, target, targetType).List...)
		return
	}
	var stmt *ast.IfStmt
	v := fl.whenCastVar(e)
	if c := br.Conds[0]; len(br.Conds) == 1 && (c.Kind == syntax.WhenIs || c.Kind == syntax.WhenNotIs) {
		// A single type test goes in the if's init; `is T` binds the cast
		// value for the branch.
		t := fl.info.TypeTests[c]
		bind := &castBinding{typ: t, ident: ast.NewIdent("_")}
		if c.Kind == syntax.WhenIs && v != nil {
			bind.ident.Name = fl.bindingName(t)
		}
		ok := fl.temp("ok")
		stmt = &ast.IfStmt{If: fl.l.pos(br.S), Init: fl.typeAssign(bind.ident, ok, subj, t), Cond: ast.NewIdent(ok)}
		if c.Kind == syntax.WhenNotIs {
			stmt.Cond = &ast.UnaryExpr{Op: token.NOT, X: stmt.Cond}
		}
		if bind.ident.Name != "_" {
			fl.withCast(v, bind, func() { stmt.Body = fl.branch(br.Body, target, targetType) })
			bind.finish()
		} else {
			stmt.Body = fl.branch(br.Body, target, targetType)
		}
		fl.release(bind.ident.Name, ok)
	} else if in := fl.info.Ins[br.Conds[0]]; len(br.Conds) == 1 && in != nil && in.Kind == check.InMap && !boxedOptional(fl.info.TypeOf(e.Subject)) {
		// A single map test goes in the if's init: if _, ok := m[k]; ok
		ok := fl.temp("ok")
		stmt = &ast.IfStmt{If: fl.l.pos(br.S), Cond: ast.NewIdent(ok),
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_"), ast.NewIdent(ok)}, Tok: token.DEFINE,
				Rhs: []ast.Expr{&ast.IndexExpr{X: fl.expr(br.Conds[0].X), Index: subj}}}}
		if br.Conds[0].Kind == syntax.WhenNotIn {
			stmt.Cond = &ast.UnaryExpr{Op: token.NOT, X: stmt.Cond}
		}
		stmt.Body = fl.branch(br.Body, target, targetType)
		fl.release(ok)
	} else {
		stmt = &ast.IfStmt{If: fl.l.pos(br.S), Cond: fl.branchCond(br, subj, e.Subject != nil && boxedOptional(fl.info.TypeOf(e.Subject)))}
		stmt.Body = fl.branch(br.Body, target, targetType)
	}
	if p := fl.exhaustivePanic(e); p != nil && i+1 == len(e.Branches) {
		markTree(fl.l, p, e.S)
		if target == returnTarget && goTerminates(stmt.Body.List) {
			fl.emit(stmt, br.S)
			fl.emit(p, e.S)
			return
		}
		stmt.Else = &ast.BlockStmt{List: []ast.Stmt{p}}
	}
	if i+1 < len(e.Branches) {
		next := e.Branches[i+1]
		if target == returnTarget && goTerminates(stmt.Body.List) && (!next.Else || outdentable(target, stmt.Body, next.Body)) {
			// The branch returns: the rest of the chain follows the if.
			fl.emit(stmt, br.S)
			fl.chainFrom(e, i+1, subj, target, targetType)
			return
		}
		if next.Else {
			stmt.Else = fl.branch(next.Body, target, targetType)
		} else {
			sub, stmts := fl.sub()
			sub.chainFrom(e, i+1, subj, target, targetType)
			if inner, ok := (*stmts)[0].(*ast.IfStmt); ok && len(*stmts) == 1 {
				stmt.Else = inner
			} else {
				stmt.Else = &ast.BlockStmt{List: *stmts, Lbrace: fl.l.pos(next.S), Rbrace: fl.l.endPos(next.S)}
			}
		}
	}
	fl.emit(stmt, br.S)
}

// branchCond lowers the conditions of a when branch to one boolean. Each
// condition's statements run only if the conditions before it were false.
func (fl *fnLower) branchCond(br *syntax.WhenBranch, subj ast.Expr, boxed bool) ast.Expr {
	var conds []ast.Expr
	var match string // flag used once a later condition needs statements
	for _, c := range br.Conds {
		sub, stmts := fl.sub()
		x := sub.whenCond(c, subj, boxed)
		if len(*stmts) == 0 || len(conds) == 0 && match == "" {
			*fl.stmts = append(*fl.stmts, *stmts...)
			conds = append(conds, x)
			continue
		}
		// match := c1 || ...; if !match { stmts; match = x }
		if match == "" {
			match = fl.temp("match")
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(match)}, Tok: token.DEFINE, Rhs: []ast.Expr{orAll(conds)}}, c.S)
			conds = nil
		}
		body := append(*stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(match)}, Tok: token.ASSIGN, Rhs: []ast.Expr{x}})
		fl.emitSynthShallow(&ast.IfStmt{Cond: &ast.UnaryExpr{Op: token.NOT, X: ast.NewIdent(match)}, Body: &ast.BlockStmt{List: body}}, c.S)
	}
	if match != "" {
		return orAll(append([]ast.Expr{ast.NewIdent(match)}, conds...))
	}
	return orAll(conds)
}

// whenCond lowers one when condition to a boolean.
// boxedOptional reports whether t is an optional value type (Int?, an enum?),
// represented in Go as a pointer to the value.
func boxedOptional(t types.Type) bool {
	n, ok := t.(*types.Nullable)
	return ok && !types.IsNilable(n.Elem)
}

// whenCond lowers one condition of a when branch. With a boxed optional
// subject, a value condition compares contents: n != nil && *n == 1.
func (fl *fnLower) whenCond(c *syntax.WhenCond, subj ast.Expr, boxed bool) ast.Expr {
	switch c.Kind {
	case syntax.WhenIn, syntax.WhenNotIn:
		in := fl.info.Ins[c]
		if in == nil {
			return ast.NewIdent("false")
		}
		if !boxed {
			return fl.membership(in, subj, nil, c.X, c.Kind == syntax.WhenNotIn, c.S)
		}
		// An optional subject: null is in nothing, so n != nil && *n in c,
		// and n == nil || *n !in c.
		sub, stmts := fl.sub()
		test := sub.membership(in, &ast.StarExpr{X: subj}, nil, c.X, c.Kind == syntax.WhenNotIn, c.S)
		if len(*stmts) > 0 {
			// A map test needs statements, which may run only once the
			// subject is known not to be null:
			//	match := n != nil
			//	if match { _, ok := m[*n]; match = ok }
			match := fl.temp("match")
			init, guard := nilCmp(subj, token.NEQ), ast.Expr(ast.NewIdent(match))
			if c.Kind == syntax.WhenNotIn {
				init, guard = nilCmp(subj, token.EQL), &ast.UnaryExpr{Op: token.NOT, X: ast.NewIdent(match)}
			}
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(match)}, Tok: token.DEFINE, Rhs: []ast.Expr{init}}, c.S)
			body := append(*stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(match)}, Tok: token.ASSIGN, Rhs: []ast.Expr{test}})
			fl.emitSynthShallow(&ast.IfStmt{Cond: guard, Body: &ast.BlockStmt{List: body}}, c.S)
			return ast.NewIdent(match)
		}
		if c.Kind == syntax.WhenNotIn {
			return &ast.BinaryExpr{X: &ast.BinaryExpr{X: subj, Op: token.EQL, Y: ast.NewIdent("nil")}, Op: token.LOR, Y: test}
		}
		return &ast.BinaryExpr{X: &ast.BinaryExpr{X: subj, Op: token.NEQ, Y: ast.NewIdent("nil")}, Op: token.LAND, Y: test}
	case syntax.WhenIs, syntax.WhenNotIs:
		ok := fl.temp("is" + upperFirst(typeBase(fl.info.TypeTests[c])))
		fl.emitSynth(fl.typeAssign(ast.NewIdent("_"), ok, subj, fl.info.TypeTests[c]), c.S)
		if c.Kind == syntax.WhenNotIs {
			return &ast.UnaryExpr{Op: token.NOT, X: ast.NewIdent(ok)}
		}
		return ast.NewIdent(ok)
	}
	x := fl.expr(c.X)
	if subj == nil {
		return x
	}
	if _, isNull := syntax.Unparen(c.X).(*syntax.NullLit); boxed && !isNull && !boxedOptional(fl.info.TypeOf(c.X)) {
		return &ast.BinaryExpr{
			X:  &ast.BinaryExpr{X: subj, Op: token.NEQ, Y: ast.NewIdent("nil")},
			Op: token.LAND,
			Y:  &ast.BinaryExpr{X: &ast.StarExpr{X: subj}, Op: token.EQL, Y: x},
		}
	}
	return &ast.BinaryExpr{X: subj, Op: token.EQL, Y: x}
}

func orAll(xs []ast.Expr) ast.Expr {
	if len(xs) == 0 {
		return ast.NewIdent("false")
	}
	x := xs[0]
	for _, y := range xs[1:] {
		x = &ast.BinaryExpr{X: x, Op: token.LOR, Y: y}
	}
	return x
}

// trivialExpr reports whether e is a constant or a plain name, which
// lowering never spills.
func trivialExpr(info *check.Info, e syntax.Expr) bool {
	if tv, ok := info.Types[e]; ok && tv.Value != nil {
		return true
	}
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Ident, *syntax.BasicLit, *syntax.BoolLit:
		return true
	case *syntax.UnaryExpr:
		return trivialExpr(info, e.X)
	}
	return false
}

func trivialRight(info *check.Info, in *check.InInfo, y syntax.Expr) bool {
	if in.Kind == check.InRange {
		return trivialExpr(info, in.Range.Lo) && trivialExpr(info, in.Range.Hi)
	}
	_, isIdent := syntax.Unparen(y).(*syntax.Ident)
	return isIdent
}

// upperFirst upper-cases the first letter of a type's name, so a test of a
// basic type names its result like hand-written Go (isInt, isString).
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
