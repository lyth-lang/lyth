package check

import (
	"go/constant"
	"go/token"
	gotypes "go/types"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// operand is the result of checking an expression.
type operand struct {
	mode    Mode
	typ     types.Type
	val     constant.Value
	expr    syntax.Expr
	sym     Symbol      // resolved symbol for identifiers
	builtin BuiltinKind // for ModeBuiltin
	pkg     *Package    // for ModePackage
}

func invalid(e syntax.Expr) *operand { return &operand{mode: ModeInvalid, typ: types.Invalid, expr: e} }

func (c *Checker) record(x *operand) *operand {
	if x.expr != nil {
		c.info.Types[x.expr] = TypeAndValue{Mode: x.mode, Type: x.typ, Value: x.val}
	}
	return x
}

func isNothing(t types.Type) bool { return t == types.Nothing }

// isNilExpr reports typed-nil expressions left by error recovery.
func isNilExpr(e syntax.Expr) bool {
	switch x := e.(type) {
	case *syntax.Ident:
		return x == nil
	case *syntax.CallExpr:
		return x == nil
	case *syntax.LambdaExpr:
		return x == nil
	case *syntax.BadExpr:
		return x == nil
	}
	return false
}

// expr checks e with an optional expected type and records its type.
// The expected type guides inference only; callers check assignability.
func (c *Checker) expr(e syntax.Expr, want types.Type) *operand {
	if e == nil || isNilExpr(e) {
		return &operand{mode: ModeInvalid, typ: types.Invalid}
	}
	x := c.expr0(e, want)
	x.expr = e
	if x.typ == nil {
		x.typ = types.Invalid
	}
	if x.typ == types.Invalid && x.mode != ModePackage && x.mode != ModeBuiltin {
		// Values of invalid type were already reported; stay silent downstream.
		x.mode = ModeInvalid
	}
	return c.record(x)
}

// value checks e and requires a single value (not a type, package, tuple,
// or multi-value result).
func (c *Checker) value(e syntax.Expr, want types.Type) *operand {
	x := c.expr(e, want)
	return c.singleValue(x)
}

func (c *Checker) singleValue(x *operand) *operand {
	switch x.mode {
	case ModeInvalid:
		return x
	case ModeType:
		c.errorf(diag.ENotExpected, x.expr.Span(), "%s is a type, not a value", x.expr.Span().Short()).
			WithHint("construct a value with %s(...)", x.expr.Span().Short())
		return invalid(x.expr)
	case ModePackage:
		c.errorf(diag.ENotExpected, x.expr.Span(), "package %s is not a value", x.expr.Span().Short())
		return invalid(x.expr)
	case ModeBuiltin:
		c.errorf(diag.ENotExpected, x.expr.Span(), "builtin %s must be called", x.expr.Span().Short())
		return invalid(x.expr)
	case ModeNoValue:
		c.errorf(diag.EInvalidUse, x.expr.Span(), "%s does not produce a value", x.expr.Span().Short())
		return invalid(x.expr)
	}
	switch t := x.typ.(type) {
	case *types.Result:
		if cx, ok := syntax.Unparen(x.expr).(*syntax.CatchExpr); ok && cx.Type != nil {
			if ti := c.info.Tries[cx]; ti != nil && ti.CatchType != nil {
				c.errorf(diag.EUnhandledError, x.expr.Span(), "this catch handles only %s; the other errors are not handled", ti.CatchType).
					WithHint("propagate them with ? after the catch block, or end the chain with catch (err) { ... }")
				return invalid(x.expr)
			}
		}
		if len(t.Raw) == 1 {
			// A result consisting only of an error is usable as that error value.
			x.typ = t.Raw[0]
			return x
		}
		c.errorf(diag.EMultiValue, x.expr.Span(), "%s returns %d values %s; it cannot be used as a single value",
			callName(x.expr), len(t.Raw), t).
			WithHint("propagate the error with ?, handle it with catch, or destructure: %s := ...", placeholderNames(len(t.Raw)))
		return invalid(x.expr)
	case *types.Tuple:
		c.errorf(diag.EMultiValue, x.expr.Span(), "%s returns %d values %s; destructure them", callName(x.expr), len(t.Elems), t).
			WithHint("%s := ...", placeholderNames(len(t.Elems)))
		return invalid(x.expr)
	}
	return x
}

func placeholderNames(n int) string {
	names := []string{"a", "b", "c", "d", "e", "f"}
	if n > len(names) {
		n = len(names)
	}
	return strings.Join(names[:n], ", ")
}

func callName(e syntax.Expr) string {
	if call, ok := syntax.Unparen(e).(*syntax.CallExpr); ok {
		return call.Fun.Span().Short() + "(...)"
	}
	return "expression"
}

func (c *Checker) expr0(e syntax.Expr, want types.Type) *operand {
	switch e := e.(type) {
	case *syntax.Ident:
		return c.ident(e)
	case *syntax.BasicLit:
		return c.basicLit(e)
	case *syntax.TemplateLit:
		for _, p := range e.Parts {
			if p.X == nil {
				c.dollarNames(p.S)
			}
			if p.X != nil {
				x := c.value(p.X, nil)
				if x.mode != ModeInvalid {
					c.defaultValueType(x, "string template")
				}
			}
		}
		return &operand{mode: ModeValue, typ: types.String}
	case *syntax.BoolLit:
		return &operand{mode: ModeValue, typ: types.UntypedBool, val: constant.MakeBool(e.Value)}
	case *syntax.EmptyStructLit:
		return &operand{mode: ModeValue, typ: EmptyStruct}
	case *syntax.NullLit:
		return &operand{mode: ModeValue, typ: types.Null}
	case *syntax.ThisExpr:
		if c.fn == nil || c.fn.recv == nil {
			c.errorf(diag.EInvalidUse, e.S, "self is only available inside methods")
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: c.fn.recv.Type, sym: c.fn.recv}
	case *syntax.ParenExpr:
		x := c.expr(e.X, want)
		return &operand{mode: x.mode, typ: x.typ, val: x.val, sym: x.sym, builtin: x.builtin, pkg: x.pkg}
	case *syntax.UnaryExpr:
		return c.unary(e, want)
	case *syntax.BinaryExpr:
		return c.binary(e, want)
	case *syntax.ElvisExpr:
		return c.elvis(e, want)
	case *syntax.CallExpr:
		return c.call(e, want)
	case *syntax.SelectorExpr:
		return c.selector(e, want)
	case *syntax.IndexExpr:
		return c.index(e)
	case *syntax.SliceExpr:
		return c.sliceExpr(e)
	case *syntax.TryExpr:
		return c.tryExpr(e)
	case *syntax.CatchExpr:
		return c.catchExpr(e, want)
	case *syntax.NotNullExpr:
		x := c.value(e.X, nil)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		if !types.MayBeNull(x.typ) {
			c.warnf(diag.WUnnecessary, e.OpSpan, "unnecessary !! on a value of non-nullable type %s", x.typ)
			return &operand{mode: ModeValue, typ: x.typ}
		}
		if x.typ == types.Null {
			return &operand{mode: ModeValue, typ: types.Nothing}
		}
		return &operand{mode: ModeValue, typ: types.Base(x.typ)}
	case *syntax.IfExpr:
		return c.ifExpr(e, want, true).op
	case *syntax.WhenExpr:
		return c.whenExpr(e, want, true)
	case *syntax.LambdaExpr:
		return c.lambda(e, want)
	case *syntax.ListLit:
		return c.listLit(e, want)
	case *syntax.MapLit:
		return c.mapLit(e, want)
	case *syntax.JumpExpr:
		c.jump(e)
		return &operand{mode: ModeValue, typ: types.Nothing}
	case *syntax.RangeExpr:
		c.errorf(diag.EInvalidUse, e.S, "ranges can only be used in for loops and in tests (x in a..b)")
		c.value(e.Lo, nil)
		c.value(e.Hi, nil)
		return invalid(e)
	case *syntax.InstantiateExpr:
		return c.instantiateExpr(e)
	case *syntax.IsExpr:
		x := c.value(e.X, nil)
		t := c.resolveType(e.Type)
		if x.mode == ModeInvalid || t == types.Invalid || !c.assertable(x, t, e.Type.Span()) {
			return invalid(e)
		}
		c.info.TypeTests[e] = t
		return &operand{mode: ModeValue, typ: types.Bool}
	case *syntax.AsExpr:
		x := c.value(e.X, nil)
		t := c.resolveType(e.Type)
		if x.mode == ModeInvalid || t == types.Invalid || !c.assertable(x, t, e.Type.Span()) {
			return invalid(e)
		}
		c.info.TypeTests[e] = t
		if e.Safe {
			return &operand{mode: ModeValue, typ: types.MakeNullable(t)}
		}
		return &operand{mode: ModeValue, typ: t}
	case *syntax.TupleExpr:
		c.errorf(diag.EInvalidUse, e.S, "tuples can only be used in assignments and destructuring declarations").
			WithHint("swap with a, b = b, a; declare with a, b := x, y")
		for _, el := range e.Elems {
			c.expr(el, nil)
		}
		return invalid(e)
	case *syntax.InExpr:
		x := c.value(e.X, nil)
		if !c.membership(e, x, e.Y, true) {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: types.Bool}
	case *syntax.TypeOperand:
		t := c.resolveType(e.Type)
		return &operand{mode: ModeType, typ: t}
	case *syntax.BadExpr:
		return invalid(e)
	}
	c.errorf(diag.EInternal, e.Span(), "internal error: unhandled expression %T", e)
	return invalid(e)
}

// ---------------------------------------------------------------------------
// Identifiers and literals

func (c *Checker) ident(e *syntax.Ident) *operand {
	if e.Name == "_" {
		c.errorf(diag.EInvalidUse, e.S, "_ cannot be used as a value")
		return invalid(e)
	}
	var sym Symbol
	if m := c.implicitMember(e.Name); m != nil && !c.callsOther(e, m) {
		if c.bareMember(e, m) {
			return invalid(e)
		}
		sym = m
	} else {
		sym = c.lookup(e.Name)
	}
	if sym == nil {
		d := c.errorf(diag.EUndefined, e.S, "undefined: %s", e.Name)
		if path, ok := stdPackages[e.Name]; ok {
			d.WithHint("add the import: import %q", path)
		} else if t, ok := oldTypeNames[e.Name]; ok {
			d.WithFix(e.S, t, "Lyth uses Go's type names: %s", t)
		} else if h, ok := kotlinBuilders[e.Name]; ok {
			d.WithHint("%s", h)
		} else if s := c.suggest(e.Name); s != "" {
			d.WithHint("did you mean %s?", s)
		}
		if e.Name == "nil" {
			d.Hints = nil
			d.WithFix(e.S, "null", "Lyth writes null")
		}
		return invalid(e)
	}
	c.info.Uses[e] = sym
	switch s := sym.(type) {
	case *VarSym:
		s.Used = true
		if s.owner != nil && s.owner != c.fn {
			s.captured = true // used by a lambda, go block, or defer block
		}
		t := s.Type
		if s.Kind == GlobalVar {
			t = c.globalType(s)
		}
		if t == nil {
			c.errorf(diag.EInvalidUse, e.S, "%s is used before its type is known", e.Name)
			return invalid(e)
		}
		if nt, ok := c.scope.narrowed(s); ok {
			switch {
			case types.IdenticalIgnoringNull(nt, t):
				if !types.IsNilable(types.Base(t)) && types.IsNullable(t) {
					c.info.Implicit[e] = ConvUnbox
				}
			default:
				// Narrowed by a type test: read as x.(T).
				c.info.Implicit[e] = ConvAssert
				c.info.Asserts[e] = nt
			}
			t = nt
		}
		return &operand{mode: ModeValue, typ: t, sym: s}
	case *ConstSym:
		c.constValue(s)
		return &operand{mode: ModeValue, typ: s.Type, val: s.Val, sym: s}
	case *FuncSym:
		c.ensureInferred(s)
		if s.Sig == nil || s.inferCycle {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: s.Sig, sym: s}
	case *TypeSym:
		if s.Generic != "" {
			// List, Map, Chan: Kotlin-era names, not Lyth types.
			d := c.errorf(diag.EUndefined, e.S, "undefined: %s", e.Name)
			switch s.Generic {
			case "List", "MutableList", "ArrayList":
				d.WithHint("Lyth writes a list as a literal: [1, 2, 3], of type []int")
			case "Map", "HashMap":
				d.WithHint("Lyth writes a map as a literal: [\"a\": 1], of type map[string]int")
			default:
				d.WithHint("channels are made with make(chan T, n)")
			}
			return invalid(e)
		}
		if s.Alias != nil {
			return &operand{mode: ModeType, typ: c.aliasType(s), sym: s}
		}
		return &operand{mode: ModeType, typ: s.Type, sym: s}
	case *PkgSym:
		s.Used = true
		if s.Pkg == nil {
			return invalid(e)
		}
		return &operand{mode: ModePackage, pkg: s.Pkg, sym: s}
	case *BuiltinSym:
		return &operand{mode: ModeBuiltin, builtin: s.Kind, sym: s}
	case *MemberSym:
		s.Recv.Used = true
		return &operand{mode: ModeValue, typ: s.Type, sym: s}
	}
	return invalid(e)
}

// dollarNames reports $name in the source text of a string, which prints
// literally, when name is a variable or constant in scope: that is Kotlin's
// template spelling, and Lyth's is ${name}. Any other $name is text, as in
// MongoDB's "$set", JSON Schema's "$ref", or PostgreSQL's $body$ (ADR 0042).
// The two possible edits mean different things, so neither is a fix.
func (c *Checker) dollarNames(span source.Span) {
	if span.File == nil || span.End > len(span.File.Content) {
		return
	}
	src := span.File.Content[span.Start:span.End]
	for i := 0; i < len(src); i++ {
		if src[i] != '$' || i+1 >= len(src) {
			continue
		}
		escapes := 0
		for j := i - 1; j >= 0 && src[j] == '\\'; j-- {
			escapes++
		}
		if escapes%2 == 1 {
			continue // \$name keeps the dollar
		}
		end := i + 1
		for end < len(src) {
			r, size := utf8.DecodeRune(src[end:])
			if !(r == '_' || unicode.IsLetter(r) || end > i+1 && unicode.IsDigit(r)) {
				break
			}
			end += size
		}
		if end == i+1 {
			continue // $1, $$, a lone $
		}
		name := string(src[i+1 : end])
		switch sym := c.lookupSkippingMembers(name).(type) {
		case *VarSym, *ConstSym:
			at := source.Span{File: span.File, Start: span.Start + i, End: span.Start + end}
			c.errorf(diag.EDollarTemplate, at, "\"$%s\" prints literally; templates are written ${%s}", name, name).
				WithRelated(sym.DeclSpan(), "%s is declared here", name).
				WithHint("write ${%s} to interpolate it, or \\$%s to keep the dollar sign", name, name)
		}
		i = end - 1
	}
}

func (c *Checker) basicLit(e *syntax.BasicLit) *operand {
	switch e.Kind {
	case syntax.INT:
		v := constant.MakeFromLiteral(strings.ReplaceAll(e.Raw, "_", ""), token.INT, 0)
		if v.Kind() == constant.Unknown {
			c.errorf(diag.EInvalidNumber, e.S, "invalid integer literal %s", e.Raw)
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: types.UntypedInt, val: v}
	case syntax.FLOAT:
		v := constant.MakeFromLiteral(strings.ReplaceAll(e.Raw, "_", ""), token.FLOAT, 0)
		if v.Kind() == constant.Unknown {
			c.errorf(diag.EInvalidNumber, e.S, "invalid float literal %s", e.Raw)
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: types.UntypedFloat, val: v}
	case syntax.RUNE:
		r := []rune(e.Value)
		if len(r) != 1 {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: types.UntypedRune, val: constant.MakeInt64(int64(r[0]))}
	case syntax.STRING:
		c.dollarNames(e.S)
		return &operand{mode: ModeValue, typ: types.UntypedString, val: constant.MakeString(e.Value)}
	case syntax.RAWSTRING:
		return &operand{mode: ModeValue, typ: types.UntypedString, val: constant.MakeString(e.Value)}
	}
	return invalid(e)
}

// ---------------------------------------------------------------------------
// Unary and binary operators

func (c *Checker) unary(e *syntax.UnaryExpr, want types.Type) *operand {
	switch e.Op {
	case syntax.AND:
		return c.addressOf(e)
	case syntax.MUL:
		x := c.value(e.X, nil)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		if types.IsNullable(x.typ) {
			c.nullableUse(x, "dereference")
			return invalid(e)
		}
		p, ok := types.Base(x.typ).(*types.Pointer)
		if !ok {
			c.errorf(diag.EInvalidOp, e.S, "cannot dereference %s (type %s is not a pointer)", e.X.Span().Short(), x.typ)
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: p.Elem}
	}
	x := c.value(e.X, want)
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	if types.IsNullable(x.typ) {
		c.nullableUse(x, "use operator "+e.Op.String()+" on")
		return invalid(e)
	}
	t := types.Base(x.typ)
	switch e.Op {
	case syntax.NOT:
		if !types.IsBoolean(t) {
			c.errorf(diag.EInvalidOp, e.S, "operator ! requires a bool operand, not %s", t)
			return invalid(e)
		}
		if x.val != nil {
			return &operand{mode: ModeValue, typ: t, val: constant.UnaryOp(token.NOT, x.val, 0)}
		}
		return &operand{mode: ModeValue, typ: t}
	case syntax.SUB, syntax.ADD:
		if !types.IsNumeric(t) {
			c.errorf(diag.EInvalidOp, e.S, "operator %s requires a numeric operand, not %s", e.Op, t)
			return invalid(e)
		}
		if x.val != nil {
			op := token.SUB
			if e.Op == syntax.ADD {
				op = token.ADD
			}
			return &operand{mode: ModeValue, typ: t, val: constant.UnaryOp(op, x.val, 0)}
		}
		return &operand{mode: ModeValue, typ: t}
	case syntax.XOR:
		if !types.IsInteger(t) {
			c.errorf(diag.EInvalidOp, e.S, "operator ^ requires an integer operand, not %s", t)
			return invalid(e)
		}
		if x.val != nil && !types.IsUntyped(t) {
			prec := uint(0)
			if types.IsUnsigned(t) {
				prec = uint(sizeOfBasic(types.BasicOf(t)) * 8)
			}
			return &operand{mode: ModeValue, typ: t, val: constant.UnaryOp(token.XOR, x.val, prec)}
		}
		if x.val != nil {
			return &operand{mode: ModeValue, typ: t, val: constant.UnaryOp(token.XOR, x.val, 0)}
		}
		return &operand{mode: ModeValue, typ: t}
	}
	return invalid(e)
}

func sizeOfBasic(b *types.Basic) int {
	switch b.Kind {
	case types.Int8Kind, types.UInt8Kind:
		return 1
	case types.Int16Kind, types.UInt16Kind:
		return 2
	case types.Int32Kind, types.UInt32Kind, types.Float32Kind:
		return 4
	}
	return 8
}

func (c *Checker) addressOf(e *syntax.UnaryExpr) *operand {
	inner := syntax.Unparen(e.X)
	if call, ok := inner.(*syntax.CallExpr); ok {
		x := c.expr(e.X, nil)
		if ci := c.info.Calls[call]; ci != nil && ci.Kind == CallConstruct {
			if x.mode == ModeInvalid {
				return invalid(e) // reported; a *invalid type would cascade
			}
			return &operand{mode: ModeValue, typ: &types.Pointer{Elem: x.typ}}
		}
		if x.mode != ModeInvalid {
			c.errorf(diag.EInvalidOp, e.S, "cannot take the address of a call result").
				WithHint("store the value in a variable first")
		}
		return invalid(e)
	}
	x := c.value(e.X, nil)
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	if !c.mutablePath(e.X, "take the address of", diag.EAddressOfVal) {
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: &types.Pointer{Elem: x.typ}}
}

func (c *Checker) binary(e *syntax.BinaryExpr, want types.Type) *operand {
	switch e.Op {
	case syntax.LAND, syntax.LOR:
		x, _, _ := c.cond(e)
		return x
	}
	x := c.value(e.X, nil)
	if u, ok := e.Y.(*syntax.UnaryExpr); ok && e.Op == syntax.LSS && u.Op == syntax.SUB && u.OpSpan.Start == e.OpSpan.End && x.mode != ModeInvalid {
		// Go's send statement, ch <- v, reads as ch < -v.
		if _, isChan := types.Base(x.typ).(*types.Chan); isChan {
			c.errorf(diag.EUnsupportedSyntax, e.OpSpan.Join(u.OpSpan), "Lyth sends on a channel with a method").
				WithHint("write %s.send(%s)", e.X.Span().Text(), u.X.Span().Text())
			c.value(u.X, nil)
			return invalid(e)
		}
	}
	y := c.value(e.Y, nil)
	if x.mode == ModeInvalid || y.mode == ModeInvalid {
		return invalid(e)
	}
	switch e.Op {
	case syntax.EQL, syntax.NEQ:
		return c.equality(e, x, y)
	case syntax.SHL, syntax.SHR:
		return c.shift(e, x, y)
	}
	// Arithmetic, ordering, and bitwise operators need matching non-null operands.
	for _, o := range []*operand{x, y} {
		if types.MayBeNull(o.typ) && !types.IsPlatform(o.typ) {
			c.nullableUse(o, "use operator "+e.Op.String()+" on")
			return invalid(e)
		}
	}
	if !c.matchOperands(e, x, y) {
		return invalid(e)
	}
	t := types.Base(x.typ)
	ordering := e.Op == syntax.LSS || e.Op == syntax.LEQ || e.Op == syntax.GTR || e.Op == syntax.GEQ
	switch {
	case ordering:
		if !types.TermsAll(t, func(t types.Type) bool { return (types.IsNumeric(t) || types.IsString(t)) && !types.IsComplex(t) }) {
			c.errorf(diag.EInvalidOp, e.OpSpan, "operator %s is not defined for %s", e.Op, t)
			return invalid(e)
		}
		if x.val != nil && y.val != nil {
			return &operand{mode: ModeValue, typ: types.UntypedBool, val: constant.MakeBool(constant.Compare(x.val, goToken(e.Op), y.val))}
		}
		return &operand{mode: ModeValue, typ: types.Bool}
	case e.Op == syntax.ADD:
		if !types.TermsAll(t, func(t types.Type) bool { return types.IsNumeric(t) || types.IsString(t) }) {
			c.errorf(diag.EInvalidOp, e.OpSpan, "operator + is not defined for %s", t)
			return invalid(e)
		}
	case e.Op == syntax.SUB || e.Op == syntax.MUL || e.Op == syntax.QUO:
		if !types.IsNumeric(t) {
			c.errorf(diag.EInvalidOp, e.OpSpan, "operator %s is not defined for %s", e.Op, t)
			return invalid(e)
		}
	case e.Op == syntax.REM || e.Op == syntax.AND || e.Op == syntax.AND_NOT || e.Op == syntax.OR || e.Op == syntax.XOR:
		if !types.IsInteger(t) {
			c.errorf(diag.EInvalidOp, e.OpSpan, "operator %s requires integer operands, not %s", e.Op, t)
			return invalid(e)
		}
	}
	if x.val != nil && y.val != nil {
		if (e.Op == syntax.QUO || e.Op == syntax.REM) && constSign(y.val) == 0 {
			c.errorf(diag.EInvalidOp, e.Y.Span(), "division by zero")
			return invalid(e)
		}
		op := goToken(e.Op)
		if e.Op == syntax.QUO && types.IsInteger(t) {
			op = token.QUO_ASSIGN // integer division
		}
		v := constant.BinaryOp(x.val, op, y.val)
		if !types.IsUntyped(t) {
			if !c.representable(v, t) {
				c.errorf(diag.EOverflow, e.S, "constant %s overflows %s", v, t)
				return invalid(e)
			}
		}
		return &operand{mode: ModeValue, typ: t, val: v}
	}
	if (e.Op == syntax.QUO || e.Op == syntax.REM) && y.val != nil && constSign(y.val) == 0 && types.IsInteger(t) {
		c.errorf(diag.EInvalidOp, e.Y.Span(), "division by zero")
	}
	return &operand{mode: ModeValue, typ: t}
}

func goToken(k syntax.Kind) token.Token {
	switch k {
	case syntax.ADD:
		return token.ADD
	case syntax.SUB:
		return token.SUB
	case syntax.MUL:
		return token.MUL
	case syntax.QUO:
		return token.QUO
	case syntax.REM:
		return token.REM
	case syntax.AND:
		return token.AND
	case syntax.AND_NOT:
		return token.AND_NOT
	case syntax.OR:
		return token.OR
	case syntax.XOR:
		return token.XOR
	case syntax.SHL:
		return token.SHL
	case syntax.SHR:
		return token.SHR
	case syntax.EQL:
		return token.EQL
	case syntax.NEQ:
		return token.NEQ
	case syntax.LSS:
		return token.LSS
	case syntax.LEQ:
		return token.LEQ
	case syntax.GTR:
		return token.GTR
	case syntax.GEQ:
		return token.GEQ
	case syntax.LAND:
		return token.LAND
	case syntax.LOR:
		return token.LOR
	}
	return token.ILLEGAL
}

// matchOperands converts untyped operands to the other operand's type and
// requires identical types otherwise.
func (c *Checker) matchOperands(e *syntax.BinaryExpr, x, y *operand) bool {
	xu, yu := types.IsUntyped(x.typ), types.IsUntyped(y.typ)
	switch {
	case xu && yu:
		// Untyped constant arithmetic: the result kind is the "larger" kind.
		if x.val != nil && y.val != nil {
			k := x.typ.(*types.Basic).Kind
			if yk := y.typ.(*types.Basic).Kind; yk > k {
				k = yk
			}
			if types.IsString(x.typ) != types.IsString(y.typ) || types.IsBoolean(x.typ) != types.IsBoolean(y.typ) {
				c.operandMismatch(e, x, y)
				return false
			}
			t := untypedOfKind(k)
			x.typ, y.typ = t, t
		}
		return true
	case xu:
		return c.convertUntyped(x, y.typ, e)
	case yu:
		return c.convertUntyped(y, x.typ, e)
	}
	if !types.IdenticalIgnoringNull(x.typ, y.typ) && !c.interfaceComparable(x.typ, y.typ) {
		c.operandMismatch(e, x, y)
		return false
	}
	return true
}

// interfaceComparable reports whether one operand has an interface type the
// other implements, which Go allows to compare (the comparison is true when
// the dynamic type and value match).
func (c *Checker) interfaceComparable(xt, yt types.Type) bool {
	xb, yb := types.Base(xt), types.Base(yt)
	if types.IsInterface(xb) {
		if ok, _, _ := c.assignable(yb, xb); ok {
			return true
		}
	}
	if types.IsInterface(yb) {
		if ok, _, _ := c.assignable(xb, yb); ok {
			return true
		}
	}
	return false
}

func untypedOfKind(k types.BasicKind) types.Type {
	switch k {
	case types.UntypedBoolKind:
		return types.UntypedBool
	case types.UntypedIntKind:
		return types.UntypedInt
	case types.UntypedRuneKind:
		return types.UntypedRune
	case types.UntypedFloatKind:
		return types.UntypedFloat
	}
	return types.UntypedString
}

func (c *Checker) operandMismatch(e *syntax.BinaryExpr, x, y *operand) {
	d := c.errorf(diag.EOperandMismatch, e.OpSpan, "mismatched types %s and %s in %s", x.typ, y.typ, e.Op).
		WithRelated(e.X.Span(), "%s", x.typ.String()).
		WithRelated(e.Y.Span(), "%s", y.typ.String())
	xt, yt := types.Base(x.typ), types.Base(y.typ)
	if types.IsNumeric(xt) && types.IsNumeric(yt) {
		// Suggest converting the narrower operand (int -> float, small -> large).
		target, conv := xt, e.Y
		if types.IsFloat(yt) && !types.IsFloat(xt) {
			target, conv = yt, e.X
		}
		d.WithEdit(conv.Span(), target.String()+"("+conv.Span().Text()+")",
			"Lyth never converts numbers implicitly; write %s(%s)", target, conv.Span().Short())
	}
	// arr == [1, 2]: an operator gives a list literal no expected type, so it
	// is a slice; the typed literal spells the array.
	for _, side := range []struct {
		lit   syntax.Expr
		other types.Type
	}{{e.Y, xt}, {e.X, yt}} {
		if l, ok := syntax.Unparen(side.lit).(*syntax.ListLit); ok && l.Type == nil {
			if _, isArray := side.other.(*types.Array); isArray {
				text := l.S.Text()
				d.WithEdit(l.S, side.other.String()+"{"+text[1:len(text)-1]+"}", "write the array literal %s{...}", side.other)
			}
		}
	}
}

// convertUntyped converts an untyped operand to target, reporting errors.
func (c *Checker) convertUntyped(x *operand, target types.Type, e *syntax.BinaryExpr) bool {
	tb := types.Base(target)
	if x.typ == types.Null {
		return true
	}
	if x.val == nil {
		// Untyped bool from a comparison.
		if types.IsBoolean(x.typ) && types.IsBoolean(tb) {
			x.typ = tb
			c.record(x)
			return true
		}
	}
	if nb, ok := tb.(*types.Nullable); ok {
		tb = types.Base(nb.Elem)
	}
	if types.IsInterface(tb) {
		// Compared with an interface, a constant takes its default type,
		// which must implement the interface (x == "a" for x: Any).
		def := types.DefaultType(x.typ)
		if ok, _, _ := c.assignable(def, tb); ok {
			tb = def
		}
	}
	ok := false
	switch {
	case types.IsBoolean(x.typ):
		ok = types.IsBoolean(tb)
	case types.IsString(x.typ):
		ok = types.IsString(tb)
	case types.IsNumeric(x.typ):
		ok = types.IsNumeric(tb) && (x.val == nil || c.representable(x.val, tb))
	}
	if !ok {
		if e != nil {
			if x.val != nil && types.IsNumeric(x.typ) && types.IsNumeric(tb) {
				c.errorf(diag.EOverflow, x.expr.Span(), "constant %s cannot be represented as %s", x.val, tb)
			} else if other := (&operand{typ: target, expr: x.expr}); x.expr == e.X {
				c.operandMismatch(e, x, other)
			} else {
				c.operandMismatch(e, other, x)
			}
		}
		return false
	}
	x.typ = tb
	if x.val != nil {
		x.val = c.convertConst(x.val, tb)
	}
	c.record(x)
	return true
}

func (c *Checker) shift(e *syntax.BinaryExpr, x, y *operand) *operand {
	if !types.IsInteger(types.Base(x.typ)) && !(types.IsUntyped(x.typ) && x.val != nil && isIntegral(x.val)) {
		c.errorf(diag.EInvalidOp, e.X.Span(), "shifted operand must be an integer, not %s", x.typ)
		return invalid(e)
	}
	if !types.IsInteger(types.Base(y.typ)) && !(y.val != nil && isIntegral(y.val)) {
		c.errorf(diag.EInvalidOp, e.Y.Span(), "shift count must be an integer, not %s", y.typ)
		return invalid(e)
	}
	if y.val != nil && constSign(y.val) < 0 {
		c.errorf(diag.EInvalidOp, e.Y.Span(), "negative shift count %s", y.val)
		return invalid(e)
	}
	if types.IsUntyped(y.typ) {
		c.convertUntyped(y, types.UInt, nil)
	}
	if x.val != nil && y.val != nil {
		// An integral float constant (1e3) is an integer count; a count
		// beyond go/types' limit for constant shifts is too large.
		const maxShift = 1023 - 1 + 52
		s, exact := constant.Uint64Val(constant.ToInt(y.val))
		if !exact || s > maxShift {
			c.errorf(diag.EOverflow, e.Y.Span(), "shift count %s is too large", y.val)
			return invalid(e)
		}
		v := constant.Shift(constant.ToInt(x.val), goToken(e.Op), uint(s))
		t := x.typ
		if types.IsUntyped(t) {
			t = types.UntypedInt
		} else if !c.representable(v, t) {
			c.errorf(diag.EOverflow, e.S, "constant %s overflows %s", v, t)
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: t, val: v}
	}
	t := types.Base(x.typ)
	if types.IsUntyped(t) {
		t = types.Int
		c.convertUntyped(x, t, nil)
	}
	return &operand{mode: ModeValue, typ: t}
}

func isIntegral(v constant.Value) bool {
	return constant.ToInt(v).Kind() == constant.Int
}

func (c *Checker) equality(e *syntax.BinaryExpr, x, y *operand) *operand {
	// Comparisons with null.
	if x.typ == types.Null || y.typ == types.Null {
		other := x
		if x.typ == types.Null {
			other = y
		}
		if other.typ == types.Null {
			return &operand{mode: ModeValue, typ: types.Bool}
		}
		b := collectionOf(other.typ) // r.Trailer == null on an http.Header
		_, isSlice := b.(*types.Slice)
		_, isMap := b.(*types.Map)
		if !types.MayBeNull(other.typ) && !isSlice && !isMap {
			if !types.IsNilable(other.typ) {
				// Go cannot compare a string or a struct with nil.
				c.errorf(diag.ETypeMismatch, e.S, "%s has non-nullable type %s, so it is never null",
					other.expr.Span().Short(), other.typ).
					WithHint("remove the comparison, or declare the type %s? if the value can be absent", other.typ)
				return invalid(e)
			}
			if !types.IsInterface(other.typ) {
				// An interface value may still be nil when Go code stored
				// it (json.Unmarshal into []any), so only other types warn.
				c.warnf(diag.WUnnecessary, e.S, "%s has non-nullable type %s, so this comparison is always %v",
					other.expr.Span().Short(), other.typ, e.Op == syntax.NEQ)
			}
		}
		return &operand{mode: ModeValue, typ: types.Bool}
	}
	if !c.matchOperands(e, x, y) {
		return invalid(e)
	}
	if x.val != nil && y.val != nil {
		return &operand{mode: ModeValue, typ: types.UntypedBool, val: constant.MakeBool(constant.Compare(x.val, goToken(e.Op), y.val))}
	}
	for _, o := range []*operand{x, y} {
		if !comparable(o.typ) {
			c.errorf(diag.EInvalidOp, e.OpSpan, "%s values cannot be compared with %s", o.typ, e.Op)
			return invalid(e)
		}
	}
	if path, ft := optionalPart(x.typ); path != "" {
		c.errorf(diag.EInvalidOp, e.OpSpan, "%s values cannot be compared with %s: %s has type %s, which Go would compare by address", types.Base(x.typ), e.Op, path, ft).
			WithHint("compare the fields you need, unwrapping optional ones")
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: types.Bool}
}

// optionalPart returns the first field or element of a struct or array type
// that holds an optional value (int?, P?), with its type, or "". Go compares
// such values by the boxes' addresses, not by content.
func optionalPart(t types.Type) (string, types.Type) {
	return optionalPartSeen(t, map[*types.TypeDecl]bool{})
}

func optionalPartSeen(t types.Type, seen map[*types.TypeDecl]bool) (string, types.Type) {
	switch b := types.Base(t).(type) {
	case *types.Array:
		if types.IsNullable(b.Elem) && !types.IsNilable(b.Elem) {
			return "an element", b.Elem
		}
		return optionalPartSeen(b.Elem, seen)
	case *types.Named:
		if b.Decl != nil && b.Decl.Kind == types.StructDecl && !seen[b.Decl] {
			seen[b.Decl] = true
			bind := declBindings(b)
			for _, f := range b.Decl.Fields {
				t := f.Type
				if bind != nil {
					t = subst(t, bind)
				}
				if types.IsNullable(t) && !types.IsNilable(t) {
					return "field " + f.Name, t
				}
				if p, ft := optionalPartSeen(t, seen); p != "" {
					return "field " + f.Name + " (" + p + ")", ft
				}
			}
		}
	}
	return "", nil
}

func comparable(t types.Type) bool {
	return comparableSeen(t, map[*types.TypeDecl]bool{})
}

// comparableSeen is comparable, stopping at a struct already being checked
// (struct S { s: S } is reported elsewhere as an invalid recursive type).
func comparableSeen(t types.Type, seen map[*types.TypeDecl]bool) bool {
	switch b := types.Base(t).(type) {
	case *types.Slice, *types.Map, *types.Signature:
		return false
	case *types.Named:
		if b.Decl != nil && b.Decl.Kind == types.StructDecl {
			if seen[b.Decl] {
				return true
			}
			seen[b.Decl] = true
			bind := declBindings(b) // Pair[string, int]: the fields' types
			for _, f := range b.Decl.Fields {
				ft := f.Type
				if bind != nil {
					ft = subst(ft, bind)
				}
				if !comparableSeen(ft, seen) {
					return false
				}
			}
			return true
		}
	}
	return isGoComparable(t)
}

// ---------------------------------------------------------------------------
// Conditions and smart casts

type narrowing map[*VarSym]types.Type

// cond checks a Bool condition and computes the smart casts that hold when it
// is true and when it is false.
func (c *Checker) cond(e syntax.Expr) (*operand, narrowing, narrowing) {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.LAND, syntax.LOR:
			l, lt, lf := c.cond(x.X)
			// The right operand sees the left operand's narrowings.
			scope := newScope(c.scope, BlockScope)
			apply := lt
			if x.Op == syntax.LOR {
				apply = lf
			}
			for v, t := range apply {
				scope.setNarrow(v, t)
			}
			var r *operand
			var rt, rf narrowing
			c.withScope(scope, func() { r, rt, rf = c.cond(x.Y) })
			res := &operand{mode: ModeValue, typ: types.Bool, expr: e}
			if l.mode == ModeInvalid || r.mode == ModeInvalid {
				res = invalid(e)
			}
			c.record(res)
			if x != e {
				c.record(&operand{mode: res.mode, typ: res.typ, expr: x})
			}
			if x.Op == syntax.LAND {
				return res, merge(lt, rt), nil
			}
			return res, nil, merge(lf, rf)
		case syntax.EQL, syntax.NEQ:
			op := c.expr(e, nil)
			v, isNull := c.nullCheck(x)
			if v == nil || !isNull {
				return c.boolCond(op), nil, nil
			}
			nn := narrowing{v: types.Base(v.Type)}
			if x.Op == syntax.NEQ {
				return c.boolCond(op), nn, nil
			}
			return c.boolCond(op), nil, nn
		}
	case *syntax.IsExpr:
		op := c.expr(e, nil)
		if op.mode == ModeInvalid {
			return op, nil, nil
		}
		var n narrowing
		if id, ok := syntax.Unparen(x.X).(*syntax.Ident); ok {
			if v, ok := c.info.Uses[id].(*VarSym); ok && v.Settled() {
				n = narrowing{v: c.info.TypeTests[x]}
			}
		}
		if x.Not {
			return c.boolCond(op), nil, n
		}
		return c.boolCond(op), n, nil
	case *syntax.UnaryExpr:
		if x.Op == syntax.NOT {
			inner, t, f := c.cond(x.X)
			res := &operand{mode: ModeValue, typ: types.Bool, expr: e}
			if inner.mode == ModeInvalid {
				res = invalid(e)
			} else if inner.val != nil {
				res.val = constant.UnaryOp(token.NOT, inner.val, 0)
			}
			c.record(res)
			if x != e {
				c.record(&operand{mode: res.mode, typ: res.typ, val: res.val, expr: x})
			}
			return res, f, t
		}
	}
	op := c.value(e, types.Bool)
	return c.boolCond(op), nil, nil
}

