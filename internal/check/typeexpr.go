package check

import (
	"go/constant"
	gotypes "go/types"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// resolveType resolves a type expression in the current scope.
func (c *Checker) resolveType(te syntax.TypeExpr) types.Type {
	t := c.resolveType0(te)
	if !c.inConstraint && constraintOnly(t) {
		c.errorf(diag.EInvalidUse, te.Span(), "%s can only be used as the constraint of a type parameter", t).
			WithHint("declare a type parameter: func f[T: %s](...)", t)
		t = types.Invalid
	}
	c.info.TypeExprs[te] = t
	return t
}

// constraintOnly reports whether t is an interface Go allows only as a
// constraint: Comparable, or one with a type set such as cmp.Ordered.
func constraintOnly(t types.Type) bool { return constraintOnlyIn(t, map[*types.TypeDecl]bool{}) }

// constraintOnlyIn is constraintOnly, visiting each Lyth interface once: an
// interface embedding itself (interface E { E }) is reported elsewhere.
func constraintOnlyIn(t types.Type, seen map[*types.TypeDecl]bool) bool {
	switch b := types.Base(t).(type) {
	case *types.GoType:
		iface, ok := b.T.Underlying().(*gotypes.Interface)
		return ok && hasTypeTerms(iface)
	case *types.Named:
		if b.Decl != nil {
			// A Lyth interface with a type set (ADR 0047), directly or
			// through an embedded interface.
			if b.Decl.Kind != types.InterfaceDecl || seen[b.Decl] {
				return false
			}
			seen[b.Decl] = true
			for _, e := range b.Decl.Embeds {
				if constraintOnlyIn(e, seen) {
					return true
				}
			}
			return false
		}
		if b.Go == nil {
			return false
		}
		iface, ok := b.Go.Underlying().(*gotypes.Interface)
		return ok && !iface.IsMethodSet()
	}
	return false
}

// hasTypeTerms reports whether a Go interface holds a type set, looking at
// its embedded elements only, so that an interface still being declared is
// never completed.
func hasTypeTerms(iface *gotypes.Interface) bool {
	return hasTypeTermsIn(iface, map[*gotypes.Interface]bool{})
}

func hasTypeTermsIn(iface *gotypes.Interface, seen map[*gotypes.Interface]bool) bool {
	if seen[iface] {
		return false // an interface embedding itself, reported elsewhere
	}
	seen[iface] = true
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		switch e := iface.EmbeddedType(i).(type) {
		case *gotypes.Union:
			return true
		case *gotypes.Interface:
			if hasTypeTermsIn(e, seen) {
				return true
			}
		default:
			if u, ok := e.Underlying().(*gotypes.Interface); ok {
				if hasTypeTermsIn(u, seen) {
					return true
				}
			} else if e.Underlying() != nil {
				return true // interface { int }
			}
		}
	}
	return false
}

