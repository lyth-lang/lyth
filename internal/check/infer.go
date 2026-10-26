package check

import (
	gotypes "go/types"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

type bindings map[*gotypes.TypeParam]types.Type

// inferCall infers the type arguments of a call to a generic Go function by
// unifying parameter types with argument types (lambdas last), then with
// the constraints' core types. Constraint satisfaction is validated by
// go/types. It returns the instantiated signature and the checked arguments.
func (c *Checker) inferCall(e *syntax.CallExpr, sig *types.Signature, args []*syntax.Arg, want types.Type, pre bindings) (*types.Signature, []types.Type) {
	bind := bindings{}
	for tp, t := range pre {
		bind[tp] = t
	}
	np := len(sig.Params)
	paramFor := func(i int, a *syntax.Arg) types.Type {
		if sig.Variadic && i >= np-1 && !a.Spread {
			if s, ok := sig.Params[np-1].(*types.Slice); ok {
				return s.Elem
			}
		}
		if i < np {
			return sig.Params[i]
		}
		return nil
	}
	ops := make([]*operand, len(args))
	isLambda := func(a *syntax.Arg) bool { _, ok := syntax.Unparen(a.Value).(*syntax.LambdaExpr); return ok }
	var genericArgs []int // generic function values (cmp.Compare), instantiated last
	for i, a := range args {
		if isLambda(a) {
			continue
		}
		pt := paramFor(i, a)
		var hint types.Type
		if pt != nil && !hasTypeParams(pt) {
			hint = pt
		}
		x := c.value(a.Value, hint)
		ops[i] = x
		if x.mode == ModeInvalid || pt == nil || types.IsUntyped(x.typ) || x.typ == types.Null {
			continue
		}
		if isGenericFunc(x.typ) {
			genericArgs = append(genericArgs, i)
			continue
		}
		unify(pt, x.typ, bind)
	}
	// Untyped constants bind their default type to bare type parameters,
	// the largest kind among them as in Go: pair(1, 2.5) is pair[float64].
	untyped := map[*gotypes.TypeParam]types.Type{}
	var order []*gotypes.TypeParam
	for i, a := range args {
		x := ops[i]
		if x == nil || x.mode == ModeInvalid || !types.IsUntyped(x.typ) {
			continue
		}
		if tp, ok := paramFor(i, a).(*types.TypeParam); ok {
			if _, bound := bind[tp.Go]; bound {
				continue
			}
			cur, seen := untyped[tp.Go]
			if !seen {
				order = append(order, tp.Go)
			}
			if !seen || constRank(x.typ) > constRank(cur) {
				untyped[tp.Go] = x.typ
			}
		}
	}
	for _, tp := range order {
		bind[tp] = types.DefaultType(untyped[tp])
	}
	c.inferCore(sig, bind)
	if want != nil && len(sig.Results) == 1 && !types.IsInterface(want) {
		// An interface expected type (fmt.Println's any) says nothing about
		// the result type: sumBy(xs) { len(it) } still gives int.
		unify(sig.Results[0], want, bind)
		c.inferCore(sig, bind)
	}
	for i, a := range args {
		if !isLambda(a) {
			continue
		}
		pt := paramFor(i, a)
		var expected types.Type
		if pt != nil {
			expected = subst(pt, bind)
		}
		if expected != nil && c.foreignTypeParams(signatureParamsOnly(expected)) {
			c.errorf(diag.ECannotInfer, a.Value.Span(), "cannot infer the parameter types of this lambda").
				WithHint("annotate the lambda parameters")
			expected = nil
		}
		x := c.value(a.Value, expected)
		ops[i] = x
		if x.mode != ModeInvalid && pt != nil {
			unify(pt, x.typ, bind)
		}
	}
	c.inferCore(sig, bind)
	for _, x := range ops {
		if x != nil && x.mode == ModeInvalid {
			// An argument failed to check (undefined: maps): that error is
			// the one to read, not a type argument it could not give.
			return nil, nil
		}
	}
	for _, i := range genericArgs {
		// slices.SortFunc(xs, cmp.Compare): the parameter's type, known from
		// the other arguments, instantiates the generic function, as in Go.
		if pt := paramFor(i, args[i]); pt != nil {
			if want := subst(pt, bind); !hasTypeParams(want) {
				c.instantiateFuncValue(ops[i], want, false)
			}
		}
	}
	var missing []string
	for _, tp := range sig.TypeParams {
		if _, ok := bind[tp.Go]; !ok {
			missing = append(missing, tp.Name)
		}
	}
	if len(missing) > 0 {
		c.errorf(diag.ECannotInfer, e.Fun.Span(), "cannot infer type argument %s of %s", strings.Join(missing, ", "), e.Fun.Span().Short()).
			WithNote("signature: %s", sig).
			WithHint("give the type arguments: %s[...](...)", e.Fun.Span().Short())
		return nil, nil
	}
	targs := make([]gotypes.Type, len(sig.TypeParams))
	for i, tp := range sig.TypeParams {
		targs[i] = types.Lower(bind[tp.Go])
	}
	inst, err := gotypes.Instantiate(nil, sig.GoSig, targs, true)
	if err != nil {
		var names []string
		for _, tp := range sig.TypeParams {
			names = append(names, tp.Name+" = "+bind[tp.Go].String())
		}
		c.errorf(diag.ETypeArgs, e.Fun.Span(), "invalid type arguments for %s (%s)", e.Fun.Span().Short(), strings.Join(names, ", ")).
			WithNote("%s", c.lythNames(translateGoMessage(err.Error())))
		return nil, nil
	}
	c.comparableArgs(e.Fun.Span(), sig.TypeParams, bind)
	res := substSig(sig, bind)
	res.GoSig = inst.(*gotypes.Signature)
	// Remember the checked operands so checkArgs does not re-check them.
	c.preArgs = map[syntax.Expr]*operand{}
	for i, a := range args {
		if ops[i] != nil {
			c.preArgs[a.Value] = ops[i]
		}
	}
	targsLyth := make([]types.Type, len(sig.TypeParams))
	for i, tp := range sig.TypeParams {
		targsLyth[i] = bind[tp.Go]
	}
	return res, targsLyth
}

func signatureParamsOnly(t types.Type) types.Type {
	if s, ok := types.Base(t).(*types.Signature); ok {
		return &types.Tuple{Elems: s.Params}
	}
	return t
}

// inferCore unifies bound type parameters with their constraint's core
// type, e.g. S ~[]E with S = []User gives E = User.
func (c *Checker) inferCore(sig *types.Signature, bind bindings) {
	for changed := true; changed; {
		changed = false
		for _, tp := range sig.TypeParams {
			core := coreTerm(tp.Go)
			if core == nil {
				continue
			}
			lc := c.lift(core)
			if b, ok := bind[tp.Go]; ok {
				before := len(bind)
				unify(lc, underlyingOf(b), bind)
				if len(bind) != before {
					changed = true
				}
			} else if !hasTypeParams(subst(lc, bind)) {
				bind[tp.Go] = types.Base(subst(lc, bind))
				changed = true
			}
		}
	}
}

func coreTerm(tp *gotypes.TypeParam) gotypes.Type {
	iface, ok := tp.Constraint().Underlying().(*gotypes.Interface)
	if !ok {
		return nil
	}
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		if u, ok := iface.EmbeddedType(i).(*gotypes.Union); ok && u.Len() == 1 {
			t := u.Term(0).Type()
			switch t.Underlying().(type) {
			case *gotypes.Slice, *gotypes.Map, *gotypes.Pointer, *gotypes.Chan, *gotypes.Signature, *gotypes.Array:
				return t
			}
		}
	}
	return nil
}