func (c *Checker) boolCond(x *operand) *operand {
	if x.mode == ModeInvalid {
		return x
	}
	if !types.IsBoolean(types.Base(x.typ)) {
		c.errorf(diag.ETypeMismatch, x.expr.Span(), "condition must be a bool, not %s", x.typ)
		return invalid(x.expr)
	}
	if types.IsNullable(x.typ) {
		c.nullableUse(x, "use as a condition")
		return invalid(x.expr)
	}
	if types.IsUntyped(x.typ) {
		c.convertUntyped(x, types.Bool, nil)
	}
	return x
}

// nullCheck recognizes `v == null` / `v != null` on a smart-castable binding.
func (c *Checker) nullCheck(x *syntax.BinaryExpr) (*VarSym, bool) {
	var other syntax.Expr
	switch {
	case isNullLit(x.Y):
		other = x.X
	case isNullLit(x.X):
		other = x.Y
	default:
		return nil, false
	}
	id, ok := syntax.Unparen(other).(*syntax.Ident)
	if !ok {
		return nil, true
	}
	v, ok := c.info.Uses[id].(*VarSym)
	if !ok || !v.Settled() || v.Type == nil || !types.MayBeNull(v.Type) {
		return nil, true
	}
	return v, true
}

func isNullLit(e syntax.Expr) bool {
	_, ok := syntax.Unparen(e).(*syntax.NullLit)
	return ok
}

