package check

import (
	"bytes"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// stmtList checks statements in the current scope and reports whether the
// list always terminates (returns, throws, breaks, continues, or panics).
func (c *Checker) stmtList(stmts []syntax.Stmt) bool {
	terminated := false
	for _, s := range stmts {
		if terminated {
			c.warnf(diag.WUnreachable, s.Span(), "unreachable code")
			// Keep checking for errors, but report unreachable code once.
			for _, rest := range stmts[indexOf(stmts, s):] {
				c.stmt(rest)
			}
			return true
		}
		if c.stmt(s) {
			terminated = true
		}
	}
	return terminated
}

func indexOf(stmts []syntax.Stmt, s syntax.Stmt) int {
	for i, x := range stmts {
		if x == s {
			return i
		}
	}
	return len(stmts)
}

// block checks a block in a new scope.
func (c *Checker) block(b *syntax.Block) bool {
	var t bool
	c.withScope(newScope(c.scope, BlockScope), func() { t = c.stmtList(b.Stmts) })
	return t
}

// blockResult is the outcome of checking a block that may yield a value.
type blockResult struct {
	op         *operand
	valueExpr  syntax.Expr
	terminates bool
	flow       map[*VarSym]types.Type // the smart casts at its end, if it does not terminate
}

// blockValue checks a block whose last expression statement provides a value
// (if/when branches, catch handlers, lambda bodies).
func (c *Checker) blockValue(b *syntax.Block, want types.Type, needValue bool) blockResult {
	var res blockResult
	c.withScope(newScope(c.scope, BlockScope), func() {
		defer func() {
			if !res.terminates {
				res.flow = c.flowState()
			}
		}()
		n := len(b.Stmts)
		last, isExpr := syntax.Stmt(nil), false
		if n > 0 {
			last = b.Stmts[n-1]
			if es, ok := last.(*syntax.ExprStmt); ok {
				if _, isJump := es.X.(*syntax.JumpExpr); !isJump {
					isExpr = true
				}
			}
		}
		if !needValue || !isExpr {
			res.terminates = c.stmtList(b.Stmts)
			return
		}
		if c.stmtList(b.Stmts[:n-1]) {
			res.terminates = true
			c.expr(last.(*syntax.ExprStmt).X, want)
			return
		}
		x := last.(*syntax.ExprStmt).X
		op := c.expr(x, want)
		if op.mode == ModeNoValue {
			res.terminates = false
			return
		}
		if op.mode == ModeValue && isStatementCall(x, op) {
			// fmt.Println(e) yields (int, error), not a value: the block
			// ends in a statement, which the caller reports if it needs one.
			if r, ok := op.typ.(*types.Result); ok && !c.errorIgnorable(callOf(x)) {
				c.unhandledError(callOf(x), r)
			}
			return
		}
		op = c.singleValue(op)
		if isNothing(op.typ) {
			res.terminates = true
			return
		}
		res.op = op
		res.valueExpr = x
		c.info.BlockValues[b] = x
	})
	return res
}

func (c *Checker) stmt(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.LocalDecl:
		return c.localDecl(s)
	case *syntax.ConstDecl:
		c.localConst(s)
	case *syntax.StructDecl:
		c.localStruct(s)
	case *syntax.AssignStmt:
		c.assignStmt(s)
	case *syntax.IncDecStmt:
		x := c.lhs(s.X)
		if x.mode != ModeInvalid {
			if !types.IsNumeric(types.Base(x.typ)) || types.IsNullable(x.typ) {
				c.errorf(diag.EInvalidOp, s.S, "%s requires a numeric variable, not %s", map[bool]string{true: "++", false: "--"}[s.Inc], x.typ)
			} else {
				c.mutablePath(s.X, "modify", diag.EReassignVal)
			}
		}
	case *syntax.ExprStmt:
		return c.exprStmt(s)
	case *syntax.Block:
		return c.block(s)
	case *syntax.ForStmt:
		c.forStmt(s)
	case *syntax.WhileStmt:
		return c.whileStmt(s)
	case *syntax.ForClauseStmt:
		return c.forClauseStmt(s)
	case *syntax.SelectStmt:
		return c.selectStmt(s)
	case *syntax.DeferStmt:
		c.deferGo(s.Call, s.Body, s)
	case *syntax.GoStmt:
		c.deferGo(s.Call, s.Body, nil)
	case *syntax.BadStmt:
	}
	return false
}

// lhs checks an assignment target without counting it as a use.
func (c *Checker) lhs(e syntax.Expr) *operand {
	if id, ok := syntax.Unparen(e).(*syntax.Ident); ok && c.implicitMember(id.Name) == nil {
		sym := c.lookup(id.Name)
		if v, ok := sym.(*VarSym); ok {
			c.info.Uses[id] = v
			if v.owner != nil && v.owner != c.fn {
				v.captured = true // assigned by a lambda, go block, or defer block
			}
			t := v.Type
			if v.Kind == GlobalVar {
				t = c.globalType(v)
			}
			return c.record(&operand{mode: ModeValue, typ: t, sym: v, expr: e})
		}
	}
	x := c.value(e, nil)
	if ix, ok := syntax.Unparen(e).(*syntax.IndexExpr); ok && x.mode != ModeInvalid {
		if m, ok := collectionOf(c.info.TypeOf(ix.X)).(*types.Map); ok {
			// Reading m[k] can find no entry, which is null for a value
			// type without a zero value; m[k] = v stores a value of the
			// map's own element type.
			x.typ = m.Elem
		}
	}
	return x
}

func (c *Checker) localDecl(s *syntax.LocalDecl) bool {
	if s.Destructure {
		return c.destructure(s)
	}
	name := s.Names[0]
	var want types.Type
	if s.Type != nil {
		want = c.resolveType(s.Type)
	}
	redeclared := false
	if s.Define && name.Name == "_" && s.Type == nil && s.Value != nil {
		// Go's _ := e is "no new variables on left side of :=".
		d := c.errorf(diag.EColonEquals, name.S, "_ := declares no variable")
		f := name.S.File
		if i := bytes.Index(f.Content[name.S.End:s.Value.Span().Start], []byte(":=")); i >= 0 {
			op := source.Span{File: f, Start: name.S.End + i, End: name.S.End + i + 2}
			d.WithFix(op, "=", "discard the value with _ = %s", s.Value.Span().Short())
		}
	}
	var param *VarSym // the parameter this declaration may replace (ADR 0039)
	if s.Define && name.Name != "_" {
		param = c.paramNamed(name.Name)
		if old := c.scope.LookupLocal(name.Name); old != nil && (param == nil || old != param) {
			// Go's "no new variables on left side of :=".
			c.errorf(diag.EColonEquals, name.S, "%s is already declared in this scope; := declares a new variable", name.Name).
				WithRelated(old.DeclSpan(), "previous declaration").
				WithHint("use = to assign to %s", name.Name)
			redeclared = true
		}
	}
	var t, init types.Type
	terminates := false
	switch {
	case s.Value == nil:
		if want != nil && want != types.Invalid && !types.Zeroable(want) {
			c.errorf(diag.ENoZeroValue, s.S, "%s of type %s needs an initializer: %s", name.Name, want, types.NoZeroReason(want)).
				WithHint("initialize it, or declare it nullable: %s: %s = null", name.Name, types.MakeNullable(want))
		}
		t = want
	default:
		x := c.expr(s.Value, want)
		if x.mode == ModeNoValue {
			c.errorf(diag.EInvalidUse, s.Value.Span(), "%s does not produce a value", s.Value.Span().Short())
			x = invalid(s.Value)
		} else {
			x = c.singleValue(x)
		}
		if want != nil {
			c.assign(x, want, "initializer of "+name.Name)
			t = want
			if x.mode != ModeInvalid {
				init = x.typ
			}
		} else {
			t = c.defaultValueType(x, "initializer of "+name.Name)
		}
		if x.mode != ModeInvalid && isNothing(x.typ) {
			terminates = true
			if want == nil {
				c.warnf(diag.WUnreachable, s.S, "%s is never initialized: its initializer always exits", name.Name)
			}
		}
	}
	if t == nil {
		t = types.Invalid
	}
	if t == types.Unit {
		c.errorf(diag.EInvalidUse, s.S, "%s cannot have type unit", name.Name)
		t = types.Invalid
	}
	if param != nil {
		sameScope := c.scope.LookupLocal(name.Name) == param
		if c.readsVar(s.Value, param) && (sameScope || c.fitsParam(t, param)) {
			c.info.Locals[s] = []*VarSym{c.rebind(param, name, t, sameScope)}
			return terminates
		}
		if sameScope {
			c.errorf(diag.EColonEquals, name.S, "%s is a parameter; := cannot declare it again", name.Name).
				WithRelated(param.Span, "parameter %s declared here", name.Name).
				WithHint("this would ignore the argument %s; choose another name, or derive the new value from it (%s := f(%s))", name.Name, name.Name, name.Name)
			redeclared = true
		}
	}
	v := c.newLocal(name, t)
	if !redeclared {
		c.declareLocal(v, name)
		c.castByValue(v, init) // x: T? = nonNull
	}
	c.info.Locals[s] = []*VarSym{v}
	return terminates
}

