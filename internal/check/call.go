package check

import (
	"fmt"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// callArgs returns the arguments of a call including a trailing lambda.
func callArgs(e *syntax.CallExpr) []*syntax.Arg {
	if e.Trailing == nil {
		return e.Args
	}
	return append(append([]*syntax.Arg(nil), e.Args...), &syntax.Arg{Value: e.Trailing, S: e.Trailing.S})
}

func (c *Checker) call(e *syntax.CallExpr, want types.Type) *operand {
	args := callArgs(e)
	saveCallee, saveArgs := c.callee, c.calleeArgs
	c.callee, c.calleeArgs = syntax.Unparen(e.Fun), len(args)
	f := c.expr(e.Fun, nil)
	c.callee, c.calleeArgs = saveCallee, saveArgs
	switch f.mode {
	case ModeInvalid:
		for _, a := range args {
			var want types.Type
			if _, ok := syntax.Unparen(a.Value).(*syntax.LambdaExpr); ok {
				// The callee was already reported; its lambda's parameter
				// types are unknown rather than missing.
				want = types.Invalid
			}
			c.expr(a.Value, want)
		}
		return invalid(e)
	case ModeBuiltin:
		return c.builtinCall(e, f.builtin, args, want)
	case ModeType:
		return c.typeCall(e, f.typ, args, want)
	case ModePackage:
		c.errorf(diag.ENotCallable, e.Fun.Span(), "package %s cannot be called", e.Fun.Span().Short())
		return invalid(e)
	case ModeNoValue:
		c.errorf(diag.ENotCallable, e.Fun.Span(), "%s does not produce a value that can be called", e.Fun.Span().Short())
		return invalid(e)
	}
	info := &CallInfo{Kind: CallFunc}
	var sel *Selection
	if s, ok := syntax.Unparen(e.Fun).(*syntax.SelectorExpr); ok {
		sel = c.info.Selections[s]
	}
	if sel != nil {
		switch sel.Kind {
		case SelMethod:
			info.Kind = CallMethod
		case SelPrelude:
			info.Kind, info.Prelude = CallPrelude, sel.Prelude
		case SelExtension:
			info.Ext = sel.Obj.(*ExtCall)
			if info.Ext.Addr {
				s := syntax.Unparen(e.Fun).(*syntax.SelectorExpr)
				c.mutablePath(s.X, "call pointer extension "+s.Sel.Name+" on", diag.EPtrMethodVal)
			}
		}
		info.Safe, info.TryFirst = sel.Safe, sel.TryFirst
		if sel.Kind == SelField && sel.Safe {
			c.errorf(diag.EUnsupported, e.S, "calling a function-typed field through ?. is not supported yet")
			return invalid(e)
		}
	}
	if mem, ok := f.sym.(*MemberSym); ok && mem.Method != nil {
		info.Kind = CallMethod
	}
	ft := f.typ
	if types.MayBeNull(ft) && !types.IsPlatform(ft) && !info.Safe {
		c.nullableUse(f, "call")
		return invalid(e)
	}
	sig := signatureOf(ft)
	if sig == nil {
		c.errorf(diag.ENotCallable, e.Fun.Span(), "%s is not a function (type %s)", e.Fun.Span().Short(), ft)
		for _, a := range args {
			c.expr(a.Value, nil)
		}
		return invalid(e)
	}
	// Pointer-receiver methods need mutable storage when called on a value.
	if info.Kind == CallMethod && sel != nil && sel.PtrRecv {
		if s, ok := syntax.Unparen(e.Fun).(*syntax.SelectorExpr); ok {
			recvT := c.info.TypeOf(s.X)
			if _, isPtr := types.Base(recvT).(*types.Pointer); !isPtr && !sel.TryFirst {
				c.ptrMethodReceiver(s)
			}
		}
	}
	if info.Kind == CallMethod && sel == nil {
		if mem, ok := f.sym.(*MemberSym); ok && mem.Method != nil && mem.Method.PtrRecv {
			if _, isPtr := types.Base(mem.Recv.Type).(*types.Pointer); !isPtr {
				c.errorf(diag.EPtrMethodVal, e.Fun.Span(), "cannot call %s: it has a pointer receiver, but this method's receiver is a value copy", mem.Method.Name).
					WithHint("declare the enclosing method with a pointer receiver")
			}
		}
	}
	callee, lyth := c.lythCallee(e, f)
	if lyth && (hasNamedArgs(args) || callee != nil && hasDefaults(callee)) {
		return c.boundCall(e, sig, args, info, callee)
	}
	return c.callSig(e, sig, args, want, info)
}

// lythCallee returns the declaration of the Lyth function or method a call
// invokes (nil for a Lyth interface method), and whether the callee is
// declared in Lyth at all: only Lyth functions take named arguments and
// default values; Go functions and function values never do.
func (c *Checker) lythCallee(e *syntax.CallExpr, f *operand) (*FuncSym, bool) {
	switch s := f.sym.(type) {
	case *FuncSym:
		return s, s.Decl != nil
	case *MemberSym:
		if s.Method != nil {
			fs, _ := s.Method.Sym.(*FuncSym)
			return fs, true
		}
	}
	if sel, ok := syntax.Unparen(e.Fun).(*syntax.SelectorExpr); ok {
		if info := c.info.Selections[sel]; info != nil {
			switch o := info.Obj.(type) {
			case *types.Method:
				fs, _ := o.Sym.(*FuncSym)
				return fs, true
			case *ExtCall:
				return o.Func, true
			}
		}
	}
	return nil, false
}

func hasNamedArgs(args []*syntax.Arg) bool {
	for _, a := range args {
		if a.Name != nil {
			return true
		}
	}
	return false
}

func hasDefaults(fs *FuncSym) bool {
	if fs.Decl == nil {
		return false
	}
	for _, p := range fs.Decl.Params {
		if p.Default != nil {
			return true
		}
	}
	return false
}

// boundCall checks a call of a Lyth function with named arguments or default
// values. Positional arguments come first and fill parameters in order;
// named ones follow in any order; a trailing lambda fills the last
// parameter; parameters left out take their default values. Arguments are
// evaluated in the order written, then the defaults.
func (c *Checker) boundCall(e *syntax.CallExpr, sig *types.Signature, args []*syntax.Arg, info *CallInfo, callee *FuncSym) *operand {
	generic := sig
	if len(sig.TypeParams) > 0 && sig.Variadic {
		c.errorf(diag.EUnsupported, e.S, "named arguments and default values are not supported for variadic generic functions yet").
			WithHint("pass every argument positionally")
		for _, a := range args {
			c.value(a.Value, nil)
		}
		return invalid(e)
	}
	np := len(sig.Params)
	variadic := -1
	if sig.Variadic {
		variadic = np - 1
	}
	name := e.Fun.Span().Short()
	filled := make([]bool, np)
	argParams := make([]int, len(args))
	for i := range argParams {
		argParams[i] = -1 // an argument that binds to no parameter is only checked
	}
	ok, named := true, false
	for i, a := range args {
		trailing := e.Trailing != nil && i == len(args)-1
		p := -1
		switch {
		case trailing:
			p = np - 1
			if np == 0 {
				c.errorf(diag.EArgCount, a.S, "%s takes no parameters, so it cannot take a trailing lambda", e.Fun.Span().Short())
				ok = false
				continue
			}
			if filled[p] {
				c.errorf(diag.EArgCount, a.S, "the trailing lambda would fill parameter %s, which is already given", paramName(sig, np-1))
				ok = false
				continue
			}
		case a.Name == nil:
			if named {
				c.errorf(diag.EInvalidUse, a.S, "positional arguments must come before named ones")
				ok = false
				continue
			}
			p = i
			if p >= np || variadic >= 0 && p > variadic {
				if variadic < 0 {
					c.argCountError(e, sig, len(args))
					c.checkArgsLoose(args)
					return invalid(e)
				}
				p = variadic
			}
		default:
			named = true
			p = slices.Index(sig.ParamNames, a.Name.Name)
			if p < 0 {
				d := c.errorf(diag.EArgCount, a.Name.S, "%s has no parameter named %s", name, a.Name.Name)
				if s := closest(a.Name.Name, sig.ParamNames); s != "" {
					d.WithHint("did you mean %s?", s)
				}
				ok = false
				continue
			}
			if p == variadic {
				c.errorf(diag.EInvalidUse, a.Name.S, "the variadic parameter %s cannot be passed by name", a.Name.Name)
				ok = false
				continue
			}
			if filled[p] {
				c.errorf(diag.EDuplicate, a.Name.S, "parameter %s is given twice", a.Name.Name)
				ok = false
				continue
			}
		}
		if a.Spread && p != variadic {
			c.errorf(diag.EInvalidUse, a.S, "... can only spread a list into the variadic parameter")
			ok = false
		}
		argParams[i] = p
		filled[p] = true
	}
	var missing []string
	for p := 0; p < np; p++ {
		if filled[p] || p == variadic {
			continue
		}
		if callee != nil && callee.Decl != nil && p < len(callee.Decl.Params) && callee.Decl.Params[p].Default != nil {
			info.Defaults = append(info.Defaults, DefaultArg{Param: p, Expr: callee.Decl.Params[p].Default, Info: callee.Pkg.Info})
			continue
		}
		missing = append(missing, paramName(sig, p))
	}
	if len(missing) > 0 {
		c.errorf(diag.EArgCount, e.Rparen, "missing %s for %s", plural(len(missing), "argument"), strings.Join(missing, ", ")).
			WithNote("signature: %s", sig)
		ok = false
	}
	if len(generic.TypeParams) > 0 {
		// Infer from the arguments given, in their parameters' places, then
		// instantiate the whole signature: defaults are constants, which
		// take the type the other arguments give their parameter.
		inst, targs := c.inferBound(e, generic, args, argParams, info)
		if inst == nil {
			return invalid(e)
		}
		sig = inst
		info.TypeArgs = targs
		info.ExplicitTypeArgs = generic.GoSig != nil && !goInfers(generic.GoSig)
		for _, d := range info.Defaults {
			if tv, ok := d.Info.Types[d.Expr]; ok && tv.Value != nil && hasTypeParams(generic.Params[d.Param]) {
				// The constant must fit the type this call gives T.
				c.assign(&operand{mode: ModeValue, typ: untypedOf(tv.Value), val: tv.Value, expr: e}, sig.Params[d.Param], "default value of "+paramName(sig, d.Param))
			}
		}
	}
	info.Sig = sig
	for _, a := range args {
		info.Args = append(info.Args, a.Value)
	}
	info.ArgParams = argParams
	c.info.Calls[e] = info
	for i, a := range args {
		p := argParams[i]
		if p < 0 {
			c.expr(a.Value, nil)
			continue
		}
		pt := sig.Params[p]
		if p == variadic && !a.Spread {
			if sl, isSlice := pt.(*types.Slice); isSlice {
				pt = sl.Elem
			}
		}
		x := c.argValue(a.Value, pt) // inferBound checked some already
		if !c.assign(x, pt, argContext(e, p, sig)) {
			ok = false
		}
	}
	c.preArgs = nil
	if !ok {
		return invalid(e)
	}
	return c.callResult(sig, info, e.S)
}

// untypedOf is the untyped constant type of a constant's kind.
func untypedOf(v constant.Value) types.Type {
	switch v.Kind() {
	case constant.Bool:
		return types.UntypedBool
	case constant.String:
		return types.UntypedString
	case constant.Float:
		return types.UntypedFloat
	case constant.Complex:
		return types.UntypedComplex
	}
	return types.UntypedInt
}

// inferBound infers the type arguments of a generic Lyth function called
// with named arguments or defaults: the arguments given are unified with
// their parameters, through a signature holding just those parameters.
func (c *Checker) inferBound(e *syntax.CallExpr, sig *types.Signature, args []*syntax.Arg, argParams []int, info *CallInfo) (*types.Signature, []types.Type) {
	given := *sig
	given.Params, given.ParamNames = nil, nil
	var ordered []*syntax.Arg
	for i, a := range args {
		if p := argParams[i]; p >= 0 {
			given.Params = append(given.Params, sig.Params[p])
			ordered = append(ordered, &syntax.Arg{Value: a.Value, Spread: a.Spread, S: a.S})
		}
	}
	var pre bindings
	if info.Ext != nil {
		pre = info.Ext.Bind
	}
	inst, targs := c.inferCall(e, &given, ordered, nil, pre)
	if inst == nil {
		return nil, nil
	}
	bind := bindings{}
	for i, tp := range sig.TypeParams {
		bind[tp.Go] = targs[i]
	}
	full := substSig(sig, bind)
	full.GoSig = inst.GoSig
	return full, targs
}

func paramName(sig *types.Signature, p int) string {
	if p < len(sig.ParamNames) {
		return sig.ParamNames[p]
	}
	return fmt.Sprintf("#%d", p+1)
}

// closest returns the name in names most similar to name, if any is close.
func closest(name string, names []string) string {
	best, bestDist := "", suggestLimit(name)
	for _, n := range names {
		if d := editDistance(strings.ToLower(n), strings.ToLower(name)); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// ptrMethodReceiver checks that the receiver of a pointer method is mutable.
func (c *Checker) ptrMethodReceiver(s *syntax.SelectorExpr) {
	if c.optionalValue(s.X, s.Sel.Name, "call") {
		return
	}
	if t := c.info.TypeOf(s.X); s.Safe && t != nil && types.IsNullable(t) && !types.IsNilable(t) {
		// o?.Inc() on a P?: the box is shared like a smart-cast o's.
		name := s.X.Span().Short()
		d := c.errorf(diag.EPtrMethodVal, s.Sel.S, "cannot call %s on %s: the method modifies its receiver, and %s is an optional value, which copies share", s.Sel.Name, name, name)
		if _, isIdent := syntax.Unparen(s.X).(*syntax.Ident); isIdent {
			d.WithHint("check %s, then call it on a copy: if %s != null { v := %s; v.%s(...)%s }", name, name, name, s.Sel.Name, c.storeBack(s.X))
		}
		sharedPointerHint(d, t)
		return
	}
	inner := syntax.Unparen(s.X)
	switch x := inner.(type) {
	case *syntax.Ident:
		if v, ok := c.info.Uses[x].(*VarSym); ok && !v.Mutable {
			if v.Kind == ReceiverVar {
				c.errorf(diag.EPtrMethodVal, s.Sel.S, "cannot call %s on self: it has a pointer receiver, but this method's receiver is a value copy", s.Sel.Name).
					WithHint("declare the enclosing method with a pointer receiver")
				return
			}
			d := c.errorf(diag.EPtrMethodVal, s.Sel.S, "cannot call %s on %s: the method modifies its receiver, and %s is immutable", s.Sel.Name, x.Name, x.Name).
				WithRelated(v.Span, "%s declared here", x.Name)
			if v.Kind == LocalVar || v.Kind == GlobalVar {
				d.WithHint("copy %s into a local variable: %sCopy := %s", x.Name, x.Name, x.Name)
			}
			return
		}
		return
	case *syntax.ThisExpr:
		if c.fn != nil && c.fn.recv != nil {
			if _, isPtr := types.Base(c.fn.recv.Type).(*types.Pointer); !isPtr {
				c.errorf(diag.EPtrMethodVal, s.Sel.S, "cannot call %s on self: it has a pointer receiver, but this method's receiver is a value copy", s.Sel.Name).
					WithHint("declare the enclosing method with a pointer receiver")
			}
		}
		return
	case *syntax.CallExpr:
		c.errorf(diag.EPtrMethodVal, s.Sel.S, "cannot call pointer method %s on a call result", s.Sel.Name).
			WithHint("store the result in a variable first")
		return
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		c.mutablePath(inner, "call pointer method "+s.Sel.Name+" on", diag.EPtrMethodVal)
	}
}

// callSig checks arguments against a signature and computes the result.
func (c *Checker) callSig(e *syntax.CallExpr, sig *types.Signature, args []*syntax.Arg, want types.Type, info *CallInfo) *operand {
	for _, a := range args {
		if a.Name != nil {
			c.errorf(diag.EInvalidUse, a.Name.S, "named arguments are only for functions declared in Lyth").
				WithNote("Go functions take positional arguments: their parameter names are not part of their API")
			break
		}
	}
	if len(sig.TypeParams) > 0 {
		var pre bindings
		if info.Ext != nil {
			// The receiver has bound the type parameters it mentions.
			pre = info.Ext.Bind
		}
		inst, targs := c.inferCall(e, sig, args, want, pre)
		if inst == nil {
			return invalid(e)
		}
		info.TypeArgs = targs
		info.ExplicitTypeArgs = sig.GoSig != nil && !goInfers(sig.GoSig)
		sig = inst
	}
	info.Sig = sig
	for _, a := range args {
		info.Args = append(info.Args, a.Value)
	}
	c.info.Calls[e] = info
	if !c.checkArgs(e, sig, args) {
		return invalid(e)
	}
	return c.callResult(sig, info, e.S)
}

func (c *Checker) callResult(sig *types.Signature, info *CallInfo, span source.Span) *operand {
	if r := types.ResultOf(sig); r != nil {
		if info.Safe {
			c.errorf(diag.EUnsupported, span, "safe calls to methods that return errors are not supported yet").
				WithHint("check the receiver first: if x != null { x.M()? }")
			return &operand{mode: ModeInvalid, typ: types.Invalid}
		}
		return &operand{mode: ModeValue, typ: r}
	}
	switch len(sig.Results) {
	case 0:
		return &operand{mode: ModeNoValue, typ: types.Unit}
	case 1:
		t := sig.Results[0]
		if info.Safe {
			t = types.MakeNullable(t)
		}
		return &operand{mode: ModeValue, typ: t}
	}
	if info.Safe {
		c.errorf(diag.EUnsupported, span, "a safe call cannot return several values").
			WithHint("check the receiver first: if x != null { a, b := x.M() }")
		return &operand{mode: ModeInvalid, typ: types.Invalid}
	}
	return &operand{mode: ModeValue, typ: &types.Tuple{Elems: sig.Results}}
}

// checkArgs checks call arguments, including variadic spreading.
func (c *Checker) checkArgs(e *syntax.CallExpr, sig *types.Signature, args []*syntax.Arg) bool {
	np := len(sig.Params)
	if len(args) == 1 && (np >= 2 || sig.Variadic) && !args[0].Spread && args[0].Name == nil {
		if _, isCall := syntax.Unparen(args[0].Value).(*syntax.CallExpr); isCall {
			if forwarded, ok := c.forwardedArgs(e, sig, args[0]); forwarded {
				return ok
			}
		}
	}
	ok := true
	spread := false
	for i, a := range args {
		if a.Spread {
			if !sig.Variadic || i != len(args)-1 || i != np-1 {
				c.errorf(diag.EInvalidUse, a.S, "... can only spread a list into the variadic parameter")
				ok = false
			}
			spread = true
		}
	}
	if sig.Variadic && !spread {
		if len(args) < np-1 {
			c.argCountError(e, sig, len(args))
			c.checkArgsLoose(args)
			return false
		}
	} else if len(args) != np {
		c.argCountError(e, sig, len(args))
		c.checkArgsLoose(args)
		return false
	}
	for i, a := range args {
		var pt types.Type
		switch {
		case sig.Variadic && i >= np-1 && !a.Spread:
			if sl, ok := sig.Params[np-1].(*types.Slice); ok {
				pt = sl.Elem
			} else {
				pt = types.Invalid
			}
		default:
			pt = sig.Params[i]
		}
		x, pre := c.preArgs[a.Value]
		if !pre {
			x = c.value(a.Value, pt)
		}
		if !c.assign(x, pt, argContext(e, i, sig)) {
			ok = false
		}
	}
	c.preArgs = nil
	c.templateFormat(sig, args)
	return ok
}

// templateFormat warns about a string template passed as the format of a
// printf-like function (format string, args ...any): the values put into
// the template are read as format text, so a % in them breaks the output
// (W0010).
func (c *Checker) templateFormat(sig *types.Signature, args []*syntax.Arg) {
	np := len(sig.Params)
	if !sig.Variadic || np < 2 || len(sig.ParamNames) != np || sig.ParamNames[np-2] != "format" || len(args) < np-1 {
		return
	}
	tmpl, ok := syntax.Unparen(args[np-2].Value).(*syntax.TemplateLit)
	if !ok || args[np-2].Name != nil {
		return
	}
	for _, part := range tmpl.Parts {
		if part.X != nil {
			c.warnf(diag.WFormatString, tmpl.S, "a string template is used as a format string: a %% in the values put into it is read as a formatting verb").
				WithHint("pass the values as arguments (\"%%v\", x), or call the function without f (Print, Error) with the template")
			return
		}
	}
}

func (c *Checker) checkArgsLoose(args []*syntax.Arg) {
	for _, a := range args {
		c.expr(a.Value, nil)
	}
}

func argContext(e *syntax.CallExpr, i int, sig *types.Signature) string {
	name := e.Fun.Span().Short()
	ctx := fmt.Sprintf("argument %d of %s", i+1, name)
	if i < len(sig.ParamNames) && sig.ParamNames[i] != "" && sig.ParamNames[i] != "_" {
		ctx = fmt.Sprintf("argument %s of %s", sig.ParamNames[i], name)
	}
	return ctx
}

func (c *Checker) argCountError(e *syntax.CallExpr, sig *types.Signature, got int) {
	want := len(sig.Params)
	what := plural(want, "argument")
	if sig.Variadic {
		what = fmt.Sprintf("at least %s", plural(want-1, "argument"))
	}
	span := e.Rparen
	if got > want && !sig.Variadic && got > 0 {
		args := callArgs(e)
		span = args[want].S.Join(args[got-1].S)
	}
	c.errorf(diag.EArgCount, span, "%s expects %s, but %s given", e.Fun.Span().Short(), what, plural(got, "argument")).
		WithNote("signature: %s", sig)
}

// ---------------------------------------------------------------------------
// Construction and conversion

func (c *Checker) typeCall(e *syntax.CallExpr, t types.Type, args []*syntax.Arg, want types.Type) *operand {
	if t == types.Invalid {
		c.checkArgsLoose(args)
		return invalid(e)
	}
	if types.IsStruct(t) {
		return c.construct(e, t, args)
	}
	// Conversion.
	if len(args) != 1 || args[0].Name != nil {
		c.errorf(diag.EArgCount, e.S, "conversion to %s takes exactly one argument", t)
		c.checkArgsLoose(args)
		return invalid(e)
	}
	c.info.Calls[e] = &CallInfo{Kind: CallConvert, Target: t, Args: []syntax.Expr{args[0].Value}}
	var want0 types.Type
	switch a := syntax.Unparen(args[0].Value).(type) {
	case *syntax.LambdaExpr:
		// A lambda converted to a named function type, as in
		// http.HandlerFunc({ w, r -> ... }), takes its parameter types from
		// the type's signature.
		want0 = underlyingSignature(t)
	case *syntax.ListLit:
		// gin.H(["error": msg]): a literal converted to a collection type is
		// a literal of that type.
		if a.Type == nil {
			want0 = t
		}
	case *syntax.MapLit:
		if a.Type == nil {
			want0 = t
		}
	}
	x := c.value(args[0].Value, want0)
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	if types.IsNullable(x.typ) {
		c.nullableUse(x, "convert")
		return invalid(e)
	}
	if x.val != nil && types.IsUntyped(x.typ) {
		if types.BasicOf(t) != nil {
			if types.IsNumeric(x.typ) && types.IsNumeric(t) {
				if types.IsInteger(t) && !isIntegral(x.val) {
					c.errorf(diag.EConversion, args[0].Value.Span(), "constant %s truncated converting to %s", x.val, t).
						WithHint("write a whole number, or convert a variable at run time")
					return invalid(e)
				}
				if !c.representable(x.val, t) {
					c.errorf(diag.EOverflow, args[0].Value.Span(), "constant %s overflows %s", x.val, t)
					return invalid(e)
				}
				v := c.convertConst(x.val, t)
				c.assign(x, types.DefaultType(x.typ), "")
				return &operand{mode: ModeValue, typ: t, val: v}
			}
			if types.IsString(x.typ) && types.IsString(t) {
				c.assign(x, types.String, "")
				return &operand{mode: ModeValue, typ: t, val: x.val}
			}
		}
		c.defaultValueType(x, "conversion")
	}
	src := types.Base(x.typ)
	if types.IsInteger(src) && types.IsString(t) && !isRuneOrByte(src) {
		c.errorf(diag.EConversion, e.S, "converting %s to %s yields a rune, not a decimal string", x.typ, t).
			WithHint("use a template for the number as text: \"${%s}\"", args[0].Value.Span().Short()).
			WithHint("or string(rune(%s)) for a character", args[0].Value.Span().Short())
		return invalid(e)
	}
	if unboundTypeParam(types.Lower(src)) || unboundTypeParam(types.Lower(t)) {
		// A conversion inside a type parameter's own constraint
		// (func f[T: [T(1)]int]): the parameter cannot be used yet.
		c.errorf(diag.EInvalidUse, e.S, "%s cannot be converted here: a type parameter's constraint cannot use the parameters it declares", args[0].Value.Span().Short())
		return invalid(e)
	}
	if c.undefined(src) || c.undefined(t) {
		// [T(1)]int inside T's own declaration: go/types cannot convert to
		// a type whose definition is not set yet.
		c.errorf(diag.EInvalidUse, e.S, "%s cannot be converted here: the type is still being declared", args[0].Value.Span().Short())
		return invalid(e)
	}
	if !gotypes.ConvertibleTo(types.Lower(src), types.Lower(t)) {
		d := c.errorf(diag.EConversion, e.S, "cannot convert %s (type %s) to %s", args[0].Value.Span().Short(), x.typ, t)
		if types.IsString(src) && types.IsNumeric(t) {
			s := args[0].Value.Span().Short()
			switch b := types.BasicOf(t); {
			case b != nil && b.Kind == types.IntKind:
				d.WithHint("parse it: %s.toInt()?", s)
			case b != nil && b.Kind == types.Int64Kind:
				d.WithHint("parse it: %s.toInt64()?", s)
			case b != nil && b.Kind == types.Float64Kind:
				d.WithHint("parse it: %s.toFloat64()?", s)
			default:
				d.WithHint("parse it with strconv, then convert: %s(%s.toInt64()?)", t, s)
			}
		}
		return invalid(e)
	}
	if x.val != nil && types.BasicOf(t) != nil && !types.IsInterface(t) {
		// A constant converts to a constant, as in Go: string(rune(98)),
		// int64(N).
		switch {
		case types.IsNumeric(src) && types.IsNumeric(t):
			if c.representable(x.val, t) && (!types.IsInteger(t) || isIntegral(x.val)) {
				return &operand{mode: ModeValue, typ: t, val: c.convertConst(x.val, t)}
			}
		case types.IsInteger(src) && types.IsString(t):
			if n, ok := constant.Int64Val(x.val); ok {
				r := rune(n)
				if n < 0 || n > utf8.MaxRune {
					r = utf8.RuneError
				}
				return &operand{mode: ModeValue, typ: t, val: constant.MakeString(string(r))}
			}
		case types.IsString(src) && types.IsString(t):
			return &operand{mode: ModeValue, typ: t, val: x.val}
		}
	}
	return &operand{mode: ModeValue, typ: t}
}

// construct checks struct construction T(args).
func (c *Checker) construct(e *syntax.CallExpr, t types.Type, args []*syntax.Arg) *operand {
	if n, ok := types.Base(t).(*types.Named); ok && n.Decl != nil && len(n.Decl.TypeParams) > 0 && len(n.Args) == 0 {
		// Page(Items = users): the type arguments come from the fields.
		inst := c.inferConstruction(e, n, args)
		if inst == nil {
			c.preArgs = nil
			return invalid(e)
		}
		t = inst
	}
	defer func() { c.preArgs = nil }()
	info := &CallInfo{Kind: CallConstruct, Target: t}
	c.info.Calls[e] = info
	named := 0
	for _, a := range args {
		if a.Name != nil {
			named++
		}
		if a.Spread {
			c.errorf(diag.EInvalidUse, a.S, "... cannot be used when constructing a struct")
		}
	}
	if named > 0 && named < len(args) {
		c.errorf(diag.EConstruction, e.S, "cannot mix named and positional fields in a struct construction")
		c.checkArgsLoose(args)
		return invalid(e)
	}
	var fields []fieldInfo
	isLyth := false
	tname := t.String()
	switch b := types.Base(t).(type) {
	case *types.Named:
		if b.Decl != nil {
			isLyth = true
			bind := declBindings(b)
			for _, f := range b.Decl.Fields {
				ft := f.Type
				if bind != nil {
					ft = subst(ft, bind)
				}
				fields = append(fields, fieldInfo{f.Name, f.GoName, ft, f.Private && b.Decl.PkgPath != c.pkg.Path, types.Zeroable(ft)})
			}
		} else {
			st := b.Go.Underlying().(*gotypes.Struct)
			for i := 0; i < st.NumFields(); i++ {
				f := st.Field(i)
				fields = append(fields, fieldInfo{f.Name(), f.Name(), c.lift(f.Type()), !f.Exported(), true})
			}
		}
	case *types.GoType:
		st := b.T.Underlying().(*gotypes.Struct)
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			fields = append(fields, fieldInfo{f.Name(), f.Name(), c.lift(f.Type()), !f.Exported(), true})
		}
	}
	ok := true
	given := map[string]bool{}
	if named == 0 && len(args) > 0 {
		if !isLyth {
			c.errorf(diag.EConstruction, e.S, "Go structs must be constructed with named fields").
				WithHint("write %s(%s = ...)", tname, firstExported(fields))
			c.checkArgsLoose(args)
			return invalid(e)
		}
		if len(args) != len(fields) {
			c.errorf(diag.EConstruction, e.S, "positional construction of %s needs all %s (got %d)", tname, plural(len(fields), "field"), len(args)).
				WithHint("name the fields to omit some: %s(%s = ...)", tname, firstExported(fields))
			c.checkArgsLoose(args)
			return invalid(e)
		}
		for i, a := range args {
			f := fields[i]
			x := c.argValue(a.Value, f.typ)
			if !c.assign(x, f.typ, "field "+f.name+" of "+tname) {
				ok = false
			}
			info.Fields = append(info.Fields, f.goName)
			info.Args = append(info.Args, a.Value)
			given[f.name] = true
		}
	} else {
		for _, a := range args {
			var f *fieldInfo
			for i := range fields {
				if fields[i].name == a.Name.Name {
					f = &fields[i]
					break
				}
			}
			if f == nil {
				d := c.errorf(diag.EConstruction, a.Name.S, "%s has no field %s", tname, a.Name.Name)
				for _, ff := range fields {
					if strings.EqualFold(ff.name, a.Name.Name) {
						d.WithEdit(a.Name.S, ff.name, "did you mean %s?", ff.name)
					}
				}
				c.expr(a.Value, nil)
				ok = false
				continue
			}
			if f.private {
				c.errorf(diag.EPrivate, a.Name.S, "field %s of %s is not accessible here", f.name, tname)
				ok = false
			}
			if given[f.name] {
				c.errorf(diag.EConstruction, a.Name.S, "field %s is given twice", f.name)
				ok = false
			}
			given[f.name] = true
			x := c.argValue(a.Value, f.typ)
			if !c.assign(x, f.typ, "field "+f.name+" of "+tname) {
				ok = false
			}
			info.Fields = append(info.Fields, f.goName)
			info.Args = append(info.Args, a.Value)
		}
	}
	if isLyth {
		var missing []string
		for _, f := range fields {
			if !given[f.name] && !f.zeroable {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			c.errorf(diag.EConstruction, e.S, "missing %s %s in construction of %s", map[bool]string{true: "field", false: "fields"}[len(missing) == 1], strings.Join(missing, ", "), tname).
				WithNote("fields with non-nullable pointer, interface, or function types have no zero value and must be given")
			ok = false
		}
	}
	if !ok {
		return invalid(e)
	}
	return &operand{mode: ModeValue, typ: types.Base(t)}
}

// argValue checks an argument, or returns its operand when inference has
// checked it already.
func (c *Checker) argValue(e syntax.Expr, hint types.Type) *operand {
	if x, ok := c.preArgs[e]; ok {
		return x
	}
	return c.value(e, hint)
}

// inferConstruction infers the type arguments of a generic struct from the
// values given for its fields: Page(Items = users) is a Page<User>. The
// values it checks are kept in preArgs for construct.
func (c *Checker) inferConstruction(e *syntax.CallExpr, n *types.Named, args []*syntax.Arg) types.Type {
	d := n.Decl
	bind := bindings{}
	checked := map[syntax.Expr]*operand{} // published as preArgs at the end: values may construct too
	fieldFor := func(i int, a *syntax.Arg) *types.Field {
		if a.Name != nil {
			return d.LookupField(a.Name.Name)
		}
		if i < len(d.Fields) {
			return d.Fields[i]
		}
		return nil
	}
	for i, a := range args {
		f := fieldFor(i, a)
		if f == nil {
			continue
		}
		var hint types.Type
		if !hasTypeParams(f.Type) {
			hint = f.Type
		}
		x := c.value(a.Value, hint)
		checked[a.Value] = x
		if x.mode == ModeInvalid || x.typ == types.Null {
			continue
		}
		if tp, isParam := f.Type.(*types.TypeParam); isParam && types.IsUntyped(x.typ) {
			if _, bound := bind[tp.Go]; !bound {
				bind[tp.Go] = types.DefaultType(x.typ)
			}
			continue
		}
		unify(f.Type, x.typ, bind)
	}
	targs := make([]types.Type, len(d.TypeParams))
	var missing []string
	for i, tp := range d.TypeParams {
		t, ok := bind[tp.Go]
		if !ok {
			missing = append(missing, tp.Name)
			continue
		}
		targs[i] = t
	}
	if len(missing) > 0 {
		c.errorf(diag.ECannotInfer, e.Fun.Span(), "cannot infer type argument %s of %s from its fields", strings.Join(missing, ", "), d.Name).
			WithHint("give the type arguments: %s[%s](...)", d.Name, typeParamNames(d.TypeParams))
		return nil
	}
	inst := &types.Named{Decl: d, Args: targs}
	if !c.validInstance(inst, e.Fun.Span()) {
		return nil
	}
	c.preArgs = checked
	return inst
}

type fieldInfo struct {
	name, goName string
	typ          types.Type
	private      bool
	zeroable     bool
}

func firstExported(fields []fieldInfo) string {
	for _, f := range fields {
		if !f.private {
			return f.name
		}
	}
	return "field"
}

// ---------------------------------------------------------------------------
// Builtins

func (c *Checker) builtinCall(e *syntax.CallExpr, k BuiltinKind, args []*syntax.Arg, want types.Type) *operand {
	info := &CallInfo{Kind: CallBuiltin, Builtin: k}
	for _, a := range args {
		info.Args = append(info.Args, a.Value)
	}
	c.info.Calls[e] = info
	name := e.Fun.Span().Short()
	nargs := func(min, max int) bool {
		if len(args) < min || max >= 0 && len(args) > max {
			want := plural(min, "argument")
			if max < 0 {
				want = "at least " + want
			} else if max != min {
				want = fmt.Sprintf("%d to %d arguments", min, max)
			}
			c.errorf(diag.EArgCount, e.S, "%s expects %s, but %s given", name, want, plural(len(args), "argument"))
			c.checkArgsLoose(args)
			return false
		}
		for _, a := range args {
			if a.Name != nil {
				c.errorf(diag.EUnsupported, a.Name.S, "builtins do not take named arguments")
				return false
			}
		}
		return true
	}
	switch k {
	case BuiltinPrint, BuiltinPrintln:
		if len(args) == 1 && !args[0].Spread {
			if _, isCall := syntax.Unparen(args[0].Value).(*syntax.CallExpr); isCall {
				// println(pair()) prints both values, as fmt.Println does.
				x := c.expr(args[0].Value, nil)
				if t, ok := x.typ.(*types.Tuple); ok && x.mode == ModeValue {
					for _, el := range t.Elems {
						if types.IsNullable(el) {
							c.errorf(diag.EMultiValue, args[0].Value.Span(), "%s cannot print the values of %s as one argument: %s would print as a pointer", name, args[0].Value.Span().Short(), el).
								WithHint("destructure them first: a, b := %s", args[0].Value.Span().Short())
							break
						}
					}
					return &operand{mode: ModeNoValue, typ: types.Unit}
				}
				if x = c.singleValue(x); x.mode != ModeInvalid {
					c.defaultValueType(x, name+" argument")
				}
				return &operand{mode: ModeNoValue, typ: types.Unit}
			}
		}
		for _, a := range args {
			x := c.value(a.Value, nil)
			if x.mode != ModeInvalid {
				c.defaultValueType(x, name+" argument")
			}
		}
		return &operand{mode: ModeNoValue, typ: types.Unit}
	case BuiltinLen, BuiltinCap:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, nil)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		if types.IsNullable(x.typ) {
			c.nullableUse(x, "take the length of")
			return invalid(e)
		}
		switch t := collectionOf(x.typ).(type) {
		case *types.Slice, *types.Chan:
			return &operand{mode: ModeValue, typ: types.Int}
		case *types.Map:
			if k == BuiltinCap {
				break
			}
			return &operand{mode: ModeValue, typ: types.Int}
		case *types.Array:
			return &operand{mode: ModeValue, typ: types.Int, val: constant.MakeInt64(t.Len)}
		case *types.Pointer:
			if a, ok := collectionOf(t.Elem).(*types.Array); ok {
				return &operand{mode: ModeValue, typ: types.Int, val: constant.MakeInt64(a.Len)}
			}
		default:
			if types.IsString(t) && k == BuiltinLen {
				if x.val != nil {
					c.assign(x, types.String, "")
					return &operand{mode: ModeValue, typ: types.Int, val: constant.MakeInt64(int64(len(constant.StringVal(x.val))))}
				}
				return &operand{mode: ModeValue, typ: types.Int}
			}
		}
		c.errorf(diag.EInvalidOp, args[0].Value.Span(), "invalid argument for %s: %s", name, x.typ)
		return invalid(e)
	case BuiltinAppend:
		if !nargs(1, -1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, want)
		if x.mode == ModeInvalid {
			c.checkArgsLoose(args[1:])
			return invalid(e)
		}
		s, ok := collectionOf(x.typ).(*types.Slice)
		if !ok {
			c.errorf(diag.EInvalidOp, args[0].Value.Span(), "first argument of append must be a slice, not %s", x.typ)
			c.checkArgsLoose(args[1:])
			return invalid(e)
		}
		sig := &types.Signature{Params: []types.Type{s, s}, Variadic: true, Results: []types.Type{s}}
		rest := args[1:]
		for i, a := range rest {
			if a.Spread {
				if i != len(rest)-1 {
					c.errorf(diag.EInvalidUse, a.S, "... must be on the last argument")
				}
				y := c.value(a.Value, s)
				if y.mode != ModeInvalid && types.IsString(types.Base(y.typ)) && types.Identical(types.Base(s.Elem), types.Byte) {
					continue // append(bytes, str...)
				}
				c.assign(y, s, "argument of append")
				continue
			}
			y := c.value(a.Value, s.Elem)
			c.assign(y, s.Elem, "element of append")
		}
		info.Sig = sig
		return &operand{mode: ModeValue, typ: types.Base(x.typ)}
	case BuiltinCopy:
		if !nargs(2, 2) {
			return invalid(e)
		}
		dst := c.value(args[0].Value, nil)
		src := c.value(args[1].Value, nil)
		if dst.mode == ModeInvalid || src.mode == ModeInvalid {
			return &operand{mode: ModeValue, typ: types.Int}
		}
		ds, ok := collectionOf(dst.typ).(*types.Slice)
		if !ok {
			c.errorf(diag.EInvalidOp, args[0].Value.Span(), "copy needs a slice to copy into, not %s", dst.typ)
			return &operand{mode: ModeValue, typ: types.Int}
		}
		if types.IsString(src.typ) && types.Identical(types.Base(ds.Elem), types.Byte) {
			c.convertUntyped(src, types.String, nil)
			return &operand{mode: ModeValue, typ: types.Int}
		}
		ss, ok := collectionOf(src.typ).(*types.Slice)
		if !ok {
			c.errorf(diag.EInvalidOp, args[1].Value.Span(), "copy needs a slice to copy from, not %s", src.typ)
			return &operand{mode: ModeValue, typ: types.Int}
		}
		if okA, conv, _ := c.assignable(ss.Elem, ds.Elem); !okA || conv != ConvNone {
			d := c.errorf(diag.ETypeMismatch, args[1].Value.Span(), "cannot copy %s into %s: the elements must have the same type", src.typ, dst.typ)
			if types.MayBeNull(ss.Elem) && !types.MayBeNull(ds.Elem) && types.Identical(types.Base(ss.Elem), types.Base(ds.Elem)) {
				d.WithNote("an element of %s may be null, and %s cannot hold null", src.typ, dst.typ)
			}
		}
		return &operand{mode: ModeValue, typ: types.Int}
	case BuiltinDelete:
		if !nargs(2, 2) {
			return invalid(e)
		}
		m := c.value(args[0].Value, nil)
		if mt, ok := collectionOf(m.typ).(*types.Map); ok {
			c.assign(c.value(args[1].Value, mt.Key), mt.Key, "key of delete")
		} else {
			if m.mode != ModeInvalid {
				c.errorf(diag.EInvalidOp, args[0].Value.Span(), "first argument of delete must be a map, not %s", m.typ)
			}
			c.value(args[1].Value, nil)
		}
		return &operand{mode: ModeNoValue, typ: types.Unit}
	case BuiltinMake:
		if !nargs(1, 3) {
			return invalid(e)
		}
		t := c.expr(args[0].Value, nil)
		if t.mode != ModeType {
			if t.mode != ModeInvalid {
				c.errorf(diag.EInvalidUse, args[0].Value.Span(), "first argument of make must be a type such as []int")
			}
			c.checkArgsLoose(args[1:])
			return invalid(e)
		}
		switch tt := collectionOf(t.typ).(type) {
		case *types.Slice:
			if len(args) < 2 {
				c.errorf(diag.EArgCount, e.S, "make(%s) needs a length", tt)
				return invalid(e)
			}
			n := c.value(args[1].Value, types.Int)
			if !types.Zeroable(tt.Elem) && !(n.val != nil && constSign(n.val) == 0) {
				written := args[0].Value.Span().Text() // PriorityQueue, not []*Item
				c.errorf(diag.ENoZeroValue, e.S, "cannot make a non-empty %s: %s has no zero value", written, tt.Elem).
					WithHint("use make(%s, 0, n) and append, or a nullable element type", written)
			}
			c.intArgs(args[1:])
			if len(args) == 3 {
				l, k := c.info.Types[args[1].Value].Value, c.info.Types[args[2].Value].Value
				if l != nil && k != nil && constant.Compare(l, token.GTR, k) {
					c.errorf(diag.EInvalidOp, args[1].Value.Span(), "the length %s is larger than the capacity %s", l, k)
				}
			}
		case *types.Map, *types.Chan:
			if len(args) > 2 {
				c.errorf(diag.EArgCount, e.S, "make(%s) takes at most one size argument", tt)
			}
			c.intArgs(args[1:])
		default:
			c.errorf(diag.EInvalidUse, args[0].Value.Span(), "cannot make %s; make creates slices, maps, and channels", t.typ)
			return invalid(e)
		}
		info.Sig = &types.Signature{Results: []types.Type{t.typ}}
		return &operand{mode: ModeValue, typ: t.typ}
	case BuiltinNew:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.expr(args[0].Value, nil)
		switch x.mode {
		case ModeType:
			if !types.Zeroable(x.typ) {
				c.errorf(diag.ENoZeroValue, args[0].Value.Span(), "new(%s) would contain a zero value, but %s has none", x.typ, x.typ)
			}
			return &operand{mode: ModeValue, typ: &types.Pointer{Elem: x.typ}}
		case ModeInvalid:
			return invalid(e)
		}
		x = c.singleValue(x)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		if x.typ == types.Null {
			c.errorf(diag.EInvalidUse, args[0].Value.Span(), "new(null) is not allowed")
			return invalid(e)
		}
		t := c.defaultValueType(x, "new")
		return &operand{mode: ModeValue, typ: &types.Pointer{Elem: t}}
	case BuiltinPanic:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, types.Any)
		if x.mode != ModeInvalid {
			c.assign(x, &types.Nullable{Elem: types.Any}, "argument of panic")
		}
		return &operand{mode: ModeValue, typ: types.Nothing}
	case BuiltinRecover:
		if !nargs(0, 0) {
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: &types.Nullable{Elem: types.Any}}
	case BuiltinMin, BuiltinMax:
		if !nargs(1, -1) {
			return invalid(e)
		}
		ops := make([]*operand, len(args))
		for i, a := range args {
			ops[i] = c.value(a.Value, want)
		}
		t := c.commonType(ops, name+" argument")
		if t == nil {
			return invalid(e)
		}
		ordered := types.TermsAll(t, func(term types.Type) bool {
			return types.IsNumeric(term) && !types.IsComplex(term) || types.IsString(term)
		})
		if !ordered {
			// A type parameter is ordered when every type its constraint
			// allows is (T: cmp.Ordered).
			c.errorf(diag.EInvalidOp, e.S, "%s requires ordered arguments, not %s", name, t)
			return invalid(e)
		}
		return &operand{mode: ModeValue, typ: t}
	case BuiltinComplex:
		// complex(re, im), as in Go: float64 parts make a complex128.
		if !nargs(2, 2) {
			return invalid(e)
		}
		ops := []*operand{c.value(args[0].Value, nil), c.value(args[1].Value, nil)}
		if ops[0].mode == ModeInvalid || ops[1].mode == ModeInvalid {
			return invalid(e)
		}
		if ops[0].val != nil && ops[1].val != nil && types.IsUntyped(ops[0].typ) && types.IsUntyped(ops[1].typ) {
			if !types.IsNumeric(ops[0].typ) || !types.IsNumeric(ops[1].typ) || types.IsComplex(ops[0].typ) || types.IsComplex(ops[1].typ) {
				c.errorf(diag.EInvalidOp, e.S, "complex takes two real numbers")
				return invalid(e)
			}
			v := constant.BinaryOp(constant.ToFloat(ops[0].val), token.ADD, constant.MakeImag(constant.ToFloat(ops[1].val)))
			return &operand{mode: ModeValue, typ: types.UntypedComplex, val: v}
		}
		t := c.commonType(ops, "complex argument")
		if t == nil {
			return invalid(e)
		}
		var res types.Type
		switch b := types.BasicOf(t); {
		case b != nil && b.Kind == types.Float32Kind:
			res = types.Complex64
		case b != nil && (b.Kind == types.Float64Kind || b.Kind == types.UntypedFloatKind || b.Kind == types.UntypedIntKind || b.Kind == types.UntypedRuneKind):
			res = types.Complex128
		default:
			c.errorf(diag.EInvalidOp, e.S, "complex takes two float32 or float64 parts, not %s", t)
			return invalid(e)
		}
		for i, x := range ops {
			c.assign(x, t, "complex argument")
			_ = i
		}
		return &operand{mode: ModeValue, typ: res}
	case BuiltinReal, BuiltinImag:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, nil)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		if x.val != nil && types.IsUntyped(x.typ) && types.IsNumeric(x.typ) {
			v := constant.ToComplex(x.val)
			if k == BuiltinReal {
				return &operand{mode: ModeValue, typ: types.UntypedFloat, val: constant.Real(v)}
			}
			return &operand{mode: ModeValue, typ: types.UntypedFloat, val: constant.Imag(v)}
		}
		switch b := types.BasicOf(x.typ); {
		case b != nil && b.Kind == types.Complex64Kind:
			return &operand{mode: ModeValue, typ: types.Float32}
		case b != nil && b.Kind == types.Complex128Kind:
			return &operand{mode: ModeValue, typ: types.Float64}
		}
		c.errorf(diag.EInvalidOp, e.S, "%s takes a complex number, not %s", name, x.typ)
		return invalid(e)
	case BuiltinClear:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, nil)
		if s, ok := collectionOf(x.typ).(*types.Slice); ok && !types.Zeroable(s.Elem) {
			d := c.errorf(diag.ENoZeroValue, args[0].Value.Span(), "clear would set every element of %s to its zero value, and %s", x.typ, types.NoZeroReason(s.Elem))
			switch a := syntax.Unparen(args[0].Value).(type) {
			case *syntax.Ident:
				d.WithHint("shorten the slice instead: %s = %s[:0]", a.Name, a.Name)
			case *syntax.SliceExpr:
				if id, ok := syntax.Unparen(a.X).(*syntax.Ident); ok && a.Lo != nil && a.Hi == nil {
					d.WithHint("keep %s[:%s] instead, or declare the elements nullable (%s?) to clear them", id.Name, a.Lo.Span().Text(), s.Elem)
					break
				}
				d.WithHint("declare the elements nullable (%s?) so that they can be cleared", s.Elem)
			default:
				d.WithHint("declare the elements nullable (%s?) so that they can be cleared", s.Elem)
			}
		}
		return &operand{mode: ModeNoValue, typ: types.Unit}
	case BuiltinClose:
		if !nargs(1, 1) {
			return invalid(e)
		}
		x := c.value(args[0].Value, nil)
		if _, ok := collectionOf(x.typ).(*types.Chan); !ok && x.mode != ModeInvalid {
			c.errorf(diag.EInvalidOp, args[0].Value.Span(), "close requires a channel, not %s", x.typ)
		}
		return &operand{mode: ModeNoValue, typ: types.Unit}
	}
	return invalid(e)
}