func merge(a, b narrowing) narrowing {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	m := narrowing{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func (c *Checker) narrowedScope(n narrowing) *Scope {
	s := newScope(c.scope, BlockScope)
	for v, t := range n {
		s.setNarrow(v, t)
	}
	return s
}

// assertable checks a type test or cast of x to t (Go type assertion rules).
func (c *Checker) assertable(x *operand, t types.Type, span source.Span) bool {
	if !types.IsInterface(x.typ) {
		c.errorf(diag.EInvalidOp, x.expr.Span(), "type tests need an interface value; %s has type %s", x.expr.Span().Short(), x.typ).
			WithNote("only interface values (any, error, io.Reader, ...) can hold values of different types")
		return false
	}
	if types.IsNullable(t) {
		c.errorf(diag.EInvalidUse, span, "test for the non-null type %s; a null value never matches a type test", types.Base(t))
		return false
	}
	if types.IsInterface(t) {
		return true
	}
	iface, ok := types.Lower(types.Base(x.typ)).Underlying().(*gotypes.Interface)
	if !ok {
		return true
	}
	if !gotypes.AssertableTo(iface, types.Lower(t)) {
		d := c.errorf(diag.EInvalidOp, span, "impossible type test: %s can never hold a %s", x.typ, t)
		if m, _ := gotypes.MissingMethod(types.Lower(t), iface, true); m != nil {
			if pm, _ := gotypes.MissingMethod(gotypes.NewPointer(types.Lower(t)), iface, true); pm == nil {
				// The method exists with a pointer receiver.
				d.WithNote("%s has a pointer receiver, so *%s implements %s and %s does not", m.Name(), t, types.Base(x.typ), t).
					WithEdit(span, "*"+span.Text(), "test for the pointer: *%s", t)
			} else {
				d.WithNote("%s does not implement %s (missing method %s)", t, types.Base(x.typ), m.Name())
			}
		}
		return false
	}
	c.warnErrorTypeTest(x, t, span)
	return true
}

// warnErrorTypeTest reports a type test of an error type on an error value
// (W0007, ADR 0041). A type test sees the value's dynamic type only, so it
// misses the error wrapped inside (err.wrap, fmt.Errorf with %w), which is
// how errors usually arrive; typed catch and errors.AsType look through the
// chain. A value bound as any first (v: any = err) is tested on purpose.
func (c *Checker) warnErrorTypeTest(x *operand, t types.Type, span source.Span) {
	goErr, ok := types.GoError().Underlying().(*gotypes.Interface)
	if !ok || types.IsInterface(t) {
		return
	}
	if _, isAny := types.Base(x.typ).(*types.AnyType); isAny {
		return
	}
	if !gotypes.Implements(types.Lower(types.Base(x.typ)), goErr) {
		return
	}
	lt := types.Lower(t)
	if !gotypes.Implements(lt, goErr) && !gotypes.Implements(gotypes.NewPointer(lt), goErr) {
		return
	}
	value := x.expr.Span().Short()
	c.warnf(diag.WErrorTypeTest, span, "a type test sees only the dynamic type of %s, so a wrapped %s does not match", value, t).
		WithHint("to match through wrapping, handle the call with catch (e: %s) { ... }, or bind it: e, ok := errors.AsType[%s](%s)", t, t, value).
		WithHint("to test the dynamic type on purpose, bind the error as any first: v: any = %s", value)
}

// nullableUse reports E0401 for using a nullable value as non-null.
func (c *Checker) nullableUse(x *operand, what string) {
	text := x.expr.Span().Short()
	d := c.errorf(diag.ENullable, x.expr.Span(), "cannot %s %s: it may be null (type %s)", what, text, x.typ)
	switch e := syntax.Unparen(x.expr).(type) {
	case *syntax.Ident:
		if v, ok := c.info.Uses[e].(*VarSym); ok && !v.Settled() {
			if v.Kind == GlobalVar {
				d.WithNote("smart casts do not apply to package-level variables; %s may change between the check and this use", v.name)
			} else {
				d.WithNote("smart casts apply only to variables that are never reassigned; %s is assigned in this function", v.name)
			}
			name := v.name + "Now"
			d.WithHint("copy it into a new variable and check that: %s := %s; if %s != null { ... }", name, text, name)
			return
		}
	case *syntax.SelectorExpr:
		// A check of a field does not smart-cast it, so suggest the copy
		// that does instead of the check the user has likely written.
		if sel := c.info.Selections[e]; sel != nil && sel.Kind == SelField {
			name := valName(e.Sel.Name)
			d.WithNote("smart casts do not apply to fields; %s is a field", text)
			d.WithHint("copy it into a variable and check that (%s := %s; if %s != null { ... }), provide a default with ?:, or assert with !!", name, text, name)
			return
		}
	}
	if _, isIdent := syntax.Unparen(x.expr).(*syntax.Ident); !isIdent {
		// m[k] or f(): a check of the expression itself does not smart-cast it.
		d.WithHint("store it in a variable and check that (v := %s; if v != null { ... }), provide a default with ?:, or assert with !!", text)
		return
	}
	d.WithHint("check it first (if %s != null { ... }), provide a default with ?:, or assert with !!", text)
}

// valName turns a field name into a local name: Email becomes email, ID id,
// and UserID userID.
func valName(field string) string {
	r := []rune(field)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	if n > 1 && n < len(r) {
		n-- // keep the start of the next word: URLPath -> urlPath
	}
	for i := 0; i < n; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// ---------------------------------------------------------------------------
// Elvis

func (c *Checker) elvis(e *syntax.ElvisExpr, want types.Type) *operand {
	var lwant types.Type
	if want != nil {
		lwant = types.MakeNullable(want)
	}
	x := c.value(e.X, lwant)
	if x.mode == ModeInvalid {
		c.value(e.Y, want)
		return invalid(e)
	}
	isColl := false
	switch collectionOf(x.typ).(type) {
	case *types.Slice, *types.Map:
		isColl = true // xs ?: default: a nil slice is the absent value
	}
	if !types.MayBeNull(x.typ) && !isColl {
		d := c.warnf(diag.WUnnecessary, e.OpSpan, "left side of ?: has non-nullable type %s, so the right side is never used", x.typ)
		if ci := c.info.Calls[callOf(e.X)]; ci != nil && ci.Kind == CallPrelude && ci.Prelude == PreludeReceive {
			d.WithNote("receiving from a closed channel of %s yields its zero value, not null", x.typ).
				WithHint("tell a closed channel apart with v, ok := ch.receive()")
		}
	}
	base := types.Base(x.typ)
	if x.typ == types.Null {
		base = want
	}
	ywant := want
	if ywant == nil {
		ywant = base
	}
	y := c.value(e.Y, ywant)
	if y.mode == ModeInvalid {
		return invalid(e)
	}
	if isNothing(y.typ) {
		return &operand{mode: ModeValue, typ: base}
	}
	if base == nil {
		return &operand{mode: ModeValue, typ: y.typ}
	}
	t, ok := c.join(base, y, e.Y)
	if !ok {
		c.errorf(diag.ETypeMismatch, e.Y.Span(), "right side of ?: has type %s, which is incompatible with %s", y.typ, base)
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: t}
}

// join computes the common type of a branch result t and another branch
// operand y, converting y as needed.
func (c *Checker) join(t types.Type, y *operand, ye syntax.Expr) (types.Type, bool) {
	if isNothing(t) {
		if y.typ == types.Null {
			return types.Null, true // a later branch gives the T of T?
		}
		return c.defaultValueType(y, ""), true
	}
	if isNothing(y.typ) {
		return t, true
	}
	if y.typ == types.Null {
		if t == types.Null {
			return t, true
		}
		return c.nullableOf(t, ye.Span()), true
	}
	if t == types.Null {
		yt := c.defaultValueType(y, "")
		return c.nullableOf(yt, ye.Span()), true
	}
	if types.IsUntyped(y.typ) {
		if c.assignableQuiet(y, t) {
			c.assign(y, t, "")
			return t, true
		}
		return nil, false
	}
	if c.assignableQuiet(y, t) {
		c.assign(y, t, "")
		return t, true
	}
	// t may be assignable to y's type (e.g. T and T?).
	if ok, _, _ := c.assignable(t, y.typ); ok {
		return y.typ, true
	}
	return nil, false
}

// nullableOf returns t? for a value that may be null (a branch or a catch
// fallback is null), reporting T? on a type parameter, which Lyth cannot
// represent yet.
func (c *Checker) nullableOf(t types.Type, span source.Span) types.Type {
	if _, isTP := types.Base(t).(*types.TypeParam); isTP && !types.IsNilable(t) {
		c.errorf(diag.ENullableTParam, span, "null makes this %s?, and nullable type parameters are not supported yet", t).
			WithHint("return a zero value, or declare the result as a pointer: *%s", t)
		return types.Invalid
	}
	return types.MakeNullable(t)
}

// ---------------------------------------------------------------------------
// Collections

func (c *Checker) listLit(e *syntax.ListLit, want types.Type) *operand {
	if e.Type != nil {
		return c.typedListLit(e)
	}
	var elem types.Type
	named := namedCollection(want)
	if want != nil {
		shape := types.Base(underlyingOf(want))
		if s, ok := shape.(*types.Slice); ok {
			elem = s.Elem
		}
		if a, ok := shape.(*types.Array); ok {
			// a: [3]int = [1, 2, 3]: a list literal is an array literal where
			// an array is expected; it gives every element.
			for _, x := range e.Elems {
				c.assign(c.value(x, a.Elem), a.Elem, "array element")
			}
			if int64(len(e.Elems)) != a.Len {
				c.errorf(diag.ETypeMismatch, e.S, "%s needs %d elements, got %d", types.Base(want), a.Len, len(e.Elems))
			}
			return &operand{mode: ModeValue, typ: typeOr(named, &types.Array{Len: a.Len, Elem: a.Elem})}
		}
	}
	if elem != nil {
		for _, x := range e.Elems {
			c.assign(c.value(x, elem), elem, "list element")
		}
		return &operand{mode: ModeValue, typ: typeOr(named, &types.Slice{Elem: elem})}
	}
	if len(e.Elems) == 0 {
		c.errorf(diag.ECannotInfer, e.S, "cannot infer the element type of an empty list").
			WithHint("add a type annotation, e.g. xs: []string = []")
		return invalid(e)
	}
	ops := make([]*operand, len(e.Elems))
	for i, x := range e.Elems {
		ops[i] = c.value(x, nil)
	}
	t := c.commonType(ops, "list element")
	if t == nil {
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: &types.Slice{Elem: t}}
}

// typedListLit checks []T{...}, [N]T{...}, and [...]T{...}. As in Go, an
// array literal may give fewer elements than the length (the rest are zero),
// but not more.
func (c *Checker) typedListLit(e *syntax.ListLit) *operand {
	var t types.Type
	if at, ok := e.Type.(*syntax.ArrayType); ok && at.Len == nil {
		elem := c.resolveType(at.Elem)
		if elem == types.Invalid {
			c.checkElems(e.Elems, nil)
			return invalid(e)
		}
		t = &types.Array{Len: int64(len(e.Elems)), Elem: elem}
		c.info.TypeExprs[e.Type] = t
	} else {
		t = c.resolveType(e.Type)
	}
	switch b := types.Base(t).(type) {
	case *types.Slice:
		c.checkElems(e.Elems, b.Elem)
	case *types.Array:
		c.checkElems(e.Elems, b.Elem)
		if int64(len(e.Elems)) > b.Len {
			c.errorf(diag.ETypeMismatch, e.Elems[b.Len].Span(), "%s holds %d elements; this literal gives %d", t, b.Len, len(e.Elems))
		}
	default:
		c.checkElems(e.Elems, nil)
		if t != types.Invalid {
			c.errorf(diag.ETypeMismatch, e.Type.Span(), "a list literal needs a slice or array type, not %s", t)
		}
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: t}
}

func (c *Checker) checkElems(elems []syntax.Expr, elem types.Type) {
	for _, x := range elems {
		if elem == nil {
			c.value(x, nil)
			continue
		}
		c.assign(c.value(x, elem), elem, "list element")
	}
}

// checkMapKeys reports constant keys a map literal with key type kt gives
// twice; Go rejects them, and the second value would silently replace the
// first. As in Go, a key is its value at its type: kt, or for an interface
// key type the constant's default type (1 and 1.0 differ in a map[any]).
func (c *Checker) checkMapKeys(keys []syntax.Expr, kt types.Type) {
	if kt == nil || kt == types.Invalid {
		return
	}
	seen := map[string]syntax.Expr{}
	for _, k := range keys {
		tv, ok := c.info.Types[k]
		if !ok || tv.Value == nil {
			continue
		}
		t := kt
		if types.IsInterface(kt) {
			t = types.DefaultType(tv.Type)
		}
		key := t.String() + " " + tv.Value.ExactString()
		if first, dup := seen[key]; dup {
			c.errorf(diag.EDuplicate, k.Span(), "duplicate key %s in map literal", k.Span().Short()).
				WithRelated(first.Span(), "first given here")
			continue
		}
		seen[key] = k
	}
}

func (c *Checker) mapLit(e *syntax.MapLit, want types.Type) *operand {
	op := c.mapLit0(e, want)
	if m, ok := types.Base(op.typ).(*types.Map); ok && op.mode != ModeInvalid {
		c.checkMapKeys(e.Keys, m.Key)
	}
	return op
}

func (c *Checker) mapLit0(e *syntax.MapLit, want types.Type) *operand {
	if e.Type != nil {
		t := c.resolveType(e.Type)
		m, ok := types.Base(t).(*types.Map)
		if !ok {
			for i := range e.Keys {
				c.value(e.Keys[i], nil)
				c.value(e.Values[i], nil)
			}
			return invalid(e)
		}
		for i := range e.Keys {
			c.assign(c.value(e.Keys[i], m.Key), m.Key, "map key")
			c.assign(c.value(e.Values[i], m.Elem), m.Elem, "map value")
		}
		return &operand{mode: ModeValue, typ: t}
	}
	var kt, vt types.Type
	if want != nil {
		if m, ok := types.Base(underlyingOf(want)).(*types.Map); ok {
			kt, vt = m.Key, m.Elem
		}
	}
	if kt != nil {
		for i := range e.Keys {
			c.assign(c.value(e.Keys[i], kt), kt, "map key")
			c.assign(c.value(e.Values[i], vt), vt, "map value")
		}
		return &operand{mode: ModeValue, typ: typeOr(namedCollection(want), &types.Map{Key: kt, Elem: vt})}
	}
	if len(e.Keys) == 0 {
		c.errorf(diag.ECannotInfer, e.S, "cannot infer the type of an empty map").
			WithHint("add a type annotation, e.g. m: map[string]int = [:]")
		return invalid(e)
	}
	keys := make([]*operand, len(e.Keys))
	vals := make([]*operand, len(e.Values))
	for i := range e.Keys {
		keys[i] = c.value(e.Keys[i], nil)
		vals[i] = c.value(e.Values[i], nil)
	}
	kt = c.commonType(keys, "map key")
	vt = c.commonType(vals, "map value")
	if kt == nil || vt == nil {
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: &types.Map{Key: kt, Elem: vt}}
}

// namedCollection returns want when it is a Go named slice, array, or map
// type (gin.H, http.Header, sort.StringSlice): a literal written where one
// is expected takes its element types from the underlying type and keeps the
// name, as Go's gin.H{...} does.
func namedCollection(want types.Type) types.Type {
	n, ok := types.Base(want).(*types.Named)
	if u := definedUnder(want); ok && u != nil {
		switch types.Base(u).(type) {
		case *types.Slice, *types.Array, *types.Map:
			return n
		}
		return nil
	}
	if !ok || n.Go == nil {
		return nil
	}
	switch n.Go.Underlying().(type) {
	case *gotypes.Slice, *gotypes.Array, *gotypes.Map:
		return n
	}
	return nil
}

// typeOr returns t, or def when t is nil.
func typeOr(t, def types.Type) types.Type {
	if t != nil {
		return t
	}
	return def
}

// allNull reports whether every operand is the null literal.
func allNull(ops []*operand) bool {
	for _, o := range ops {
		if o.typ != types.Null {
			return false
		}
	}
	return true
}

// commonType finds a type all operands are assignable to.
func (c *Checker) commonType(ops []*operand, what string) types.Type {
	var t types.Type
	for _, o := range ops {
		if o.mode == ModeInvalid {
			return nil
		}
	}
	// Prefer the first typed operand; otherwise the widest untyped kind.
	for _, o := range ops {
		if !types.IsUntyped(o.typ) && o.typ != types.Null {
			t = o.typ
			break
		}
	}
	if t == nil {
		kind := types.UntypedBoolKind
		for _, o := range ops {
			if b, ok := o.typ.(*types.Basic); ok && b.Kind > kind {
				kind = b.Kind
			}
		}
		t = types.DefaultType(untypedOfKind(kind))
		if allNull(ops) {
			c.errorf(diag.ECannotInfer, ops[0].expr.Span(), "cannot infer the %s type from null", what)
			return nil
		}
	}
	final := t
	for _, o := range ops {
		if o.typ == types.Null {
			// [1, null]: the elements are int?, each boxed.
			final = types.MakeNullable(t)
		}
	}
	for _, o := range ops {
		if o.typ == types.Null {
			c.assign(o, final, what)
			continue
		}
		if (what == "list element" || what == "map value") && !c.assignableQuiet(o, t) {
			// [1, "x"]: Go needs the element type written, []any{1, "x"}.
			first := t
			if types.IsUntyped(first) {
				first = types.DefaultType(first)
			}
			d := c.errorf(diag.ETypeMismatch, o.expr.Span(), "%ss have different types: %s and %s", what, first, types.DefaultType(o.typ))
			if what == "list element" {
				d.WithHint("give the element type, as Go does: []any{...}")
			} else {
				d.WithHint("give the value type, as Go does: map[string]any{...}")
			}
			return nil
		}
		if !c.assign(o, final, what) {
			return nil
		}
	}
	return final
}

// ---------------------------------------------------------------------------
// Index and slice

func (c *Checker) index(e *syntax.IndexExpr) *operand {
	if inst := c.indexInstantiation(e); inst != nil {
		// f[int], Page[string]: the parser cannot tell type arguments from
		// an index; a generic function or type on the left decides.
		c.info.IndexInsts[e] = inst
		x := c.instantiateExpr(inst)
		c.record(&operand{mode: x.mode, typ: x.typ, val: x.val, expr: inst})
		return x
	}
	x := c.value(e.X, nil)
	if x.mode == ModeInvalid {
		c.value(e.Index, nil)
		return invalid(e)
	}
	if types.IsNullable(x.typ) {
		c.nullableUse(x, "index")
		return invalid(e)
	}
	switch t := collectionOf(x.typ).(type) {
	case *types.Slice:
		c.indexValue(e.Index)
		return &operand{mode: ModeValue, typ: t.Elem}
	case *types.Array:
		c.indexValue(e.Index)
		return &operand{mode: ModeValue, typ: t.Elem}
	case *types.Pointer:
		if a, ok := types.Base(t.Elem).(*types.Array); ok {
			c.indexValue(e.Index)
			return &operand{mode: ModeValue, typ: a.Elem}
		}
	case *types.Map:
		k := c.value(e.Index, t.Key)
		c.assign(k, t.Key, "map key")
		vt := t.Elem
		if !types.Zeroable(vt) {
			vt = types.MakeNullable(vt)
		}
		return &operand{mode: ModeValue, typ: vt}
	}
	if types.IsString(collectionOf(x.typ)) {
		c.indexValue(e.Index)
		return &operand{mode: ModeValue, typ: types.Byte}
	}
	c.errorf(diag.EInvalidOp, e.S, "cannot index a value of type %s", x.typ)
	return invalid(e)
}

func (c *Checker) indexValue(e syntax.Expr) {
	x := c.value(e, types.Int)
	if x.mode == ModeInvalid {
		return
	}
	if types.IsUntyped(x.typ) {
		if x.val != nil && constSign(x.val) < 0 {
			c.errorf(diag.EInvalidOp, e.Span(), "index must not be negative")
			return
		}
		c.assign(x, types.Int, "index")
		return
	}
	if !types.IsInteger(types.Base(x.typ)) {
		c.errorf(diag.ETypeMismatch, e.Span(), "index must be an integer, not %s", x.typ)
	}
}

func (c *Checker) sliceExpr(e *syntax.SliceExpr) *operand {
	x := c.value(e.X, nil)
	if e.Lo != nil {
		c.indexValue(e.Lo)
	}
	if e.Hi != nil {
		c.indexValue(e.Hi)
	}
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	switch t := collectionOf(x.typ).(type) {
	case *types.Slice:
		return &operand{mode: ModeValue, typ: types.Base(x.typ)}
	case *types.Array:
		if !c.mutablePath(e.X, "slice", diag.EAddressOfVal) {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: &types.Slice{Elem: t.Elem}}
	}
	if types.IsString(types.Base(x.typ)) {
		return &operand{mode: ModeValue, typ: types.Base(x.typ)}
	}
	c.errorf(diag.EInvalidOp, e.S, "cannot slice a value of type %s", x.typ)
	return invalid(e)
}

// ---------------------------------------------------------------------------
// Errors: ? and catch

func (c *Checker) tryExpr(e *syntax.TryExpr) *operand {
	return c.tryMode(e, false)
}

// tryMode checks x?; discard reports that the success value is unused.
func (c *Checker) tryMode(e *syntax.TryExpr, discard bool) *operand {
	x := c.fallibleOperand(e.X, discard)
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	success, ok := types.FallibleShape(x.typ)
	if !ok {
		d := c.errorf(diag.ETryNotFallible, e.OpSpan, "? needs a call that returns an error; %s has type %s", e.X.Span().Short(), describeType(x))
		if types.MayBeNull(x.typ) {
			d.WithHint("for a nullable value use ?: (default or early return) or !! (assert non-null)")
		}
		return invalid(e)
	}
	c.requireFallible(e.OpSpan, "?")
	info := &TryInfo{Success: success}
	if r, ok := x.typ.(*types.Result); ok {
		info.Raw = r.Raw
	} else {
		info.Raw = []types.Type{x.typ}
	}
	c.info.Tries[e] = info
	switch len(success) {
	case 0:
		return &operand{mode: ModeNoValue, typ: types.Unit}
	case 1:
		return &operand{mode: ModeValue, typ: success[0]}
	}
	return &operand{mode: ModeValue, typ: &types.Tuple{Elems: success}}
}

func describeType(x *operand) string {
	if x.mode == ModeNoValue {
		return "unit (no value)"
	}
	return x.typ.String()
}

// requireFallible reports E0301 unless the current function may propagate.
func (c *Checker) requireFallible(span source.Span, what string) {
	fn := c.fn
	if fn != nil && fn.fallible {
		return
	}
	code := diag.ETryOutside
	if what == "throw" {
		code = diag.EThrowOutside
	}
	if fn == nil {
		c.errorf(code, span, "%s can only be used inside a function", what)
		return
	}
	switch {
	case fn.isLambda:
		d := c.errorf(code, span, "%s can only be used in a lambda whose type is fallible", what)
		if rs := fn.sig.Results; len(rs) > 0 && types.IsErrorType(rs[len(rs)-1]) {
			d.WithNote("this lambda returns %s as a value; a function type written in Lyth can fail only when it is declared throws", rs[len(rs)-1]).
				WithHint("return the error as the lambda's value (catch (err) { return err }), or declare the type with throws")
		} else {
			d.WithNote("the expected type of this lambda does not return an error").
				WithHint("handle the error inside the lambda with catch, or pass the lambda where a throwing function type is expected")
		}
	case fn.name == "package initializer":
		c.errorf(code, span, "%s cannot be used in a package-level initializer", what).
			WithHint("initialize the value in a function and propagate the error from there")
	default:
		d := c.errorf(code, span, "%s can only be used in a function that throws", what)
		if fn.declSpan.IsValid() {
			d.WithRelated(fn.declSpan, "%s is not declared throws", fn.name)
		}
		d.WithHint("add throws to the declaration of %s, or handle the error with catch", fn.name)
	}
}

func (c *Checker) catchExpr(e *syntax.CatchExpr, want types.Type) *operand {
	return c.catchMode(e, want, false)
}

// catchStmt checks a catch whose value is discarded (a statement, defer, or
// go): its handler need not produce a value.
func (c *Checker) catchStmt(e *syntax.CatchExpr) {
	op := c.catchMode(e, nil, true)
	op.expr = e
	c.record(op)
	if e.Type != nil && op.mode != ModeInvalid {
		c.errorf(diag.EUnhandledError, e.CatchSpan.Join(e.Type.Span()), "errors other than %s are not handled", types.Base(c.info.Tries[e].CatchType)).
			WithHint("propagate them with ? after the catch block, or handle them with a final catch (err) { ... }")
	}
}

func (c *Checker) catchMode(e *syntax.CatchExpr, want types.Type, discard bool) *operand {
	x := c.fallibleOperand(e.X, discard)
	if _, isError := x.typ.(*types.Result); !isError && x.mode == ModeValue && types.IsErrorType(x.typ) {
		// n := err catch { 0 }: catch handles a failing call, not a value.
		c.errorf(diag.ECatchNotFall, e.CatchSpan, "catch needs a call that returns an error; %s is an error value", e.X.Span().Short()).
			WithHint("check it: if %s != null { ... }", e.X.Span().Short())
		c.withScope(newScope(c.scope, BlockScope), func() { c.blockValue(e.Body, want, true) })
		return invalid(e)
	}
	scope := newScope(c.scope, BlockScope)
	catchType := c.catchType(e)
	var binder *VarSym
	if e.Binder != nil && e.Binder.Name != "_" {
		bt := types.Type(types.Error)
		if catchType != nil {
			bt = catchType
		}
		binder = &VarSym{name: e.Binder.Name, Type: bt, Kind: CatchVar, Span: e.Binder.S}
		scope.Insert(binder)
		c.info.Defs[e.Binder] = binder
		if c.fn != nil {
			c.fn.locals = append(c.fn.locals, binder)
		}
	}
	if x.mode == ModeInvalid || catchType == types.Invalid {
		// Reported; the handler is checked for its own errors only.
		c.withScope(scope, func() { c.blockValue(e.Body, want, true) })
		return invalid(e)
	}
	success, ok := types.FallibleShape(x.typ)
	if !ok {
		c.errorf(diag.ECatchNotFall, e.CatchSpan, "catch needs a call that returns an error; %s has type %s", e.X.Span().Short(), describeType(x))
		c.withScope(scope, func() { c.blockValue(e.Body, want, true) })
		return invalid(e)
	}
	info := &TryInfo{Success: success, Binder: binder, CatchType: catchType}
	if r, ok := x.typ.(*types.Result); ok {
		info.Raw = r.Raw
	} else {
		info.Raw = []types.Type{x.typ}
	}
	c.info.Tries[e] = info
	if catchType == types.Invalid {
		c.withScope(scope, func() { c.blockValue(e.Body, nil, false) })
		return invalid(e)
	}
	var succ types.Type
	switch len(success) {
	case 0:
		succ = types.Unit
	case 1:
		succ = success[0]
	default:
		succ = &types.Tuple{Elems: success}
	}
	needValue := succ != types.Unit && !discard
	bwant := succ
	if _, isTuple := succ.(*types.Tuple); isTuple {
		bwant = nil
	} else if want != nil && needValue && !(types.IsInterface(want) && !types.IsInterface(succ)) {
		// The expected type types the fallback (catch { null } for an int?),
		// unless it is an interface: a: any = load() catch { [] } falls
		// back to the success type, []int.
		bwant = want
	}
	var res blockResult
	c.withScope(scope, func() { res = c.blockValue(e.Body, bwant, needValue) })
	info.ValueExpr = res.valueExpr
	// A typed catch handles only some errors, so its result is still
	// fallible: the rest go to ?, a further catch, or a catch of all errors.
	still := &operand{mode: ModeValue, typ: x.typ}
	if res.terminates || !needValue {
		if catchType != nil {
			return still
		}
		if !needValue {
			return &operand{mode: ModeNoValue, typ: types.Unit}
		}
		return &operand{mode: ModeValue, typ: succ}
	}
	if !e.Body.Rbrace.IsValid() {
		return invalid(e) // the handler did not parse; that error stands
	}
	if _, isTuple := succ.(*types.Tuple); isTuple {
		c.errorf(diag.ECatchBlock, e.Body.Rbrace, "this catch handler must exit (return, throw, break, or continue)").
			WithNote("the call returns several values, so the handler cannot provide a replacement value")
		return invalid(e)
	}
	if res.op == nil {
		c.errorf(diag.ECatchBlock, e.Body.Rbrace, "catch handler must end with a value of type %s or exit", succ).
			WithHint("end the block with a fallback value, or return/throw")
		return invalid(e)
	}
	t, ok := c.join(succ, res.op, res.valueExpr)
	if !ok {
		c.errorf(diag.ETypeMismatch, res.valueExpr.Span(), "catch handler yields %s, but the call succeeds with %s", res.op.typ, succ)
		return invalid(e)
	}
	if catchType != nil {
		if !types.Identical(t, succ) {
			// The rest of the chain still produces succ; the handler's value
			// must convert to it.
			c.assign(res.op, succ, "catch handler value")
		}
		return still
	}
	return &operand{mode: ModeValue, typ: t}
}

// catchType resolves the type of a typed catch: nil for a catch of all
// errors, Invalid after an error.
func (c *Checker) catchType(e *syntax.CatchExpr) types.Type {
	if e.Type == nil {
		return nil
	}
	t := c.resolveType(e.Type)
	if t == types.Invalid {
		return t
	}
	if types.IsNullable(t) {
		c.errorf(diag.EInvalidUse, e.Type.Span(), "catch types are non-null: write %s", types.Base(t))
		return types.Invalid
	}
	if ok, _, _ := c.assignable(t, types.Error); !ok {
		c.errorf(diag.ETypeMismatch, e.Type.Span(), "%s is not an error type: it does not implement error", t).
			WithHint("catch a type with an Error(): string method, such as *NotFound")
		return types.Invalid
	}
	return t
}

// fallibleOperand checks the operand of ? or catch. A typed catch operand
// learns whether the final value is discarded, so its handlers need no value.
func (c *Checker) fallibleOperand(e syntax.Expr, discard bool) *operand {
	if cx, ok := syntax.Unparen(e).(*syntax.CatchExpr); ok && cx.Type != nil {
		op := c.catchMode(cx, nil, discard)
		op.expr = cx
		return c.record(op)
	}
	return c.expr(e, nil)
}

// ---------------------------------------------------------------------------
// Jumps

func (c *Checker) jump(e *syntax.JumpExpr) {
	fn := c.fn
	switch e.Kind {
	case syntax.JumpReturn:
		c.returnValues(e)
	case syntax.JumpThrow:
		if len(e.Values) == 0 {
			return
		}
		x := c.value(e.Values[0], types.Error)
		c.requireFallible(e.S, "throw")
		if x.mode == ModeInvalid {
			return
		}
		if x.typ == types.Null {
			c.errorf(diag.EThrowOperand, e.Values[0].Span(), "cannot throw null")
			return
		}
		if types.IsNullable(x.typ) {
			c.errorf(diag.EThrowOperand, e.Values[0].Span(), "cannot throw %s: it may be null (type %s)", e.Values[0].Span().Short(), x.typ).
				WithHint("check it first: if %s != null { throw %s }", e.Values[0].Span().Short(), e.Values[0].Span().Short())
			return
		}
		if ok, _, _ := c.assignable(x.typ, types.Error); !ok {
			d := c.errorf(diag.EThrowOperand, e.Values[0].Span(), "cannot throw a value of type %s; throw needs an error", x.typ)
			if types.IsString(types.Base(x.typ)) {
				d.WithHint("create an error: throw errors.New(%s)", e.Values[0].Span().Short())
			}
			return
		}
		c.assign(x, types.Error, "throw")
	case syntax.JumpBreak, syntax.JumpContinue:
		kw := map[bool]string{true: "break", false: "continue"}[e.Kind == syntax.JumpBreak]
		if fn == nil || fn.loopDepth == 0 {
			c.errorf(diag.EInvalidUse, e.S, "%s outside a loop", kw)
			return
		}
		if e.Label != nil && !slices.ContainsFunc(fn.labels, func(l *syntax.Ident) bool { return l.Name == e.Label.Name }) {
			d := c.errorf(diag.EUndefined, e.Label.S, "no enclosing loop is labeled %s", e.Label.Name)
			if len(fn.labels) > 0 {
				d.WithNote("enclosing loop labels: %s", labelNames(fn.labels))
			} else {
				d.WithHint("label the loop: %s@ for x in xs { ... %s@%s ... }", e.Label.Name, kw, e.Label.Name)
			}
		}
	}
}

func labelNames(labels []*syntax.Ident) string {
	var names []string
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ", ")
}

func (c *Checker) returnValues(e *syntax.JumpExpr) {
	fn := c.fn
	if fn == nil || fn.name == "package initializer" {
		c.errorf(diag.EInvalidUse, e.S, "return outside a function")
		return
	}
	if fn.noJumps {
		c.errorf(diag.EInvalidUse, e.S, "return is not allowed in this block")
	}
	if fn.infer {
		switch len(e.Values) {
		case 0:
			fn.inferred = append(fn.inferred, types.Unit)
		case 1:
			x := c.value(e.Values[0], nil)
			fn.inferred = append(fn.inferred, c.defaultValueType(x, "return value"))
		default:
			c.errorf(diag.EInvalidUse, e.S, "lambdas cannot return multiple values")
		}
		return
	}
	results := fn.sig.Results
	if len(e.Values) == 1 && fn.fallible && !fn.rawTuple {
		// return f() in a throws function returns f's results as they are.
		x := c.expr(e.Values[0], firstOr(results))
		if c.passThrough(x, results) {
			return
		}
		if len(results) == 0 {
			d := c.errorf(diag.EReturnMismatch, e.Values[0].Span(), "%s does not return a value", fn.name)
			c.callbackReturnHint(d, fn, e)
			return
		}
		if len(results) == 1 {
			if c.notPassedThrough(x, results) {
				return
			}
			x = c.singleValue(x)
			c.assign(x, results[0], "return value")
			return
		}
		c.errorf(diag.EReturnMismatch, e.S, "%s returns %s, but 1 value given", fn.name, plural(len(results), "value")).
			WithNote("%s returns %s", fn.name, resultString(fn.sig))
		return
	}
	if len(e.Values) == 0 {
		if len(results) > 0 {
			c.errorf(diag.EReturnMismatch, e.S, "missing return value; %s returns %s", fn.name, resultString(fn.sig))
		}
		return
	}
	if len(results) == 0 {
		d := c.errorf(diag.EReturnMismatch, e.Values[0].Span(), "%s does not return a value", fn.name)
		c.callbackReturnHint(d, fn, e)
		for _, v := range e.Values {
			c.value(v, nil)
		}
		return
	}
	if len(e.Values) == 1 && len(results) > 1 {
		if _, isCall := syntax.Unparen(e.Values[0]).(*syntax.CallExpr); isCall {
			// return w.Write(p): the call's results are the function's, as in Go.
			x := c.expr(e.Values[0], nil)
			if x.mode == ModeInvalid || c.forwardResults(x, results) {
				return
			}
			c.errorf(diag.EReturnMismatch, e.S, "%s returns %s, but %s returns %s", fn.name, resultString(fn.sig), e.Values[0].Span().Short(), x.typ)
			return
		}
	}
	if len(e.Values) != len(results) {
		c.errorf(diag.EReturnMismatch, e.S, "%s returns %s, but %s given", fn.name, plural(len(results), "value"), plural(len(e.Values), "value")).
			WithNote("%s returns %s", fn.name, resultString(fn.sig))
		for _, v := range e.Values {
			c.value(v, nil)
		}
		return
	}
	for i, v := range e.Values {
		x := c.value(v, results[i])
		c.assign(x, results[i], "return value")
	}
}

// ---------------------------------------------------------------------------
// Value typing helpers

// defaultValueType returns the type a value takes when no type is expected
// (untyped constants get their default type).
func (c *Checker) defaultValueType(x *operand, context string) types.Type {
	if x.mode == ModeInvalid {
		return types.Invalid
	}
	if x.typ == types.Null {
		if context != "" {
			c.errorf(diag.ECannotInfer, x.expr.Span(), "cannot infer a type from null in %s", context).
				WithHint("add a type annotation, e.g. x: User? = null")
		}
		return types.Invalid
	}
	if types.IsUntyped(x.typ) {
		t := types.DefaultType(x.typ)
		c.assign(x, t, context)
		return t
	}
	if isGenericFunc(x.typ) {
		c.genericFuncValue(x)
		return types.Invalid
	}
	return x.typ
}

func quote(s string) string { return strconv.Quote(s) }

// stdPackages maps common standard-library package names to import paths,
// for "add the import" hints.
var stdPackages = map[string]string{
	"errors": "errors", "fmt": "fmt", "strings": "strings", "strconv": "strconv", "os": "os",
	"time": "time", "context": "context", "io": "io", "bytes": "bytes", "sort": "sort",
	"slices": "slices", "maps": "maps", "math": "math", "sync": "sync", "json": "encoding/json",
	"http": "net/http", "url": "net/url", "filepath": "path/filepath", "regexp": "regexp",
	"log": "log", "slog": "log/slog", "sql": "database/sql", "atomic": "sync/atomic",
	"bufio": "bufio", "unicode": "unicode", "utf8": "unicode/utf8", "hex": "encoding/hex",
	"base64": "encoding/base64", "sha256": "crypto/sha256", "rand": "math/rand/v2",
	"signal": "os/signal", "exec": "os/exec", "httptest": "net/http/httptest", "iter": "iter",
	"cmp": "cmp", "path": "path", "binary": "encoding/binary", "csv": "encoding/csv",
}

// membership checks `x in y` (or a `when` condition `in y` with subject x):
// y is a range, a List, an array, a Map, or a String. recordElem converts
// an untyped x in place; a when subject is shared by several conditions, so
// it is only checked.
func (c *Checker) membership(node syntax.Node, x *operand, y syntax.Expr, recordElem bool) bool {
	// The right side is checked exactly once, even when x is invalid.
	elemOK := func() bool {
		if x.mode == ModeInvalid {
			return false
		}
		if types.MayBeNull(x.typ) && !types.IsPlatform(x.typ) && x.typ != types.Null {
			c.nullableUse(x, "test membership of")
			return false
		}
		return true
	}
	elemFits := func(t types.Type) bool {
		if recordElem {
			return c.assign(x, t, "in")
		}
		if !c.assignableQuiet(x, t) {
			c.reportMismatch(x, t, "in")
			return false
		}
		return true
	}
	if r, ok := syntax.Unparen(y).(*syntax.RangeExpr); ok {
		lo, hi := c.value(r.Lo, nil), c.value(r.Hi, nil)
		if lo.mode == ModeInvalid || hi.mode == ModeInvalid || !elemOK() {
			return false
		}
		// x and both bounds must be one ordered type, as for x >= lo && x <= hi.
		elem := x
		if types.IsUntyped(elem.typ) {
			elem = lo
			if types.IsUntyped(elem.typ) {
				elem = hi
			}
		}
		for _, b := range []*operand{lo, hi} {
			if types.MayBeNull(b.typ) && !types.IsPlatform(b.typ) {
				c.nullableUse(b, "use a nullable value as a range bound:")
				return false
			}
		}
		t := types.Base(elem.typ)
		if types.IsUntyped(t) {
			t = types.DefaultType(t)
		}
		if !types.IsNumeric(t) && !types.IsString(t) || types.IsComplex(t) {
			c.errorf(diag.EInvalidOp, r.S, "ranges need numbers or strings, not %s", t)
			return false
		}
		if !elemFits(t) || !c.assign(lo, t, "range bound") || !c.assign(hi, t, "range bound") {
			return false
		}
		c.info.Ins[node] = &InInfo{Kind: InRange, Range: r}
		return true
	}
	yo := c.value(y, nil)
	if yo.mode == ModeInvalid || !elemOK() {
		return false
	}
	if types.MayBeNull(yo.typ) && !types.IsPlatform(yo.typ) {
		c.nullableUse(yo, "test membership in")
		return false
	}
	info := &InInfo{}
	switch b := collectionOf(yo.typ).(type) {
	case *types.Slice, *types.Array:
		var elem types.Type
		if sl, ok := b.(*types.Slice); ok {
			elem = sl.Elem
		} else {
			elem = b.(*types.Array).Elem
		}
		if !comparable(elem) {
			c.errorf(diag.EInvalidOp, y.Span(), "%s elements cannot be compared, so in cannot search them", elem)
			return false
		}
		if path, ft := optionalPart(elem); path != "" {
			c.errorf(diag.EInvalidOp, y.Span(), "%s elements cannot be compared, so in cannot search them: %s has type %s, which Go would compare by address", elem, path, ft)
			return false
		}
		if !elemFits(elem) {
			return false
		}
		info.Kind = InList
		if _, isArray := b.(*types.Array); isArray {
			info.Kind = InArray
		}
		if types.IsNullable(elem) && !types.IsNilable(elem) && x.typ != types.Null {
			// []int?: compare contents, not the boxes' addresses.
			info.Optional = elem
		}
	case *types.Map:
		if !elemFits(b.Key) {
			return false
		}
		info.Kind = InMap
	default:
		if !types.IsString(b) {
			c.errorf(diag.EInvalidOp, y.Span(), "in needs a range, slice, array, map, or string on the right, not %s", yo.typ).
				WithHint("ranges are written a..b (inclusive) or a..<b (exclusive)")
			return false
		}
		info.Kind = InString
		xb := types.Base(x.typ)
		switch {
		case types.IsString(xb):
			if !elemFits(types.String) {
				return false
			}
		case xb == types.Rune || types.IsUntyped(xb) && types.IsNumeric(xb) && x.val != nil && x.val.Kind() == constant.Int:
			if !elemFits(types.Rune) {
				return false
			}
			info.Rune = true
		default:
			c.errorf(diag.EInvalidOp, y.Span(), "in a string, in looks for a string or a rune, not %s", x.typ)
			return false
		}
	}
	c.info.Ins[node] = info
	return true
}

func firstOr(ts []types.Type) types.Type {
	if len(ts) == 1 {
		return ts[0]
	}
	return nil
}

// passThrough reports whether returning x from the current function passes
// a fallible call's results through unchanged, and records it: the function
// throws, x is a plain call, and its success values assign to the results
// as they are (in Go too). Partial results come along with the error, as Go
// callers of io.Writer and similar APIs expect.
func (c *Checker) passThrough(x *operand, results []types.Type) bool {
	fn := c.fn
	if fn == nil || !fn.fallible || fn.rawTuple || fn.infer || x.mode == ModeInvalid {
		return false
	}
	r, ok := x.typ.(*types.Result)
	if !ok || len(r.Success) != len(results) {
		return false
	}
	call, isCall := syntax.Unparen(x.expr).(*syntax.CallExpr)
	if !isCall {
		return false
	}
	if ci := c.info.Calls[call]; ci == nil || ci.Safe || ci.TryFirst {
		return false
	}
	for i, st := range r.Success {
		if ok, conv, _ := c.assignable(st, results[i]); !ok || conv != ConvNone {
			return false
		}
		// A concrete value converted to an interface would turn the nil
		// pointer a failing call returns into a non-nil interface (Go's
		// typed-nil trap), so only identical types and interfaces pass.
		gs, gd := types.Lower(st), types.Lower(results[i])
		if !gotypes.Identical(gs, gd) && !(gotypes.IsInterface(gs) && gotypes.AssignableTo(gs, gd)) {
			return false
		}
	}
	c.info.PassThrough[x.expr] = true
	return true
}

// callbackReturnHint completes the error for return v in a function without
// results. In a lambda that can fail (a Go callback returning error, such
// as fs.WalkDirFunc), Go's return err is throw err and return nil is return.
func (c *Checker) callbackReturnHint(d *diag.Diagnostic, fn *funcCtx, e *syntax.JumpExpr) {
	if !fn.isLambda || !fn.fallible || len(e.Values) != 1 {
		d.WithHint("remove the value, or declare a result type")
		return
	}
	if _, isNull := syntax.Unparen(e.Values[0]).(*syntax.NullLit); isNull {
		d.WithEdit(e.S, "return", "this callback can fail; it succeeds with a plain return")
		return
	}
	d.WithEdit(e.S, "throw "+e.Values[0].Span().Text(), "this callback can fail: return an error with throw %s", e.Values[0].Span().Short())
}

// forwardResults reports whether the call x returns exactly the results a
// function with results returns, and records the return as a pass-through:
// each value has the result's Go type, or an interface type assignable to
// it (a concrete value converted to an interface could be Go's typed nil).
func (c *Checker) forwardResults(x *operand, results []types.Type) bool {
	var raw []types.Type
	switch t := x.typ.(type) {
	case *types.Result:
		raw = t.Raw
	case *types.Tuple:
		raw = t.Elems
	}
	if len(raw) != len(results) {
		return false
	}
	for i, t := range raw {
		if ok, _, _ := c.assignable(t, results[i]); !ok {
			return false
		}
		gs, gd := types.Lower(t), types.Lower(results[i])
		if !gotypes.Identical(gs, gd) && !(gotypes.IsInterface(gs) && gotypes.AssignableTo(gs, gd)) {
			return false
		}
	}
	c.info.PassThrough[x.expr] = true
	return true
}

// notPassedThrough reports a fallible call returned from a throws function
// whose success value would need a conversion on the way (T into an
// interface, T into T?), so it cannot pass through, and returns true then.
func (c *Checker) notPassedThrough(x *operand, results []types.Type) bool {
	r, ok := x.typ.(*types.Result)
	if !ok || x.mode == ModeInvalid || len(r.Success) != 1 || len(results) != 1 || c.fn == nil || !c.fn.fallible {
		return false
	}
	if ok, _, _ := c.assignable(r.Success[0], results[0]); !ok {
		return false
	}
	text := x.expr.Span().Short()
	c.errorf(diag.ETypeMismatch, x.expr.Span(), "%s returns %s, which would have to become %s, so its results cannot be returned as they are", text, r.Success[0], results[0]).
		WithEdit(x.expr.Span().EndSpan(), "?", "propagate the error and return the value: %s?", text)
	return true
}

// indexInstantiation returns x[A] read as the instantiation x[A] when x names
// a generic function or type, or A can only be a type; otherwise nil.
func (c *Checker) indexInstantiation(e *syntax.IndexExpr) *syntax.InstantiateExpr {
	inst := &syntax.InstantiateExpr{X: e.X, Args: []syntax.TypeExpr{syntax.ExprToType(e.Index)}, S: e.S}
	if _, ok := e.Index.(*syntax.TypeOperand); ok {
		return inst
	}
	generic := func(sym Symbol) bool {
		switch s := sym.(type) {
		case *TypeSym:
			return s.Decl != nil && len(s.Decl.TypeParams) > 0
		case *FuncSym:
			return s.Decl != nil && len(s.Decl.TypeParams) > 0 || s.Sig != nil && len(s.Sig.TypeParams) > 0
		}
		return false
	}
	switch x := e.X.(type) {
	case *syntax.Ident:
		if generic(c.lookup(x.Name)) {
			return inst
		}
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			return nil
		}
		ps, ok := c.lookup(id.Name).(*PkgSym)
		if !ok || ps.Pkg == nil {
			return nil
		}
		if ps.Pkg.Lyth {
			if generic(ps.Pkg.Scope.LookupLocal(x.Sel.Name)) {
				return inst
			}
			return nil
		}
		switch obj := ps.Pkg.Go.Scope().Lookup(x.Sel.Name).(type) {
		case *gotypes.Func:
			if sig, ok := obj.Type().(*gotypes.Signature); ok && sig.TypeParams().Len() > 0 {
				return inst
			}
		case *gotypes.TypeName:
			if n, ok := gotypes.Unalias(obj.Type()).(*gotypes.Named); ok && n.TypeParams().Len() > 0 {
				return inst
			}
		}
	}
	return nil
}

// callOf returns e as a call, or nil.
func callOf(e syntax.Expr) *syntax.CallExpr {
	c, _ := syntax.Unparen(e).(*syntax.CallExpr)
	return c
}

// kotlinBuilders are Kotlin's collection functions, which Lyth writes as
// literals.
var kotlinBuilders = map[string]string{
	"listOf":        "Lyth writes a list as a literal: [1, 2, 3]",
	"mutableListOf": "Lyth writes a list as a literal: [1, 2, 3]; slices are mutable",
	"arrayListOf":   "Lyth writes a list as a literal: [1, 2, 3]; slices are mutable",
	"emptyList":     "an empty list needs its type: xs: []int = []",
	"mapOf":         "Lyth writes a map as a literal: [\"a\": 1, \"b\": 2]",
	"mutableMapOf":  "Lyth writes a map as a literal: [\"a\": 1]; maps are mutable",
	"hashMapOf":     "Lyth writes a map as a literal: [\"a\": 1]; maps are mutable",
	"emptyMap":      "an empty map needs its type: m: map[string]int = [:]",
	"setOf":         "Lyth has no set type: use map[T]bool, or [a, b].ToSet() with import \"lyth/coll\"",
	"mutableSetOf":  "Lyth has no set type: use map[T]bool, or [a, b].ToSet() with import \"lyth/coll\"",
	"arrayOf":       "Lyth writes a list as a literal: [1, 2, 3]",
	"require":       "check and throw: if !cond { throw errors.New(\"...\") }",
	"check":         "check and throw: if !cond { throw errors.New(\"...\") }",
}