// paramNamed returns the parameter of the enclosing function or lambda that
// name refers to here, or nil when name means something else.
func (c *Checker) paramNamed(name string) *VarSym {
	if c.fn == nil {
		return nil
	}
	for s := c.scope; s != nil && s.kind != PackageScope && s.kind != UniverseScope; s = s.parent {
		if sym := s.syms[name]; sym != nil {
			if v, ok := sym.(*VarSym); ok && v.Kind == ParamVar && !v.Implicit && v.owner == c.fn {
				return v
			}
			return nil
		}
	}
	return nil
}

// fitsParam reports whether a value of type t can replace param: it has the
// parameter's type, or a Go type that Lyth treats as it (context.Context!
// from context.WithTimeout for a context.Context parameter).
func (c *Checker) fitsParam(t types.Type, param *VarSym) bool {
	if types.Identical(t, param.Type) {
		return true
	}
	if types.IsPlatform(t) && types.Identical(types.Base(t), types.Base(param.Type)) {
		return true
	}
	return false
}

// readsVar reports whether e, already checked, reads v.
func (c *Checker) readsVar(e syntax.Expr, v *VarSym) bool {
	if e == nil {
		return false
	}
	reads := false
	syntax.Walk(e, func(n syntax.Node) {
		if id, ok := n.(*syntax.Ident); ok && c.info.Uses[id] == Symbol(v) {
			reads = true
		}
	})
	return reads
}

// rebind declares the local that replaces param from here to the end of the
// block, for name := f(name) (ADR 0039): the parameter itself is never
// assigned, so parameters stay read-only. The local has the parameter's
// type, which keeps the generated line Go's own ctx, cancel := ...; in the
// function's outermost block that line assigns the parameter, which only a
// closure created before it could observe (FreshName).
func (c *Checker) rebind(param *VarSym, name *syntax.Ident, t types.Type, sameScope bool) *VarSym {
	if sameScope && t != types.Invalid && param.Type != types.Invalid && !c.fitsParam(t, param) {
		c.errorf(diag.ETypeMismatch, name.S, "%s would change type from %s to %s; choose a new name", name.Name, param.Type, t).
			WithRelated(param.Span, "parameter %s declared here", name.Name)
	}
	v := &VarSym{name: name.Name, Type: param.Type, Mutable: true, Kind: LocalVar, Span: name.S, Rebinds: param, InPlace: sameScope, FreshName: param.captured, owner: c.fn}
	v.NotReassigned = c.fn.reassigned != nil && !c.fn.reassigned[name.Name]
	c.info.Defs[name] = v
	if sameScope {
		c.scope.Replace(v) // hides the parameter from here on
	} else {
		c.scope.Insert(v)
	}
	c.fn.locals = append(c.fn.locals, v)
	return v
}

// newLocal returns a variable declared by := or name: Type. It is mutable;
// it smart-casts when its function never reassigns it.
func (c *Checker) newLocal(id *syntax.Ident, t types.Type) *VarSym {
	v := &VarSym{name: id.Name, Type: t, Mutable: true, Kind: LocalVar, Span: id.S, owner: c.fn}
	v.NotReassigned = c.fn != nil && c.fn.reassigned != nil && !c.fn.reassigned[id.Name]
	c.warnShadow(id)
	return v
}

// warnShadow reports a declaration that hides a local or parameter of an
// enclosing scope in the same function, the shadowing that makes Go's
// x, err := f() in an inner block leave the outer err unchanged.
func (c *Checker) warnShadow(id *syntax.Ident) {
	if id.Name == "_" || c.fn == nil || c.scope.LookupLocal(id.Name) != nil {
		return
	}
	for s := c.scope.parent; s != nil && s.kind != PackageScope && s.kind != UniverseScope; s = s.parent {
		if v, ok := s.syms[id.Name].(*VarSym); ok && (v.Kind == LocalVar || v.Kind == ParamVar) && !v.Implicit {
			c.warnf(diag.WShadow, id.S, "%s hides the %s declared in an enclosing scope", id.Name, map[bool]string{true: "parameter", false: "variable"}[v.Kind == ParamVar]).
				WithRelated(v.Span, "%s declared here", id.Name).
				WithHint("choose another name; to change the outer %s, assign it with = instead of declaring it", id.Name)
			return
		}
	}
}

func (c *Checker) declareLocal(v *VarSym, id *syntax.Ident) {
	c.info.Defs[id] = v
	if id.Name == "_" {
		return
	}
	if old := c.scope.LookupLocal(id.Name); old != nil {
		c.errorf(diag.EDuplicate, id.S, "%s is already declared in this scope", id.Name).
			WithRelated(old.DeclSpan(), "previous declaration")
		return
	}
	c.scope.Insert(v)
	if c.fn != nil {
		c.fn.locals = append(c.fn.locals, v)
	}
}