// unionType resolves a type set, ~int | ~float64 (ADR 0047), to the implicit
// Go interface holding the union, as Go reads [T ~int | ~float64].
func (c *Checker) unionType(u *syntax.UnionType) types.Type {
	save := c.inConstraint
	c.inConstraint = true
	defer func() { c.inConstraint = save }()
	var terms []*gotypes.Term
	for _, tm := range u.Terms {
		t := c.resolveType(tm.Type)
		if t == types.Invalid {
			return types.Invalid
		}
		if types.IsNullable(t) {
			c.errorf(diag.ETypeMismatch, tm.S, "a type set cannot hold the nullable %s", t)
			return types.Invalid
		}
		if _, ok := types.Base(t).(*types.TypeParam); ok {
			c.errorf(diag.EInvalidUse, tm.S, "a type set cannot hold the type parameter %s", t)
			return types.Invalid
		}
		if n, ok := types.Base(t).(*types.Named); ok && n.Decl != nil {
			// A Lyth type may still be being declared, so its Go type is not
			// asked for its underlying type: the declaration answers.
			if n.Decl.Kind == types.InterfaceDecl && len(n.Decl.Methods) > 0 {
				c.errorf(diag.EInvalidUse, tm.S, "a type set cannot hold %s, an interface with methods", t)
				return types.Invalid
			}
			if tm.Tilde {
				c.errorf(diag.EInvalidUse, tm.S, "~%s: the type after ~ must be its own underlying type", t)
				return types.Invalid
			}
			terms = append(terms, gotypes.NewTerm(false, types.Lower(t)))
			continue
		}
		g := types.Lower(t)
		if g == nil || g.Underlying() == nil {
			return types.Invalid // a type still being declared, reported elsewhere
		}
		if iface, ok := g.Underlying().(*gotypes.Interface); ok && iface.NumExplicitMethods() > 0 {
			c.errorf(diag.EInvalidUse, tm.S, "a type set cannot hold %s, an interface with methods", t)
			return types.Invalid
		}
		if tm.Tilde && !gotypes.Identical(g, g.Underlying()) {
			c.errorf(diag.EInvalidUse, tm.S, "~%s: the type after ~ must be its own underlying type", t).
				WithHint("write ~%s", types.Lift(g.Underlying()))
			return types.Invalid
		}
		terms = append(terms, gotypes.NewTerm(tm.Tilde, g))
	}
	iface := gotypes.NewInterfaceType(nil, []gotypes.Type{gotypes.NewUnion(terms)})
	iface.MarkImplicit() // completed by go/types when first used
	return &types.GoType{T: iface}
}

// arrayLen checks the length of an array type. While type declarations
// are resolved, a length may name a type that is not defined yet
// ([T(1)]int inside T, [Node(0)]int inside Node), which go/types cannot
// look into; a length is a constant, so such an expression is reported as
// one that is not.
func (c *Checker) arrayLen(e syntax.Expr) (x *operand) {
	if c.defining != nil {
		n := c.sink.Len()
		defer func() {
			if r := recover(); r != nil {
				c.sink.Truncate(n)
				c.errorf(diag.EInvalidUse, e.Span(), "array length must be a constant integer")
				x = invalid(e)
			}
		}()
	}
	return c.expr(e, types.Int)
}

// aliasArgs instantiates a generic alias with the type arguments of te,
// Set[string] for type Set[T: comparable] = map[T]bool, and reports type
// arguments given to an alias that takes none.
func (c *Checker) aliasArgs(ts *TypeSym, t types.Type, te *syntax.NamedType) types.Type {
	if len(ts.AliasParams) == 0 {
		if len(te.Args) > 0 {
			c.errorf(diag.ETypeArgs, te.S, "type alias %s does not take type arguments", te.Name.Name)
		}
		return t
	}
	if len(te.Args) != len(ts.AliasParams) {
		c.errorf(diag.ETypeArgs, te.S, "%s takes %s, but %d given", te.Name.Name, plural(len(ts.AliasParams), "type argument"), len(te.Args)).
			WithHint("write %s[%s]", te.Name.Name, typeParamNamesOf(ts.AliasParams))
		return types.Invalid
	}
	bind := bindings{}
	for i, tp := range ts.AliasParams {
		a := c.resolveType(te.Args[i])
		if a == types.Invalid {
			return types.Invalid
		}
		if tp.Constraint != nil && !c.satisfies(a, tp) {
			c.errorf(diag.ETypeArgs, te.Args[i].Span(), "%s does not satisfy the constraint %s of %s", a, tp.Constraint, tp.Name)
			return types.Invalid
		}
		bind[tp.Go] = a
	}
	return subst(t, bind)
}

// satisfies reports whether t meets the constraint of tp, by go/types.
func (c *Checker) satisfies(t types.Type, tp *types.TypeParam) bool {
	iface, ok := tp.Go.Constraint().Underlying().(*gotypes.Interface)
	if !ok {
		return true
	}
	return gotypes.Satisfies(types.Lower(t), iface)
}

func typeParamNamesOf(tps []*types.TypeParam) string {
	var names []string
	for _, tp := range tps {
		names = append(names, tp.Name)
	}
	return strings.Join(names, ", ")
}