func (c *Checker) intArgs(args []*syntax.Arg) {
	for _, a := range args {
		x := c.value(a.Value, types.Int)
		if x.mode == ModeInvalid {
			continue
		}
		if x.val != nil && constSign(x.val) < 0 {
			c.errorf(diag.EInvalidOp, a.Value.Span(), "size must not be negative")
			continue
		}
		if types.IsUntyped(x.typ) {
			c.assign(x, types.Int, "size argument")
		} else if !types.IsInteger(types.Base(x.typ)) {
			c.errorf(diag.ETypeMismatch, a.Value.Span(), "size must be an integer, not %s", x.typ)
		}
	}
}

// underlyingSignature returns the function signature underlying t, or nil.
func underlyingSignature(t types.Type) types.Type {
	if u := definedUnder(t); u != nil {
		if sig, ok := types.Base(u).(*types.Signature); ok {
			return sig
		}
		return nil
	}
	switch b := types.Base(t).(type) {
	case *types.Signature:
		return b
	case *types.Named:
		if b.Go != nil {
			if origin := b.Go.Origin(); len(b.Args) > 0 && origin.TypeParams().Len() == len(b.Args) {
				// iter.Seq[Item]: the generic signature with the Lyth type
				// arguments, which keep their nullability (Item's Loc: string?).
				if gs, ok := origin.Underlying().(*gotypes.Signature); ok {
					bind := bindings{}
					for i := range b.Args {
						bind[origin.TypeParams().At(i)] = b.Args[i]
					}
					return subst(types.LiftSignature(gs), bind)
				}
			}
			if gs, ok := b.Go.Underlying().(*gotypes.Signature); ok {
				return types.LiftSignature(gs)
			}
		}
	}
	return nil
}