func (c *Checker) destructure(s *syntax.LocalDecl) bool {
	vars := make([]*VarSym, len(s.Names))
	var rawNull []bool // the values null when the destructured call fails
	// As in Go, a, b := e declares the names not yet declared in this scope
	// and assigns the others; at least one must be new.
	declareAll := func(ts []types.Type) {
		fresh := false
		for i, id := range s.Names {
			t := types.Type(types.Invalid)
			if ts != nil && i < len(ts) {
				t = ts[i]
			}
			if id.Name == "_" {
				continue
			}
			if p := c.paramNamed(id.Name); p != nil && ts != nil && c.readsVar(s.Value, p) {
				// g, ctx := errgroup.WithContext(ctx) (ADR 0039).
				if same := c.scope.LookupLocal(id.Name) == p; same || c.fitsParam(t, p) {
					vars[i] = c.rebind(p, id, t, same)
					fresh = true
					continue
				}
			}
			if old := c.scope.LookupLocal(id.Name); old != nil {
				ov, ok := old.(*VarSym)
				switch {
				case !ok || ov.Kind != LocalVar:
					c.errorf(diag.EReassignVal, id.S, "%s cannot be assigned: := may reuse only local variables of this scope", id.Name).
						WithRelated(old.DeclSpan(), "%s declared here", id.Name)
				case ts != nil && t != types.Invalid && ov.Type != types.Invalid:
					c.assign(&operand{mode: ModeValue, typ: t, expr: id}, ov.Type, "assignment to "+id.Name)
				}
				if ok {
					c.info.Uses[id] = ov
					vars[i] = ov
					c.assigned(id, t)
				}
				continue
			}
			fresh = true
			v := c.newLocal(id, t)
			if i < len(rawNull) && rawNull[i] {
				v.rawCall = s.Value.Span().Short()
			}
			c.declareLocal(v, id)
			vars[i] = v
		}
		if !fresh && ts != nil {
			c.errorf(diag.EColonEquals, s.S, "no new variables on the left of :=").
				WithHint("use = to assign")
		}
		c.info.Locals[s] = vars
	}
	if s.Value == nil {
		c.errorf(diag.EInvalidUse, s.S, "destructuring declaration needs an initializer")
		declareAll(nil)
		return false
	}
	var elems []types.Type
	if tup, ok := syntax.Unparen(s.Value).(*syntax.TupleExpr); ok {
		// val (a, b) = (x, y)
		for _, el := range tup.Elems {
			x := c.value(el, nil)
			elems = append(elems, c.defaultValueType(x, "destructuring declaration"))
		}
		c.info.Types[s.Value] = TypeAndValue{Mode: ModeValue, Type: &types.Tuple{Elems: elems}}
	} else if ix, ok := syntax.Unparen(s.Value).(*syntax.IndexExpr); ok {
		// v, ok := m[k]: Go's comma-ok form.
		x := c.expr(s.Value, nil)
		if xt := c.info.TypeOf(ix.X); xt != nil {
			if m, ok := collectionOf(xt).(*types.Map); ok && x.mode != ModeInvalid {
				vt := m.Elem
				if !types.Zeroable(vt) {
					vt = types.MakeNullable(vt)
				}
				c.info.Types[s.Value] = TypeAndValue{Mode: ModeValue, Type: &types.Tuple{Elems: []types.Type{vt, types.Bool}}}
				elems = []types.Type{vt, types.Bool}
			}
		}
		if elems == nil {
			if x.mode != ModeInvalid {
				c.errorf(diag.EMultiValue, s.Value.Span(), "only map lookups and calls returning several values can be destructured")
			}
			declareAll(nil)
			return false
		}
	} else {
		x := c.expr(s.Value, nil)
		if x.mode == ModeInvalid {
			declareAll(nil)
			return false
		}
		if call, ok := syntax.Unparen(s.Value).(*syntax.CallExpr); ok {
			if ci := c.info.Calls[call]; ci != nil && ci.Kind == CallPrelude && ci.Prelude == PreludeReceive {
				// val (v, ok) = ch.receive(): Go's v, ok := <-ch.
				x = &operand{mode: ModeValue, typ: &types.Tuple{Elems: []types.Type{x.typ, types.Bool}}}
				c.info.Types[s.Value] = TypeAndValue{Mode: ModeValue, Type: x.typ}
			}
		}
		switch t := x.typ.(type) {
		case *types.Result:
			elems = rawComponents(t)
			rawNull = make([]bool, len(elems))
			for i, st := range t.Success {
				rawNull[i] = !types.MayBeNull(st) && types.MayBeNull(elems[i])
			}
		case *types.Tuple:
			elems = t.Elems
		default:
			c.errorf(diag.EMultiValue, s.Value.Span(), "cannot destructure a single value of type %s", describeType(x)).
				WithHint("declare it with name := ...")
			declareAll(nil)
			return false
		}
	}
	if len(elems) != len(s.Names) {
		c.errorf(diag.EMultiValue, s.S, "%s returns %s, but %s declared", callName(s.Value), plural(len(elems), "value"), plural(len(s.Names), "name")).
			WithNote("the values are %s", (&types.Tuple{Elems: elems}).String())
		declareAll(nil)
		return false
	}
	declareAll(elems)
	return false
}

// rawComponents returns the types v, err := f() binds (errors.md §5): the
// error is error?, null on success, and a success value is null on failure
// unless its type has a zero value (Z(T), types.md §6).
func rawComponents(r *types.Result) []types.Type {
	out := make([]types.Type, len(r.Raw))
	for i, t := range r.Raw {
		switch {
		case i == len(r.Raw)-1 && types.IsErrorType(t):
			out[i] = types.MakeNullable(types.Error)
		case !types.Zeroable(t) && !types.MayBeNull(t):
			out[i] = types.MakeNullable(t)
		default:
			out[i] = t
		}
	}
	return out
}

func (c *Checker) assignStmt(s *syntax.AssignStmt) {
	if tup, ok := syntax.Unparen(s.Lhs).(*syntax.TupleExpr); ok {
		c.tupleAssign(s, tup)
		return
	}
	if id, ok := syntax.Unparen(s.Lhs).(*syntax.Ident); ok && id.Name == "_" {
		if s.Op != syntax.ASSIGN {
			c.errorf(diag.EInvalidUse, s.OpSpan, "cannot use %s with _", s.Op)
		}
		x := c.expr(s.Rhs, nil)
		if x.mode == ModeNoValue {
			c.errorf(diag.EInvalidUse, s.Rhs.Span(), "%s does not produce a value to discard", s.Rhs.Span().Short())
		} else if x.mode != ModeInvalid {
			if _, isResult := x.typ.(*types.Result); !isResult {
				c.singleValue(x)
			}
		}
		return
	}
	l := c.lhs(s.Lhs)
	if l.mode == ModeInvalid {
		c.expr(s.Rhs, nil)
		return
	}
	if !c.mutablePath(s.Lhs, "assign to", diag.EReassignVal) {
		c.expr(s.Rhs, nil)
		return
	}
	target := l.typ
	if s.Op == syntax.ASSIGN {
		r := c.value(s.Rhs, target)
		c.assign(r, target, "assignment to "+s.Lhs.Span().Short())
		c.assigned(s.Lhs, r.typ)
		return
	}
	defer c.assigned(s.Lhs, nil)
	// Compound assignment: lhs op= rhs.
	op := compoundOp(s.Op)
	bin := &syntax.BinaryExpr{Op: op, X: s.Lhs, Y: s.Rhs, OpSpan: s.OpSpan, S: s.S}
	r := c.value(s.Rhs, types.Base(target))
	if types.IsNullable(target) {
		c.nullableUse(l, "use operator "+s.Op.String()+" on")
		return
	}
	lop := &operand{mode: ModeValue, typ: types.Base(target), expr: s.Lhs}
	if r.mode == ModeInvalid {
		return
	}
	if op == syntax.SHL || op == syntax.SHR {
		c.shift(bin, lop, r)
		return
	}
	if !c.matchOperands(bin, lop, r) {
		return
	}
	t := types.Base(target)
	if c.unconstrained(t) {
		return
	}
	switch op {
	case syntax.ADD:
		if !types.TermsAll(t, func(t types.Type) bool { return types.IsNumeric(t) || types.IsString(t) }) {
			c.errorf(diag.EInvalidOp, s.OpSpan, "operator += is not defined for %s", t)
		}
	case syntax.SUB, syntax.MUL, syntax.QUO:
		if !types.IsNumeric(t) {
			c.errorf(diag.EInvalidOp, s.OpSpan, "operator %s is not defined for %s", s.Op, t)
		}
	default:
		if !types.IsInteger(t) {
			c.errorf(diag.EInvalidOp, s.OpSpan, "operator %s requires integer operands, not %s", s.Op, t)
		}
	}
}

