package check

import (
	"fmt"
	"go/constant"
	gotypes "go/types"
	"math"
	"slices"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// assign checks that x can be assigned to target, converting untyped
// constants and recording implicit conversions. It reports errors with the
// given context ("argument 1 of f", "return value", ...).
func (c *Checker) assign(x *operand, target types.Type, context string) bool {
	if x.mode == ModeInvalid || target == nil || target == types.Invalid || x.typ == types.Invalid {
		return x.mode != ModeInvalid
	}
	if x.mode == ModeNoValue {
		c.errorf(diag.EInvalidUse, x.expr.Span(), "%s does not produce a value", x.expr.Span().Short())
		return false
	}
	if isGenericFunc(x.typ) && !c.instantiateFuncValue(x, target, true) {
		// g: (string) -> string = Ident instantiates Ident; any other use
		// needs the type arguments.
		c.genericFuncValue(x)
		return false
	}
	// Untyped constants and untyped bools.
	if types.IsUntyped(x.typ) {
		tb := types.Base(target)
		if nb, ok := tb.(*types.Nullable); ok {
			tb = nb.Elem
		}
		if types.IsInterface(tb) {
			// Constants assigned to interfaces take their default type.
			def := types.DefaultType(x.typ)
			x.typ = def
			c.record(x)
			if types.IsNullable(target) && !types.IsNilable(types.Base(target)) {
				c.info.Implicit[x.expr] = ConvBox
			}
			return true
		}
		if !c.untypedFits(x, tb) {
			c.reportMismatch(x, target, context)
			return false
		}
		x.typ = tb
		if x.val != nil {
			x.val = c.convertConst(x.val, tb)
		}
		c.record(x)
		if types.IsNullable(target) && !types.IsNilable(target) {
			c.info.Implicit[x.expr] = ConvBox
		}
		return true
	}
	ok, conv, reason := c.assignable(x.typ, target)
	if !ok {
		c.reportMismatchReason(x, target, context, reason)
		return false
	}
	if conv == ConvBox && c.reuseBox(x.expr) {
		return true
	}
	if conv != ConvNone {
		c.info.Implicit[x.expr] = conv
		if conv == ConvNilIface {
			c.info.IfaceTargets[x.expr] = target
		}
	}
	return true
}

// reuseBox reports whether e is a smart-cast optional value (a T? read as T)
// and, if so, marks it to be stored as the box it came from. Optional values
// are immutable, so `return pct` returns pct rather than a copy, new(*pct).
func (c *Checker) reuseBox(e syntax.Expr) bool {
	inner := syntax.Unparen(e)
	switch c.info.Implicit[inner] {
	case ConvUnbox, ConvReuse:
		c.info.Implicit[inner] = ConvReuse
		c.info.Implicit[e] = ConvReuse
		return true
	}
	return false
}

// assignableQuiet reports assignability without diagnostics.
func (c *Checker) assignableQuiet(x *operand, target types.Type) bool {
	if types.IsUntyped(x.typ) {
		tb := types.Base(target)
		if nb, ok := tb.(*types.Nullable); ok {
			tb = nb.Elem
		}
		return types.IsInterface(tb) || c.untypedFits(x, tb)
	}
	ok, _, _ := c.assignable(x.typ, target)
	return ok
}

func (c *Checker) untypedFits(x *operand, t types.Type) bool {
	switch {
	case types.IsBoolean(x.typ):
		return types.IsBoolean(t)
	case types.IsString(x.typ):
		return types.IsString(t)
	case types.IsNumeric(x.typ):
		if !types.IsNumeric(t) {
			return false
		}
		return x.val == nil || c.representable(x.val, t)
	}
	return false
}

func (c *Checker) reportMismatch(x *operand, target types.Type, context string) {
	c.reportMismatchReason(x, target, context, "")
}

func (c *Checker) reportMismatchReason(x *operand, target types.Type, context, reason string) {
	span := x.expr.Span()
	if x.val != nil && types.IsUntyped(x.typ) && types.IsNumeric(x.typ) && types.IsNumeric(types.Base(target)) {
		// Only an untyped constant can fail by not fitting: a typed one
		// (UserID(3), time.Second) has the wrong type.
		c.errorf(diag.EOverflow, span, "constant %s cannot be represented as %s", x.val, target).
			WithLabel("does not fit in %s", target)
		return
	}
	msg := "type mismatch"
	if context != "" {
		msg += " in " + context
	}
	found := x.typ
	if types.IsUntyped(found) {
		found = types.DefaultType(found)
	}
	d := c.errorf(diag.ETypeMismatch, span, "%s", msg).WithLabel("expected %s, found %s", target, found)
	if reason != "" {
		d.WithNote("%s", reason)
	}
	c.mismatchHints(d, x, target)
}

// mismatchHints adds conversion suggestions for common mismatches.
func (c *Checker) mismatchHints(d *diag.Diagnostic, x *operand, target types.Type) {
	src, dst := types.Base(x.typ), types.Base(target)
	text := x.expr.Span().Short()
	switch {
	case types.MayBeNull(x.typ) && !types.MayBeNull(target) && types.IdenticalIgnoringNull(x.typ, target):
		d.WithHint("the value may be null; check it first, use ?: to provide a default, or !! to assert")
	case types.IsString(src) && types.IsInteger(dst):
		fn := "toInt64"
		if b := types.BasicOf(dst); b != nil && b.Kind == types.IntKind {
			fn = "toInt"
		}
		d.WithHint("convert explicitly: %s.%s()? (parsing can fail)", text, fn)
	case types.IsString(src) && types.IsFloat(dst):
		d.WithHint("convert explicitly: %s.toFloat64()? (parsing can fail)", text)
	case definedUnder(src) != nil && definedUnder(dst) != nil:
		// UserID and OrderID: distinct types on purpose (ADR 0037).
		d.WithNote("%s and %s are different defined types; convert explicitly only if the value really is a %s", src, dst, dst)
	case types.IsNumeric(src) && types.IsNumeric(dst):
		d.WithEdit(x.expr.Span(), dst.String()+"("+x.expr.Span().Text()+")", "Lyth never converts numbers implicitly; write %s(%s)", dst, text)
	case types.IsNumeric(src) && types.IsString(dst):
		d.WithHint("use a string template: \"${%s}\"", text)
	}
	if r, ok := x.typ.(*types.Result); ok && len(r.Success) == 1 && types.IdenticalIgnoringNull(r.Success[0], target) {
		d.WithHint("the call can fail; propagate the error with %s?", text)
	}
}

// assignable implements Lyth's assignability (types.md §8).
func (c *Checker) assignable(src, dst types.Type) (bool, ConversionKind, string) {
	if src == types.Invalid || dst == types.Invalid || src == types.Nothing {
		return true, ConvNone, ""
	}
	if types.Identical(src, dst) {
		return true, ConvNone, ""
	}
	if src == types.Null {
		switch collectionOf(dst).(type) {
		case *types.Slice, *types.Map:
			return true, ConvNone, ""
		}
		if types.MayBeNull(dst) {
			return true, ConvNone, ""
		}
		return false, ConvNone, dst.String() + " is not nullable; null can only be assigned to nullable types such as " + types.MakeNullable(dst).String()
	}
	// Nullable targets.
	if dn, ok := dst.(*types.Nullable); ok {
		inner := src
		srcNullable := false
		switch s := src.(type) {
		case *types.Nullable:
			inner, srcNullable = s.Elem, true
		case *types.Platform:
			inner, srcNullable = s.Elem, true
		}
		if ok, conv, reason := c.assignable(inner, dn.Elem); ok {
			if !srcNullable && !types.IsNilable(dn.Elem) && conv == ConvNone {
				conv = ConvBox
			}
			if srcNullable && conv == ConvNone && nilInterface(inner, dn.Elem) {
				conv = ConvNilIface
			}
			return true, conv, reason
		}
		// *T and *T? are read-only views of T? for value types.
		if !types.IsNilable(dn.Elem) {
			if p, ok := types.Base(src).(*types.Pointer); ok && types.Identical(types.Base(p.Elem), types.Base(dn.Elem)) {
				return true, ConvNone, ""
			}
		}
		return false, ConvNone, ""
	}
	if dp, ok := dst.(*types.Platform); ok {
		if sn, ok := src.(*types.Nullable); ok {
			// Go APIs do not declare nullability, so a nullable value may be
			// passed to them (Kotlin's platform-type rule).
			if ok, _, _ := c.assignable(sn.Elem, dp.Elem); ok && nilInterface(sn.Elem, dp.Elem) {
				// To an interface parameter (any, ...any, fmt.Stringer) it
				// goes in as Lyth converts to its own interfaces: null as a
				// nil interface, an optional value unboxed (ADR 0040). fmt,
				// slog, and testify print the value, and drivers and
				// encoders read a nil interface as NULL or null, as they read
				// a nil *T.
				return true, ConvNilIface, ""
			}
			// To a pointer parameter it goes in its Go representation, as Go
			// code passes it: *T for an optional value.
			if ok, _, _ := c.assignable(sn.Elem, dp.Elem); ok && gotypes.AssignableTo(types.Lower(sn), types.Lower(dp.Elem)) {
				return true, ConvNone, ""
			}
		}
	}
	switch s := src.(type) {
	case *types.Platform:
		// Platform values may be used as non-null.
		return c.assignable(s.Elem, types.Base(dst))
	case *types.Nullable:
		if ok, _, _ := c.assignable(s.Elem, dst); ok {
			return false, ConvNone, "the value may be null"
		}
		return false, ConvNone, ""
	}
	if dp, ok := dst.(*types.Platform); ok {
		return c.assignable(src, dp.Elem)
	}
	// Both non-null from here on.
	if r, ok := src.(*types.Result); ok {
		if len(r.Raw) == 1 {
			return c.assignable(r.Raw[0], dst)
		}
		return false, ConvNone, ""
	}
	if _, ok := src.(*types.Tuple); ok {
		return false, ConvNone, ""
	}
	// Composite types must agree structurally, with compatible nullability.
	if !c.nestedCompatible(src, dst) {
		return false, ConvNone, ""
	}
	gs, gd := types.Lower(src), types.Lower(dst)
	if gs == nil || gd == nil || gs.Underlying() == nil || gd.Underlying() == nil {
		// A type still being defined, reached from an expression inside a
		// declaration's own type (an array length); errors were reported.
		return false, ConvNone, ""
	}
	if reason := embeddedSealed(src, dst); reason != "" {
		return false, ConvNone, reason
	}
	if gotypes.AssignableTo(gs, gd) {
		if types.IsInterface(dst) {
			if reason := c.methodNullability(src, dst); reason != "" {
				return false, ConvNone, reason
			}
		}
		return true, ConvNone, ""
	}
	if types.IsInterface(dst) {
		if m, wrong := gotypes.MissingMethod(gs, gd.Underlying().(*gotypes.Interface), true); m != nil {
			return false, ConvNone, missingMethodReason(src, dst, m, wrong)
		}
	}
	return false, ConvNone, ""
}

// embeddedSealed explains why a Lyth struct that embeds a member of a sealed
// interface is not a member itself: in Go the marker method is promoted,
// but membership is declared (ADR 0025, ADR 0043). It is "" otherwise.
func embeddedSealed(src, dst types.Type) string {
	sd := types.SealedOf(dst)
	if sd == nil {
		return ""
	}
	n, ok := types.Base(src).(*types.Named)
	p, isPtr := types.Base(src).(*types.Pointer)
	if isPtr {
		n, ok = types.Base(p.Elem).(*types.Named)
	}
	if !ok || n.Decl == nil || n.Decl.Kind != types.StructDecl || n.Decl.PkgPath != sd.PkgPath {
		return ""
	}
	for _, m := range sd.Members {
		if mn, ok := types.Base(m).(*types.Named); ok && mn.Decl == n.Decl {
			if isPtr {
				// Go gives *T the methods of T, but a when over the
				// interface tests for the member T itself.
				return "*" + n.Decl.Name + " is not a member of sealed " + types.Base(dst).String() + "; " + n.Decl.Name + " is: use the value, not its address"
			}
			return ""
		}
		if mp, ok := types.Base(m).(*types.Pointer); ok {
			if mn, ok := types.Base(mp.Elem).(*types.Named); ok && mn.Decl == n.Decl {
				return ""
			}
		}
	}
	if !slices.ContainsFunc(n.Decl.Fields, func(f *types.Field) bool { return f.Embedded }) {
		return "" // go/types explains the missing marker
	}
	return dst.String() + " is sealed: embedding one of its members does not make " + n.Decl.Name + " a member; declare it (struct " + n.Decl.Name + " : " + types.Base(dst).String() + ")"
}

func missingMethodReason(src, dst types.Type, m *gotypes.Func, wrong bool) string {
	if sd := types.SealedOf(dst); sd != nil && m.Name() == sd.Marker {
		name := types.Base(src).String()
		if p, ok := types.Base(src).(*types.Pointer); ok {
			name = types.Base(p.Elem).String()
		}
		return dst.String() + " is sealed: only types that declare it implement it (struct " + name + " : " + types.Base(dst).String() + ")"
	}
	if wrong {
		// go/types also reports a method with a pointer receiver as wrong.
		gs := types.Lower(src)
		if _, isPtr := gs.Underlying().(*gotypes.Pointer); !isPtr {
			if iface, ok := types.Lower(dst).Underlying().(*gotypes.Interface); ok && gotypes.Implements(gotypes.NewPointer(gs), iface) {
				return "method " + m.Name() + " has a pointer receiver, so only *" + src.String() + " implements " + dst.String() + "; use &value"
			}
		}
		return src.String() + " has method " + m.Name() + " with the wrong signature for " + dst.String()
	}
	if p, ok := types.Base(src).(*types.Named); ok && p.Decl != nil {
		for _, dm := range p.Decl.Methods {
			if strings.EqualFold(dm.GoName, m.Name()) && dm.GoName != m.Name() {
				return "method " + dm.Name + " must be named " + m.Name() + " to implement " + dst.String()
			}
			if dm.GoName == m.Name() && dm.PtrRecv {
				return "method " + dm.Name + " has a pointer receiver; use &value (type *" + src.String() + ")"
			}
		}
	}
	return src.String() + " does not implement " + dst.String() + " (missing method " + m.Name() + ")"
}

// nilInterface reports whether a nullable value of type inner needs
// ConvNilIface to become a value of interface type iface. Its Go
// representation is a pointer, function, or channel (an optional value is a
// pointer too), which Go would wrap in a non-nil interface even when it is
// nil. Interfaces convert as they are, and slices and maps are never null.
func nilInterface(inner, iface types.Type) bool {
	if !types.IsInterface(iface) || types.IsInterface(inner) {
		return false
	}
	switch types.Base(inner).(type) {
	case *types.Slice, *types.Map:
		return false
	}
	return true
}

// nestedCompatible checks nullability compatibility inside composite types:
// at each nested position, nullability must match unless either side is a
// platform type. Top-level nullability is handled by assignable.
func (c *Checker) nestedCompatible(src, dst types.Type) bool {
	src, dst = types.Base(src), types.Base(dst)
	switch s := src.(type) {
	case *types.Slice:
		d, ok := dst.(*types.Slice)
		return !ok || nullCompat(s.Elem, d.Elem)
	case *types.Array:
		d, ok := dst.(*types.Array)
		return !ok || nullCompat(s.Elem, d.Elem)
	case *types.Map:
		d, ok := dst.(*types.Map)
		return !ok || nullCompat(s.Key, d.Key) && nullCompat(s.Elem, d.Elem)
	case *types.Pointer:
		d, ok := dst.(*types.Pointer)
		return !ok || nullCompat(s.Elem, d.Elem)
	case *types.Chan:
		d, ok := dst.(*types.Chan)
		return !ok || nullCompat(s.Elem, d.Elem)
	case *types.Signature:
		d, ok := dst.(*types.Signature)
		if !ok {
			return true
		}
		if len(s.Params) != len(d.Params) {
			return true // go/types decides
		}
		for i := range s.Params {
			if !nullCompat(d.Params[i], s.Params[i]) {
				return false
			}
		}
		sr, dr := types.RawResults(s), types.RawResults(d)
		if len(sr) != len(dr) {
			return true
		}
		for i := range sr {
			if !nullCompat(sr[i], dr[i]) {
				return false
			}
		}
	}
	return true
}

func nullCompat(a, b types.Type) bool {
	if types.IsPlatform(a) || types.IsPlatform(b) {
		return true
	}
	if types.IsNullable(a) != types.IsNullable(b) {
		return false
	}
	return true
}

func isGoComparable(t types.Type) bool {
	return gotypes.Comparable(types.Lower(t))
}

// representable reports whether constant v fits in type t.
func (c *Checker) representable(v constant.Value, t types.Type) bool {
	if _, ok := types.Base(t).(*types.TypeParam); ok {
		return types.TermsAll(t, func(term types.Type) bool { return c.representable(v, term) })
	}
	b := types.BasicOf(t)
	if b == nil {
		return false
	}
	switch b.Kind {
	case types.BoolKind, types.UntypedBoolKind:
		return v.Kind() == constant.Bool
	case types.StringKind, types.UntypedStringKind:
		return v.Kind() == constant.String
	}
	if v.Kind() == constant.Bool || v.Kind() == constant.String {
		return false
	}
	if types.IsInteger(b) {
		iv := constant.ToInt(v)
		if iv.Kind() != constant.Int {
			return false
		}
		if types.IsUntyped(b) {
			return true
		}
		lo, hi := intRange(b.Kind)
		return constant.Compare(iv, tokenGEQ, lo) && constant.Compare(iv, tokenLEQ, hi)
	}
	if types.IsFloat(b) {
		fv := constant.ToFloat(v)
		if fv.Kind() != constant.Float && fv.Kind() != constant.Int {
			return false
		}
		if b.Kind == types.Float32Kind {
			f, _ := constant.Float64Val(fv)
			return !math.IsInf(float64(float32(f)), 0)
		}
		f, _ := constant.Float64Val(fv)
		return !math.IsInf(f, 0)
	}
	if types.IsComplex(b) {
		return constant.ToComplex(v).Kind() != constant.Unknown
	}
	return false
}

func intRange(k types.BasicKind) (constant.Value, constant.Value) {
	switch k {
	case types.Int8Kind:
		return constant.MakeInt64(math.MinInt8), constant.MakeInt64(math.MaxInt8)
	case types.Int16Kind:
		return constant.MakeInt64(math.MinInt16), constant.MakeInt64(math.MaxInt16)
	case types.Int32Kind:
		return constant.MakeInt64(math.MinInt32), constant.MakeInt64(math.MaxInt32)
	case types.Int64Kind, types.IntKind:
		return constant.MakeInt64(math.MinInt64), constant.MakeInt64(math.MaxInt64)
	case types.UInt8Kind:
		return constant.MakeInt64(0), constant.MakeUint64(math.MaxUint8)
	case types.UInt16Kind:
		return constant.MakeInt64(0), constant.MakeUint64(math.MaxUint16)
	case types.UInt32Kind:
		return constant.MakeInt64(0), constant.MakeUint64(math.MaxUint32)
	}
	return constant.MakeInt64(0), constant.MakeUint64(math.MaxUint64)
}

// ---------------------------------------------------------------------------
// Mutability

// mutablePath reports whether e denotes mutable storage, reporting an error
// that uses verb ("assign to", "take the address of") when it does not.
func (c *Checker) mutablePath(e syntax.Expr, verb string, code diag.Code) bool {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.Ident:
		switch s := c.info.Uses[x].(type) {
		case *VarSym:
			if s.Mutable {
				return true
			}
			c.immutableBinding(x, s, verb, code)
			return false
		case *MemberSym:
			if s.Field != nil {
				return c.receiverMutable(s.Recv, x.S, s.Field.Name, verb)
			}
			c.errorf(code, x.S, "cannot %s method %s", verb, s.Method.Name)
			return false
		case *GoObj:
			if _, ok := s.Obj.(*gotypes.Var); ok {
				return true
			}
		case *ConstSym:
			c.errorf(code, x.S, "cannot %s constant %s", verb, x.Name)
			return false
		case *FuncSym:
			c.errorf(code, x.S, "cannot %s function %s", verb, x.Name)
			return false
		case nil:
			return false // already reported
		}
		c.errorf(code, x.S, "cannot %s %s", verb, x.Name)
		return false
	case *syntax.ThisExpr:
		c.errorf(code, x.S, "cannot %s self: the receiver is read-only", verb)
		return false
	case *syntax.SelectorExpr:
		sel := c.info.Selections[x]
		if sel == nil {
			return false
		}
		if sel.Kind == SelPkgMember {
			if _, ok := sel.Obj.(*gotypes.Var); ok {
				return true
			}
			if s, ok := sel.Obj.(*VarSym); ok {
				if s.Mutable {
					return true
				}
				c.errorf(diag.EReassignVal, x.Sel.S, "cannot %s %s: it is read-only", verb, x.Sel.Name)
				return false
			}
			c.errorf(code, x.S, "cannot %s %s", verb, x.Span().Short())
			return false
		}
		if sel.Kind != SelField {
			c.errorf(code, x.S, "cannot %s %s", verb, x.Span().Short())
			return false
		}
		if x.Safe {
			c.errorf(code, x.OpSpan, "cannot %s a safe-call result", verb)
			return false
		}
		recvType := c.info.TypeOf(x.X)
		if _, isPtr := types.Base(recvType).(*types.Pointer); isPtr {
			return true // mutation through a pointer
		}
		if c.optionalValue(x.X, x.Sel.Name, verb) {
			return false
		}
		if id, ok := syntax.Unparen(x.X).(*syntax.ThisExpr); ok && c.fn != nil && c.fn.recv != nil {
			return c.receiverMutable(c.fn.recv, id.S, x.Sel.Name, verb)
		}
		return c.mutablePathFor(x.X, x.Sel.Name, verb)
	case *syntax.IndexExpr:
		t := collectionOf(c.info.TypeOf(x.X))
		switch t.(type) {
		case *types.Map:
			if code == diag.EAddressOfVal || code == diag.EPtrMethodVal {
				// Go's map elements are not addressable: a map may move them.
				c.errorf(code, x.S, "cannot %s a map element: map elements are not addressable", verb).
					WithHint("copy it into a variable, change that, and store it back: v := %s; ...; %s = v", x.S.Text(), x.S.Text())
				return false
			}
			return true
		case *types.Slice:
			return true
		case *types.Pointer:
			return true
		case *types.Array:
			return c.mutablePathFor(x.X, "element", verb)
		}
		if types.IsString(t) {
			c.errorf(code, x.S, "cannot %s a byte of a string: strings are immutable", verb)
			return false
		}
		return false
	case *syntax.UnaryExpr:
		if x.Op == syntax.MUL {
			return true
		}
	}
	c.errorf(code, e.Span(), "cannot %s %s", verb, e.Span().Short())
	return false
}