// EmptyStruct is Go's struct{}.
var EmptyStruct = &types.GoType{T: gotypes.NewStruct(nil, nil)}

func (c *Checker) resolveType0(te syntax.TypeExpr) types.Type {
	switch te := te.(type) {
	case *syntax.UnionType:
		return c.unionType(te)
	case *syntax.EmptyStructType:
		return EmptyStruct
	case *syntax.NamedType:
		if te.Pkg != nil {
			sym := c.lookup(te.Pkg.Name)
			ps, ok := sym.(*PkgSym)
			if !ok {
				if sym == nil {
					d := c.errorf(diag.EUndefined, te.Pkg.S, "undefined package %s", te.Pkg.Name)
					if path, ok := stdPackages[te.Pkg.Name]; ok {
						d.WithHint("add the import: import %q", path)
					}
				} else {
					c.errorf(diag.ENotExpected, te.Pkg.S, "%s is not a package", te.Pkg.Name)
				}
				return types.Invalid
			}
			ps.Used = true
			c.info.Uses[te.Pkg] = ps
			if ps.Pkg == nil {
				return types.Invalid
			}
			return c.qualifiedType(ps.Pkg, te)
		}
		sym := c.lookup(te.Name.Name)
		if sym == nil {
			d := c.errorf(diag.EUndefined, te.Name.S, "undefined type %s", te.Name.Name)
			if hint, ok := oldTypeNames[te.Name.Name]; ok {
				d.WithFix(te.Name.S, hint, "Lyth uses Go's type names: %s", hint)
			} else if s := c.suggest(te.Name.Name); s != "" {
				d.WithHint("did you mean %s?", s)
			}
			return types.Invalid
		}
		ts, ok := sym.(*TypeSym)
		if !ok {
			c.errorf(diag.ENotExpected, te.Name.S, "%s is not a type", te.Name.Name)
			return types.Invalid
		}
		c.info.Uses[te.Name] = ts
		if ts.Alias != nil {
			t := c.aliasType(ts)
			return c.aliasArgs(ts, t, te)
		}
		if ts.Generic != "" {
			return c.builtinGeneric(ts.Generic, te)
		}
		if ts.Decl != nil {
			return c.lythTypeRef(ts, te)
		}
		if len(te.Args) > 0 {
			c.errorf(diag.ETypeArgs, te.S, "type %s does not take type arguments", te.Name.Name)
		}
		return ts.Type
	case *syntax.PointerType:
		elem := c.resolveType(te.Elem)
		if elem == types.Unit {
			c.errorf(diag.EInvalidUse, te.Elem.Span(), "cannot point to unit")
			return types.Invalid
		}
		return &types.Pointer{Elem: elem}
	case *syntax.NullableType:
		elem := c.resolveType(te.Elem)
		switch b := elem.(type) {
		case *types.Slice, *types.Map:
			c.errorf(diag.ENullableColl, te.S, "%s cannot be nullable; its empty value already represents absence", elem).
				WithHint("use %s and an empty value instead of null", elem)
			return elem
		case *types.Nullable:
			return b
		}
		if n, ok := elem.(*types.Named); ok && n.Go != nil {
			switch collectionOf(n).(type) {
			case *types.Slice, *types.Map:
				// http.Header?, json.RawMessage?: a slice or map underneath.
				c.errorf(diag.ENullableColl, te.S, "%s cannot be nullable; its empty value already represents absence", elem).
					WithHint("use %s and an empty value instead of null", elem)
				return elem
			}
		}
		if elem == types.Unit || elem == types.Invalid {
			return elem
		}
		if _, ok := elem.(*types.TypeParam); ok {
			c.errorf(diag.ENullableTParam, te.S, "nullable type parameters are not supported yet")
		}
		return &types.Nullable{Elem: elem}
	case *syntax.SliceType:
		return &types.Slice{Elem: c.resolveType(te.Elem)}
	case *syntax.MapType:
		key, elem := c.resolveType(te.Key), c.resolveType(te.Value)
		if key == types.Unit || elem == types.Unit {
			c.errorf(diag.EInvalidUse, te.S, "unit cannot be a map key or value")
			return types.Invalid
		}
		// A type parameter used in a constraint may have no bound yet
		// (func f[K: map[K]int]); go/types cannot judge it then.
		gk := types.Lower(key)
		if tp, ok := gk.(*gotypes.TypeParam); ok && tp.Constraint() == nil {
			return &types.Map{Key: key, Elem: elem}
		}
		keySpan := te.Key.Span()
		// A Lyth type of this package (an enum key in a struct field) is
		// comparable or not only once the package's types are defined.
		c.whenDeclared(func() {
			if key != types.Invalid && !gotypes.Comparable(types.Lower(key)) {
				c.errorf(diag.ETypeArgs, keySpan, "%s cannot be a map key: it is not comparable", key)
				return
			}
			if types.IsNullable(key) && !types.IsNilable(key) {
				c.errorf(diag.ETypeArgs, keySpan, "%s cannot be a map key: Go would compare the optional values by address, not by value", key).
					WithHint("key the map by %s, and keep the entry for null apart", types.Base(key))
				return
			}
			if path, ft := optionalPart(key); path != "" {
				c.errorf(diag.ETypeArgs, keySpan, "%s cannot be a map key: %s has type %s, which Go would compare by address", key, path, ft)
			}
		})
		return &types.Map{Key: key, Elem: elem}
	case *syntax.ChanType:
		elem := c.resolveType(te.Elem)
		if elem == types.Unit {
			c.errorf(diag.EInvalidUse, te.Elem.Span(), "unit cannot be a channel element")
			return types.Invalid
		}
		dir := map[syntax.ChanDir]types.ChanDir{syntax.ChanBoth: types.SendRecv, syntax.ChanSend: types.SendOnly, syntax.ChanReceive: types.RecvOnly}[te.Dir]
		return &types.Chan{Dir: dir, Elem: elem}
	case *syntax.ArrayType:
		if te.Len == nil {
			c.errorf(diag.EInvalidUse, te.S, "[...] gives an array its length only in a typed literal: [...]%s{a, b}", te.Elem.Span().Text())
			return types.Invalid
		}
		n := int64(0)
		x := c.arrayLen(te.Len)
		if x.mode != ModeInvalid {
			if x.val == nil || x.val.Kind() != constant.Int {
				c.errorf(diag.EInvalidUse, te.Len.Span(), "array length must be a constant integer")
			} else if v, ok := constant.Int64Val(x.val); !ok || v < 0 {
				c.errorf(diag.EInvalidUse, te.Len.Span(), "invalid array length %s", x.val)
			} else {
				n = v
			}
		}
		return &types.Array{Len: n, Elem: c.resolveType(te.Elem)}
	case *syntax.FuncType:
		sig := &types.Signature{Throws: te.Throws}
		for _, p := range te.Params {
			sig.Params = append(sig.Params, c.resolveType(p))
		}
		for _, n := range te.ParamNames {
			sig.ParamNames = append(sig.ParamNames, n.Name)
		}
		if te.Result != nil {
			for _, r := range te.Result.Types {
				t := c.resolveType(r)
				if t != types.Unit {
					sig.Results = append(sig.Results, t)
				}
			}
		}
		return sig
	case *syntax.BadType:
		return types.Invalid
	}
	return types.Invalid
}

