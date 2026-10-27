package check

import (
	"fmt"

	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// Smart casts of reassigned locals.
//
// A local that its function never reassigns keeps a smart cast for the rest
// of the scope where the check holds. A local that is reassigned keeps one
// only until its next assignment: x, err := f(); if err != null { throw err }
// works although y, err := g() assigns err again later. Each assignment
// starts a new generation of the variable, and a smart cast recorded for an
// older generation no longer applies. An assignment of a non-null value is a
// smart cast itself.
//
// The rule needs to see every assignment in order, so it applies only to a
// local of the function being checked whose address the function never
// takes, that no closure assigns, and that no if, when, or catch used as a
// value assigns (such a block runs before the plain reads of the expression
// around it, ADR 0045); and a smart cast made outside a closure does not
// apply inside it, since the closure may run after the next assignment.
// A loop forgets, at its start, the smart casts of the variables its body
// assigns: the next iteration runs after those assignments. After an
// if/else, a variable stays smart-cast when every branch that continues
// leaves it non-null.

// narrowEntry is a smart cast recorded in a scope: the narrowed type, and for
// a reassigned local, the generation and function it holds for.
type narrowEntry struct {
	t    types.Type
	gen  int
	fn   *funcCtx
	flow bool
}

// flowReason says why a local is never smart-cast between assignments.
type flowReason int

const (
	flowAddressTaken flowReason = iota + 1
	flowClosureAssigns
	flowValueAssigns
)

// flowOff returns the names whose locals cannot be smart-cast between
// assignments: their address is taken, or a closure (lambda, go or defer
// block) assigns them. A name a closure declares itself does not count.
func flowOff(body syntax.Node) map[string]flowReason {
	f := &flowScan{off: map[string]flowReason{}}
	f.push()
	f.walk(body)
	return f.off
}

type flowScan struct {
	off      map[string]flowReason
	scopes   []map[string]bool // block scopes, innermost last
	closures []int             // where each enclosing closure's scopes start
	inValue  int               // if, when, and catch blocks used as values around the walk
}

func (f *flowScan) push(names ...*syntax.Ident) {
	sc := map[string]bool{}
	for _, id := range names {
		sc[id.Name] = true
	}
	f.scopes = append(f.scopes, sc)
}

func (f *flowScan) pop() { f.scopes = f.scopes[:len(f.scopes)-1] }

// assigns records an assignment to name: one by a closure to a variable it
// does not declare turns the name off.
func (f *flowScan) assigns(name string) {
	if f.inValue > 0 && f.off[name] == 0 {
		f.off[name] = flowValueAssigns
	}
	if len(f.closures) == 0 {
		return
	}
	for i := len(f.scopes) - 1; i >= f.closures[len(f.closures)-1]; i-- {
		if f.scopes[i][name] {
			return
		}
	}
	if f.off[name] == 0 {
		f.off[name] = flowClosureAssigns
	}
}

func (f *flowScan) closure(params []*syntax.Ident, body syntax.Node) {
	saveValue := f.inValue
	f.inValue = 0 // the body runs when called, not with the expression around it
	f.closures = append(f.closures, len(f.scopes))
	f.push(params...)
	f.walk(body)
	f.pop()
	f.closures = f.closures[:len(f.closures)-1]
	f.inValue = saveValue
}

func (f *flowScan) walk(n syntax.Node) {
	switch x := n.(type) {
	case *syntax.Block:
		if x == nil {
			return
		}
		f.push()
		syntax.Children(n, f.walk)
		f.pop()
		return
	case *syntax.LambdaExpr:
		var params []*syntax.Ident
		for _, p := range x.Params {
			params = append(params, p.Name)
		}
		f.closure(params, x.Body)
		return
	case *syntax.DeferStmt:
		if x.Body != nil {
			f.closure(nil, x.Body)
			return
		}
	case *syntax.GoStmt:
		if x.Body != nil {
			f.closure(nil, x.Body)
			return
		}
	case *syntax.LocalDecl:
		syntax.Children(n, f.walk)
		for _, id := range x.Names {
			f.scopes[len(f.scopes)-1][id.Name] = true
		}
		return
	case *syntax.ExprStmt:
		switch x.X.(type) {
		case *syntax.IfExpr, *syntax.WhenExpr, *syntax.CatchExpr:
			// A statement: its blocks run on their own.
			syntax.Children(x.X, f.walk)
			return
		}
	case *syntax.IfExpr, *syntax.WhenExpr, *syntax.CatchExpr:
		f.inValue++
		syntax.Children(n, f.walk)
		f.inValue--
		return
	case *syntax.ForStmt:
		if x.Iter != nil {
			f.walk(x.Iter)
		}
		f.push(x.Names...)
		f.walk(x.Body)
		f.pop()
		return
	case *syntax.ForClauseStmt:
		f.push() // the init's variables belong to the loop
		syntax.Children(n, f.walk)
		f.pop()
		return
	case *syntax.SelectCase:
		if x.Op != nil {
			f.walk(x.Op)
		}
		f.push(x.Names...)
		f.walk(x.Body)
		f.pop()
		return
	case *syntax.AssignStmt:
		for _, name := range targetNames(x.Lhs) {
			f.assigns(name)
		}
	case *syntax.IncDecStmt:
		for _, name := range targetNames(x.X) {
			f.assigns(name)
		}
	case *syntax.UnaryExpr:
		if x.Op == syntax.AND {
			if id, ok := syntax.Unparen(x.X).(*syntax.Ident); ok {
				f.off[id.Name] = flowAddressTaken
			}
		}
	}
	syntax.Children(n, f.walk)
}

// assignedIn returns the names a statement list assigns: = and compound
// assignment targets, ++ and --, and the names a := list may reuse.
func assignedIn(n syntax.Node) map[string]bool {
	out := map[string]bool{}
	syntax.Walk(n, func(x syntax.Node) {
		switch x := x.(type) {
		case *syntax.AssignStmt:
			for _, name := range targetNames(x.Lhs) {
				out[name] = true
			}
		case *syntax.IncDecStmt:
			for _, name := range targetNames(x.X) {
				out[name] = true
			}
		case *syntax.LocalDecl:
			if x.Destructure {
				for _, id := range x.Names {
					out[id.Name] = true
				}
			}
		}
	})
	return out
}

func targetNames(e syntax.Expr) []string {
	if tup, ok := syntax.Unparen(e).(*syntax.TupleExpr); ok {
		var out []string
		for _, el := range tup.Elems {
			out = append(out, targetNames(el)...)
		}
		return out
	}
	if id, ok := syntax.Unparen(e).(*syntax.Ident); ok {
		return []string{id.Name}
	}
	return nil
}

// flowVar reports whether v, a reassigned local, may be smart-cast between
// its assignments in the function being checked.
func (c *Checker) flowVar(v *VarSym) bool {
	return v != nil && v.Kind == LocalVar && !v.Settled() && c.fn != nil && v.owner == c.fn &&
		c.fn.flowOff != nil && c.fn.flowOff[v.name] == 0
}

// castable reports whether a check of v can smart-cast it.
func (c *Checker) castable(v *VarSym) bool {
	return v.Settled() || c.flowVar(v)
}

// assigned records an assignment of a value of type t to the variable the
// target names: a new generation, and a smart cast when the value is never
// null.
func (c *Checker) assigned(target syntax.Expr, t types.Type) {
	id, ok := syntax.Unparen(target).(*syntax.Ident)
	if !ok {
		return
	}
	v, ok := c.info.Uses[id].(*VarSym)
	if !ok || v.Kind != LocalVar {
		return
	}
	v.gen = c.nextGen()
	v.loopForgot = false
	c.castByValue(v, t)
}

// nextGen returns a generation no variable has had, so that a smart cast
// recorded for one generation never matches another.
func (c *Checker) nextGen() int {
	c.gens++
	return c.gens
}

// castByValue smart-casts v, just assigned a value of type t, when the
// value is never null.
func (c *Checker) castByValue(v *VarSym, t types.Type) {
	if t != nil && t != types.Invalid && t != types.Null && c.castable(v) && types.MayBeNull(v.Type) && !types.MayBeNull(t) {
		c.scope.setNarrow(v, types.Base(v.Type))
	}
}

// forgetLoopAssigned ends the smart casts of the variables a loop body
// assigns, before the body is checked.
func (c *Checker) forgetLoopAssigned(body syntax.Node) {
	for name := range assignedIn(body) {
		if v, ok := c.lookup(name).(*VarSym); ok && v.Kind == LocalVar {
			v.gen = c.nextGen()
			v.loopForgot = true
		}
	}
}

// noCastNote explains why a check of v did not smart-cast it where it is
// used as non-null, and whether copying it into a new variable helps; ""
// when nothing stopped a check.
func (c *Checker) noCastNote(v *VarSym) (note string, copyHelps bool) {
	switch {
	case v.Kind == GlobalVar:
		return fmt.Sprintf("smart casts do not apply to package-level variables; %s may change between the check and this use", v.name), true
	case v.Kind != LocalVar || v.Settled() || c.fn == nil:
		return "", false
	case v.owner != c.fn:
		return fmt.Sprintf("checks do not smart-cast %s inside a lambda or a go or defer block: its function assigns it, and this code may run after any assignment", v.name), true
	case c.fn.flowOff[v.name] == flowAddressTaken:
		return fmt.Sprintf("checks do not smart-cast %s: its address is taken, so it may change through the pointer", v.name), true
	case c.fn.flowOff[v.name] == flowClosureAssigns:
		return fmt.Sprintf("checks do not smart-cast %s: a lambda, go block, or defer block assigns it", v.name), true
	case c.fn.flowOff[v.name] == flowValueAssigns:
		return fmt.Sprintf("checks do not smart-cast %s: an if, when, or catch used as a value assigns it", v.name), true
	case !c.scope.staleNarrow(v):
		return "", false
	case v.loopForgot:
		return fmt.Sprintf("this loop assigns %s, so a check before the loop does not hold inside it; check it inside the loop", v.name), false
	}
	return fmt.Sprintf("%s is assigned after the check, which ends the smart cast; check it again after the assignment", v.name), false
}

// flowState returns the variables a check or an assignment smart-casts to
// non-null here.
func (c *Checker) flowState() map[*VarSym]types.Type {
	out := map[*VarSym]types.Type{}
	seen := map[*VarSym]bool{}
	for sc := c.scope; sc != nil; sc = sc.parent {
		for v, e := range sc.narrow {
			if seen[v] {
				continue
			}
			seen[v] = true
			if t, ok := c.scope.narrowed(v, c.fn); ok && v.Type != nil && types.MayBeNull(v.Type) && types.Identical(t, types.Base(v.Type)) && e.t != nil {
				out[v] = t
			}
		}
	}
	return out
}

// gens records the generations of the function's locals.
func (c *Checker) gensNow() map[*VarSym]int {
	if c.fn == nil {
		return nil
	}
	out := make(map[*VarSym]int, len(c.fn.locals))
	for _, v := range c.fn.locals {
		out[v] = v.gen
	}
	return out
}

// branchFlow follows the smart casts through the branches of an if or when.
// Each branch starts from the generations before the first, since an
// assignment in one branch does not happen in another; afterwards, the
// variables that every path that continues leaves non-null stay smart-cast.
type branchFlow struct {
	c       *Checker
	start   map[*VarSym]int
	changed map[*VarSym]bool
	paths   []map[*VarSym]types.Type
	n       int
}

func (c *Checker) startBranches() *branchFlow {
	return &branchFlow{c: c, start: c.gensNow(), changed: map[*VarSym]bool{}}
}

// next starts another branch.
func (b *branchFlow) next() {
	if b.n > 0 {
		for v, g := range b.start {
			if v.gen != g {
				b.changed[v] = true
				v.gen = g
			}
		}
	}
	b.n++
}

// end records how a branch ended: its smart casts, unless it terminates.
func (b *branchFlow) end(terminates bool, flow map[*VarSym]types.Type) {
	if !terminates {
		b.paths = append(b.paths, flow)
	}
}

// finish gives the variables any branch assigned a new generation and
// smart-casts those every continuing path left non-null.
func (b *branchFlow) finish() {
	c := b.c
	for v, g := range b.start {
		if b.changed[v] || v.gen != g {
			v.gen = c.nextGen()
		}
	}
	if len(b.paths) == 0 {
		return
	}
	for v, t := range b.paths[0] {
		all := true
		for _, p := range b.paths[1:] {
			if pt, ok := p[v]; !ok || !types.Identical(pt, t) {
				all = false
				break
			}
		}
		if all {
			c.scope.setNarrow(v, t)
		}
	}
}