// tupleAssign checks (a, b) = (x, y) and (a, b) = f(): every right-hand
// value is evaluated before any assignment, as in Go, so (a, b) = (b, a)
// swaps.
func (c *Checker) tupleAssign(s *syntax.AssignStmt, lhs *syntax.TupleExpr) {
	if s.Op != syntax.ASSIGN {
		c.errorf(diag.EInvalidUse, s.OpSpan, "%s cannot assign several values; use =", s.Op)
		if rhs, ok := syntax.Unparen(s.Rhs).(*syntax.TupleExpr); ok {
			for _, el := range rhs.Elems {
				c.expr(el, nil)
			}
		} else {
			c.expr(s.Rhs, nil)
		}
		return
	}
	targets := make([]types.Type, len(lhs.Elems)) // nil for _
	ok := true
	for i, el := range lhs.Elems {
		if id, isIdent := syntax.Unparen(el).(*syntax.Ident); isIdent && id.Name == "_" {
			continue
		}
		l := c.lhs(el)
		if l.mode == ModeInvalid || !c.mutablePath(el, "assign to", diag.EReassignVal) {
			ok = false
			continue
		}
		targets[i] = l.typ
	}
	if rhs, isTuple := syntax.Unparen(s.Rhs).(*syntax.TupleExpr); isTuple {
		vals := make([]types.Type, len(lhs.Elems))
		if len(rhs.Elems) != len(lhs.Elems) {
			c.errorf(diag.EMultiValue, s.Rhs.Span(), "%s assigned to %s", plural(len(rhs.Elems), "value"), plural(len(lhs.Elems), "target"))
			for _, el := range rhs.Elems {
				c.expr(el, nil)
			}
			return
		}
		for i, el := range rhs.Elems {
			if targets[i] == nil {
				x := c.value(el, nil)
				if x.mode != ModeInvalid && ok {
					c.defaultValueType(x, "")
				}
				continue
			}
			x := c.value(el, targets[i])
			c.assign(x, targets[i], "assignment to "+lhs.Elems[i].Span().Short())
			vals[i] = x.typ
		}
		for i, el := range lhs.Elems {
			c.assigned(el, vals[i])
		}
		return
	}
	defer func() {
		for _, el := range lhs.Elems {
			c.assigned(el, nil)
		}
	}()
	x := c.expr(s.Rhs, nil)
	if x.mode == ModeInvalid {
		return
	}
	if len(lhs.Elems) == 2 {
		// Go's comma-ok forms: v, ok = m[k] and v, ok = ch.receive().
		if t := c.commaOk(s.Rhs, x); t != nil {
			c.info.Types[s.Rhs] = TypeAndValue{Mode: ModeValue, Type: t}
			x = &operand{mode: ModeValue, typ: t, expr: s.Rhs}
			if vt := t.(*types.Tuple).Elems[0]; targets[0] != nil && types.IsNullable(vt) && Zeroless(types.Base(vt)) {
				c.errorf(diag.ENoZeroValue, s.S, "%s has no zero value, so %s = %s cannot assign it", types.Base(vt), lhs.Span().Short(), s.Rhs.Span().Short()).
					WithHint("assign %s = %s, which is null when there is no value, or declare new variables with :=", lhs.Elems[0].Span().Short(), s.Rhs.Span().Short())
				return
			}
		}
	}
	var elems []types.Type
	switch t := x.typ.(type) {
	case *types.Result:
		elems = rawComponents(t)
	case *types.Tuple:
		elems = t.Elems
	default:
		c.errorf(diag.EMultiValue, s.Rhs.Span(), "cannot assign a single value of type %s to %s", describeType(x), plural(len(lhs.Elems), "target")).
			WithHint("assign several values with a, b = x, y")
		return
	}
	if len(elems) != len(lhs.Elems) {
		c.errorf(diag.EMultiValue, s.S, "%s returns %s, but %s given", callName(s.Rhs), plural(len(elems), "value"), plural(len(lhs.Elems), "target"))
		return
	}
	for i, et := range elems {
		if targets[i] == nil {
			continue
		}
		// The values come from one Go call: they must assign as they are.
		if okA, conv, reason := c.assignable(et, targets[i]); !okA || conv != ConvNone {
			d := c.errorf(diag.ETypeMismatch, lhs.Elems[i].Span(), "cannot assign value %d of %s (type %s) to %s (type %s)", i+1, callName(s.Rhs), et, lhs.Elems[i].Span().Short(), targets[i])
			if conv == ConvNilIface {
				reason = "a null value needs a check to become a nil interface; destructure into new variables (a, b := ...) and assign from them"
			}
			if reason != "" {
				d.WithNote("%s", reason)
			}
		}
	}
}

func compoundOp(k syntax.Kind) syntax.Kind {
	switch k {
	case syntax.ADD_ASSIGN:
		return syntax.ADD
	case syntax.SUB_ASSIGN:
		return syntax.SUB
	case syntax.MUL_ASSIGN:
		return syntax.MUL
	case syntax.QUO_ASSIGN:
		return syntax.QUO
	case syntax.REM_ASSIGN:
		return syntax.REM
	case syntax.AND_ASSIGN:
		return syntax.AND
	case syntax.OR_ASSIGN:
		return syntax.OR
	case syntax.XOR_ASSIGN:
		return syntax.XOR
	case syntax.SHL_ASSIGN:
		return syntax.SHL
	case syntax.SHR_ASSIGN:
		return syntax.SHR
	case syntax.AND_NOT_ASSIGN:
		return syntax.AND_NOT
	}
	return k
}

func (c *Checker) exprStmt(s *syntax.ExprStmt) bool {
	switch x := s.X.(type) {
	case *syntax.JumpExpr:
		c.jump(x)
		c.record(&operand{mode: ModeValue, typ: types.Nothing, expr: x})
		return true
	case *syntax.IfExpr:
		r := c.ifExpr(x, nil, false)
		if x.Else == nil && r.thenTerminates {
			for v, t := range r.falseNarrow {
				c.scope.setNarrow(v, t)
			}
		}
		return r.terminates
	case *syntax.WhenExpr:
		op := c.whenStmt(x)
		return op
	case *syntax.CatchExpr:
		c.catchStmt(x)
		return false
	case *syntax.TryExpr:
		op := c.tryMode(x, true)
		op.expr = x
		c.record(op)
		return false
	case *syntax.ElvisExpr:
		op := c.expr(x, nil)
		if id, ok := syntax.Unparen(x.X).(*syntax.Ident); ok && op.mode != ModeInvalid {
			if v, ok := c.info.Uses[id].(*VarSym); ok && v.Settled() && types.MayBeNull(v.Type) {
				if yt := c.info.TypeOf(x.Y); yt == types.Nothing {
					c.scope.setNarrow(v, types.Base(v.Type))
					return false
				}
			}
		}
		c.errorf(diag.EInvalidUse, x.S, "the value of this ?: expression is not used")
		return false
	}
	x := c.expr(s.X, nil)
	if x.mode == ModeInvalid {
		return false
	}
	switch inner := syntax.Unparen(s.X).(type) {
	case *syntax.CallExpr:
		if r, ok := x.typ.(*types.Result); ok {
			if !c.errorIgnorable(inner) {
				c.unhandledError(inner, r)
			}
			return false
		}
		if ci := c.info.Calls[inner]; ci != nil && ci.Kind == CallBuiltin && ci.Builtin == BuiltinPanic {
			return true
		}
		if ci := c.info.Calls[inner]; ci != nil && (ci.Kind == CallConstruct || ci.Kind == CallConvert) {
			c.errorf(diag.EInvalidUse, s.X.Span(), "the result of %s is not used", s.X.Span().Short())
		}
		return isNothing(x.typ)
	case *syntax.TryExpr, *syntax.CatchExpr:
		return isNothing(x.typ)
	case *syntax.NotNullExpr:
		return false
	}
	if isNothing(x.typ) {
		return true
	}
	c.errorf(diag.EInvalidUse, s.X.Span(), "expression %s is not used", s.X.Span().Short())
	return false
}

// unhandledError reports E0303 for a fallible call used as a statement.
func (c *Checker) unhandledError(call *syntax.CallExpr, r *types.Result) {
	text := call.Span().Short()
	d := c.errorf(diag.EUnhandledError, call.Span(), "the error returned by %s is not handled", call.Fun.Span().Short())
	if c.fn != nil && c.fn.fallible {
		d.WithEdit(call.Span().EndSpan(), "?", "propagate it: %s?", text)
	} else {
		d.WithHint("propagate it with %s? (inside a function that throws)", text)
	}
	binder := "err"
	if c.lookup("err") != nil {
		binder = "e" // inside catch (err) { ... }, a new err would hide it
	}
	d.WithHint("handle it: %s catch (%s) { ... }", text, binder)
	if len(r.Success) == 0 {
		d.WithHint("or discard it explicitly: _ = %s", text)
	}
}

// ---------------------------------------------------------------------------
// if and when

type ifResult struct {
	op             *operand
	terminates     bool
	thenTerminates bool
	falseNarrow    narrowing
}

func (c *Checker) ifExpr(e *syntax.IfExpr, want types.Type, needValue bool) ifResult {
	if e.Init == nil {
		return c.ifBranches(e, want, needValue)
	}
	// if v := f(); cond: the init's names are in scope in every branch.
	var res ifResult
	c.withScope(newScope(c.scope, BlockScope), func() {
		c.stmt(e.Init)
		res = c.ifBranches(e, want, needValue)
	})
	return res
}