// lythTypeRef resolves a reference to a Lyth-declared type. A generic one
// takes all its type arguments, which must satisfy its constraints.
func (c *Checker) lythTypeRef(ts *TypeSym, te *syntax.NamedType) types.Type {
	d := ts.Decl
	if len(d.TypeParams) == 0 {
		if len(te.Args) > 0 {
			c.errorf(diag.ETypeArgs, te.S, "type %s does not take type arguments", te.Name.Name)
		}
		return ts.Type
	}
	if len(te.Args) != len(d.TypeParams) {
		c.errorf(diag.ETypeArgs, te.S, "%s takes %s (%s), got %d", d.Name, plural(len(d.TypeParams), "type argument"), typeParamNames(d.TypeParams), len(te.Args)).
			WithHint("write %s[%s]", d.Name, typeParamNames(d.TypeParams))
		return types.Invalid
	}
	args := make([]types.Type, len(te.Args))
	for i, a := range te.Args {
		if args[i] = c.resolveType(a); args[i] == types.Invalid {
			return types.Invalid
		}
	}
	inst := &types.Named{Decl: d, Args: args}
	if !c.validInstance(inst, te.S) {
		return types.Invalid
	}
	return inst
}

// validInstance reports whether the type arguments of a generic Lyth type
// satisfy its constraints, reporting an error at span if not.
func (c *Checker) validInstance(inst *types.Named, span source.Span) bool {
	if c.declaring {
		// An instance in a declaration: its arguments may lack methods that
		// are declared later, and its constraints may not be set yet.
		c.pendingChecks = append(c.pendingChecks, func() { c.validInstance(inst, span) })
		return true
	}
	if named := inst.Decl.Shadow; named != nil {
		tps := named.TypeParams()
		for i := range tps.Len() {
			if tps.At(i).Constraint() == nil {
				c.errorf(diag.EInvalidUse, span, "a type parameter's constraint cannot refer to %s, the type being declared", inst.Decl.Name)
				return false
			}
		}
	}
	args := make([]gotypes.Type, len(inst.Args))
	for i, a := range inst.Args {
		args[i] = types.Lower(a)
		if unboundTypeParam(args[i]) {
			// func f[T: Page[T]]: T's own constraint is being resolved, so
			// go/types cannot judge the instance yet (nor can Lyth).
			return true
		}
	}
	if _, err := gotypes.Instantiate(nil, inst.Decl.Shadow, args, true); err != nil {
		c.errorf(diag.ETypeArgs, span, "invalid type arguments for %s", inst.Decl.Name).
			WithNote("%s", c.lythNames(translateGoMessage(err.Error())))
		return false
	}
	if len(inst.Decl.TypeParams) == len(inst.Args) {
		bind := bindings{}
		for i, tp := range inst.Decl.TypeParams {
			bind[tp.Go] = inst.Args[i]
		}
		c.comparableArgs(span, inst.Decl.TypeParams, bind)
	}
	return true
}