// collectionOf returns t without nullability and, for a Go named type whose
// underlying type is a collection, channel, or basic type, that underlying
// type: http.Header indexes, ranges, and takes len like the map it is.
func collectionOf(t types.Type) types.Type {
	b := types.Base(t)
	if u := definedUnder(b); u != nil {
		switch ub := types.Base(u).(type) {
		case *types.Slice, *types.Map, *types.Array, *types.Chan, *types.Basic:
			return ub
		}
	}
	if n, ok := b.(*types.Named); ok && n.Go != nil {
		switch n.Go.Underlying().(type) {
		case *gotypes.Slice, *gotypes.Map, *gotypes.Array, *gotypes.Chan, *gotypes.Basic:
			return types.Base(types.Lift(n.Go.Underlying()))
		}
	}
	return b
}

// CollectionOf is collectionOf for the lowering.
func CollectionOf(t types.Type) types.Type { return collectionOf(t) }

func underlyingOf(t types.Type) types.Type {
	if u := definedUnder(t); u != nil {
		return u
	}
	if n, ok := types.Base(t).(*types.Named); ok && n.Go != nil {
		return types.Lift(n.Go.Underlying())
	}
	return t
}

// definedUnder returns the underlying type of a Lyth defined type (ADR
// 0037), with an instance's type arguments substituted, or nil for any
// other type.
func definedUnder(t types.Type) types.Type {
	n, ok := types.Base(t).(*types.Named)
	if !ok || n.Decl == nil || n.Decl.Kind != types.DefinedDecl {
		return nil
	}
	u := types.DefinedUnder(n)
	if u == nil || u == types.Invalid {
		return nil
	}
	if bind := declBindings(n); bind != nil {
		u = subst(u, bind)
	}
	return u
}

