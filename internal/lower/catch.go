package lower

import (
	"go/ast"
	"go/token"

	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// A chain of typed catch clauses, ended by ? or by a catch of all errors,
// lowers to one error check that dispatches with errors.AsType:
//
//	val u = find(id) catch (e: *NotFound) { null }?
//
//	u, err := find(id)
//	if err != nil {
//		if _, ok := errors.AsType[*NotFound](err); ok {
//			u = nil
//		} else {
//			return nil, err
//		}
//	}

// typedChain returns the typed catch clauses x consists of, innermost
// (first matched) first, or nil if x is not a typed catch.
func typedChain(x syntax.Expr) []*syntax.CatchExpr {
	var out []*syntax.CatchExpr
	for {
		c, ok := syntax.Unparen(x).(*syntax.CatchExpr)
		if !ok || c.Type == nil {
			return out
		}
		out = append([]*syntax.CatchExpr{c}, out...)
		x = c.X
	}
}

// catchChain lowers typed catch clauses over their fallible operand. final
// is the catch of all errors that ends the chain, or nil when ? ends it and
// unmatched errors propagate. names receive the success values (nil or "_"
// when the value is discarded); define declares them. rt is the type of the
// chain's value.
func (fl *fnLower) catchChain(clauses []*syntax.CatchExpr, final *syntax.CatchExpr, tailSpan source.Span, names []string, define bool, rt types.Type, span source.Span) {
	base := clauses[0].X
	info := fl.info.Tries[clauses[0]]
	if info == nil {
		return
	}
	if !define && len(names) > 0 && names[0] != "_" {
		// Existing targets: bind fresh temporaries, then assign.
		tmps := make([]string, len(names))
		for i := range names {
			tmps[i] = fl.temp("v")
		}
		fl.catchChain(clauses, final, tailSpan, tmps, true, rt, span)
		var lhs, rhs []ast.Expr
		for i, n := range names {
			if n != "_" {
				lhs, rhs = append(lhs, ast.NewIdent(n)), append(rhs, ast.NewIdent(tmps[i]))
			}
		}
		fl.emitSynth(&ast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, Rhs: rhs}, span)
		return
	}
	if len(names) == 1 && names[0] != "_" && len(info.Success) == 1 && boxes(info.Success[0], rt) {
		fl.catchBoxed(final, clauses, tailSpan, names[0], rt, span)
		return
	}
	operand := fl.expr(base)
	n := 0
	if _, isResult := fl.info.TypeOf(base).(*types.Result); isResult {
		n = len(info.Success)
	}
	var target ast.Expr
	if n == 1 && len(names) == 1 && names[0] != "_" {
		target = ast.NewIdent(names[0])
	}
	bound := false
	for _, name := range names {
		bound = bound || name != "_"
	}
	errName := ""
	if final != nil {
		if fi := fl.info.Tries[final]; fi != nil && fi.Binder != nil {
			errName = fl.catchBinder(fi.Binder, final.Body, !bound)
		}
	}
	if errName == "" {
		errName = fl.errVar()
	}
	cond := nilCmp(ast.NewIdent(errName), token.NEQ)
	if !bound {
		// if _, err := call; err != nil { ... }
		init := &ast.AssignStmt{Lhs: append(blanks(n), ast.NewIdent(errName)), Tok: token.DEFINE, Rhs: []ast.Expr{operand}}
		fl.l.mark(init, base.Span())
		fl.l.markSynth(cond, clauses[0].CatchSpan)
		ifs := &ast.IfStmt{If: fl.l.pos(span), Init: init, Cond: cond, Body: fl.chainDispatch(clauses, final, tailSpan, errName, nil, nil)}
		fl.emit(ifs, span)
		return
	}
	lhs := make([]ast.Expr, 0, n+1)
	for i := 0; i < n; i++ {
		name := "_"
		if i < len(names) {
			name = names[i]
		}
		lhs = append(lhs, ast.NewIdent(name))
	}
	lhs = append(lhs, ast.NewIdent(errName))
	fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: token.DEFINE, TokPos: fl.l.pos(span), Rhs: []ast.Expr{operand}}, span)
	ifs := &ast.IfStmt{If: fl.l.pos(clauses[0].CatchSpan), Cond: cond, Body: fl.chainDispatch(clauses, final, tailSpan, errName, target, firstOr(info.Success))}
	fl.emitSynthShallow(ifs, clauses[0].CatchSpan)
}

// chainDispatch builds the body of the error check: one errors.AsType test
// per clause, then the catch of all errors or the propagation. Handlers
// assign their values, of type want, to target. When every clause's handler
// returns, the tests follow one another in Go's early-return style;
// otherwise they form an else-if chain.
func (fl *fnLower) chainDispatch(clauses []*syntax.CatchExpr, final *syntax.CatchExpr, tailSpan source.Span, errName string, target ast.Expr, want types.Type) *ast.BlockStmt {
	var tests []*ast.IfStmt
	asType := sel(fl.l.useImport("errors", "errors"), "AsType")
	// Every clause declares its own ok in its if statement, as Go code
	// writes `} else if e, ok := ...; ok {`.
	ok := fl.temp("ok")
	early := true
	for _, cl := range clauses {
		ci := fl.info.Tries[cl]
		binder := "_"
		if v := ci.Binder; v != nil && !fl.info.Unused[v] {
			// The binder lives in its clause's if statement, so it keeps its
			// Lyth name unless that would hide the error variable, which later
			// clauses and the final catch read, or a name still in use.
			binder = v.Name()
			if binder == errName || !fl.binderFree(binder) {
				binder = fl.freshBinder(binder)
			}
			fl.locals[v] = binder
		}
		init := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(binder), ast.NewIdent(ok)}, Tok: token.DEFINE,
			Rhs: []ast.Expr{&ast.CallExpr{Fun: &ast.IndexExpr{X: asType, Index: fl.l.typeExpr(ci.CatchType)}, Args: []ast.Expr{ast.NewIdent(errName)}}}}
		markTree(fl.l, init, cl.CatchSpan.Join(cl.Type.Span()))
		ifs := &ast.IfStmt{If: fl.l.pos(cl.CatchSpan), Init: init, Cond: ast.NewIdent(ok), Body: fl.catchHandler(cl, ci, target, want)}
		fl.l.markSynth(ifs.Cond, cl.CatchSpan)
		fl.l.markSynth(ifs, cl.CatchSpan)
		tests = append(tests, ifs)
		early = early && goTerminates(ifs.Body.List)
	}
	var rest *ast.BlockStmt
	if final != nil {
		rest = fl.catchHandler(final, fl.info.Tries[final], target, want)
	} else {
		fail := fl.failStmts(ast.NewIdent(errName))
		for _, s := range fail {
			markTree(fl.l, s, tailSpan)
		}
		rest = &ast.BlockStmt{List: fail}
	}
	fl.release(ok)
	if early {
		list := make([]ast.Stmt, 0, len(tests)+len(rest.List))
		for _, t := range tests {
			list = append(list, t)
		}
		return &ast.BlockStmt{List: append(list, rest.List...)}
	}
	for i := 1; i < len(tests); i++ {
		tests[i-1].Else = tests[i]
	}
	tests[len(tests)-1].Else = rest
	return &ast.BlockStmt{List: []ast.Stmt{tests[0]}}
}