func (c *Checker) ifBranches(e *syntax.IfExpr, want types.Type, needValue bool) ifResult {
	_, nt, nf := c.cond(e.Cond)
	var tr, er blockResult
	bf := c.startBranches()
	bf.next()
	c.withScope(c.narrowedScope(nt), func() { tr = c.blockValue(e.Then, want, needValue) })
	bf.end(tr.terminates, tr.flow)
	bf.next()
	if e.Else != nil {
		c.withScope(c.narrowedScope(nf), func() { er = c.blockValue(e.Else, want, needValue) })
		bf.end(er.terminates, er.flow)
	} else {
		c.withScope(c.narrowedScope(nf), func() { bf.end(false, c.flowState()) })
	}
	bf.finish()
	res := ifResult{
		terminates:     e.Else != nil && tr.terminates && er.terminates,
		thenTerminates: tr.terminates,
		falseNarrow:    nf,
	}
	if !needValue {
		res.op = &operand{mode: ModeNoValue, typ: types.Unit}
		return res
	}
	c.info.UsedAsValue[e] = true
	if e.Else == nil {
		c.errorf(diag.EInvalidUse, e.S, "if used as a value needs an else branch")
		res.op = invalid(e)
		return res
	}
	res.op = c.joinBranches([]blockResult{tr, er}, []*syntax.Block{e.Then, e.Else}, want, e)
	return res
}

// joinBranches computes the type of an if/when used as a value.
func (c *Checker) joinBranches(results []blockResult, blocks []*syntax.Block, want types.Type, e syntax.Expr) *operand {
	allTerminate := true
	for i, r := range results {
		if r.terminates {
			continue
		}
		allTerminate = false
		if r.op == nil {
			c.errorf(diag.EInvalidUse, blocks[i].S, "this branch does not produce a value")
			return invalid(e)
		}
	}
	if allTerminate {
		return &operand{mode: ModeValue, typ: types.Nothing}
	}
	if want == nil {
		// if c { 1 } else { 2.5 }: untyped constants of several kinds take
		// the largest kind's type, as Go's constant expressions do.
		var top types.Type
		hasNull, mixed, all := false, false, true
		for _, r := range results {
			switch {
			case r.op == nil || r.terminates:
			case r.op.typ == types.Null:
				hasNull = true
			case constRank(r.op.typ) > 0:
				if top != nil && constRank(r.op.typ) != constRank(top) {
					mixed = true
				}
				if top == nil || constRank(r.op.typ) > constRank(top) {
					top = r.op.typ
				}
			default:
				all = false
			}
		}
		if all && mixed {
			want = types.DefaultType(top)
			if hasNull {
				want = types.MakeNullable(want)
			}
		}
	}
	if want != nil && e == c.widen {
		for _, r := range results {
			if r.op != nil && (r.op.typ == types.Null || types.IsNullable(r.op.typ)) {
				want = c.nullableOf(want, e.Span()) // catch { if c { null } else { 3 } }
				break
			}
		}
	}
	if want != nil {
		ok := true
		for _, r := range results {
			if r.op != nil && !c.assign(r.op, want, "branch value") {
				ok = false
			}
		}
		if !ok {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: want}
	}
	var t types.Type = types.Nothing
	for _, r := range results {
		if r.op == nil {
			continue
		}
		if r.op.mode == ModeInvalid {
			return invalid(e)
		}
		nt, ok := c.join(t, r.op, r.valueExpr)
		if !ok {
			c.errorf(diag.ETypeMismatch, r.valueExpr.Span(), "branch has type %s, which is incompatible with %s", r.op.typ, t).
				WithHint("annotate the expected type, e.g. x: any = ...")
			return invalid(e)
		}
		t = nt
	}
	// A branch joined before a later one widened the type (T, then null or
	// T?) converts to the result too: if c { n } else { null } boxes n.
	for _, r := range results {
		if r.op != nil && t != types.Null && !types.Identical(r.op.typ, t) {
			c.assign(r.op, t, "branch value")
		}
	}
	return &operand{mode: ModeValue, typ: t}
}

// whenStmt checks a when used as a statement and reports termination.
func (c *Checker) whenStmt(e *syntax.WhenExpr) bool {
	_, terminates := c.when(e, nil, false)
	return terminates
}

func (c *Checker) whenExpr(e *syntax.WhenExpr, want types.Type, needValue bool) *operand {
	op, _ := c.when(e, want, needValue)
	return op
}