// declBindings binds the type parameters of a generic Lyth type to the type
// arguments of instance n, or returns nil for a non-generic type.
func declBindings(n *types.Named) bindings {
	if n.Decl == nil || len(n.Args) != len(n.Decl.TypeParams) || len(n.Args) == 0 {
		return nil
	}
	bind := bindings{}
	for i, tp := range n.Decl.TypeParams {
		bind[tp.Go] = n.Args[i]
	}
	return bind
}

// typeParamTypes returns type parameters as types: the arguments with which
// a generic declaration refers to itself.
func typeParamTypes(tps []*types.TypeParam) []types.Type {
	out := make([]types.Type, len(tps))
	for i, tp := range tps {
		out[i] = tp
	}
	return out
}

func typeParamNames(tps []*types.TypeParam) string {
	names := make([]string, len(tps))
	for i, tp := range tps {
		names[i] = tp.Name
	}
	return strings.Join(names, ", ")
}

// goInfers reports whether Go infers all type arguments of a call of a
// function with signature gs from its arguments: each type parameter occurs
// in a parameter type, or in the constraint of one that does (core types:
// E in func Max[S ~[]E, E cmp.Ordered](x S)). A type parameter occurring
// only in the results needs explicit type arguments.
func goInfers(gs *gotypes.Signature) bool {
	known := map[*gotypes.TypeParam]bool{}
	for i := 0; i < gs.Params().Len(); i++ {
		goTypeParams(gs.Params().At(i).Type(), known)
	}
	tps := gs.TypeParams()
	for changed := true; changed; {
		changed = false
		for i := 0; i < tps.Len(); i++ {
			if tp := tps.At(i); known[tp] {
				n := len(known)
				goTypeParams(tp.Constraint(), known)
				changed = changed || len(known) > n
			}
		}
	}
	for i := 0; i < tps.Len(); i++ {
		if !known[tps.At(i)] {
			return false
		}
	}
	return true
}

// goTypeParams adds the type parameters occurring in t to out.
func goTypeParams(t gotypes.Type, out map[*gotypes.TypeParam]bool) {
	switch t := t.(type) {
	case *gotypes.TypeParam:
		out[t] = true
	case *gotypes.Pointer:
		goTypeParams(t.Elem(), out)
	case *gotypes.Slice:
		goTypeParams(t.Elem(), out)
	case *gotypes.Array:
		goTypeParams(t.Elem(), out)
	case *gotypes.Map:
		goTypeParams(t.Key(), out)
		goTypeParams(t.Elem(), out)
	case *gotypes.Chan:
		goTypeParams(t.Elem(), out)
	case *gotypes.Signature:
		for i := 0; i < t.Params().Len(); i++ {
			goTypeParams(t.Params().At(i).Type(), out)
		}
		for i := 0; i < t.Results().Len(); i++ {
			goTypeParams(t.Results().At(i).Type(), out)
		}
	case *gotypes.Named:
		if args := t.TypeArgs(); args != nil {
			for i := 0; i < args.Len(); i++ {
				goTypeParams(args.At(i), out)
			}
		}
	case *gotypes.Interface:
		for i := 0; i < t.NumEmbeddeds(); i++ {
			goTypeParams(t.EmbeddedType(i), out)
		}
	case *gotypes.Union:
		for i := 0; i < t.Len(); i++ {
			goTypeParams(t.Term(i).Type(), out)
		}
	}
}