// isRuneOrByte reports whether t is rune or byte, whose conversion to string
// gives a character (go vet accepts string(r) for them).
func isRuneOrByte(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && (b.Name == "rune" || b.Name == "byte" || b.Kind == types.UntypedRuneKind)
}

// forwardedArgs checks f(g()) where g returns several values, which Go
// passes as f's arguments: template.Must(t.Parse(s)), sum(pair()) for a
// variadic sum. forwarded is false when g returns one value, which the
// ordinary argument check handles; a variadic f does not receive an error
// this way, which would hide one Lyth must see handled.
func (c *Checker) forwardedArgs(e *syntax.CallExpr, sig *types.Signature, a *syntax.Arg) (forwarded, ok bool) {
	x, pre := c.preArgs[a.Value]
	if !pre {
		x = c.expr(a.Value, nil)
	}
	c.preArgs = map[syntax.Expr]*operand{a.Value: x}
	if x.mode == ModeInvalid {
		return true, false
	}
	single := func() (bool, bool) {
		// Not forwarded: the argument is one value, as c.value checks it.
		c.preArgs = map[syntax.Expr]*operand{a.Value: c.singleValue(x)}
		return false, false
	}
	var raw []types.Type
	switch t := x.typ.(type) {
	case *types.Result:
		if sig.Variadic {
			return single()
		}
		raw = t.Raw
	case *types.Tuple:
		raw = t.Elems
	}
	if len(raw) < 2 {
		return single()
	}
	c.preArgs = nil
	np := len(sig.Params)
	params := sig.Params
	if sig.Variadic {
		if len(raw) < np-1 {
			c.argCountError(e, sig, len(raw))
			return true, false
		}
		params = append([]types.Type{}, sig.Params[:np-1]...)
		var elem types.Type = types.Invalid
		if sl, ok := sig.Params[np-1].(*types.Slice); ok {
			elem = sl.Elem
		}
		for len(params) < len(raw) {
			params = append(params, elem)
		}
	} else if len(raw) != np {
		c.argCountError(e, sig, 1)
		return true, false
	}
	for i, t := range raw {
		v := &operand{mode: ModeValue, typ: t, expr: a.Value}
		if !c.assignableQuiet(v, params[i]) {
			c.errorf(diag.ETypeMismatch, a.Value.Span(), "%s returns %s, which cannot be passed as the arguments of %s", a.Value.Span().Short(), (&types.Tuple{Elems: raw}).String(), e.Fun.Span().Short()).
				WithNote("result %d has type %s; parameter %d needs %s", i+1, t, i+1, params[i])
			return true, false
		}
	}
	return true, true
}