func (c *Checker) when(e *syntax.WhenExpr, want types.Type, needValue bool) (*operand, bool) {
	scope := newScope(c.scope, BlockScope)
	var subject *operand
	c.withScope(scope, func() {
		if e.Subject != nil {
			subject = c.value(e.Subject, nil)
			if subject.mode != ModeInvalid {
				st := c.defaultValueType(subject, "when subject")
				subject.typ = st
				if e.Bind != nil {
					v := &VarSym{name: e.Bind.Name, Type: st, Kind: LocalVar, Span: e.Bind.S, Used: true}
					c.declareLocal(v, e.Bind)
				}
			}
		}
	})
	var results []blockResult
	var blocks []*syntax.Block
	hasElse := false
	var prevFalse narrowing
	// A when over an enum or sealed interface tracks what its branches cover.
	// Only a non-null subject can be covered; entries resolve unqualified
	// either way.
	var enum, sealed, entries *types.TypeDecl
	// A nullable subject is covered by every entry or member and null; it
	// need not be, as a statement (ADR 0035's rule is for non-null ones).
	var nEnum, nSealed *types.TypeDecl
	nullCovered := false
	if subject != nil && subject.mode != ModeInvalid {
		entries = types.EnumOf(subject.typ)
		if !types.MayBeNull(subject.typ) {
			enum, sealed = entries, types.SealedOf(subject.typ)
		} else {
			nEnum, nSealed = entries, types.SealedOf(subject.typ)
		}
	}
	covered := map[string]bool{}
	// Conditions already seen: Go's switch rejects duplicate cases.
	seen := map[string]source.Span{}
	dup := func(key, what string, span source.Span) {
		if prev, ok := seen[key]; ok {
			c.errorf(diag.EDuplicate, span, "duplicate when condition %s", what).
				WithRelated(prev, "already handled here")
			return
		}
		seen[key] = span
	}
	allTerminate := true
	bf := c.startBranches()
	for i, br := range e.Branches {
		if hasElse {
			c.warnf(diag.WUnreachable, br.S, "branch after else is unreachable")
		}
		bf.next()
		before := prevFalse
		var nt, nf narrowing
		c.withScope(scope, func() {
			if br.Else {
				hasElse = true
				if i != len(e.Branches)-1 {
					c.errorf(diag.EInvalidUse, br.S, "else must be the last branch of when")
				}
				return
			}
			for _, cond := range br.Conds {
				switch cond.Kind {
				case syntax.WhenExprCond:
					if subject == nil {
						c.withScope(c.narrowedScope(before), func() {
							_, t, f := c.cond(cond.X)
							if len(br.Conds) == 1 {
								nt, nf = t, f
							}
						})
					} else {
						var x *operand
						if id, ok := cond.X.(*syntax.Ident); ok && entries != nil && entries.LookupEntry(id.Name) != nil {
							// Red in a when over a Color is Color.Red.
							entry := entries.LookupEntry(id.Name)
							et := types.Base(subject.typ)
							c.info.Entries[cond.X] = entry
							c.info.Types[cond.X] = TypeAndValue{Mode: ModeValue, Type: et}
							x = &operand{mode: ModeValue, typ: et, expr: cond.X}
						} else {
							x = c.value(cond.X, subject.typ)
						}
						if entry := c.info.Entries[cond.X]; entry != nil && (enum != nil && entry.Decl == enum || nEnum != nil && entry.Decl == nEnum) {
							covered[entry.Name] = true
						}
						switch {
						case c.info.Entries[cond.X] != nil:
							entry := c.info.Entries[cond.X]
							dup("entry "+entry.Decl.Name+"."+entry.Name, cond.X.Span().Short(), cond.S)
						case x.mode != ModeInvalid && x.typ == types.Null:
							dup("null", "null", cond.S)
							nullCovered = true
							if v := whenSubjectVar(c, e); v != nil && len(br.Conds) == 1 && types.MayBeNull(v.Type) {
								// when s { null -> ...; else -> s is not null }
								nf = narrowing{v: types.Base(v.Type)}
							}
						case x.mode != ModeInvalid && x.val != nil:
							// 1 and 1.0 differ in a when over any: the key has the type.
							dup("value "+types.DefaultType(x.typ).String()+" "+x.val.ExactString(), cond.X.Span().Short(), cond.S)
						}
						if x.mode != ModeInvalid && subject.mode != ModeInvalid {
							bin := &syntax.BinaryExpr{Op: syntax.EQL, X: e.Subject, Y: cond.X, OpSpan: cond.S, S: cond.S}
							s2 := &operand{mode: ModeValue, typ: subject.typ, expr: e.Subject}
							c.equality(bin, s2, x)
							if types.IsErrorType(subject.typ) && x.typ != types.Null && types.IsErrorType(x.typ) {
								// when err { ErrNotFound -> }: Go's ==, as in a switch.
								subj, val := e.Subject.Span().Short(), cond.X.Span().Short()
								c.warnf(diag.WErrorTypeTest, cond.S, "a when over an error compares with ==, so a wrapped %s does not match", val).
									WithHint("to match through wrapping, test errors.Is in a when without a subject: when { errors.Is(%s, %s) -> ... }", subj, val)
							}
						}
					}
				case syntax.WhenIs, syntax.WhenNotIs:
					t := c.resolveType(cond.Type)
					if subject == nil {
						c.errorf(diag.EInvalidUse, cond.S, "is conditions need a when subject: when x { is T -> ... }")
						continue
					}
					if subject.mode == ModeInvalid || t == types.Invalid {
						continue
					}
					if !c.assertable(subject, t, cond.Type.Span()) {
						continue
					}
					c.info.TypeTests[cond] = t
					if cond.Kind == syntax.WhenIs {
						dup("is "+t.String(), "is "+t.String(), cond.S)
					}
					if cond.Kind == syntax.WhenIs && (sealed != nil || nSealed != nil) {
						covered[memberKey(t)] = true
					}
					if cond.Kind == syntax.WhenIs && len(br.Conds) == 1 {
						if v := whenSubjectVar(c, e); v != nil {
							nt = narrowing{v: t}
						}
					}
				default: // in, !in
					if subject == nil {
						c.errorf(diag.EInvalidUse, cond.S, "in conditions need a when subject: when x { in 1..9 -> ... }")
						c.value(cond.X, nil)
						continue
					}
					if subject.mode == ModeInvalid {
						c.value(cond.X, nil)
						continue
					}
					subj := subject
					if n, ok := subject.typ.(*types.Nullable); ok && !types.IsNilable(n.Elem) {
						// when n { in 1..9 -> } on an int?: null is in nothing.
						subj = &operand{mode: subject.mode, typ: n.Elem, expr: subject.expr}
					}
					c.membership(cond, subj, cond.X, false)
				}
			}
		})
		prevFalse = merge(before, nf)
		var r blockResult
		bodyScope := newScope(scope, BlockScope)
		for v, t := range merge(before, nt) {
			bodyScope.setNarrow(v, t)
		}
		c.withScope(bodyScope, func() { r = c.blockValue(br.Body, want, needValue) })
		bf.end(r.terminates, r.flow)
		if !r.terminates {
			allTerminate = false
		}
		results = append(results, r)
		blocks = append(blocks, br.Body)
	}
	if (nEnum != nil || nSealed != nil) && nullCovered && !hasElse {
		all := true
		if nEnum != nil {
			for _, en := range nEnum.Entries {
				all = all && covered[en.Name]
			}
		} else {
			for _, m := range nSealed.Members {
				all = all && covered[memberKey(m)]
			}
		}
		if all {
			c.info.Exhaustive[e] = subject.typ
			hasElse = true // null and every entry or member have a branch
		}
	}
	if (enum != nil || sealed != nil) && !hasElse {
		var missing []string
		if enum != nil {
			for _, en := range enum.Entries {
				if !covered[en.Name] {
					missing = append(missing, en.Name)
				}
			}
		} else {
			for _, m := range sealed.Members {
				if !covered[memberKey(m)] {
					missing = append(missing, c.qualified(memberName(m, subject.typ), m))
				}
			}
		}
		if len(missing) == 0 {
			c.info.Exhaustive[e] = subject.typ
			hasElse = true // every value has a branch
		} else {
			what := "is "
			if enum != nil {
				what = ""
			}
			c.errorf(diag.EInvalidUse, e.S.At().Join(e.Subject.Span()), "when over %s must cover every %s: missing %s", c.qualified(subject.typ.String(), subject.typ), map[bool]string{true: "entry", false: "member type"}[enum != nil], strings.Join(missing, ", ")).
				WithHint("add %s%s -> ... or an else branch", what, missing[0])
			hasElse = true // reported; avoid a second error for a value use
		}
	}
	if !hasElse {
		// No branch may run.
		bf.next()
		c.withScope(c.narrowedScope(prevFalse), func() { bf.end(false, c.flowState()) })
	}
	bf.finish()
	terminates := hasElse && allTerminate && len(e.Branches) > 0
	if !needValue {
		return &operand{mode: ModeNoValue, typ: types.Unit}, terminates
	}
	c.info.UsedAsValue[e] = true
	if !hasElse {
		c.errorf(diag.EInvalidUse, e.S, "when used as a value needs an else branch")
		return invalid(e), terminates
	}
	return c.joinBranches(results, blocks, want, e), terminates
}

// whenSubjectVar returns the immutable binding a when subject names, which
// is smart-cast by `is` branches.
func whenSubjectVar(c *Checker, e *syntax.WhenExpr) *VarSym {
	if e.Bind != nil {
		v, _ := c.info.Defs[e.Bind].(*VarSym)
		return v
	}
	if v := c.varOf(e.Subject); v != nil && v.Settled() {
		return v
	}
	return nil
}

// ---------------------------------------------------------------------------
// Loops

func (c *Checker) forStmt(s *syntax.ForStmt) {
	scope := newScope(c.scope, BlockScope)
	info := &ForInfo{}
	var keyT, valT types.Type
	twoAllowed := true
	if r, ok := s.Iter.(*syntax.RangeExpr); ok {
		info.Kind = ForRange
		lo := c.value(r.Lo, nil)
		hi := c.value(r.Hi, nil)
		valT = c.rangeType(lo, hi, r)
		twoAllowed = false
		c.record(&operand{mode: ModeValue, typ: valT, expr: r})
	} else {
		x := c.value(s.Iter, nil)
		if x.mode != ModeInvalid {
			if types.IsNullable(x.typ) {
				c.nullableUse(x, "iterate over")
			}
			switch t := collectionOf(x.typ).(type) {
			case *types.Slice:
				info.Kind, keyT, valT = ForSlice, types.Int, t.Elem
			case *types.Array:
				info.Kind, keyT, valT = ForArray, types.Int, t.Elem
			case *types.Pointer:
				if a, ok := collectionOf(t.Elem).(*types.Array); ok {
					info.Kind, keyT, valT = ForArray, types.Int, a.Elem
				} else {
					c.errorf(diag.EInvalidOp, s.Iter.Span(), "cannot iterate over a value of type %s", x.typ)
				}
			case *types.Map:
				info.Kind, keyT, valT = ForMap, t.Key, t.Elem
			case *types.Chan:
				info.Kind, valT = ForChan, t.Elem
				twoAllowed = false
				if t.Dir == types.SendOnly {
					c.errorf(diag.EInvalidOp, s.Iter.Span(), "cannot receive from %s: it is send-only", x.typ)
				}
			default:
				if types.IsString(t) {
					info.Kind, keyT, valT = ForString, types.Int, types.Rune
					if types.IsUntyped(x.typ) {
						c.assign(x, types.String, "")
					}
				} else if yield := iteratorYield(x.typ); yield != nil {
					// Go's range-over-func: iter.Seq[V] binds V, iter.Seq2[K, V]
					// binds K or K and V.
					info.Kind = ForFunc
					switch len(yield.Params) {
					case 0:
						if len(s.Names) != 1 || s.Names[0].Name != "_" {
							c.errorf(diag.EInvalidUse, s.Names[0].S, "this iterator yields no values; write for _ in ...")
						}
					case 1:
						keyT, valT = yield.Params[0], yield.Params[0]
						twoAllowed = false
					case 2:
						keyT, valT = yield.Params[0], yield.Params[1]
					}
				} else if _, isSig := types.Lower(t).Underlying().(*gotypes.Signature); isSig {
					c.errorf(diag.EInvalidOp, s.Iter.Span(), "cannot iterate over a function of type %s", x.typ).
						WithHint("for loops iterate over iterator functions such as iter.Seq[V] and iter.Seq2[K, V]: func(yield func(V) bool)")
				} else {
					c.errorf(diag.EInvalidOp, s.Iter.Span(), "cannot iterate over a value of type %s", x.typ).
						WithHint("for loops iterate over slices, arrays, maps, strings, channels, Go iterators, and ranges like 0..<n")
				}
			}
		}
	}
	if keyT == nil {
		keyT = types.Invalid
	}
	if valT == nil {
		valT = types.Invalid
	}
	var varTypes []types.Type
	switch len(s.Names) {
	case 1:
		if info.Kind == ForMap || info.Kind == ForFunc {
			varTypes = []types.Type{keyT}
		} else {
			varTypes = []types.Type{valT}
		}
	case 2:
		if !twoAllowed {
			c.errorf(diag.EInvalidUse, s.Names[0].S.Join(s.Names[1].S), "this loop has only one variable")
		}
		varTypes = []types.Type{keyT, valT}
	default:
		c.errorf(diag.EInvalidUse, s.S, "for loops declare one or two variables")
	}
	c.withScope(scope, func() {
		for i, id := range s.Names {
			t := types.Type(types.Invalid)
			if i < len(varTypes) {
				t = varTypes[i]
			}
			v := &VarSym{name: id.Name, Type: t, Kind: LoopVar, Span: id.S}
			c.declareLocal(v, id)
			if id.Name == "_" {
				info.Vars = append(info.Vars, nil)
			} else {
				info.Vars = append(info.Vars, v)
			}
		}
		c.forgetLoopAssigned(s.Body)
		c.loop(s.Label, func() { c.block(s.Body) })
	})
	c.info.Fors[s] = info
}