// optionalValue reports (and returns true) when e reads an optional value
// (P? smart-cast to P): its box may be shared by copies of the variable, so
// changing it in place would change them too.
func (c *Checker) optionalValue(e syntax.Expr, member, verb string) bool {
	if c.info.Implicit[e] != ConvUnbox {
		return false
	}
	name := e.Span().Short()
	if verb == "call" {
		c.errorf(diag.EPtrMethodVal, e.Span(), "cannot call %s on %s: the method modifies its receiver, and %s is an optional value, which copies share", member, name, name).
			WithHint("call it on a copy and store that back: v := %s; v.%s(...); %s = v", name, member, name)
		return true
	}
	c.errorf(diag.EMutateVal, e.Span(), "cannot %s %s.%s: %s is an optional value, which copies share", verb, name, member, name).
		WithHint("change a copy and store it back: v := %s; v.%s = ...; %s = v", name, member, name)
	return true
}

// mutablePathFor checks the container of a field or array element.
func (c *Checker) mutablePathFor(container syntax.Expr, member, verb string) bool {
	inner := syntax.Unparen(container)
	switch x := inner.(type) {
	case *syntax.Ident:
		if v, ok := c.info.Uses[x].(*VarSym); ok && !v.Mutable {
			what := "binding"
			switch v.Kind {
			case ParamVar:
				what = "parameter"
			case LoopVar:
				what = "loop variable"
			case ReceiverVar:
				return c.receiverMutable(v, x.S, member, verb)
			}
			d := c.errorf(diag.EMutateVal, x.S, "cannot %s %s.%s: %s is an immutable %s", verb, x.Name, member, x.Name, what)
			if v.Kind == LocalVar || v.Kind == GlobalVar {
				d.WithRelated(v.Span, "%s declared here", v.name)
			}
			return false
		}
		if m, ok := c.info.Uses[x].(*MemberSym); ok && m.Field != nil {
			return c.receiverMutable(m.Recv, x.S, m.Field.Name, verb)
		}
	case *syntax.CallExpr:
		c.errorf(diag.EMutateVal, container.Span(), "cannot %s a field of a call result; store it in a variable first", verb)
		return false
	case *syntax.IndexExpr:
		if _, isMap := collectionOf(c.info.TypeOf(x.X)).(*types.Map); isMap {
			c.errorf(diag.EMutateVal, container.Span(), "cannot %s %s of a map element: map elements are not addressable", verb, member).
				WithHint("copy it into a variable, change that, and store it back: v := %s; v.%s = ...; %s = v", x.S.Text(), member, x.S.Text())
			return false
		}
	}
	return c.mutablePath(container, verb, diag.EMutateVal)
}