// foreignTypeParams reports whether t mentions a type parameter that is not
// in scope: one of a generic callee still to infer. The type parameters of
// the enclosing generic declaration are known types inside it.
func (c *Checker) foreignTypeParams(t types.Type) bool {
	found := false
	var visit func(t types.Type)
	visit = func(t types.Type) {
		switch t := t.(type) {
		case *types.TypeParam:
			if ts, ok := c.lookup(t.Name).(*TypeSym); !ok || ts.Type != types.Type(t) {
				found = true
			}
		case *types.Nullable:
			visit(t.Elem)
		case *types.Platform:
			visit(t.Elem)
		case *types.Pointer:
			visit(t.Elem)
		case *types.Slice:
			visit(t.Elem)
		case *types.Array:
			visit(t.Elem)
		case *types.Map:
			visit(t.Key)
			visit(t.Elem)
		case *types.Chan:
			visit(t.Elem)
		case *types.Signature:
			for _, p := range t.Params {
				visit(p)
			}
			for _, r := range t.Results {
				visit(r)
			}
		case *types.Tuple:
			for _, e := range t.Elems {
				visit(e)
			}
		case *types.Named:
			for _, a := range t.Args {
				visit(a)
			}
		}
	}
	visit(t)
	return found
}

func hasTypeParams(t types.Type) bool {
	switch t := t.(type) {
	case *types.TypeParam:
		return true
	case *types.Nullable:
		return hasTypeParams(t.Elem)
	case *types.Platform:
		return hasTypeParams(t.Elem)
	case *types.Pointer:
		return hasTypeParams(t.Elem)
	case *types.Slice:
		return hasTypeParams(t.Elem)
	case *types.Array:
		return hasTypeParams(t.Elem)
	case *types.Map:
		return hasTypeParams(t.Key) || hasTypeParams(t.Elem)
	case *types.Chan:
		return hasTypeParams(t.Elem)
	case *types.Signature:
		for _, p := range t.Params {
			if hasTypeParams(p) {
				return true
			}
		}
		for _, r := range t.Results {
			if hasTypeParams(r) {
				return true
			}
		}
	case *types.Tuple:
		for _, e := range t.Elems {
			if hasTypeParams(e) {
				return true
			}
		}
	case *types.Named:
		for _, a := range t.Args {
			if hasTypeParams(a) {
				return true
			}
		}
	}
	return false
}

// unify binds type parameters in p by matching it against a.
func unify(p, a types.Type, bind bindings) {
	switch pt := p.(type) {
	case *types.TypeParam:
		if _, ok := bind[pt.Go]; !ok && a != types.Invalid && a != types.Null && a != types.Nothing {
			bind[pt.Go] = a
		}
		return
	case *types.Platform:
		unify(pt.Elem, types.Base(a), bind)
		return
	case *types.Nullable:
		unify(pt.Elem, types.Base(a), bind)
		return
	}
	ab := types.Base(a)
	switch pt := p.(type) {
	case *types.Pointer:
		if x, ok := ab.(*types.Pointer); ok {
			unify(pt.Elem, x.Elem, bind)
		}
	case *types.Slice:
		if x, ok := underlyingOf(ab).(*types.Slice); ok {
			unify(pt.Elem, x.Elem, bind)
		}
	case *types.Array:
		if x, ok := ab.(*types.Array); ok {
			unify(pt.Elem, x.Elem, bind)
		}
	case *types.Map:
		if x, ok := underlyingOf(ab).(*types.Map); ok {
			unify(pt.Key, x.Key, bind)
			unify(pt.Elem, x.Elem, bind)
		}
	case *types.Chan:
		if x, ok := ab.(*types.Chan); ok {
			unify(pt.Elem, x.Elem, bind)
		}
	case *types.Signature:
		x := signatureOf(ab)
		if x == nil {
			return
		}
		if len(x.Params) == len(pt.Params) {
			for i := range pt.Params {
				unify(pt.Params[i], x.Params[i], bind)
			}
		}
		pr, xr := types.RawResults(pt), types.RawResults(x)
		if len(pr) == len(xr) {
			for i := range pr {
				unify(pr[i], xr[i], bind)
			}
		}
	case *types.Named:
		x, ok := ab.(*types.Named)
		if ok && pt.Decl != nil && x.Decl == pt.Decl && len(x.Args) == len(pt.Args) {
			// Box[T] against Box[int], both Lyth-declared.
			for i := range pt.Args {
				unify(pt.Args[i], x.Args[i], bind)
			}
			return
		}
		if !ok && pt.Go != nil && len(pt.Args) > 0 {
			// A defined generic type against a type literal unifies through
			// its underlying type, as Go's inference does: RowToFunc[T] with
			// func(CollectableRow) (User, error) gives T = User.
			unify(types.Lift(pt.Go.Underlying()), ab, bind)
			return
		}
		if !ok || x.Go == nil || pt.Go == nil || len(x.Args) != len(pt.Args) || x.Go.Origin() != pt.Go.Origin() {
			return
		}
		for i := range pt.Args {
			unify(pt.Args[i], x.Args[i], bind)
		}
	}
}