func (c *Checker) rangeType(lo, hi *operand, r *syntax.RangeExpr) types.Type {
	if lo.mode == ModeInvalid || hi.mode == ModeInvalid {
		return types.Invalid
	}
	bin := &syntax.BinaryExpr{Op: syntax.RANGE, X: r.Lo, Y: r.Hi, OpSpan: r.S, S: r.S}
	if !c.matchOperands(bin, lo, hi) {
		return types.Invalid
	}
	t := lo.typ
	if types.IsUntyped(t) {
		t = types.Int
		c.assign(lo, t, "range bound")
		c.assign(hi, t, "range bound")
	}
	if !types.IsInteger(types.Base(t)) {
		c.errorf(diag.ETypeMismatch, r.S, "range bounds must be integers, not %s", t)
		return types.Invalid
	}
	if !r.Exclusive && hi.val != nil && !types.IsSmallInt(t) && !c.representable(constant.BinaryOp(hi.val, token.ADD, constant.MakeInt64(1)), types.Base(t)) {
		// for i := lo; i <= max; i++ never ends: i wraps around. Ranges of
		// smaller integers are counted in int64 (lower.rangeLoop).
		c.errorf(diag.EOverflow, r.Hi.Span(), "a range of %s up to %s never ends: %s is the largest %s, so the counter wraps around", t, hi.val, hi.val, t)
		return types.Invalid
	}
	return types.Base(t)
}

// loop checks a loop body, with the loop's label (if any) available to
// break@label and continue@label inside it.
func (c *Checker) loop(label *syntax.Ident, body func()) {
	if c.fn == nil {
		body()
		return
	}
	if label != nil {
		for _, l := range c.fn.labels {
			if l.Name == label.Name {
				c.errorf(diag.EDuplicate, label.S, "label %s is already used by an enclosing loop", label.Name).
					WithRelated(l.S, "the enclosing loop's label")
				break
			}
		}
		c.fn.labels = append(c.fn.labels, label)
		defer func() { c.fn.labels = c.fn.labels[:len(c.fn.labels)-1] }()
	}
	c.fn.loopDepth++
	body()
	c.fn.loopDepth--
}

// forClauseStmt checks Go's three-part loop (ADR 0047): init in the loop's
// scope, the condition before every iteration, the post statement after the
// body. Like while true, a loop without a condition terminates unless it
// breaks.
func (c *Checker) forClauseStmt(s *syntax.ForClauseStmt) bool {
	terminates := false
	c.withScope(newScope(c.scope, BlockScope), func() {
		if s.Init != nil {
			c.stmt(s.Init)
		}
		c.forgetLoopAssigned(s) // the condition runs again after the body and post
		var nt narrowing
		if s.Cond != nil {
			_, nt, _ = c.cond(s.Cond)
		}
		breaks := c.countBreaks(s.Body, s.Label)
		c.withScope(c.narrowedScope(nt), func() {
			c.loop(s.Label, func() {
				c.block(s.Body)
				if s.Post != nil {
					c.stmt(s.Post)
				}
			})
		})
		terminates = s.Cond == nil && breaks == 0
	})
	return terminates
}

func (c *Checker) whileStmt(s *syntax.WhileStmt) bool {
	c.forgetLoopAssigned(s.Body) // the condition runs again after the body
	cond, nt, _ := c.cond(s.Cond)
	breaks := c.countBreaks(s.Body, s.Label)
	c.withScope(c.narrowedScope(nt), func() { c.loop(s.Label, func() { c.block(s.Body) }) })
	if cond.mode != ModeInvalid && cond.val != nil && constant.BoolVal(cond.val) && breaks == 0 {
		return true
	}
	return false
}

// selectStmt checks select: every branch but else is a channel operation,
// a receive may bind its value (and ok), and the bodies are checked in their
// own scopes. It terminates when every branch body does, as in Go.
func (c *Checker) selectStmt(s *syntax.SelectStmt) bool {
	allTerminate := len(s.Cases) > 0
	hasElse := false
	for _, sc := range s.Cases {
		scope := newScope(c.scope, BlockScope)
		c.withScope(scope, func() {
			if sc.Else {
				if hasElse {
					c.errorf(diag.EDuplicate, sc.S, "select has more than one else branch")
				}
				hasElse = true
			} else {
				c.selectOp(sc)
			}
			if !c.block(sc.Body) {
				allTerminate = false
			}
		})
	}
	return allTerminate
}

// selectOp checks the channel operation of a select branch and declares its
// bindings.
func (c *Checker) selectOp(sc *syntax.SelectCase) {
	x := c.expr(sc.Op, nil)
	prelude := PreludeNone
	if call, ok := syntax.Unparen(sc.Op).(*syntax.CallExpr); ok {
		if ci := c.info.Calls[call]; ci != nil && ci.Kind == CallPrelude {
			prelude = ci.Prelude
		}
	}
	var elems []types.Type
	switch {
	case x.mode == ModeInvalid:
	case prelude == PreludeReceive:
		elems = []types.Type{x.typ, types.Bool}
		if call := syntax.Unparen(sc.Op).(*syntax.CallExpr); c.info.Calls[call].Safe {
			// off?.receive(): a null channel is never ready, as Go's nil
			// channel, so the branch runs only with a received value.
			if sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr); ok {
				if ch, ok := collectionOf(c.info.TypeOf(sel.X)).(*types.Chan); ok {
					elems[0] = ReceiveType(ch)
				}
			}
		}
	case prelude == PreludeSend:
		if sc.Names != nil {
			c.errorf(diag.EInvalidUse, sc.Names[0].S.Join(sc.Names[len(sc.Names)-1].S), "a send has no value to bind")
		}
	default:
		c.errorf(diag.EInvalidUse, sc.Op.Span(), "a select branch must be a channel operation: ch.receive() or ch.send(v)")
	}
	if len(sc.Names) > 2 {
		c.errorf(diag.EMultiValue, sc.Names[2].S, "a receive gives at most two values: v, ok := ch.receive()")
	}
	vars := make([]*VarSym, len(sc.Names))
	for i, id := range sc.Names {
		t := types.Type(types.Invalid)
		if i < len(elems) && (prelude == PreludeReceive) {
			t = elems[i]
		}
		v := c.newLocal(id, t)
		c.declareLocal(v, id)
		if id.Name != "_" {
			vars[i] = v
		}
	}
	c.info.SelectVars[sc] = vars
}