func (c *Checker) receiverMutable(recv *VarSym, span source.Span, member, verb string) bool {
	if _, isPtr := types.Base(recv.Type).(*types.Pointer); isPtr {
		return true
	}
	c.errorf(diag.EMutateVal, span, "cannot %s %s: the method has a value receiver, so this is a copy", verb, member).
		WithHint("declare the method with a pointer receiver: func *%s.%s(...)", types.Base(recv.Type), c.methodName())
	return false
}

func (c *Checker) immutableBinding(id *syntax.Ident, v *VarSym, verb string, code diag.Code) {
	if code == diag.EAddressOfVal {
		c.errorf(diag.EAddressOfVal, id.S, "cannot take the address of %s: it is read-only", id.Name).
			WithRelated(v.Span, "declared here").
			WithHint("copy it into a local variable and take that variable's address")
		return
	}
	switch v.Kind {
	case ParamVar:
		c.errorf(diag.EReassignVal, id.S, "cannot %s parameter %s: parameters are read-only", verb, id.Name).
			WithHint("to replace it for the rest of the block with a value derived from it, declare it again: %s := f(%s)", id.Name, id.Name).
			WithHint("or copy it into a local: %sNew := %s", id.Name, id.Name)
	case LoopVar:
		c.errorf(diag.EReassignVal, id.S, "cannot %s loop variable %s", verb, id.Name)
	case CatchVar:
		c.errorf(diag.EReassignVal, id.S, "cannot %s %s", verb, id.Name)
	default:
		c.errorf(diag.EReassignVal, id.S, "cannot %s %s: it is read-only", verb, id.Name).
			WithRelated(v.Span, "%s declared here", id.Name)
	}
}