// unboundTypeParam reports whether t contains a type parameter whose
// constraint is not set yet.
func unboundTypeParam(t gotypes.Type) bool {
	switch t := t.(type) {
	case *gotypes.TypeParam:
		return t.Constraint() == nil
	case *gotypes.Pointer:
		return unboundTypeParam(t.Elem())
	case *gotypes.Slice:
		return unboundTypeParam(t.Elem())
	case *gotypes.Array:
		return unboundTypeParam(t.Elem())
	case *gotypes.Map:
		return unboundTypeParam(t.Key()) || unboundTypeParam(t.Elem())
	case *gotypes.Chan:
		return unboundTypeParam(t.Elem())
	case *gotypes.Named:
		for i := range t.TypeArgs().Len() {
			if unboundTypeParam(t.TypeArgs().At(i)) {
				return true
			}
		}
	}
	return false
}

// oldTypeNames maps the type names of Lyth before ADR 0031, and Kotlin's,
// to Lyth's (Go's) spelling.
var oldTypeNames = map[string]string{
	"Bool": "bool", "Int": "int", "Int8": "int8", "Int16": "int16", "Int32": "int32", "Int64": "int64",
	"UInt": "uint", "UInt8": "uint8", "UInt16": "uint16", "UInt32": "uint32", "UInt64": "uint64",
	"UIntPtr": "uintptr", "Float32": "float32", "Float64": "float64", "Complex64": "complex64",
	"Complex128": "complex128", "String": "string", "Byte": "byte", "Rune": "rune",
	"Any": "any", "Error": "error", "Comparable": "comparable", "Unit": "unit", "Nothing": "nothing",
	"Long": "int64", "Double": "float64", "Boolean": "bool", "Float": "float32", "Char": "rune",
	"Integer": "int", "Object": "any", "Short": "int16", "Void": "unit",
}