// subst replaces bound type parameters in t.
func subst(t types.Type, bind bindings) types.Type {
	switch t := t.(type) {
	case *types.TypeParam:
		if b, ok := bind[t.Go]; ok {
			return b
		}
		return t
	case *types.Platform:
		inner := subst(t.Elem, bind)
		if types.MayBeNull(inner) {
			return inner
		}
		if !types.IsNilable(inner) {
			return inner
		}
		return &types.Platform{Elem: inner}
	case *types.Nullable:
		return types.MakeNullable(subst(t.Elem, bind))
	case *types.Pointer:
		return &types.Pointer{Elem: subst(t.Elem, bind)}
	case *types.Slice:
		return &types.Slice{Elem: subst(t.Elem, bind)}
	case *types.Array:
		return &types.Array{Len: t.Len, Elem: subst(t.Elem, bind)}
	case *types.Map:
		return &types.Map{Key: subst(t.Key, bind), Elem: subst(t.Elem, bind)}
	case *types.Chan:
		return &types.Chan{Dir: t.Dir, Elem: subst(t.Elem, bind)}
	case *types.Signature:
		return substSig(t, bind)
	case *types.Tuple:
		out := &types.Tuple{}
		for _, e := range t.Elems {
			out.Elems = append(out.Elems, subst(e, bind))
		}
		return out
	case *types.Named:
		if t.Decl != nil && len(t.Args) > 0 {
			args := make([]types.Type, len(t.Args))
			for i, a := range t.Args {
				args[i] = subst(a, bind)
			}
			return &types.Named{Decl: t.Decl, Args: args}
		}
		if len(t.Args) == 0 || t.Go == nil {
			return t
		}
		args := make([]types.Type, len(t.Args))
		goArgs := make([]gotypes.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = subst(a, bind)
			goArgs[i] = types.Lower(args[i])
		}
		inst, err := gotypes.Instantiate(nil, t.Go.Origin(), goArgs, false)
		if err != nil {
			return t
		}
		return &types.Named{Go: inst.(*gotypes.Named), Args: args}
	}
	return t
}

func substSig(s *types.Signature, bind bindings) *types.Signature {
	out := &types.Signature{Variadic: s.Variadic, Throws: s.Throws, ParamNames: s.ParamNames}
	for _, p := range s.Params {
		out.Params = append(out.Params, substGo(p, bind))
	}
	for _, r := range s.Results {
		out.Results = append(out.Results, substGo(r, bind))
	}
	return out
}

// substGo substitutes a parameter or result type of a Go signature. A bare
// type parameter bound to a nil-able type becomes a platform type, like any
// nil-able Go value: errors.AsType<*E> returns nil when it fails.
func substGo(t types.Type, bind bindings) types.Type {
	out := subst(t, bind)
	if _, bare := t.(*types.TypeParam); bare && !types.MayBeNull(out) {
		switch types.Base(out).(type) {
		case *types.Pointer, *types.Signature, *types.Chan:
			return &types.Platform{Elem: out}
		}
		if types.IsInterface(out) {
			return &types.Platform{Elem: out}
		}
	}
	return out
}

// TranslateGoMessage rewrites a go/types message into Lyth's terms. Types
// are spelled as in Go; only nil becomes null.
func TranslateGoMessage(msg string) string { return translateGoMessage(msg) }

func translateGoMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "untyped nil", "null")
	var b strings.Builder
	i := 0
	for i < len(msg) {
		j := i
		for j < len(msg) && (msg[j] == '_' || msg[j] >= 'a' && msg[j] <= 'z' || msg[j] >= 'A' && msg[j] <= 'Z' || msg[j] >= '0' && msg[j] <= '9') {
			j++
		}
		if j > i {
			if w := msg[i:j]; w == "nil" && (i == 0 || msg[i-1] != '.') {
				b.WriteString("null")
			} else {
				b.WriteString(w)
			}
			i = j
			continue
		}
		b.WriteByte(msg[i])
		i++
	}
	return b.String()
}