var (
	tokenGEQ = goToken(syntax.GEQ)
	tokenLEQ = goToken(syntax.LEQ)
)

// constSign is constant.Sign for numeric constants; it returns 1 for
// non-numeric constants (which never compare as zero or negative).
func constSign(v constant.Value) int {
	switch v.Kind() {
	case constant.Int, constant.Float, constant.Complex:
		return constant.Sign(v)
	}
	return 1
}

// methodName names the method being checked, for hints; lambdas inside it
// report the method's name too.
func (c *Checker) methodName() string {
	for s := c.scope; s != nil; s = s.parent {
		if s.kind == FuncScope && s.fn != nil && !s.fn.isLambda && s.fn.name != "" {
			return s.fn.name
		}
	}
	return "name"
}

// methodNullability explains why src, whose Go method set implements the
// Lyth interface dst, still does not implement it in Lyth: a method returns
// null where dst promises a value, or rejects null that dst lets callers
// pass. It is "" when the methods fit or are not Lyth methods.
func (c *Checker) methodNullability(src, dst types.Type) string {
	dn, ok := types.Base(dst).(*types.Named)
	if !ok || dn.Decl == nil || dn.Decl.Kind != types.InterfaceDecl {
		return ""
	}
	key := [2]string{src.String(), dst.String()}
	if c.implementing[key] {
		return "" // already being compared further up
	}
	if c.implementing == nil {
		c.implementing = map[[2]string]bool{}
	}
	c.implementing[key] = true
	defer delete(c.implementing, key)
	wantBind := declBindings(dn)
	for _, im := range dn.Decl.Methods {
		dm, bind := lythMethod(types.Base(src), im.GoName, map[*types.TypeDecl]bool{})
		if dm == nil || dm.Sig == nil || im.Sig == nil {
			continue
		}
		for i, wp := range im.Sig.Params {
			if i >= len(dm.Sig.Params) {
				break
			}
			w, g := subst(wp, wantBind), subst(dm.Sig.Params[i], bind)
			if ok, conv, _ := c.assignable(w, g); !ok || conv != ConvNone {
				return fmt.Sprintf("method %s takes %s where %s passes %s", im.Name, g, dst, w)
			}
		}
		for i, wr := range im.Sig.Results {
			if i >= len(dm.Sig.Results) {
				break
			}
			w, g := subst(wr, wantBind), subst(dm.Sig.Results[i], bind)
			if ok, conv, _ := c.assignable(g, w); !ok || conv != ConvNone {
				return fmt.Sprintf("method %s returns %s where %s promises %s", im.Name, g, dst, w)
			}
		}
	}
	return ""
}

// lythMethod finds the Lyth method goName of t (a Lyth struct, a pointer to
// one, or a Lyth interface), including methods promoted from embedded
// fields, with the type arguments of its declaration; nil when the method
// comes from Go or is missing.
func lythMethod(t types.Type, goName string, seen map[*types.TypeDecl]bool) (*types.Method, bindings) {
	if p, ok := t.(*types.Pointer); ok {
		t = types.Base(p.Elem)
	}
	n, ok := t.(*types.Named)
	if !ok || n.Decl == nil || seen[n.Decl] {
		return nil, nil
	}
	seen[n.Decl] = true
	for _, m := range n.Decl.Methods {
		if m.GoName == goName {
			return m, declBindings(n)
		}
	}
	for _, f := range n.Decl.Fields {
		if f.Embedded {
			if m, bind := lythMethod(types.Base(subst(f.Type, declBindings(n))), goName, seen); m != nil {
				return m, bind
			}
		}
	}
	return nil, nil
}