// builtinGeneric reports the collection spellings of Lyth before ADR 0031
// (Map<K, V>, Chan<T>, ...) and Kotlin's List<T>, resolving them for recovery.
func (c *Checker) builtinGeneric(name string, te *syntax.NamedType) types.Type {
	want := 1
	if name == "Map" || name == "HashMap" {
		want = 2
	}
	args := make([]string, len(te.Args))
	for i, a := range te.Args {
		args[i] = a.Span().Text()
	}
	if len(te.Args) != want {
		c.errorf(diag.EUnsupportedSyntax, te.S, "Lyth uses Go's spelling for %s types", map[bool]string{true: "map", false: "slice and channel"}[want == 2])
		return types.Invalid
	}
	var spelling string
	switch name {
	case "List", "MutableList", "ArrayList", "Array":
		spelling = "[]" + args[0]
	case "Map", "HashMap":
		spelling = "map[" + args[0] + "]" + args[1]
	case "Chan":
		spelling = "chan " + args[0]
	case "SendChan":
		spelling = "chan<- " + args[0]
	case "ReceiveChan":
		spelling = "<-chan " + args[0]
	}
	c.errorf(diag.EUnsupportedSyntax, te.S, "Lyth uses Go's spelling for this type").
		WithFix(te.S, spelling, "write %s", spelling)
	elems := make([]types.Type, len(te.Args))
	for i, a := range te.Args {
		elems[i] = c.resolveType(a)
	}
	switch name {
	case "Map", "HashMap":
		return &types.Map{Key: elems[0], Elem: elems[1]}
	case "Chan":
		return &types.Chan{Dir: types.SendRecv, Elem: elems[0]}
	case "SendChan":
		return &types.Chan{Dir: types.SendOnly, Elem: elems[0]}
	case "ReceiveChan":
		return &types.Chan{Dir: types.RecvOnly, Elem: elems[0]}
	}
	return &types.Slice{Elem: elems[0]}
}

