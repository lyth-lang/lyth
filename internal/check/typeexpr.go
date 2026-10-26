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
func constraintOnly(t types.Type) bool {
	n, ok := types.Base(t).(*types.Named)
	if !ok || n.Go == nil {
		return false
	}
	iface, ok := n.Go.Underlying().(*gotypes.Interface)
	return ok && !iface.IsMethodSet()
}

// EmptyStruct is Go's struct{}.
var EmptyStruct = &types.GoType{T: gotypes.NewStruct(nil, nil)}

func (c *Checker) resolveType0(te syntax.TypeExpr) types.Type {
	switch te := te.(type) {
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
			if len(te.Args) > 0 {
				c.errorf(diag.ETypeArgs, te.S, "type alias %s does not take type arguments", te.Name.Name)
			}
			return c.aliasType(ts)
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
		x := c.expr(te.Len, types.Int)
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