// countBreaks counts break statements that target the loop with this body:
// unlabeled breaks outside nested loops, and break@label anywhere inside.
func (c *Checker) countBreaks(b *syntax.Block, label *syntax.Ident) int {
	n := 0
	var visit func(nd syntax.Node, nested bool)
	visit = func(nd syntax.Node, nested bool) {
		switch x := nd.(type) {
		case *syntax.LambdaExpr:
			return // a break cannot leave a lambda
		case *syntax.ForStmt, *syntax.WhileStmt, *syntax.ForClauseStmt:
			// Nested loops have their own unlabeled breaks; break@label
			// still exits this loop.
			nested = true
		case *syntax.JumpExpr:
			if x.Kind == syntax.JumpBreak {
				if x.Label != nil {
					if label != nil && x.Label.Name == label.Name {
						n++
					}
				} else if !nested {
					n++
				}
			}
		}
		syntax.Children(nd, func(ch syntax.Node) { visit(ch, nested) })
	}
	visit(b, false)
	return n
}

// ---------------------------------------------------------------------------
// defer and go

// deferGo checks a defer statement (ds, nil for go) or a go statement.
func (c *Checker) deferGo(call syntax.Expr, body *syntax.Block, ds *syntax.DeferStmt) {
	isGo := ds == nil
	kw := "defer"
	if isGo {
		kw = "go"
	}
	if body != nil {
		saveFn, saveScope := c.fn, c.scope
		fn := &funcCtx{isLambda: true, sig: &types.Signature{}, name: kw + " block"}
		if saveFn != nil {
			fn.recv, fn.recvDecl = saveFn.recv, saveFn.recvDecl
			fn.reassigned, fn.flowOff = saveFn.reassigned, saveFn.flowOff
			if !isGo && saveFn.fallible && !saveFn.isLambda {
				// ? and throw in a deferred block of a function that can
				// fail set the function's error, unless it has one already.
				fn.fallible = true
				fn.deferOf, fn.deferStmt = saveFn, ds
			}
		}
		c.fn = fn
		c.scope = newScope(saveScope, FuncScope)
		c.scope.fn = fn
		c.stmtList(body.Stmts)
		c.finishLocals(fn)
		c.fn, c.scope = saveFn, saveScope
		return
	}
	inner := syntax.Unparen(call)
	switch x := inner.(type) {
	case *syntax.CallExpr:
		op := c.expr(call, nil)
		if ci := c.info.Calls[x]; ci != nil && ci.Kind == CallPrelude && (ci.Prelude == PreludeSend || ci.Prelude == PreludeReceive) {
			// Go's go and defer take a function call, and a channel
			// operation is not one.
			c.errorf(diag.EInvalidUse, call.Span(), "%s needs a function call; a channel operation is not one", kw).
				WithHint("use a block: %s { %s }", kw, call.Span().Text())
			return
		}
		if r, ok := op.typ.(*types.Result); ok && op.mode != ModeInvalid && !c.errorIgnorable(x) {
			if isGo {
				c.errorf(diag.EUnhandledError, call.Span(), "the error returned by %s would be lost in the goroutine", x.Fun.Span().Short()).
					WithHint("handle it inside: go { %s catch (err) { ... } }", call.Span().Short())
				return
			}
			d := c.errorf(diag.EUnhandledError, call.Span(), "the error returned by deferred call %s would be discarded", x.Fun.Span().Short())
			d.WithHint("handle it: defer %s catch (err) { ... }", call.Span().Short())
			if len(r.Success) == 0 {
				d.WithHint("or discard it explicitly: defer { _ = %s }", call.Span().Short())
			}
		}
	case *syntax.CatchExpr:
		c.catchStmt(x)
	case *syntax.TryExpr:
		// defer f.Close()?: a failure of the deferred call becomes the
		// function's error, unless it is failing already.
		switch fn := c.fn; {
		case isGo:
			c.errorf(diag.EInvalidUse, x.OpSpan, "a goroutine has no caller to return an error to").
				WithHint("handle the error inside: go { %s catch (err) { ... } }", x.X.Span().Short())
			c.expr(x.X, nil)
		case fn != nil && fn.fallible && fn.isLambda:
			c.errorf(diag.EUnsupported, x.OpSpan, "defer with ? is supported in functions and methods, not in lambdas yet").
				WithHint("use defer %s catch (err) { ... }", x.X.Span().Short())
			c.expr(x.X, nil)
		default:
			switch syntax.Unparen(x.X).(type) {
			case *syntax.CallExpr, *syntax.CatchExpr: // f(), or a typed catch chain f() catch (e: T) { }
			default:
				c.errorf(diag.EInvalidUse, call.Span(), "defer needs a function call or a block")
				c.expr(x.X, nil)
				return
			}
			c.tryMode(x, true)
			if fn != nil && fn.fallible {
				fn.deferErr = true
				c.info.FallibleDefers[ds] = true
			}
		}
	default:
		c.errorf(diag.EInvalidUse, call.Span(), "%s needs a function call or a block", kw)
		c.expr(call, nil)
	}
}

// commaOk returns the (value, bool) type of Go's comma-ok forms, a map
// index or a channel receive, already checked as x; otherwise nil.
func (c *Checker) commaOk(e syntax.Expr, x *operand) types.Type {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.IndexExpr:
		if xt := c.info.TypeOf(e.X); xt != nil {
			if m, ok := collectionOf(xt).(*types.Map); ok {
				vt := m.Elem
				if !types.Zeroable(vt) {
					vt = types.MakeNullable(vt)
				}
				return &types.Tuple{Elems: []types.Type{vt, types.Bool}}
			}
		}
	case *syntax.CallExpr:
		if ci := c.info.Calls[e]; ci != nil && ci.Kind == CallPrelude && ci.Prelude == PreludeReceive {
			return &types.Tuple{Elems: []types.Type{x.typ, types.Bool}}
		}
	}
	return nil
}

// iteratorYield returns the yield function's signature when t is a Go
// iterator, func(yield func(...) bool) with at most two yielded values.
func iteratorYield(t types.Type) *types.Signature {
	sig, ok := underlyingSignature(t).(*types.Signature)
	if !ok || len(sig.Params) != 1 || len(sig.Results) != 0 || sig.Throws || sig.Variadic {
		return nil
	}
	yield, ok := underlyingSignature(sig.Params[0]).(*types.Signature)
	if !ok || len(yield.Params) > 2 || len(yield.Results) != 1 || yield.Throws || yield.Variadic ||
		!types.Identical(types.Base(yield.Results[0]), types.Bool) {
		return nil
	}
	return yield
}

// memberKey identifies a member of a sealed interface by its declaration,
// so that is Leaf[int] covers the generic member Leaf.
func memberKey(t types.Type) string {
	b, ptr := types.Base(t), ""
	if p, ok := b.(*types.Pointer); ok {
		b, ptr = types.Base(p.Elem), "*"
	}
	if n, ok := b.(*types.Named); ok && n.Decl != nil {
		return ptr + n.Decl.PkgPath + "." + n.Decl.Name
	}
	return t.String()
}

// memberName spells a sealed member for a when over subject: a generic
// member takes the subject's type arguments (Leaf[int] for a Node[int]).
// qualified prefixes text, the name of the Lyth type t, with its package
// when that is another package, as code here must write it: *shapes.Tri.
func (c *Checker) qualified(text string, t types.Type) string {
	b := types.Base(t)
	if p, ok := b.(*types.Pointer); ok {
		b = types.Base(p.Elem)
	}
	n, ok := b.(*types.Named)
	if !ok || n.Decl == nil || n.Decl.PkgPath == "" || n.Decl.PkgPath == c.pkg.Path || n.Decl.PkgName == "" {
		return text
	}
	star := ""
	if strings.HasPrefix(text, "*") {
		star, text = "*", text[1:]
	}
	return star + n.Decl.PkgName + "." + text
}

func memberName(m, subject types.Type) string {
	b, ptr := types.Base(m), ""
	if p, ok := b.(*types.Pointer); ok {
		b, ptr = types.Base(p.Elem), "*"
	}
	n, ok := b.(*types.Named)
	sn, sok := types.Base(subject).(*types.Named)
	if !ok || n.Decl == nil || len(n.Args) > 0 || len(n.Decl.TypeParams) == 0 || !sok || len(sn.Args) != len(n.Decl.TypeParams) {
		return m.String()
	}
	return ptr + (&types.Named{Decl: n.Decl, Args: sn.Args}).String()
}
