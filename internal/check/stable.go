package check

import "github.com/lyth-lang/lyth/internal/syntax"

// reassignedNames returns the names a function body may assign after
// declaring them: targets of = and compound assignments, ++ and --, &x,
// and names a := list redeclares. A local whose name is not in the set is
// never reassigned, so a check of it holds for the rest of its scope and
// smart-casts it, as a val did before ADR 0031.
//
// The analysis is syntactic and goes by name, so it is conservative: an
// assignment to one variable also unsettles every other variable of the same
// name in the function, including those in its lambdas.
func reassignedNames(params []string, body syntax.Node) map[string]bool {
	out := map[string]bool{}
	declared := map[string]bool{}
	for _, p := range params {
		declared[p] = true
	}
	target := func(e syntax.Expr) {
		if id, ok := syntax.Unparen(e).(*syntax.Ident); ok {
			out[id.Name] = true
		}
	}
	syntax.Walk(body, func(n syntax.Node) {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			if tup, ok := syntax.Unparen(n.Lhs).(*syntax.TupleExpr); ok {
				for _, e := range tup.Elems {
					target(e)
				}
			} else {
				target(n.Lhs)
			}
		case *syntax.IncDecStmt:
			target(n.X)
		case *syntax.UnaryExpr:
			if n.Op == syntax.AND {
				target(n.X)
			}
		case *syntax.LocalDecl:
			for _, id := range n.Names {
				if n.Destructure && declared[id.Name] {
					out[id.Name] = true // a, err := f() may assign err
				}
				declared[id.Name] = true
			}
		case *syntax.ForStmt:
			for _, id := range n.Names {
				declared[id.Name] = true
			}
		case *syntax.LambdaExpr:
			for _, p := range n.Params {
				declared[p.Name.Name] = true
			}
		case *syntax.CatchExpr:
			if n.Binder != nil {
				declared[n.Binder.Name] = true
			}
		case *syntax.SelectCase:
			for _, id := range n.Names {
				declared[id.Name] = true
			}
		case *syntax.WhenExpr:
			if n.Bind != nil {
				declared[n.Bind.Name] = true
			}
		}
	})
	return out
}

// Settled reports whether the variable's value cannot change after its
// declaration, so a null check or type test of it smart-casts it:
// parameters and receivers, and locals the function never reassigns.
func (v *VarSym) Settled() bool { return !v.Mutable || v.NotReassigned }