// qualifiedType resolves pkg.Name type references.
func (c *Checker) qualifiedType(pkg *Package, te *syntax.NamedType) types.Type {
	name := te.Name.Name
	if pkg.Lyth {
		sym := pkg.Scope.LookupLocal(name)
		ts, ok := sym.(*TypeSym)
		if ok && ts.Alias != nil {
			if !ts.AliasPub {
				c.errorf(diag.EPrivate, te.Name.S, "type %s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
			}
			c.info.Uses[te.Name] = ts
			return ts.Type
		}
		if !ok || ts.Decl == nil {
			c.errorf(diag.EUnknownPkgName, te.Name.S, "package %s has no type %s", pkg.Name, name)
			return types.Invalid
		}
		if !ts.Decl.Pub {
			c.errorf(diag.EPrivate, te.Name.S, "type %s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
		}
		c.info.Uses[te.Name] = ts
		return c.lythTypeRef(ts, te)
	}
	obj := pkg.Go.Scope().Lookup(name)
	if obj == nil || !obj.Exported() {
		d := c.errorf(diag.EUnknownPkgName, te.Name.S, "package %s has no exported type %s", pkg.Name, name)
		if s := suggestGoName(pkg.Go, name, true); s != "" {
			d.WithHint("did you mean %s.%s?", pkg.Name, s)
		}
		return types.Invalid
	}
	tn, ok := obj.(*gotypes.TypeName)
	if !ok {
		c.errorf(diag.ENotExpected, te.Name.S, "%s.%s is not a type", pkg.Name, name)
		return types.Invalid
	}
	c.info.Uses[te.Name] = &GoObj{Obj: obj, Pkg: pkg}
	t := tn.Type()
	if named, ok := gotypes.Unalias(t).(*gotypes.Named); ok && named.TypeParams().Len() > 0 && named.TypeArgs().Len() == 0 {
		if len(te.Args) != named.TypeParams().Len() {
			c.errorf(diag.ETypeArgs, te.S, "%s.%s requires %s", pkg.Name, name, plural(named.TypeParams().Len(), "type argument"))
			return types.Invalid
		}
		args := make([]types.Type, len(te.Args))
		goArgs := make([]gotypes.Type, len(te.Args))
		for i, a := range te.Args {
			args[i] = c.resolveType(a)
			goArgs[i] = types.Lower(args[i])
		}
		report := func(err error) {
			c.errorf(diag.ETypeArgs, te.S, "invalid type arguments for %s.%s: %s", pkg.Name, name, c.lythNames(translateGoMessage(err.Error())))
		}
		if c.declaring {
			// Validate once the package's methods are declared (see
			// validInstance).
			inst, err := gotypes.Instantiate(nil, named, goArgs, false)
			if err != nil {
				report(err)
				return types.Invalid
			}
			c.pendingChecks = append(c.pendingChecks, func() {
				if _, err := gotypes.Instantiate(nil, named, goArgs, true); err != nil {
					report(err)
				}
			})
			return &types.Named{Go: inst.(*gotypes.Named), Args: args}
		}
		inst, err := gotypes.Instantiate(nil, named, goArgs, true)
		if err != nil {
			report(err)
			return types.Invalid
		}
		return &types.Named{Go: inst.(*gotypes.Named), Args: args}
	}
	if len(te.Args) > 0 {
		c.errorf(diag.ETypeArgs, te.S, "%s.%s does not take type arguments", pkg.Name, name)
	}
	// A type written in Lyth source is non-null by default.
	return types.Base(c.lift(t))
}

// lythNames rewrites the qualifiers go/types puts on names, which are package
// paths (lythrun.mem, example.com/app/lib.Store), the way Lyth writes them:
// this package's names unqualified, other packages' by package name (mem,
// lib.Store).
func (c *Checker) lythNames(msg string) string {
	quals := map[string]string{c.shadow.Path(): ""}
	var addGo func(p *gotypes.Package)
	addGo = func(p *gotypes.Package) {
		if _, ok := quals[p.Path()]; !ok {
			quals[p.Path()] = p.Name() + "."
			for _, q := range p.Imports() {
				addGo(q)
			}
		}
	}
	var add func(p *Package)
	add = func(p *Package) {
		if _, ok := quals[p.Path]; ok {
			return
		}
		quals[p.Path] = p.Name + "."
		for _, q := range p.Imports {
			add(q)
		}
		if !p.Lyth && p.Go != nil {
			for _, q := range p.Go.Imports() {
				addGo(q)
			}
		}
	}
	for _, p := range c.pkg.Imports {
		add(p)
	}
	var b strings.Builder
	for i := 0; i < len(msg); {
		j := i
		for j < len(msg) && pathByte(msg[j]) {
			j++
		}
		if j == i {
			b.WriteByte(msg[i])
			i++
			continue
		}
		b.WriteString(requalify(msg[i:j], quals))
		i = j
	}
	return b.String()
}

// requalify replaces the package path that qualifies word (lib.Store,
// example.com/app/lib.Store.Get) by its entry in quals, if it has one.
func requalify(word string, quals map[string]string) string {
	for k := strings.LastIndexByte(word, '/') + 1; k < len(word); k++ {
		if word[k] == '.' {
			if q, ok := quals[word[:k]]; ok {
				return q + word[k+1:]
			}
		}
	}
	return word
}

// pathByte reports whether b can appear in a package path or a name.
func pathByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' ||
		b == '_' || b == '-' || b == '.' || b == '/' || b == '~' || b >= 0x80
}

// whenDeclared runs f now, or once every declaration of the package is
// resolved when types are still being declared (struct fields may not be).
func (c *Checker) whenDeclared(f func()) {
	if c.declaring {
		c.pendingChecks = append(c.pendingChecks, f)
		return
	}
	f()
}