// genericFuncValue reports a generic function used as a value where nothing
// determines its type arguments.
func (c *Checker) genericFuncValue(x *operand) {
	name := x.expr.Span().Short()
	c.errorf(diag.ECannotInfer, x.expr.Span(), "cannot use generic function %s without type arguments", name).
		WithHint("write them: %s[...], or give the expected function type: f: (A) -> R = %s", name, name)
}

// isGenericFunc reports whether t is the type of a generic function used
// without type arguments (cmp.Compare, slices.Reverse).
func isGenericFunc(t types.Type) bool {
	sig, ok := types.Base(t).(*types.Signature)
	return ok && len(sig.TypeParams) > 0
}

// instantiateFuncValue gives the generic function value x the type
// arguments that make it a want, as Go infers them from the function type a
// generic function is assigned or passed to. It reports whether want
// determines them all; x keeps its generic type otherwise.
func (c *Checker) instantiateFuncValue(x *operand, want types.Type, explicit bool) bool {
	sig, ok := types.Base(x.typ).(*types.Signature)
	if !ok || len(sig.TypeParams) == 0 || sig.GoSig == nil {
		return false
	}
	ws := signatureOf(types.Base(want))
	if ws == nil || hasTypeParams(ws) {
		return false
	}
	bind := bindings{}
	unify(&types.Signature{Params: sig.Params, Results: sig.Results, Throws: sig.Throws}, ws, bind)
	c.inferCore(sig, bind) // slices.Sort for ([]int) -> unit: S = []int gives E = int
	targs := make([]gotypes.Type, len(sig.TypeParams))
	for i, tp := range sig.TypeParams {
		b, ok := bind[tp.Go]
		if !ok {
			return false
		}
		targs[i] = types.Lower(b)
	}
	inst, err := gotypes.Instantiate(nil, sig.GoSig, targs, true)
	if err != nil {
		return false
	}
	res := substSig(sig, bind)
	res.GoSig = inst.(*gotypes.Signature)
	x.typ = res
	if explicit && x.expr != nil {
		// Go infers a generic function's type arguments from an assignment,
		// but not reliably from every context (struct fields): write them.
		lyth := make([]types.Type, len(sig.TypeParams))
		for i, tp := range sig.TypeParams {
			lyth[i] = bind[tp.Go]
		}
		c.info.ValueInsts[x.expr] = lyth
	}
	return true
}

// nullableTypeArg reports a type argument that may be null for a type
// parameter whose constraint has methods: the generic code calls them on its
// values, which would be null.
func (c *Checker) nullableTypeArg(span source.Span, tp *types.TypeParam, iface *gotypes.Interface, arg types.Type) {
	if arg == nil || iface.NumMethods() == 0 || !types.MayBeNull(arg) || types.IsPlatform(arg) {
		return
	}
	c.errorf(diag.ETypeArgs, span, "%s cannot be the type %s: it may be null, and %s's constraint has methods that are called on its values", arg, tp.Name, tp.Name).
		WithHint("use %s, and leave the nulls out first", types.Base(arg))
}

// comparableArgs reports a type argument holding an optional value (a
// struct with an int? field) for a comparable type parameter: Go would
// compare it by the boxes' addresses (slices.Contains, map keys).
func (c *Checker) comparableArgs(span source.Span, tps []*types.TypeParam, bind bindings) {
	for _, tp := range tps {
		iface, ok := tp.Go.Constraint().Underlying().(*gotypes.Interface)
		if ok {
			c.nullableTypeArg(span, tp, iface, bind[tp.Go])
		}
		if !ok || !iface.IsComparable() {
			continue
		}
		if t := bind[tp.Go]; t != nil && types.IsNullable(t) && !types.IsNilable(t) {
			c.errorf(diag.ETypeArgs, span, "%s cannot be the comparable type %s: Go would compare the optional values by address, not by value", t, tp.Name)
		} else if path, ft := optionalPart(t); path != "" {
			c.errorf(diag.ETypeArgs, span, "%s cannot be the comparable type %s: %s has type %s, which Go would compare by address", t, tp.Name, path, ft)
		}
	}
}

// constRank orders the numeric kinds of untyped constants as Go does when
// several bind one type parameter: int, then rune, then float.
func constRank(t types.Type) int {
	if b := types.BasicOf(t); b != nil {
		switch b.Kind {
		case types.UntypedIntKind:
			return 1
		case types.UntypedRuneKind:
			return 2
		case types.UntypedFloatKind:
			return 3
		}
	}
	return 0
}
