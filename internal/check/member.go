package check

import (
	gotypes "go/types"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// member is a resolved field or method.
type member struct {
	kind    SelKind
	goName  string
	typ     types.Type
	ptrRecv bool
	obj     any
	prelude PreludeKind
}

func (c *Checker) selector(e *syntax.SelectorExpr, want types.Type) *operand {
	name := e.Sel.Name
	// Package members.
	if id, ok := syntax.Unparen(e.X).(*syntax.Ident); ok {
		member := c.implicitMember(id.Name)
		// In func Circle.Kind(): Kind = Kind.Round, Kind meant the enum
		// even when members were in scope by name: a method value has no
		// members. It still does, with no migration error.
		if member != nil && member.Method != nil {
			if ts, ok := c.lookup(id.Name).(*TypeSym); ok && ts.Decl != nil && ts.Decl.Kind == types.EnumDecl {
				if entry := ts.Decl.LookupEntry(name); entry != nil {
					c.info.Uses[id] = ts
					c.info.Entries[e] = entry
					return &operand{mode: ModeValue, typ: ts.Type, val: entryConst(entry)}
				}
			}
		}
		// strings.Join in a method of a struct with a field strings: []string
		// names the package, since the field has no member Join and so
		// cannot have been meant.
		if ps, ok := c.lookup(id.Name).(*PkgSym); ok && (member == nil || !c.memberHas(member, e.Sel)) {
			c.info.Uses[id] = ps
			ps.Used = true
			c.record(&operand{mode: ModePackage, typ: types.Invalid, expr: e.X, pkg: ps.Pkg})
			if e.Safe {
				c.errorf(diag.EInvalidUse, e.OpSpan, "?. cannot be used on a package")
			}
			if ps.Pkg == nil {
				return invalid(e)
			}
			return c.pkgMember(e, ps.Pkg)
		}
	}
	saveX := c.selectorX
	c.selectorX = syntax.Unparen(e.X)
	x := c.expr(e.X, nil)
	c.selectorX = saveX
	if x.mode == ModeInvalid {
		return invalid(e)
	}
	if x.mode == ModeType {
		if d := types.EnumOf(x.typ); d != nil && !types.IsNullable(x.typ) {
			if entry := d.LookupEntry(name); entry != nil {
				c.info.Entries[e] = entry // an iota constant in Go
				return &operand{mode: ModeValue, typ: x.typ, val: entryConst(entry)}
			}
			d := c.errorf(diag.EUnknownMember, e.Sel.S, "enum %s has no entry %s", d.Name, name)
			if s := closest(name, entryNames(types.EnumOf(x.typ))); s != "" {
				d.WithHint("did you mean %s?", s)
			}
			return invalid(e)
		}
		c.errorf(diag.EUnsupported, e.S, "selecting %s on the type %s is not supported", name, e.X.Span().Short()).
			WithNote("method expressions and static members do not exist in Lyth; call the method on a value")
		return invalid(e)
	}
	sel := &Selection{}
	t := x.typ
	if r, ok := t.(*types.Result); ok && e.Safe {
		// x?.name on a fallible x means (x?).name.
		c.requireFallible(e.OpSpan, "?")
		sel.TryFirst = true
		c.info.Tries[e] = &TryInfo{Success: r.Success, Raw: r.Raw}
		if len(r.Success) != 1 {
			c.errorf(diag.EMultiValue, e.X.Span(), "%s returns %s on success; destructure them instead of selecting .%s",
				callName(e.X), plural(len(r.Success), "value"), name)
			return invalid(e)
		}
		t = r.Success[0]
		if types.IsNullable(t) {
			// find()?.Name on a T? result: the error is handled, the null is not.
			c.errorf(diag.ENullable, e.S, "%s may be null on success (type %s)", callName(e.X), t).
				WithEdit(e.OpSpan, "??.", "propagate the error, then call safely: %s??.%s", e.X.Span().Short(), name)
			return invalid(e)
		}
	} else {
		x = c.singleValue(x)
		if x.mode == ModeInvalid {
			return invalid(e)
		}
		t = x.typ
		if e.Safe {
			if types.MayBeNull(t) {
				sel.Safe = true
			} else {
				why := e.X.Span().Short() + " has non-nullable type " + t.String()
				if v := c.smartCast(syntax.Unparen(e.X)); v != nil {
					why = v.name + " is not null here"
				}
				c.warnf(diag.WUnnecessary, e.OpSpan, "unnecessary safe call: %s", why).
					WithEdit(e.OpSpan, ".", "use . instead of ?.")
			}
		} else if types.IsNullable(t) || t == types.Null {
			c.nullableUse(x, "access ."+name+" on")
			return invalid(e)
		}
	}
	t = types.DefaultType(t) // "hi".Shout() and "x".Nope() see a String
	sel.Recv = t
	m := c.quietMember(types.Base(t), name, e.Sel)
	if m == nil && name == "wrap" && c.implements(t, types.Error) {
		// (&Invalid(...)).wrap("ctx"): any error value wraps, not only error.
		m = &member{kind: SelPrelude, typ: &types.Signature{Params: []types.Type{types.String}, ParamNames: []string{"message"}, Results: []types.Type{types.Error}}, prelude: PreludeWrap}
	}
	if m == nil {
		// Members win; then an extension function for the receiver's type.
		ext, reported := c.findExtension(t, name, e.Sel)
		if ext != nil {
			return c.extensionSelector(e, sel, ext)
		}
		if reported {
			return invalid(e)
		}
		m = c.lookupMember(types.Base(t), name, e.Sel) // reports the error
	}
	if m == nil {
		return invalid(e)
	}
	sel.Kind, sel.GoName, sel.Type, sel.PtrRecv, sel.Obj, sel.Prelude = m.kind, m.goName, m.typ, m.ptrRecv, m.obj, m.prelude
	c.info.Selections[e] = sel
	if m.kind == SelMethod && m.ptrRecv && c.callee != syntax.Expr(e) && !sel.TryFirst {
		// f := o.Inc: a pointer method value binds the receiver's address,
		// so it needs the same mutable storage as a call.
		if _, isPtr := types.Base(c.info.TypeOf(e.X)).(*types.Pointer); !isPtr {
			c.ptrMethodReceiver(e)
		}
	}
	rt := m.typ
	if sel.Safe && (m.kind == SelField || m.kind == SelMethod && c.callee != syntax.Expr(e)) {
		// u?.Name, and the method value u?.Hello: null when u is.
		rt = types.MakeNullable(rt)
	}
	return &operand{mode: ModeValue, typ: rt}
}

// quietMember is lookupMember without diagnostics.
func (c *Checker) quietMember(t types.Type, name string, id *syntax.Ident) *member {
	save := c.sink
	c.sink = &diag.Sink{}
	m := c.lookupMember(t, name, id)
	c.sink = save
	return m
}

// extensionSelector records a selector naming an extension function. An
// extension has no value of its own: it must be called.
func (c *Checker) extensionSelector(e *syntax.SelectorExpr, sel *Selection, ext *ExtCall) *operand {
	if c.callee != syntax.Expr(e) {
		c.errorf(diag.EInvalidUse, e.Sel.S, "extension function %s must be called; it is not a method value", e.Sel.Name).
			WithHint("wrap it in a lambda: { %s.%s(it) }", e.X.Span().Short(), e.Sel.Name)
		return invalid(e)
	}
	if sel.Safe || sel.TryFirst {
		c.errorf(diag.EUnsupported, e.OpSpan, "?. on an extension function call is not supported yet").
			WithHint("check for null first, then call %s", e.Sel.Name)
		return invalid(e)
	}
	c.ensureInferred(ext.Func)
	sel.Kind, sel.GoName, sel.Obj, sel.Type = SelExtension, ext.Func.GoName, ext, ext.Func.Sig
	c.info.Selections[e] = sel
	return &operand{mode: ModeValue, typ: ext.Func.Sig}
}

// lookupMember finds field or method name on t (auto-dereferencing one
// pointer level), reporting an error if it does not exist.
func (c *Checker) lookupMember(t types.Type, name string, id *syntax.Ident) *member {
	base := t
	if p, ok := t.(*types.Pointer); ok {
		base = types.Base(p.Elem)
		if pp, ok := base.(*types.Pointer); ok {
			c.errorf(diag.EInvalidOp, id.S, "cannot select .%s on a pointer to a pointer (%s)", name, pp)
			return nil
		}
	}
	if types.IsErrorType(base) && name == "wrap" {
		return &member{kind: SelPrelude, typ: &types.Signature{Params: []types.Type{types.String}, ParamNames: []string{"message"}, Results: []types.Type{types.Error}}, prelude: PreludeWrap}
	}
	if ch := ChanOf(base); ch != nil {
		if m, ok := c.chanMember(t, ch, name, id); ok {
			return m
		}
	}
	switch b := base.(type) {
	case *types.Named:
		if b.Decl != nil {
			m := c.lythMember(b.Decl, name, id)
			if bind := declBindings(b); m != nil && bind != nil {
				m.typ = subst(m.typ, bind)
			}
			return m
		}
		return c.goMember(t, base, name, id)
	case *types.GoType, *types.AnyType:
		return c.goMember(t, base, name, id)
	case *types.TypeParam:
		// The constraint's methods; a Lyth interface keeps its Lyth signatures.
		if n, ok := b.Constraint.(*types.Named); ok && n.Decl != nil {
			// S: Store[int]: the constraint's methods with its arguments.
			m := c.lythMember(n.Decl, name, id)
			if bind := declBindings(n); m != nil && bind != nil {
				m.typ = subst(m.typ, bind)
			}
			return m
		}
		return c.goMember(t, base, name, id)
	case *types.Basic:
		if m := preludeMember(b, name); m != nil {
			return m
		}
	}
	d := c.errorf(diag.EUnknownMember, id.S, "%s has no field or method %s", t, name)
	switch base.(type) {
	case *types.Slice, *types.Map, *types.Array, *types.Chan:
		switch name {
		case "size", "length", "len":
			d.WithHint("use the builtin len(...)")
		case "add", "push", "append":
			d.WithHint("use append: xs = append(xs, x)")
		case "contains", "includes", "has", "containsKey":
			d.WithHint("test membership with in: x in xs (for a map, the keys)")
		case "isEmpty", "isNotEmpty":
			d.WithHint("compare the length: len(xs) == 0")
		case "forEach", "each":
			d.WithHint("use a loop: for x in xs { ... }")
		case "exclude", "filterNot", "reject":
			d.WithHint("negate the condition: xs.Filter { !... } (import \"lyth/coll\")")
		default:
			if coll, ok := collNames[name]; ok {
				if coll == name {
					d.WithHint("%s is in the standard library: import \"lyth/coll\"", name)
				} else {
					d.WithHint("Lyth's collection operations are exported Go names: %s (import \"lyth/coll\")", coll)
				}
			}
		}
	case *types.Basic:
		if types.IsString(base) && (name == "length" || name == "size" || name == "len") {
			d.WithHint("use len(s) for the length in bytes")
		}
	}
	return nil
}

// ChanOf returns the channel type t is or is defined as (a named Go channel
// type), or nil.
func ChanOf(t types.Type) *types.Chan {
	switch b := types.Base(t).(type) {
	case *types.Chan:
		return b
	case *types.Named:
		if u := definedUnder(b); u != nil {
			ch, _ := types.Base(u).(*types.Chan)
			return ch
		}
		if b.Go != nil {
			if ch, ok := types.Base(types.Lift(b.Go.Underlying())).(*types.Chan); ok {
				return ch
			}
		}
	}
	return nil
}

// chanMember resolves the channel operations send, receive, and close, which
// lower to Go's operators (ADR 0024). ok is false for other names; m is nil
// when the operation is not allowed on the channel's direction.
func (c *Checker) chanMember(t types.Type, ch *types.Chan, name string, id *syntax.Ident) (m *member, ok bool) {
	switch name {
	case "send":
		if ch.Dir == types.RecvOnly {
			c.errorf(diag.EInvalidOp, id.S, "cannot send on %s: it is receive-only", t)
			return nil, true
		}
		return &member{kind: SelPrelude, typ: &types.Signature{Params: []types.Type{ch.Elem}, ParamNames: []string{"value"}}, prelude: PreludeSend}, true
	case "receive":
		if ch.Dir == types.SendOnly {
			c.errorf(diag.EInvalidOp, id.S, "cannot receive from %s: it is send-only", t)
			return nil, true
		}
		return &member{kind: SelPrelude, typ: &types.Signature{Results: []types.Type{ReceiveType(ch)}}, prelude: PreludeReceive}, true
	case "close":
		if ch.Dir == types.RecvOnly {
			c.errorf(diag.EInvalidOp, id.S, "cannot close %s: it is receive-only", t)
			return nil, true
		}
		return &member{kind: SelPrelude, typ: &types.Signature{}, prelude: PreludeClose}, true
	}
	return nil, false
}

// ReceiveType is the type of ch.receive(). A closed channel yields the zero
// value, so an element type without one (a non-null pointer, interface,
// function, or channel) becomes nullable, as for map lookups.
func ReceiveType(ch *types.Chan) types.Type {
	if !types.Zeroable(ch.Elem) && types.IsNilable(ch.Elem) {
		return types.MakeNullable(ch.Elem)
	}
	return ch.Elem
}

func preludeMember(b *types.Basic, name string) *member {
	if !types.IsString(b) || types.IsUntyped(b) && b.Kind != types.UntypedStringKind {
		return nil
	}
	fallible := func(t types.Type, k PreludeKind) *member {
		return &member{kind: SelPrelude, typ: &types.Signature{Results: []types.Type{t}, Throws: true}, prelude: k}
	}
	switch name {
	case "toInt":
		return fallible(types.Int, PreludeToInt)
	case "toInt64":
		return fallible(types.Int64, PreludeToInt64)
	case "toFloat64":
		return fallible(types.Float64, PreludeToFloat64)
	case "toBool":
		return fallible(types.Bool, PreludeToBool)
	}
	return nil
}

func (c *Checker) lythMember(d *types.TypeDecl, name string, id *syntax.Ident) *member {
	if f := d.LookupField(name); f != nil {
		if f.Private && d.PkgPath != c.pkg.Path {
			c.errorf(diag.EPrivate, id.S, "field %s of %s is unexported", name, d.Name)
		}
		return &member{kind: SelField, goName: f.GoName, typ: f.Type, obj: f}
	}
	if m := d.LookupMethod(name); m != nil {
		if !m.Pub && d.PkgPath != c.pkg.Path {
			c.errorf(diag.EPrivate, id.S, "method %s of %s is unexported", name, d.Name)
		}
		if fs, ok := m.Sym.(*FuncSym); ok && fs.Pkg == c.pkg {
			c.ensureInferred(fs)
		}
		return &member{kind: SelMethod, goName: m.GoName, typ: m.Sig, ptrRecv: m.PtrRecv, obj: m}
	}
	for _, e := range d.Embeds {
		if mm := c.lookupMemberQuiet(types.Base(e), name); mm != nil {
			return mm
		}
	}
	if m, found := c.promotedMember(d, name, id); found {
		return m
	}
	dg := c.errorf(diag.EUnknownMember, id.S, "%s has no field or method %s", d.Name, name)
	best, bestDist := "", 3
	for _, f := range d.Fields {
		if dist := editDistance(strings.ToLower(f.Name), strings.ToLower(name)); dist < bestDist {
			best, bestDist = f.Name, dist
		}
	}
	for _, m := range d.Methods {
		if dist := editDistance(strings.ToLower(m.Name), strings.ToLower(name)); dist < bestDist {
			best, bestDist = m.Name, dist
		}
	}
	if best != "" {
		dg.WithHint("did you mean %s?", best)
	}
	return nil
}

// promotedMember finds a field or method promoted through the embedded
// fields of a Lyth struct (ADR 0043). go/types resolves the promotion over
// the shadow package, with Go's depth and ambiguity rules; the path is then
// followed through the Lyth declarations, so a promoted Lyth member keeps its
// Lyth type (nullability, throws). found is false when nothing is promoted
// under that name; m is nil when found reports an error.
func (c *Checker) promotedMember(d *types.TypeDecl, name string, id *syntax.Ident) (m *member, found bool) {
	if d.Kind != types.StructDecl || d.Shadow == nil || !slices.ContainsFunc(d.Fields, func(f *types.Field) bool { return f.Embedded }) {
		return nil, false
	}
	obj, index, indirect := gotypes.LookupFieldOrMethod(d.Shadow, true, c.shadow, name)
	if obj == nil {
		if index != nil {
			c.errorf(diag.EUnknownMember, id.S, "ambiguous selector %s: more than one embedded field of %s promotes it at the same depth", name, d.Name).
				WithHint("select it through the embedded field that is meant")
			return nil, true
		}
		return nil, false
	}
	// Follow the embedded fields while they are Lyth structs.
	var cur types.Type = &types.Named{Decl: d, Args: typeParamTypes(d.TypeParams)}
	var through *types.Field
	path := index[:len(index)-1]
	for _, i := range path {
		n, ok := types.Base(cur).(*types.Named)
		if !ok || n.Decl == nil || n.Decl.Kind != types.StructDecl || i >= len(n.Decl.Fields) {
			cur = nil
			break
		}
		f := n.Decl.Fields[i]
		ft := f.Type
		if bind := declBindings(n); bind != nil {
			ft = subst(ft, bind)
		}
		if through == nil && types.MayBeNull(ft) && !types.IsPlatform(ft) {
			through = f
		}
		cur = types.Base(ft)
		if p, ok := cur.(*types.Pointer); ok {
			cur = types.Base(p.Elem)
		}
	}
	if through != nil {
		c.errorf(diag.ENullable, id.S, "%s is promoted through the nullable field %s", name, through.Name).
			WithHint("check the field first: if x.%s != null { x.%s.%s }", through.Name, through.Name, name)
		return nil, true
	}
	owner, _ := cur.(*types.Named)
	var bind bindings
	if owner != nil {
		bind = declBindings(owner)
	}
	lyth := func(t types.Type) types.Type {
		if bind != nil {
			return subst(t, bind)
		}
		return t
	}
	switch o := obj.(type) {
	case *gotypes.Var:
		last := index[len(index)-1]
		if owner != nil && owner.Decl != nil && owner.Decl.Kind == types.StructDecl && last < len(owner.Decl.Fields) {
			f := owner.Decl.Fields[last]
			return &member{kind: SelField, goName: f.GoName, typ: lyth(f.Type), obj: f}, true
		}
		return &member{kind: SelField, goName: o.Name(), typ: c.lift(o.Type()), obj: o}, true
	case *gotypes.Func:
		if owner != nil && owner.Decl != nil {
			for _, lm := range owner.Decl.Methods {
				if lm.GoName == o.Name() {
					if fs, ok := lm.Sym.(*FuncSym); ok && fs.Pkg == c.pkg {
						c.ensureInferred(fs)
					}
					return &member{kind: SelMethod, goName: lm.GoName, typ: lyth(lm.Sig), ptrRecv: lm.PtrRecv && !indirect, obj: lm}, true
				}
			}
		}
		sig := o.Type().(*gotypes.Signature)
		ptr := false
		if recv := sig.Recv(); recv != nil && !indirect {
			_, ptr = recv.Type().(*gotypes.Pointer)
		}
		return &member{kind: SelMethod, goName: o.Name(), typ: c.liftSig(sig), ptrRecv: ptr, obj: o}, true
	}
	return nil, false
}

func (c *Checker) lookupMemberQuiet(t types.Type, name string) *member {
	switch b := t.(type) {
	case *types.Named:
		if b.Decl != nil {
			// A method of an embedded interface instance (Reader[T] in
			// Store[T]) is seen with the instance's type arguments.
			bind := declBindings(b)
			if m := b.Decl.LookupMethod(name); m != nil {
				var mt types.Type = m.Sig
				if bind != nil {
					mt = subst(mt, bind)
				}
				return &member{kind: SelMethod, goName: m.GoName, typ: mt, obj: m}
			}
			for _, e := range b.Decl.Embeds {
				et := types.Base(e)
				if bind != nil {
					et = subst(et, bind)
				}
				if mm := c.lookupMemberQuiet(et, name); mm != nil {
					return mm
				}
			}
			return nil
		}
	}
	obj, _, _ := gotypes.LookupFieldOrMethod(types.Lower(t), true, nil, name)
	if fn, ok := obj.(*gotypes.Func); ok && fn.Exported() {
		return &member{kind: SelMethod, goName: name, typ: c.liftSig(fn.Type().(*gotypes.Signature)), obj: fn}
	}
	return nil
}

// goMember resolves a field or method of a Go type via go/types, so that
// promotion through embedded fields is exactly Go's.
func (c *Checker) goMember(t, base types.Type, name string, id *syntax.Ident) *member {
	gt := types.Lower(t)
	obj, _, indirect := gotypes.LookupFieldOrMethod(gt, true, nil, name)
	if obj == nil || !obj.Exported() {
		d := c.errorf(diag.EUnknownMember, id.S, "%s has no exported field or method %s", base, name)
		if r, size := utf8.DecodeRuneInString(name); unicode.IsLower(r) {
			alt := string(unicode.ToUpper(r)) + name[size:]
			if o, _, _ := gotypes.LookupFieldOrMethod(gt, true, nil, alt); o != nil {
				d.WithFix(id.S, alt, "Go names are used verbatim: write %s", alt)
				return nil
			}
		}
		if s := suggestMember(gt, name); s != "" {
			d.WithHint("did you mean %s?", s)
		}
		return nil
	}
	switch o := obj.(type) {
	case *gotypes.Var:
		return &member{kind: SelField, goName: name, typ: c.lift(o.Type()), obj: o}
	case *gotypes.Func:
		sig := o.Type().(*gotypes.Signature)
		ptr := false
		if recv := sig.Recv(); recv != nil {
			if _, ok := recv.Type().(*gotypes.Pointer); ok {
				ptr = true
			}
		}
		_ = indirect
		return &member{kind: SelMethod, goName: name, typ: c.liftSig(sig), ptrRecv: ptr, obj: o}
	}
	return nil
}

func suggestMember(t gotypes.Type, name string) string {
	ms := gotypes.NewMethodSet(t)
	if _, isPtr := t.(*gotypes.Pointer); !isPtr {
		ms = gotypes.NewMethodSet(gotypes.NewPointer(t))
	}
	best, bestDist := "", 3
	for i := 0; i < ms.Len(); i++ {
		n := ms.At(i).Obj().Name()
		if !ms.At(i).Obj().Exported() {
			continue
		}
		if d := editDistance(strings.ToLower(n), strings.ToLower(name)); d < bestDist {
			best, bestDist = n, d
		}
	}
	if st, ok := derefGo(t).Underlying().(*gotypes.Struct); ok {
		for i := 0; i < st.NumFields(); i++ {
			n := st.Field(i).Name()
			if !st.Field(i).Exported() {
				continue
			}
			if d := editDistance(strings.ToLower(n), strings.ToLower(name)); d < bestDist {
				best, bestDist = n, d
			}
		}
	}
	return best
}

func derefGo(t gotypes.Type) gotypes.Type {
	if p, ok := t.(*gotypes.Pointer); ok {
		return p.Elem()
	}
	return t
}

func suggestGoName(pkg *gotypes.Package, name string, typesOnly bool) string {
	best, bestDist := "", 3
	for _, n := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(n)
		if !obj.Exported() {
			continue
		}
		if _, isType := obj.(*gotypes.TypeName); typesOnly && !isType {
			continue
		}
		if strings.EqualFold(n, name) {
			return n
		}
		if d := editDistance(strings.ToLower(n), strings.ToLower(name)); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// pkgMember resolves pkg.Name.
func (c *Checker) pkgMember(e *syntax.SelectorExpr, pkg *Package) *operand {
	name := e.Sel.Name
	if pkg.Lyth {
		sym := pkg.Scope.LookupLocal(name)
		if sym == nil {
			d := c.errorf(diag.EUnknownPkgName, e.Sel.S, "package %s has no member %s", pkg.Name, name)
			if ext := pkg.Extensions[name]; ext != nil && ext.ExtRecv != nil {
				d.WithHint("%s is an extension function on %s: call it on the value, x.%s(...)", name, ext.ExtRecv, name)
			}
			return invalid(e)
		}
		sel := &Selection{Kind: SelPkgMember, Obj: sym}
		c.info.Uses[e.Sel] = sym
		switch s := sym.(type) {
		case *FuncSym:
			if !s.Pub {
				c.errorf(diag.EPrivate, e.Sel.S, "%s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
			}
			sel.GoName, sel.Type = s.GoName, s.Sig
			c.info.Selections[e] = sel
			return &operand{mode: ModeValue, typ: s.Sig, sym: s}
		case *VarSym:
			if !s.Pub {
				c.errorf(diag.EPrivate, e.Sel.S, "%s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
			}
			sel.GoName, sel.Type = s.GoName, s.Type
			c.info.Selections[e] = sel
			return &operand{mode: ModeValue, typ: s.Type, sym: s}
		case *ConstSym:
			if !s.Pub {
				c.errorf(diag.EPrivate, e.Sel.S, "%s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
			}
			sel.GoName, sel.Type = s.GoName, s.Type
			c.info.Selections[e] = sel
			return &operand{mode: ModeValue, typ: s.Type, val: s.Val, sym: s}
		case *TypeSym:
			if s.Decl != nil && !s.Decl.Pub {
				c.errorf(diag.EPrivate, e.Sel.S, "type %s is not exported by package %s", name, pkg.Name).WithHint("rename it to %s to export it", exportedName(name))
			}
			sel.GoName, sel.Type = s.Decl.GoName, s.Type
			c.info.Selections[e] = sel
			return &operand{mode: ModeType, typ: s.Type, sym: s}
		}
		return invalid(e)
	}
	obj := pkg.Go.Scope().Lookup(name)
	if obj == nil || !obj.Exported() {
		d := c.errorf(diag.EUnknownPkgName, e.Sel.S, "package %s has no exported member %s", pkg.Name, name)
		if s := suggestGoName(pkg.Go, name, false); s != "" {
			if strings.EqualFold(s, name) {
				d.WithFix(e.Sel.S, s, "Go names are used verbatim: write %s.%s", pkg.Name, s)
			} else {
				d.WithHint("did you mean %s.%s?", pkg.Name, s)
			}
		}
		return invalid(e)
	}
	g := &GoObj{Obj: obj, Pkg: pkg}
	c.info.Uses[e.Sel] = g
	sel := &Selection{Kind: SelPkgMember, GoName: name, Obj: obj}
	c.info.Selections[e] = sel
	switch o := obj.(type) {
	case *gotypes.Func:
		sig := c.liftSig(o.Type().(*gotypes.Signature))
		sel.Type = sig
		return &operand{mode: ModeValue, typ: sig, sym: g}
	case *gotypes.Var:
		t := c.lift(o.Type())
		sel.Type = t
		return &operand{mode: ModeValue, typ: t, sym: g}
	case *gotypes.Const:
		t := c.lift(o.Type())
		sel.Type = t
		return &operand{mode: ModeValue, typ: t, val: o.Val(), sym: g}
	case *gotypes.TypeName:
		nt := &syntax.NamedType{Pkg: syntax.Unparen(e.X).(*syntax.Ident), Name: e.Sel, S: e.S}
		t := c.qualifiedType(pkg, nt)
		sel.Type = t
		return &operand{mode: ModeType, typ: t, sym: g}
	}
	return invalid(e)
}

// instantiateExpr resolves Name[Args] used in expression position: a generic
// function of this package or another (zero[int], lib.Zero[int],
// errors.AsType[*E]), or else a type.
func (c *Checker) instantiateExpr(e *syntax.InstantiateExpr) *operand {
	nt := &syntax.NamedType{Args: e.Args, S: e.S}
	switch x := e.X.(type) {
	case *syntax.Ident:
		nt.Name = x
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			c.errorf(diag.EUnsupported, e.S, "explicit type arguments are only supported on type names")
			return invalid(e)
		}
		nt.Pkg, nt.Name = id, x.Sel
	default:
		c.errorf(diag.EUnsupported, e.S, "explicit type arguments are only supported on type names")
		return invalid(e)
	}
	if nt.Pkg == nil {
		switch s := c.lookup(nt.Name.Name).(type) {
		case *TypeSym:
		case *FuncSym:
			if s.Sig != nil && len(s.Sig.TypeParams) > 0 {
				return c.instantiateFunc(e)
			}
			c.errorf(diag.ETypeArgs, e.S, "%s is not a generic function", nt.Name.Name)
			return invalid(e)
		default:
			c.errorf(diag.ETypeArgs, e.S, "%s does not take type arguments", nt.Name.Name)
			return invalid(e)
		}
	} else if ps, ok := c.lookup(nt.Pkg.Name).(*PkgSym); ok && ps.Pkg != nil {
		if ps.Pkg.Lyth {
			if _, isFunc := ps.Pkg.Scope.LookupLocal(nt.Name.Name).(*FuncSym); isFunc {
				return c.instantiateFunc(e)
			}
		} else if _, isFunc := ps.Pkg.Go.Scope().Lookup(nt.Name.Name).(*gotypes.Func); isFunc {
			return c.instantiateFunc(e)
		}
	}
	t := c.resolveType(nt)
	if t == types.Invalid {
		return invalid(e)
	}
	return &operand{mode: ModeType, typ: t}
}

// instantiateFunc checks a generic function with explicit type arguments,
// called (errors.AsType[*NotFound](err), lib.Zero[int]()) or used as a value
// (pgx.RowToStructByName[User]). All type arguments must be given.
func (c *Checker) instantiateFunc(e *syntax.InstantiateExpr) *operand {
	f := c.expr(e.X, nil)
	if f.mode == ModeInvalid {
		return invalid(e)
	}
	sig := signatureOf(f.typ)
	if sig == nil || len(sig.TypeParams) == 0 {
		c.errorf(diag.ETypeArgs, e.S, "%s is not a generic function", e.X.Span().Short())
		return invalid(e)
	}
	tooMany := len(e.Args) > len(sig.TypeParams)
	bind := bindings{}
	if !tooMany {
		for i, a := range e.Args {
			t := c.resolveType(a)
			if t == types.Invalid {
				return invalid(e)
			}
			bind[sig.TypeParams[i].Go] = t
		}
		// As in Go, leading type arguments may determine the rest through
		// constraints' core types: slices.Reverse[[]int] gives E = int.
		c.inferCore(sig, bind)
	}
	if tooMany || len(bind) != len(sig.TypeParams) {
		var names []string
		for _, tp := range sig.TypeParams {
			names = append(names, tp.Name)
		}
		c.errorf(diag.ETypeArgs, e.S, "%s takes %s (%s), got %d", e.X.Span().Short(), plural(len(sig.TypeParams), "type argument"), strings.Join(names, ", "), len(e.Args)).
			WithNote("give all type arguments, or none to infer them from the call's arguments")
		return invalid(e)
	}
	targs := make([]types.Type, len(sig.TypeParams))
	goArgs := make([]gotypes.Type, len(sig.TypeParams))
	for i, tp := range sig.TypeParams {
		targs[i] = bind[tp.Go]
		goArgs[i] = types.Lower(targs[i])
	}
	inst, err := gotypes.Instantiate(nil, sig.GoSig, goArgs, true)
	if err != nil {
		c.errorf(diag.ETypeArgs, e.S, "invalid type arguments for %s", e.X.Span().Short()).
			WithNote("%s", c.lythNames(translateGoMessage(err.Error())))
		return invalid(e)
	}
	c.comparableArgs(e.S, sig.TypeParams, bind)
	res := substSig(sig, bind)
	res.GoSig = inst.(*gotypes.Signature)
	c.info.FuncInsts[e] = targs
	return &operand{mode: ModeValue, typ: res, sym: f.sym}
}

// exportedName is name with its first letter upper-cased.
func exportedName(name string) string {
	r, size := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(r)) + name[size:]
}

func entryNames(d *types.TypeDecl) []string {
	names := make([]string, len(d.Entries))
	for i, e := range d.Entries {
		names[i] = e.Name
	}
	return names
}

// collNames maps the operations of lyth/coll, and the Kotlin names users
// try for them, to their lyth/coll names. TestCollNames keeps it in step
// with std/coll.
var collNames = map[string]string{
	"Filter": "Filter", "Map": "Map", "FlatMap": "FlatMap", "Find": "Find", "Any": "Any",
	"All": "All", "Count": "Count", "Fold": "Fold", "Partition": "Partition",
	"Distinct": "Distinct", "DistinctBy": "DistinctBy", "SortedBy": "SortedBy",
	"SortedWith": "SortedWith", "Take": "Take", "ToSet": "ToSet", "GroupBy": "GroupBy",
	"AssociateBy": "AssociateBy", "Keys": "Keys", "SortedKeys": "SortedKeys", "Values": "Values",

	"filter": "Filter", "map": "Map", "flatMap": "FlatMap", "find": "Find", "first": "Find",
	"firstOrNull": "Find", "any": "Any", "all": "All", "count": "Count", "fold": "Fold",
	"reduce": "Fold", "partition": "Partition", "distinct": "Distinct", "unique": "Distinct",
	"distinctBy": "DistinctBy", "sortedBy": "SortedBy", "sortBy": "SortedBy",
	"sortedWith": "SortedWith", "take": "Take", "toSet": "ToSet", "groupBy": "GroupBy",
	"associateBy": "AssociateBy", "keys": "Keys", "values": "Values",
}
