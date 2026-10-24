package lower

import (
	"go/ast"
	"go/token"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// Membership tests lower to what Go code writes by hand:
//
//	x in a..b       x >= a && x <= b         (a..<b: x < b)
//	x in xs         slices.Contains(xs, x)
//	k in m          _, ok := m[k]            (the condition is ok)
//	s in text       strings.Contains(text, s)
//
// Lyth evaluates x first, then the right side, each once. Operands are
// spilled into temporaries only when an impure one would otherwise be
// evaluated twice or out of order.

func (fl *fnLower) inExpr(e *syntax.InExpr) ast.Expr {
	in := fl.info.Ins[e]
	if in == nil {
		return ast.NewIdent("false")
	}
	return fl.membership(in, nil, e.X, e.Y, e.Not, e.S)
}

// membership lowers a test of an element in a container. elem is the
// element already lowered (a when subject), or nil to lower elemSyn first.
func (fl *fnLower) membership(in *check.InInfo, elem ast.Expr, elemSyn syntax.Expr, container syntax.Expr, not bool, span source.Span) ast.Expr {
	if in.Kind == check.InRange {
		return fl.rangeTest(in.Range, elem, elemSyn, not)
	}
	var y ast.Expr
	if elem == nil {
		xs := fl.exprList([]syntax.Expr{elemSyn, container}, nil)
		elem, y = xs[0], xs[1]
		// Go evaluates the container before the element in all forms below.
		if !fl.pure(elemSyn, elem) && !fl.pure(container, y) {
			elem = fl.stash(elemSyn, elem)
		}
	} else {
		y = fl.expr(container)
	}
	var test ast.Expr
	switch in.Kind {
	case check.InList, check.InArray:
		if in.Kind == check.InArray {
			if _, ok := y.(*ast.Ident); !ok {
				y = fl.stash(container, y) // slicing needs an addressable array
			}
			y = &ast.SliceExpr{X: y}
		}
		test = &ast.CallExpr{Fun: sel(fl.l.useImport("slices", "slices"), "Contains"), Args: []ast.Expr{y, elem}}
	case check.InString:
		fn := "Contains"
		if in.Rune {
			fn = "ContainsRune"
		}
		test = &ast.CallExpr{Fun: sel(fl.l.useImport("strings", "strings"), fn), Args: []ast.Expr{y, elem}}
	case check.InMap:
		ok := fl.temp("ok")
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_"), ast.NewIdent(ok)}, Tok: token.DEFINE,
			Rhs: []ast.Expr{&ast.IndexExpr{X: y, Index: elem}}}, span)
		test = ast.NewIdent(ok)
	}
	if not {
		return &ast.UnaryExpr{Op: token.NOT, X: test}
	}
	return test
}

// rangeTest lowers x in lo..hi to x >= lo && x <= hi (lo..<hi: x < hi), and
// x !in lo..hi to x < lo || x > hi.
func (fl *fnLower) rangeTest(r *syntax.RangeExpr, elem ast.Expr, elemSyn syntax.Expr, not bool) ast.Expr {
	ops := []syntax.Expr{r.Lo, r.Hi}
	if elem == nil {
		ops = append([]syntax.Expr{elemSyn}, ops...)
	}
	xs := fl.exprList(ops, nil)
	if elem == nil {
		elem, xs = xs[0], xs[1:]
		if !fl.pure(elemSyn, elem) {
			elem = fl.stash(elemSyn, elem) // read twice
		}
	}
	lo, hi := xs[0], xs[1]
	// && and || would skip hi, and lo would run after x: keep each once, in order.
	if !fl.pure(r.Lo, lo) {
		lo = fl.stash(r.Lo, lo)
	}
	if !fl.pure(r.Hi, hi) {
		hi = fl.stash(r.Hi, hi)
	}
	geq, upper, join := token.GEQ, token.LEQ, token.LAND
	if r.Exclusive {
		upper = token.LSS
	}
	if not {
		geq, join = token.LSS, token.LOR
		upper = token.GTR
		if r.Exclusive {
			upper = token.GEQ
		}
	}
	return &ast.BinaryExpr{
		X:  &ast.BinaryExpr{X: elem, Op: geq, Y: lo},
		Op: join,
		Y:  &ast.BinaryExpr{X: cloneIdentExpr(elem), Op: upper, Y: hi},
	}
}

// ifMapTest lowers `if (k in m)` to `if _, ok := m[k]; ok`, keeping the ok
// variable in the if statement's scope, and returns its name for release
// once the statement is lowered. It returns "" for other conditions.
func (fl *fnLower) ifMapTest(stmt *ast.IfStmt, cond syntax.Expr) string {
	e, isIn := syntax.Unparen(cond).(*syntax.InExpr)
	if !isIn || fl.info.Ins[e] == nil || fl.info.Ins[e].Kind != check.InMap {
		return ""
	}
	xs := fl.exprList([]syntax.Expr{e.X, e.Y}, nil)
	elem, m := xs[0], xs[1]
	if !fl.pure(e.X, elem) && !fl.pure(e.Y, m) {
		elem = fl.stash(e.X, elem)
	}
	ok := fl.temp("ok")
	stmt.Init = &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("_"), ast.NewIdent(ok)}, Tok: token.DEFINE,
		Rhs: []ast.Expr{&ast.IndexExpr{X: m, Index: elem}}}
	stmt.Cond = ast.NewIdent(ok)
	if e.Not {
		stmt.Cond = &ast.UnaryExpr{Op: token.NOT, X: stmt.Cond}
	}
	return ok
}
