package check

import (
	"fmt"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"reflect"
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

// scopeFor returns the file scope for a span's file.
func (c *Checker) scopeFor(span source.Span) *Scope {
	for f, s := range c.fileScope {
		if f.Src == span.File {
			return s
		}
	}
	return c.pkgScope
}

// withScope runs fn with c.scope set to s.
func (c *Checker) withScope(s *Scope, fn func()) {
	save := c.scope
	c.scope = s
	fn()
	c.scope = save
}

// ---------------------------------------------------------------------------
// Type declarations

// aliasType resolves a type alias (type Name = Type) on first use.
func (c *Checker) aliasType(ts *TypeSym) types.Type {
	if ts.Type != nil {
		return ts.Type
	}
	if ts.aliasBusy {
		c.errorf(diag.EInvalidUse, ts.Alias.Name.S, "type alias %s refers to itself", ts.name)
		ts.Type = types.Invalid
		return ts.Type
	}
	ts.aliasBusy = true
	c.withScope(c.scopeFor(ts.Alias.S), func() { ts.Type = c.resolveType(ts.Alias.Type) })
	ts.aliasBusy = false
	if ts.Type == types.Unit {
		c.errorf(diag.EInvalidUse, ts.Alias.Type.Span(), "unit cannot be aliased")
		ts.Type = types.Invalid
	}
	return ts.Type
}

func (c *Checker) resolveTypeDecls() {
	c.defining = map[*types.TypeDecl]bool{}
	for _, ts := range c.pkg.Types {
		if ts.Decl != nil {
			c.defining[ts.Decl] = true
		}
	}
	defer c.finishPending()
	// Type parameters first: aliases, fields, and signatures instantiate
	// generic types, which needs the constraints.
	for _, ts := range c.pkg.Types {
		var tps []*syntax.TypeParam
		var span source.Span
		switch d := ts.syn.(type) {
		case *syntax.StructDecl:
			tps, span = d.TypeParams, d.S
		case *syntax.InterfaceDecl:
			tps, span = d.TypeParams, d.S
		case *syntax.TypeDefDecl:
			tps, span = d.TypeParams, d.S
		}
		if len(tps) > 0 && ts.Decl != nil {
			ts.tscope = c.typeParamScope(tps, ts.Decl.TypeParams, c.scopeFor(span))
		}
	}
	for _, a := range c.aliases {
		c.aliasType(a)
	}
	// Fields and interface methods first; shadow underlying types after, so
	// that recursive references through pointers see the shadow objects.
	for _, ts := range c.pkg.Types {
		scopeOf := func(span source.Span) *Scope {
			if ts.tscope != nil {
				return ts.tscope
			}
			return c.scopeFor(span)
		}
		switch d := ts.syn.(type) {
		case *syntax.StructDecl:
			c.withScope(scopeOf(d.S), func() { c.resolveStruct(ts.Decl, d) })
		case *syntax.InterfaceDecl:
			c.withScope(scopeOf(d.S), func() { c.resolveInterface(ts.Decl, d) })
		case *syntax.EnumDecl:
			c.resolveEnum(ts.Decl, d)
		case *syntax.TypeDefDecl:
			c.withScope(scopeOf(d.S), func() { c.resolveDefined(ts.Decl, d) })
		}
	}
	c.checkEmbedCycles()
	var ifaces []*gotypes.Interface
	for _, ts := range c.pkg.Types {
		decl := ts.Decl
		switch decl.Kind {
		case types.StructDecl:
			fields := make([]*gotypes.Var, len(decl.Fields))
			tags := make([]string, len(decl.Fields))
			for i, f := range decl.Fields {
				fields[i] = gotypes.NewField(token.NoPos, c.shadow, f.GoName, types.Lower(f.Type), f.Embedded)
				tags[i] = f.Tag
			}
			decl.Shadow.SetUnderlying(gotypes.NewStruct(fields, tags))
		case types.InterfaceDecl:
			var funcs []*gotypes.Func
			for _, m := range decl.Methods {
				funcs = append(funcs, gotypes.NewFunc(token.NoPos, c.shadow, m.GoName, types.LowerSignature(m.Sig, nil)))
			}
			if decl.Sealed {
				funcs = append(funcs, gotypes.NewFunc(token.NoPos, c.shadow, decl.Marker, gotypes.NewSignatureType(nil, nil, nil, nil, nil, false)))
			}
			var embeds []gotypes.Type
			for _, e := range decl.Embeds {
				// An embed that failed to resolve was reported already.
				if g := types.Lower(e); g != nil && e != types.Invalid {
					embeds = append(embeds, g)
				}
			}
			iface := gotypes.NewInterfaceType(funcs, embeds)
			decl.Shadow.SetUnderlying(iface)
			ifaces = append(ifaces, iface)
		case types.EnumDecl:
			if decl.Backing != nil {
				decl.Shadow.SetUnderlying(gotypes.Typ[gotypes.String])
			} else {
				decl.Shadow.SetUnderlying(gotypes.Typ[gotypes.Int])
			}
			recv := gotypes.NewVar(token.NoPos, c.shadow, "", decl.Shadow)
			for _, m := range decl.Methods {
				decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, m.GoName, types.LowerSignature(m.Sig, recv)))
			}
		}
	}
	// Defined types last: type A B takes B's underlying type, which the loop
	// above has set for structs and enums, and which a defined B gets here.
	visiting := map[*types.TypeDecl]bool{}
	for _, ts := range c.pkg.Types {
		if ts.Decl.Kind == types.DefinedDecl {
			c.setDefinedUnderlying(ts.Decl, visiting)
		}
	}
	// An interface's method set includes its embedded interfaces', which may
	// be declared later in the package: complete them once all are defined.
	for _, iface := range ifaces {
		iface.Complete()
	}
}

// finishPending sets the constraints that waited for the package's types
// to be defined.
func (c *Checker) finishPending() {
	c.defining = nil
	for _, set := range c.pendingConstraints {
		set()
	}
	c.pendingConstraints = nil
}

// validatePendingInstances validates the instances written in
// declarations, now that every type is defined and every method declared.
func (c *Checker) validatePendingInstances() {
	c.declaring = false
	pending := c.pendingChecks
	c.pendingChecks = nil
	for _, check := range pending {
		check()
	}
}

// undefined reports whether t names a Lyth type whose definition is not set
// yet: go/types cannot compute the type set of such a constraint, nor
// expand an instance of it.
func (c *Checker) undefined(t types.Type) bool {
	n, ok := types.Base(t).(*types.Named)
	if !ok || n.Decl == nil || c.defining == nil {
		return false
	}
	if c.defining[n.Decl] {
		return true
	}
	for _, a := range n.Args {
		if c.undefined(a) {
			return true
		}
	}
	return false
}

// resolveEnum records the entries of an enum and its generated String
// method. Entry i of a plain enum is the Go constant <Type><Entry> with value
// i; an entry of a string-backed enum has the string written for it (ADR
// 0035). Each entry has one wire text, so no two entries may share it.
func (c *Checker) resolveEnum(decl *types.TypeDecl, d *syntax.EnumDecl) {
	if len(d.Entries) == 0 {
		c.errorf(diag.EInvalidUse, d.Name.S, "enum %s has no entries", d.Name.Name)
	}
	if d.Backing != nil {
		if t := c.resolveType(d.Backing); t != types.Invalid {
			if !types.Identical(t, types.String) {
				c.errorf(diag.EInvalidUse, d.Backing.Span(), "an enum is backed only by string, not %s", t).
					WithHint("for numeric codes, declare a defined type and constants: type Code int")
			}
			decl.Backing = types.String
		}
	}
	wires := map[string]*types.EnumEntry{}
	for i, id := range d.Entries {
		if old := decl.LookupEntry(id.Name); old != nil {
			c.errorf(diag.EDuplicate, id.S, "enum %s already has an entry %s", decl.Name, id.Name).WithRelated(old.Span, "previous entry")
			continue
		}
		e := &types.EnumEntry{Name: id.Name, GoName: decl.GoName + exportedName(id.Name), Value: i, Decl: decl, Span: id.S}
		var value syntax.Expr
		if i < len(d.Values) {
			value = d.Values[i]
		}
		valid := true
		switch {
		case d.Backing == nil:
			if value != nil {
				c.errorf(diag.EUnsupported, value.Span(), "entries of a plain enum have no values").
					WithHint("declare a string-backed enum to give entries values: enum %s: string { %s = \"...\" }", decl.Name, id.Name)
			}
			e.Wire = JSONName(id.Name)
		case value == nil:
			c.errorf(diag.EInvalidUse, id.S, "entry %s of the string-backed enum %s needs a value", id.Name, decl.Name).
				WithHint("write %s = \"%s\"", id.Name, JSONName(id.Name))
			valid = false
		default:
			lit, ok := value.(*syntax.BasicLit)
			if !ok || lit.Kind != syntax.STRING {
				c.errorf(diag.EInvalidUse, value.Span(), "the value of %s.%s must be a string literal", decl.Name, id.Name)
				valid = false
				break
			}
			e.Wire = lit.Value
		}
		if old := wires[e.Wire]; valid && old != nil {
			if d.Backing != nil {
				c.errorf(diag.EDuplicate, id.S, "entries %s and %s of enum %s have the same value %q", old.Name, id.Name, decl.Name, e.Wire).WithRelated(old.Span, "%s declared here", old.Name)
			} else {
				c.errorf(diag.EDuplicate, id.S, "entries %s and %s of enum %s have the same wire name %q", old.Name, id.Name, decl.Name, e.Wire).
					WithRelated(old.Span, "%s declared here", old.Name).
					WithHint("rename one entry, or declare MarshalText and UnmarshalText for %s", decl.Name)
			}
		} else if valid {
			wires[e.Wire] = e
			decl.HasEmpty = decl.HasEmpty || d.Backing != nil && e.Wire == ""
		}
		c.claimGoName(e.GoName, id.S, decl.Name+"."+id.Name)
		decl.Entries = append(decl.Entries, e)
	}
	decl.Methods = append(decl.Methods, &types.Method{Name: "String", GoName: "String", Sig: &types.Signature{Results: []types.Type{types.String}}, Pub: true, Span: d.Name.S})
}

// finishEnums decides, once every method is declared, whether the compiler
// generates an enum's MarshalText and UnmarshalText (ADR 0035): it does
// unless the enum declares them itself. A plain enum declares both or
// neither, since the generated half would not read what the other writes.
func (c *Checker) finishEnums() {
	for _, ts := range c.pkg.Types {
		decl := ts.Decl
		if decl.Kind != types.EnumDecl {
			continue
		}
		marshal, unmarshal := decl.LookupMethod("MarshalText"), decl.LookupMethod("UnmarshalText")
		if decl.Backing == nil && (marshal == nil) != (unmarshal == nil) {
			have, missing := marshal, "UnmarshalText"
			if have == nil {
				have, missing = unmarshal, "MarshalText"
			}
			c.errorf(diag.EInvalidUse, have.Span, "enum %s declares %s but not %s", decl.Name, have.Name, missing).
				WithNote("Lyth generates both methods for an enum that declares neither; one declared alone would not read what the generated other writes")
			continue
		}
		if unmarshal != nil || marshal != nil && decl.Backing == nil {
			continue
		}
		decl.TextMethods = true
		bytes := &types.Slice{Elem: types.Byte}
		recv := gotypes.NewVar(token.NoPos, c.shadow, "", decl.Shadow)
		ptr := gotypes.NewVar(token.NoPos, c.shadow, "", gotypes.NewPointer(decl.Shadow))
		var gen []*types.Method
		if decl.Backing == nil {
			gen = append(gen, &types.Method{Name: "MarshalText", GoName: "MarshalText", Sig: &types.Signature{Results: []types.Type{bytes}, Throws: true}, Pub: true, Span: decl.Span})
		}
		gen = append(gen, &types.Method{Name: "UnmarshalText", GoName: "UnmarshalText", Sig: &types.Signature{Params: []types.Type{bytes}, ParamNames: []string{"text"}, Throws: true}, PtrRecv: true, Pub: true, Span: decl.Span})
		for _, m := range gen {
			r := recv
			if m.PtrRecv {
				r = ptr
			}
			decl.Methods = append(decl.Methods, m)
			decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, m.GoName, types.LowerSignature(m.Sig, r)))
		}
	}
}

// entryConst is the constant value of an enum entry.
func entryConst(e *types.EnumEntry) constant.Value {
	if e.Decl.Backing != nil {
		return constant.MakeString(e.Wire)
	}
	return constant.MakeInt64(int64(e.Value))
}

// resolveDefined resolves the underlying type of type Name Underlying (ADR
// 0037). Structs and interfaces have their own declarations, which keep one
// spelling for each and give them Lyth's field syntax.
func (c *Checker) resolveDefined(decl *types.TypeDecl, d *syntax.TypeDefDecl) {
	t := c.resolveType(d.Type)
	switch {
	case t == types.Invalid:
	case t == types.Unit:
		c.errorf(diag.EInvalidUse, d.Type.Span(), "unit cannot be the underlying type of %s", decl.Name)
		t = types.Invalid
	case types.IsNullable(t):
		c.errorf(diag.EInvalidUse, d.Type.Span(), "a defined type cannot be nullable; declare %s over %s and write %s? where a value may be absent", decl.Name, types.Base(t), decl.Name)
		t = types.Invalid
	case types.IsInterface(t):
		c.errorf(diag.EUnsupported, d.Type.Span(), "a defined type cannot be an interface; declare interface %s { ... }, or an alias: type %s = %s", decl.Name, decl.Name, t)
		t = types.Invalid
	case types.IsStruct(t):
		c.errorf(diag.EUnsupported, d.Type.Span(), "a defined type cannot be a struct; declare struct %s { ... }, or an alias: type %s = %s", decl.Name, decl.Name, t)
		t = types.Invalid
	case types.EnumOf(t) != nil:
		c.errorf(diag.EUnsupported, d.Type.Span(), "a defined type cannot be an enum; declare enum %s { ... }, or an alias: type %s = %s", decl.Name, decl.Name, t)
		t = types.Invalid
	}
	decl.Underlying = t
}

// setDefinedUnderlying sets the shadow underlying type of a defined type,
// first that of a defined type it is declared over.
func (c *Checker) setDefinedUnderlying(decl *types.TypeDecl, visiting map[*types.TypeDecl]bool) {
	if decl.Shadow.Underlying() != nil {
		return
	}
	if visiting[decl] || decl.Underlying == nil || decl.Underlying == types.Invalid {
		if visiting[decl] {
			c.errorf(diag.EInvalidUse, decl.Span, "invalid recursive type %s: it is defined in terms of itself", decl.Name)
		}
		decl.Underlying = types.Invalid
		decl.Shadow.SetUnderlying(gotypes.Typ[gotypes.Invalid])
		return
	}
	visiting[decl] = true
	if n, ok := types.Base(decl.Underlying).(*types.Named); ok && n.Decl != nil && n.Decl.Kind == types.DefinedDecl {
		c.setDefinedUnderlying(n.Decl, visiting)
		if n.Decl.Underlying == types.Invalid {
			decl.Underlying = types.Invalid
		}
	}
	delete(visiting, decl)
	if decl.Shadow.Underlying() != nil {
		return // set through a cycle
	}
	if decl.Underlying == types.Invalid {
		decl.Shadow.SetUnderlying(gotypes.Typ[gotypes.Invalid])
		return
	}
	decl.Shadow.SetUnderlying(types.Lower(decl.Underlying).Underlying())
}

func (c *Checker) resolveStruct(decl *types.TypeDecl, d *syntax.StructDecl) {
	seen := map[string]source.Span{}
	goSeen := map[string]source.Span{}
	for _, sf := range d.Fields {
		if sf.Embedded && sf.Name == nil {
			c.errorf(diag.EInvalidUse, sf.Type.Span(), "only a type name can be embedded, as in Go").
				WithHint("give the field a name: name: Type")
			continue
		}
		if sf.Name == nil {
			continue
		}
		name := sf.Name.Name
		if name == "_" {
			if sf.Name.S.Len() > 0 {
				c.errorf(diag.EInvalidUse, sf.Name.S, "struct fields cannot be named _")
			}
			continue
		}
		if old, ok := seen[name]; ok {
			c.errorf(diag.EDuplicate, sf.Name.S, "field %s is already declared", name).WithRelated(old, "previous declaration")
			continue
		}
		seen[name] = sf.Name.S
		t := c.resolveType(sf.Type)
		if t == types.Unit {
			c.errorf(diag.EInvalidUse, sf.Type.Span(), "unit cannot be used as a field type")
			t = types.Invalid
		}
		f := &types.Field{Name: name, Type: t, Private: !Exported(name), Span: sf.Name.S, Embedded: sf.Embedded}
		f.GoName = GoName(name)
		if sf.Embedded && !c.embeddable(t, sf.Type.Span()) {
			t, f.Type = types.Invalid, types.Invalid
		}
		if old, ok := goSeen[f.GoName]; ok {
			c.errorf(diag.EDuplicate, sf.Name.S, "field %s would be named %s in Go, which another field already uses", name, f.GoName).
				WithRelated(old, "conflicting field")
			continue
		}
		goSeen[f.GoName] = sf.Name.S
		f.Tag = c.fieldTag(sf, f)
		decl.Fields = append(decl.Fields, f)
	}
	c.warnDuplicateKeys(decl)
}

// warnDuplicateKeys reports two fields of a struct with the same json key
// (IDs and Ids are both ids): encoding/json then drops both, silently, as
// go vet's structtag check says (W0008).
func (c *Checker) warnDuplicateKeys(decl *types.TypeDecl) {
	keys := map[string]*types.Field{}
	for _, f := range decl.Fields {
		if f.Private || f.Embedded {
			continue
		}
		value, ok := reflect.StructTag(f.Tag).Lookup("json")
		key, _, _ := strings.Cut(value, ",")
		if !ok || key == "" || value == "-" {
			continue
		}
		if old := keys[key]; old != nil {
			c.warnf(diag.WJSONKey, f.Span, "%s and %s have the same json key %q, so encoding/json leaves both out", old.Name, f.Name, key).
				WithRelated(old.Span, "%s declared here", old.Name).
				WithHint("give one of them another key with a tag, such as `json:\"%s2\"`", key)
			continue
		}
		keys[key] = f
	}
}

// embeddable reports whether t may be embedded in a struct, as Go allows: a
// type name, or a pointer to a type name that is not a pointer or an
// interface, possibly nullable (ADR 0043).
func (c *Checker) embeddable(t types.Type, span source.Span) bool {
	if t == types.Invalid {
		return true
	}
	b := types.Base(t)
	if p, ok := b.(*types.Pointer); ok {
		e := types.Base(p.Elem)
		if _, named := e.(*types.Named); !named || types.IsInterface(e) {
			c.errorf(diag.EInvalidUse, span, "%s cannot be embedded: an embedded pointer points to a type name that is not an interface", t)
			return false
		}
		// The Lyth types of this package are still being defined: ask the
		// Lyth declaration, and go/types only for a Go type.
		n := e.(*types.Named)
		var isPtr bool
		if n.Decl != nil {
			_, isPtr = types.Base(types.DefinedUnder(n)).(*types.Pointer)
		} else if n.Go != nil {
			_, isPtr = n.Go.Underlying().(*gotypes.Pointer)
		}
		if isPtr {
			c.errorf(diag.EInvalidUse, span, "%s cannot be embedded: an embedded pointer points to a type name that is not a pointer", t)
			return false
		}
		return true
	}
	switch b.(type) {
	case *types.Named:
		return true
	case *types.TypeParam:
		c.errorf(diag.EInvalidUse, span, "a type parameter cannot be embedded, as in Go")
		return false
	}
	c.errorf(diag.EInvalidUse, span, "only a type name can be embedded, as in Go, not %s", t).
		WithHint("give the field a name: name: %s", t)
	return false
}

// fieldTag computes the Go struct tag of an exported field (ADR 0013, ADR
// 0022, ADR 0036): a tag without a json key gets the automatic key, with
// omitzero for a nullable field, and a json key written without a name
// (json:",omitempty") gets the automatic name.
func (c *Checker) fieldTag(sf *syntax.Field, f *types.Field) string {
	tag := sf.Tag
	if sf.HasTag {
		if _, ok := reflect.StructTag(tag).Lookup("json"); !ok && !validTag(tag) {
			c.warnf(diag.WUnnecessary, sf.TagSpan, "struct tag is not in the conventional key:\"value\" format")
		}
	}
	if f.Private || f.Embedded {
		// An embedded struct without a tag is flattened into its outer
		// struct's JSON, as in Go (ADR 0043).
		return tag
	}
	if value, ok := reflect.StructTag(tag).Lookup("json"); ok {
		if name, opts, _ := strings.Cut(value, ","); name == "" && value != "" {
			// json:",omitempty" keeps the automatic name, which Go would
			// replace with the field name (NextCursor next to items). A T?
			// field also keeps omitzero unless the tag says how to omit it.
			c.warnSplitKey(sf, f)
			// Before ADR 0036 such a tag was used as written, so Go's key
			// was the field name.
			c.warnKeyChange(sf, f, f.GoName, value)
			key := JSONName(f.Name) + value
			if types.MayBeNull(f.Type) && !types.IsPlatform(f.Type) && !slices.ContainsFunc(strings.Split(opts, ","), func(o string) bool { return o == "omitempty" || o == "omitzero" }) {
				key += ",omitzero"
			}
			return strings.Replace(tag, `json:"`+value+`"`, `json:"`+key+`"`, 1)
		}
		return tag
	}
	c.warnSplitKey(sf, f)
	c.warnKeyChange(sf, f, jsonNameBefore0036(f.Name), "")
	key := JSONName(f.Name)
	if types.MayBeNull(f.Type) && !types.IsPlatform(f.Type) {
		key += ",omitzero" // T? may be absent: left out when null, in json v1 and v2
	}
	auto := "json:" + strconv.Quote(key)
	if tag == "" {
		return auto
	}
	return auto + " " + tag
}

// warnKeyChange reports a field whose json key differs from the key it had
// before ADR 0036 (old): IDs was iDs, and a tag without a name, such as
// json:",string", used the field name itself. The wire format changed under
// the field. It is a migration notice for the releases right after the
// change (W0009); opts are the tag's options, kept in the hint.
func (c *Checker) warnKeyChange(sf *syntax.Field, f *types.Field, old, opts string) {
	now := JSONName(f.Name)
	if old == now {
		return
	}
	c.warnf(diag.WJSONKeyChange, sf.Name.S, "the json key of %s is now %q; it was %q before Lyth 0.4.0", f.Name, now, old).
		WithHint("to keep the old key on the wire, write the tag `json:\"%s%s\"`", old, opts)
}

// jsonNameBefore0036 is JSONName before ADR 0036, kept unchanged for
// warnKeyChange: the leading uppercase run lowered, keeping its last letter
// when a lowercase letter follows (IDs -> iDs).
func jsonNameBefore0036(name string) string {
	rs := []rune(name)
	n := 0
	for n < len(rs) && unicode.IsUpper(rs[n]) {
		n++
	}
	if n > 1 && n < len(rs) && unicode.IsLower(rs[n]) {
		n-- // the last uppercase letter starts the next word
	}
	if n == 0 {
		n = 1
	}
	for i := 0; i < n && i < len(rs); i++ {
		rs[i] = unicode.ToLower(rs[i])
	}
	return string(rs)
}

// warnSplitKey reports a field whose automatic json key splits its leading
// run of capitals after one letter (ETag -> eTag, OAuthToken -> oAuthToken):
// the name may be one mixed-case word or two, so the key needs a tag (W0008).
func (c *Checker) warnSplitKey(sf *syntax.Field, f *types.Field) {
	if !JSONKeySplitsRun(f.Name) {
		return
	}
	key := JSONName(f.Name)
	run := leadingRun(f.Name)
	// Read as one word, the run and the lowercase letters after it: ETag ->
	// etag, OAuthToken -> oauthToken.
	end := len(run)
	for end < len(f.Name) {
		r, size := utf8.DecodeRuneInString(f.Name[end:])
		if !unicode.IsLower(r) {
			break
		}
		end += size
	}
	oneWord := strings.ToLower(f.Name[:end]) + f.Name[end:]
	c.warnf(diag.WJSONKey, sf.Name.S, "the automatic json key of %s is %q, which splits %s after its first letter", f.Name, key, run).
		WithHint("if %s is one word, write the tag `json:\"%s\"`; to keep %q, write `json:\"%s\"`", f.Name[:end], oneWord, key, key)
}

// commonInitialisms is Go's canonical list of initialisms (golint's
// commonInitialisms, the list Go style checkers enforce). JSONName uses it
// only to keep a listed run whole before a lowercase letter (IPv4 -> ipv4).
var commonInitialisms = map[string]bool{
	"ACL": true, "API": true, "ASCII": true, "CPU": true, "CSS": true, "DNS": true, "EOF": true,
	"GUID": true, "HTML": true, "HTTP": true, "HTTPS": true, "ID": true, "IP": true, "JSON": true,
	"LHS": true, "QPS": true, "RAM": true, "RHS": true, "RPC": true, "SLA": true, "SMTP": true,
	"SQL": true, "SSH": true, "TCP": true, "TLS": true, "TTL": true, "UDP": true, "UI": true,
	"UID": true, "UUID": true, "URI": true, "URL": true, "UTF8": true, "VM": true, "XML": true,
	"XMPP": true, "XSRF": true, "XSS": true,
}

// leadingRun returns the leading run of uppercase letters of name.
func leadingRun(name string) string {
	n := 0
	for n < len(name) {
		r, size := utf8.DecodeRuneInString(name[n:])
		if !unicode.IsUpper(r) {
			break
		}
		n += size
	}
	return name[:n]
}

// jsonFirstWord returns the length in bytes of the first word of an
// exported name, the part the automatic json key lowers (ADR 0036).
func jsonFirstWord(name string) int {
	r := leadingRun(name)
	n := len(r)
	letters := utf8.RuneCountInString(r)
	next, nextSize := utf8.DecodeRuneInString(name[n:])
	after, _ := utf8.DecodeRuneInString(name[min(n+nextSize, len(name)):])
	switch {
	case n == 0:
		_, size := utf8.DecodeRuneInString(name)
		return size
	case n == len(name) || unicode.IsDigit(next):
		return n // ID, URL, UTF8Name
	case next == 's' && letters >= 2 && (n+1 == len(name) || unicode.IsDigit(after) || unicode.IsUpper(after)):
		return n + 1 // IDs, URLs: the plural of an initialism
	case letters >= 2:
		_, lastSize := utf8.DecodeLastRuneInString(r)
		if commonInitialisms[r] && !commonInitialisms[r[:n-lastSize]] {
			return n // IPv4: a listed initialism keeps its letters
		}
		return n - lastSize // HTTPServer: the run's last letter starts a word
	}
	return n // Name
}

// JSONName is the automatic json key of an exported field, and the wire name
// of an enum entry: the name with its first word lowered (ADR 0036). ID ->
// id, UserID -> userID, IDs -> ids, HTTPServer -> httpServer, IPv4 -> ipv4,
// ETag -> eTag (an exception, see JSONKeySplitsRun).
func JSONName(name string) string {
	n := jsonFirstWord(name)
	return strings.ToLower(name[:n]) + name[n:]
}

// JSONKeySplitsRun reports whether the automatic key of name splits its
// leading run of capitals after one letter, as in ETag, OAuthToken, and
// XCoord: one mixed-case word or two, which a tag must say (W0008).
func JSONKeySplitsRun(name string) bool {
	r := leadingRun(name)
	_, first := utf8.DecodeRuneInString(r)
	return utf8.RuneCountInString(r) == 2 && jsonFirstWord(name) == first
}

// validTag reports whether tag follows the key:"value" convention.
func validTag(tag string) bool {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return false
		}
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return false
		}
		if _, err := strconv.Unquote(tag[:i+1]); err != nil {
			return false
		}
		tag = tag[i+1:]
	}
	return true
}

func (c *Checker) resolveInterface(decl *types.TypeDecl, d *syntax.InterfaceDecl) {
	seen := map[string]source.Span{}
	for _, m := range d.Methods {
		if old, ok := seen[m.Name.Name]; ok {
			c.errorf(diag.EDuplicate, m.Name.S, "method %s is already declared", m.Name.Name).WithRelated(old, "previous declaration")
			continue
		}
		seen[m.Name.Name] = m.Name.S
		for _, prm := range m.Params {
			if prm.Default != nil {
				c.errorf(diag.EUnsupported, prm.Default.Span(), "interface methods cannot have default values").
					WithNote("declare defaults on the functions that implement or call the method")
			}
		}
		sig := c.signature(m.Params, m.Result, m.Throws, nil)
		decl.Methods = append(decl.Methods, &types.Method{Name: m.Name.Name, GoName: GoName(m.Name.Name), Sig: sig, Pub: Exported(m.Name.Name), Span: m.Name.S})
	}
	for _, e := range d.Embeds {
		t := c.resolveType(e)
		if t != types.Invalid && !types.IsInterface(t) {
			c.errorf(diag.ETypeMismatch, e.Span(), "%s is not an interface and cannot be embedded in interface %s", t, decl.Name)
			continue
		}
		decl.Embeds = append(decl.Embeds, t)
	}
}

// signature resolves a function signature.
func (c *Checker) signature(params []*syntax.Param, result *syntax.ResultType, throws bool, names *[]string) *types.Signature {
	sig := &types.Signature{Throws: throws}
	for i, p := range params {
		t := c.resolveType(p.Type)
		if t == types.Unit {
			c.errorf(diag.EInvalidUse, p.Type.Span(), "unit cannot be used as a parameter type")
			t = types.Invalid
		}
		if p.Variadic {
			if i != len(params)-1 {
				c.errorf(diag.EInvalidUse, p.S, "only the last parameter can be variadic")
			} else {
				sig.Variadic = true
			}
			t = &types.Slice{Elem: t}
		}
		sig.Params = append(sig.Params, t)
		sig.ParamNames = append(sig.ParamNames, p.Name.Name)
	}
	if result != nil {
		for _, rt := range result.Types {
			t := c.resolveType(rt)
			if t == types.Unit {
				if len(result.Types) > 1 {
					c.errorf(diag.EInvalidUse, rt.Span(), "unit cannot appear in a result list")
				}
				continue
			}
			sig.Results = append(sig.Results, t)
		}
	}
	return sig
}

// ---------------------------------------------------------------------------
// Functions and methods

func (c *Checker) resolveFuncDecls() {
	// Plain functions.
	for _, fs := range c.pkg.Funcs {
		d := fs.Decl
		scope := c.scopeFor(d.S)
		var tparams []*types.TypeParam
		if len(d.TypeParams) > 0 {
			scope, tparams = c.typeParams(d.TypeParams, scope)
			fs.tscope = scope
		}
		c.withScope(scope, func() {
			fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
		})
		if len(tparams) > 0 {
			genericSignature(fs.Sig, tparams)
		}
		if fs.IsMain {
			if len(d.Params) > 0 || len(fs.Sig.Results) > 0 {
				c.errorf(diag.EInvalidUse, d.Name.S, "func main must have no parameters and no results").
					WithNote("read arguments with os.Args; report failure by declaring func main() throws")
			}
		}
		if fs.IsInit {
			switch {
			case len(d.TypeParams) > 0:
				c.errorf(diag.EInvalidUse, d.Name.S, "func init cannot have type parameters")
			case len(d.Params) > 0 || d.Result != nil || d.ExprBody != nil && fs.Sig != nil && len(fs.Sig.Results) > 0:
				c.errorf(diag.EInvalidUse, d.Name.S, "func init must have no parameters and no results")
			case d.Throws:
				c.errorf(diag.EInvalidUse, d.ThrowsSpan, "func init cannot throw: Go runs it before main, with no caller to return an error to").
					WithHint("handle the error in the body, or panic: f() catch (err) { panic(err) }")
			}
		}
		if fs.IsTest {
			c.checkTestFunc(fs)
		}
		if inferable(fs) {
			fs.infer = inferPending
		}
	}
	// Methods.
	for _, f := range c.pkg.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*syntax.FunDecl)
			if !ok || fd.Recv == nil {
				continue
			}
			c.withScope(c.fileScope[f], func() { c.resolveMethod(fd) })
		}
	}
	// Results inferred from bodies, now that every signature they may use
	// exists. Bodies referring to other inferred functions infer those first.
	for _, fs := range c.pkg.Funcs {
		c.ensureInferred(fs)
	}
	for _, fs := range c.pkg.Funcs {
		c.checkDefaults(fs)
	}
}

// typeParams declares the type parameters of a generic function in a new
// scope inside parent and returns the scope with the parameters. Each is a
// go/types type parameter of the shadow package, so instantiation and
// constraint satisfaction are Go's. A missing constraint is Any.
func (c *Checker) typeParams(tps []*syntax.TypeParam, parent *Scope) (*Scope, []*types.TypeParam) {
	out := make([]*types.TypeParam, len(tps))
	for i, tp := range tps {
		tn := gotypes.NewTypeName(token.NoPos, c.shadow, tp.Name.Name, nil)
		out[i] = &types.TypeParam{Go: gotypes.NewTypeParam(tn, nil), Name: tp.Name.Name}
	}
	return c.typeParamScope(tps, out, parent), out
}

// typeParamScope declares type parameters out, written as tps, in a new
// scope inside parent and resolves their constraints there.
func (c *Checker) typeParamScope(tps []*syntax.TypeParam, out []*types.TypeParam, parent *Scope) *Scope {
	scope := newScope(parent, BlockScope)
	for i, tp := range tps {
		sym := &TypeSym{name: tp.Name.Name, Type: out[i], Span: tp.Name.S, Pkg: c.pkg}
		c.declare(scope, sym, tp.Name.S)
		c.info.Defs[tp.Name] = sym
	}
	c.withScope(scope, func() {
		for i, tp := range tps {
			var constraint types.Type = types.Any
			if tp.Constraint != nil {
				save := c.inConstraint
				c.inConstraint = true
				t := c.resolveType(tp.Constraint)
				c.inConstraint = save
				switch {
				case t == types.Invalid:
				case !types.IsInterface(t) || types.IsNullable(t):
					c.errorf(diag.ETypeMismatch, tp.Constraint.Span(), "the constraint of %s must be an interface, not %s", tp.Name.Name, t).
						WithHint("constrain it with an interface such as Comparable, cmp.Ordered, or fmt.Stringer")
				default:
					constraint = types.Base(t)
				}
			}
			out[i].Constraint = constraint
			if c.undefined(constraint) {
				tp, gc := out[i].Go, types.Lower(constraint)
				c.pendingConstraints = append(c.pendingConstraints, func() { tp.SetConstraint(gc) })
			} else {
				out[i].Go.SetConstraint(types.Lower(constraint))
			}
		}
	})
	return scope
}

// genericSignature makes sig the signature of a generic function with type
// parameters tparams, with a go/types signature for instantiation.
func genericSignature(sig *types.Signature, tparams []*types.TypeParam) {
	sig.TypeParams = tparams
	gs := types.LowerSignature(sig, nil)
	gps := make([]*gotypes.TypeParam, len(tparams))
	for i, tp := range tparams {
		gps[i] = tp.Go
	}
	sig.GoSig = gotypes.NewSignatureType(nil, nil, gps, gs.Params(), gs.Results(), gs.Variadic())
}

// checkDefaults checks the default values of fs's parameters. A default is
// evaluated at each call site that leaves the parameter out, so it must be
// a simple expression that does not refer to other parameters, and a pub
// function's defaults may only use public names (callers in other packages
// evaluate them).
func (c *Checker) checkDefaults(fs *FuncSym) {
	d := fs.Decl
	if d == nil || fs.Sig == nil {
		return
	}
	if len(fs.Sig.TypeParams) > 0 {
		for _, p := range d.Params {
			if p.Default != nil {
				c.errorf(diag.EUnsupported, p.Default.Span(), "generic functions cannot have default values yet")
				return
			}
		}
	}
	params := map[string]bool{}
	for _, p := range d.Params {
		params[p.Name.Name] = true
	}
	for i, p := range d.Params {
		if p.Default == nil {
			continue
		}
		if p.Variadic {
			c.errorf(diag.EInvalidUse, p.Default.Span(), "a variadic parameter cannot have a default value; leaving it out passes no values")
			continue
		}
		if bad := complexDefault(p.Default); bad != nil {
			c.errorf(diag.EInvalidUse, bad.Span(), "a default value must be a simple expression, without ?, catch, if, when, ?:, ?., !!, or casts").
				WithNote("defaults are evaluated at every call site that leaves the parameter out")
			continue
		}
		if id := paramReference(p.Default, params); id != nil {
			c.errorf(diag.EInvalidUse, id.S, "a default value cannot refer to parameter %s", id.Name).
				WithNote("defaults are evaluated at the call site, before the call")
			continue
		}
		saveScope, saveFn := c.scope, c.fn
		c.scope = newScope(c.scopeFor(d.S), FuncScope)
		c.fn = c.initCtx(d.S)
		c.fn.name = "default value"
		c.scope.fn = c.fn
		x := c.value(p.Default, fs.Sig.Params[i])
		c.assign(x, fs.Sig.Params[i], "default value of "+p.Name.Name)
		c.scope, c.fn = saveScope, saveFn
		if fs.Pub {
			syntax.Walk(p.Default, func(n syntax.Node) {
				id, ok := n.(*syntax.Ident)
				if !ok {
					return
				}
				if name, private := c.privateDecl(c.info.Uses[id]); private {
					c.errorf(diag.EPrivate, id.S, "the default value of exported %s uses unexported %s", fs.name, name).
						WithNote("callers in other packages evaluate default values, so they can only use exported names")
				}
			})
		}
	}
}

// privateDecl reports whether sym is an unexported declaration of this package.
func (c *Checker) privateDecl(sym any) (string, bool) {
	switch s := sym.(type) {
	case *FuncSym:
		return s.name, !s.Pub && s.Pkg == c.pkg
	case *ConstSym:
		return s.name, !s.Pub && s.Pkg == c.pkg
	case *VarSym:
		return s.name, s.Kind == GlobalVar && !s.Pub && s.Pkg == c.pkg
	case *TypeSym:
		return s.name, s.Decl != nil && !s.Decl.Pub && s.Pkg == c.pkg
	}
	return "", false
}

// complexDefault returns the first part of a default value that would need
// statements at the call site, or nil.
func complexDefault(e syntax.Expr) syntax.Node {
	var bad syntax.Node
	syntax.Walk(e, func(n syntax.Node) {
		if bad != nil {
			return
		}
		switch x := n.(type) {
		case *syntax.TryExpr, *syntax.CatchExpr, *syntax.IfExpr, *syntax.WhenExpr, *syntax.ElvisExpr,
			*syntax.NotNullExpr, *syntax.IsExpr, *syntax.InExpr, *syntax.JumpExpr:
			bad = n
		case *syntax.AsExpr:
			bad = n
		case *syntax.SelectorExpr:
			if x.Safe {
				bad = n
			}
		}
	})
	return bad
}

// paramReference returns an identifier in e naming one of params, outside
// lambdas (whose own parameters may reuse the names).
func paramReference(e syntax.Expr, params map[string]bool) *syntax.Ident {
	var found *syntax.Ident
	var visit func(syntax.Node)
	visit = func(n syntax.Node) {
		if found != nil {
			return
		}
		switch x := n.(type) {
		case *syntax.LambdaExpr:
			return
		case *syntax.Ident:
			if params[x.Name] {
				found = x
			}
		case *syntax.SelectorExpr:
			visit(x.X) // x.Sel names a member, not a parameter
			return
		}
		syntax.Children(n, visit)
	}
	visit(e)
	return found
}

// inferable reports whether fs's result type comes from its body: a
// non-throwing function or method with an expression body and no declared
// result. Only unexported functions may rely on it (an exported function's
// result is part of its API, so leaving it out is an error), but exported ones are inferred
// too, so that their callers see the type instead of follow-on errors.
func inferable(fs *FuncSym) bool {
	d := fs.Decl
	return d != nil && d.ExprBody != nil && d.Result == nil && !d.Throws && !fs.IsMain && !fs.IsInit && !fs.IsTest && len(d.TypeParams) == 0
}

// ensureInferred infers fs's result type if it is still pending, by checking
// its body now.
func (c *Checker) ensureInferred(fs *FuncSym) {
	switch fs.infer {
	case inferPending:
		fs.infer = inferRunning
		c.funcBody(fs)
		fs.bodyChecked = true
		fs.infer = inferDone
		if fs.Recv != nil {
			c.addShadowMethod(fs)
		}
	case inferRunning:
		if !fs.inferCycle {
			fs.inferCycle = true
			c.errorf(diag.ECannotInfer, fs.Span, "cannot infer the result type of %s: it depends on itself", fs.name).
				WithHint("declare it: func %s(...): Type = ...", fs.name)
		}
	}
}

// checkTestFunc checks the signature go test requires of fs. Tests,
// benchmarks, and fuzz targets may throw: the generated wrapper reports the
// error with Fatal.
func (c *Checker) checkTestFunc(fs *FuncSym) {
	var param, form string
	throwsOK := true
	switch name := fs.name; {
	case name == "TestMain":
		param, form, throwsOK = "*testing.M", "(m: *testing.M)", false
	case strings.HasPrefix(name, "Test"):
		param, form = "*testing.T", "(t: *testing.T)"
	case strings.HasPrefix(name, "Benchmark"):
		param, form = "*testing.B", "(b: *testing.B)"
	case strings.HasPrefix(name, "Fuzz"):
		param, form = "*testing.F", "(f: *testing.F)"
	default:
		form, throwsOK = "()", false // examples
	}
	sig := fs.Sig
	ok := len(sig.Results) == 0 && (throwsOK || !sig.Throws)
	if param == "" {
		ok = ok && len(sig.Params) == 0
	} else {
		ok = ok && len(sig.Params) == 1 && types.Lower(sig.Params[0]).String() == param
	}
	if !ok {
		d := c.errorf(diag.EInvalidUse, fs.Span, "%s is run by go test, so it must be declared func %s%s", fs.name, fs.name, form)
		if throwsOK {
			d.WithNote("it may throw: a thrown error fails the test")
		}
	}
}

func (c *Checker) resolveMethod(d *syntax.FunDecl) {
	// A struct or enum of this package gets a method; any other receiver
	// type an extension function.
	var ts *TypeSym
	if d.Recv.Name != nil {
		ts, _ = c.lookup(d.Recv.Name.Name).(*TypeSym)
	}
	if ts != nil && ts.Alias != nil && len(d.Recv.TypeArgs) == 0 {
		// type Point = Pt: a method of Point is a method of Pt, as in Go.
		if n, ok := c.aliasType(ts).(*types.Named); ok && n.Decl != nil && len(n.Args) == 0 {
			if target, ok := c.pkgScope.LookupLocal(n.Decl.Name).(*TypeSym); ok && target.Decl == n.Decl {
				ts = target
			}
		}
	}
	if ts == nil || ts.Decl == nil || ts.Pkg != c.pkg || ts.Decl.Kind == types.InterfaceDecl {
		c.resolveExtension(d)
		return
	}
	recvName := d.Recv.Name.Name
	c.info.Uses[d.Recv.Name] = ts
	decl := ts.Decl
	if decl.Kind == types.DefinedDecl {
		if _, ok := types.Base(decl.Underlying).(*types.Pointer); ok {
			c.errorf(diag.EInvalidUse, d.Recv.S, "%s is a pointer type, which cannot have methods (Go's rule)", recvName).
				WithHint("declare the methods on the type it points to")
			return
		}
	}
	if len(d.TypeParams) > 0 {
		span := d.TypeParams[0].S.Join(d.TypeParams[len(d.TypeParams)-1].S)
		if len(decl.TypeParams) > 0 {
			c.errorf(diag.EInvalidUse, span, "the receiver %s[...] already names the type parameters of %s", recvName, recvName).
				WithHint("remove the type parameter list: func %s[%s].%s(...)", recvName, typeParamNames(decl.TypeParams), d.Name.Name)
		} else {
			c.errorf(diag.EUnsupported, span, "a method cannot declare its own type parameters: Go has no generic methods").
				WithHint("declare a generic function that takes the %s as a parameter", recvName)
		}
	}
	// A generic type's methods name its type parameters: func Page[T].First().
	var tscope *Scope
	var recvArgs []types.Type
	switch {
	case len(decl.TypeParams) != len(d.Recv.TypeArgs) && len(decl.TypeParams) > 0:
		c.errorf(diag.ETypeArgs, d.Recv.S, "%s is generic: declare the method as func %s[%s].%s(...)", recvName, recvName, typeParamNames(decl.TypeParams), d.Name.Name)
		return
	case len(d.Recv.TypeArgs) > 0 && len(decl.TypeParams) == 0:
		c.errorf(diag.ETypeArgs, d.Recv.S, "%s is not generic", recvName)
		return
	case len(decl.TypeParams) > 0:
		tscope = newScope(c.scope, BlockScope)
		for i, ta := range d.Recv.TypeArgs {
			nt, ok := ta.(*syntax.NamedType)
			if !ok || nt.Pkg != nil || len(nt.Args) > 0 {
				c.errorf(diag.ETypeArgs, ta.Span(), "the receiver names the type parameters of %s; write a name such as %s", recvName, decl.TypeParams[i].Name)
				return
			}
			sym := &TypeSym{name: nt.Name.Name, Type: decl.TypeParams[i], Span: nt.Name.S, Pkg: c.pkg}
			c.declare(tscope, sym, nt.Name.S)
			c.info.Defs[nt.Name] = sym
			recvArgs = append(recvArgs, decl.TypeParams[i])
		}
	}
	name := d.Name.Name
	pub := Exported(name)
	fs := &FuncSym{name: name, Pub: pub, Pkg: c.pkg, Decl: d, Recv: decl, RecvArgs: recvArgs, PtrRecv: d.Recv.Pointer, Span: d.Name.S, tscope: tscope}
	fs.GoName = GoName(name)
	if tscope != nil {
		c.withScope(tscope, func() { fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil) })
	} else {
		fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
	}
	if old := decl.LookupMethod(name); old != nil {
		if decl.Kind == types.EnumDecl && old.Sym == nil {
			c.errorf(diag.EDuplicate, d.Name.S, "enum %s already has a %s method: it returns the entry's name", recvName, name).
				WithRelated(old.Span, "generated for this enum")
			return
		}
		c.errorf(diag.EDuplicate, d.Name.S, "method %s.%s is already declared", recvName, name).WithRelated(old.Span, "previous declaration")
		return
	}
	if f := decl.LookupField(name); f != nil {
		c.errorf(diag.EDuplicate, d.Name.S, "%s has both a field and a method named %s", recvName, name).WithRelated(f.Span, "field declared here")
		return
	}
	for _, f := range decl.Fields {
		if f.GoName == fs.GoName {
			c.errorf(diag.EDuplicate, d.Name.S, "method %s would be named %s in Go, which conflicts with field %s", name, fs.GoName, f.Name).WithRelated(f.Span, "field declared here")
			return
		}
	}
	for _, m := range decl.Methods {
		if m.GoName == fs.GoName {
			c.errorf(diag.EDuplicate, d.Name.S, "method %s would be named %s in Go, which conflicts with method %s", name, fs.GoName, m.Name).WithRelated(m.Span, "method declared here")
			return
		}
	}
	m := &types.Method{Name: name, GoName: fs.GoName, Sig: fs.Sig, PtrRecv: d.Recv.Pointer, Pub: pub, Span: d.Name.S, Sym: fs}
	decl.Methods = append(decl.Methods, m)
	if inferable(fs) {
		fs.infer = inferPending // its shadow method waits for the result type
	} else {
		c.addShadowMethod(fs)
	}
	c.pkg.Funcs = append(c.pkg.Funcs, fs)
}

// resolveExtension declares an extension function: a function on a type that
// is not a struct or enum of this package (String, []T, Map<K, V>, a Go or
// interface type), called like a method and lowered to a Go function that
// takes the receiver as its first parameter. Its body reaches the receiver
// through this.
func (c *Checker) resolveExtension(d *syntax.FunDecl) {
	scope := c.scope
	var tparams []*types.TypeParam
	if len(d.TypeParams) > 0 {
		scope, tparams = c.typeParams(d.TypeParams, scope)
	}
	name := d.Name.Name
	fs := &FuncSym{name: name, Pub: Exported(name), Pkg: c.pkg, Decl: d, Span: d.Name.S}
	fs.GoName = GoName(name)
	if tparams != nil {
		fs.tscope = scope
	}
	var recv types.Type
	c.withScope(scope, func() {
		recv = c.resolveType(d.Recv.Type)
		if d.Recv.Pointer && recv != types.Invalid {
			recv = &types.Pointer{Elem: recv}
		}
		fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
	})
	switch {
	case recv == types.Invalid:
		recv = nil
	case types.IsNullable(recv):
		c.errorf(diag.EUnsupported, d.Recv.Type.Span(), "an extension function cannot have a nullable receiver yet")
		recv = nil
	default:
		if _, bare := types.Base(recv).(*types.TypeParam); bare {
			c.errorf(diag.EInvalidUse, d.Recv.Type.Span(), "an extension's receiver cannot be a bare type parameter: it would apply to every type")
			recv = nil
		}
	}
	fs.ExtRecv = recv
	if recv == nil {
		fs.ExtRecv = types.Invalid
	}
	if tparams != nil {
		// The go/types signature of the Go function, receiver first.
		full := &types.Signature{Params: append([]types.Type{fs.ExtRecv}, fs.Sig.Params...), Variadic: fs.Sig.Variadic, Results: fs.Sig.Results, Throws: fs.Sig.Throws}
		genericSignature(full, tparams)
		fs.Sig.TypeParams, fs.Sig.GoSig = tparams, full.GoSig
	}
	if old := c.pkg.Extensions[name]; old != nil {
		c.errorf(diag.EDuplicate, d.Name.S, "extension %s is already declared for %s; Go has one function per name", name, old.ExtRecv).
			WithRelated(old.Span, "previous declaration")
	} else if recv != nil {
		c.pkg.Extensions[name] = fs
		c.claimGoName(fs.GoName, d.Name.S, name)
	}
	if inferable(fs) {
		fs.infer = inferPending
	}
	c.pkg.Funcs = append(c.pkg.Funcs, fs)
}

// matchReceiver reports whether an extension with receiver type recv (in
// terms of type parameters tps) applies to a value of type t, and binds the
// type parameters. A pointer receiver applies to a value through its address
// and a value receiver to a pointer through it; an interface receiver applies
// to the types that implement it.
func (c *Checker) matchReceiver(recv types.Type, tps []*types.TypeParam, t types.Type) (bindings, *ExtCall, bool) {
	t = types.DefaultType(types.Base(t)) // "hi".Shout() on String
	try := func(recv, t types.Type) (bindings, bool) {
		bind := bindings{}
		if len(tps) > 0 {
			unify(recv, t, bind)
		}
		if types.Identical(subst(recv, bind), t) {
			return bind, true
		}
		if len(tps) == 0 && types.IsInterface(recv) && !types.IsInterface(t) && c.implements(t, recv) {
			return bind, true
		}
		return nil, false
	}
	if bind, ok := try(recv, t); ok {
		return bind, &ExtCall{}, true
	}
	if p, ok := recv.(*types.Pointer); ok {
		if bind, ok := try(p.Elem, t); ok {
			return bind, &ExtCall{Addr: true}, true
		}
	}
	if p, ok := t.(*types.Pointer); ok {
		if _, recvPtr := recv.(*types.Pointer); !recvPtr {
			if bind, ok := try(recv, types.Base(p.Elem)); ok {
				return bind, &ExtCall{Deref: true}, true
			}
		}
	}
	switch recv.(type) {
	case *types.Slice, *types.Map:
		// sort.StringSlice and url.Values are a []string and a map, which a
		// []T or map[K]V parameter accepts in Go too.
		if u := collectionOf(t); u != t {
			if bind, ok := try(recv, u); ok {
				return bind, &ExtCall{}, true
			}
		}
	}
	return nil, nil, false
}

// findExtension finds the extension function name for a receiver of type t:
// declared in this package or exported by a Lyth package the file imports.
// Two applicable extensions of different packages are ambiguous, which it
// reports (reported is then true).
func (c *Checker) findExtension(t types.Type, name string, id *syntax.Ident) (found *ExtCall, reported bool) {
	type candidate struct {
		fs  *FuncSym
		pkg *Package
		ps  *PkgSym
	}
	var cands []candidate
	if fs := c.pkg.Extensions[name]; fs != nil {
		cands = append(cands, candidate{fs, c.pkg, nil})
	}
	for s := c.scope; s != nil; s = s.parent {
		if s.kind != FileScope {
			continue
		}
		for _, sym := range s.order {
			if ps, ok := sym.(*PkgSym); ok && ps.Pkg != nil && ps.Pkg.Lyth && ps.Pkg != c.pkg {
				if fs := ps.Pkg.Extensions[name]; fs != nil && fs.Pub {
					cands = append(cands, candidate{fs, ps.Pkg, ps})
				}
			}
		}
	}
	for _, cand := range cands {
		bind, ext, ok := c.matchReceiver(cand.fs.ExtRecv, cand.fs.Sig.TypeParams, t)
		if !ok {
			continue
		}
		if cand.ps != nil {
			cand.ps.Used = true
		}
		if found != nil {
			c.errorf(diag.EDuplicate, id.S, "%s is ambiguous: packages %s and %s both declare it for %s", name, found.Pkg.Name, cand.pkg.Name, types.DefaultType(types.Base(t)))
			reported = true
			continue
		}
		ext.Func, ext.Pkg, ext.Bind = cand.fs, cand.pkg, bind
		found = ext
	}
	if reported {
		return nil, true
	}
	return found, false
}

// addShadowMethod declares method fs on its type in the shadow go/types
// package, for interface satisfaction and validation.
func (c *Checker) addShadowMethod(fs *FuncSym) {
	decl := fs.Recv
	sig := fs.Sig
	var recvType gotypes.Type = decl.Shadow
	var rtps []*gotypes.TypeParam
	if len(decl.TypeParams) > 0 {
		// Go binds the type parameters of a method's receiver anew:
		// func (p Page[T]) First() T.
		bind := bindings{}
		targs := make([]gotypes.Type, len(decl.TypeParams))
		for i, tp := range decl.TypeParams {
			fresh := gotypes.NewTypeParam(gotypes.NewTypeName(token.NoPos, c.shadow, tp.Name, nil), tp.Go.Constraint())
			rtps = append(rtps, fresh)
			targs[i] = fresh
			bind[tp.Go] = &types.TypeParam{Go: fresh, Name: tp.Name, Constraint: tp.Constraint}
		}
		inst, err := gotypes.Instantiate(nil, decl.Shadow, targs, false)
		if err != nil {
			return
		}
		recvType = inst
		sig = substSig(sig, bind)
	}
	if fs.PtrRecv {
		recvType = gotypes.NewPointer(recvType)
	}
	recvVar := gotypes.NewVar(token.NoPos, c.shadow, "this", recvType)
	gs := types.LowerSignature(sig, recvVar)
	if rtps != nil {
		gs = gotypes.NewSignatureType(recvVar, rtps, nil, gs.Params(), gs.Results(), gs.Variadic())
	}
	decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, fs.GoName, gs))
}

// ---------------------------------------------------------------------------
// Globals and constants

func (c *Checker) resolveGlobals() {
	for _, g := range c.pkg.Globals {
		switch g := g.(type) {
		case *VarSym:
			c.globalType(g)
		case *ConstSym:
			c.constValue(g)
		}
	}
}

// initCtx returns a function context for package-level initializers.
func (c *Checker) initCtx(span source.Span) *funcCtx {
	return &funcCtx{name: "package initializer", declSpan: span}
}

// globalType resolves a global's type, checking its initializer on demand.
func (c *Checker) globalType(v *VarSym) types.Type {
	switch v.state {
	case 2:
		return v.Type
	case 1:
		c.errorf(diag.EInvalidUse, v.Span, "initialization cycle: %s refers to itself", v.name)
		v.Type = types.Invalid
		return v.Type
	}
	v.state = 1
	d := v.Decl
	saveScope, saveFn := c.scope, c.fn
	c.scope = newScope(c.scopeFor(d.S), FuncScope)
	c.fn = c.initCtx(d.S)
	c.scope.fn = c.fn
	var want types.Type
	if d.Type != nil {
		want = c.resolveType(d.Type)
	}
	if d.Value != nil {
		x := c.expr(d.Value, want)
		if x.mode == ModeNoValue {
			c.errorf(diag.EInvalidUse, d.Value.Span(), "%s does not produce a value", d.Value.Span().Short())
			x = invalid(d.Value)
		} else {
			x = c.singleValue(x)
		}
		if want != nil {
			c.assign(x, want, "initializer of "+v.name)
			v.Type = want
		} else {
			v.Type = c.defaultValueType(x, "initializer of "+v.name)
		}
	} else if want != nil {
		v.Type = want
		if want != types.Invalid && !types.Zeroable(want) && !hasEmbed(d) {
			c.errorf(diag.ENoZeroValue, d.S, "%s of type %s needs an initializer: %s", v.name, want, types.NoZeroReason(want)).
				WithHint("initialize it, or declare it nullable: var %s: %s = null", v.name, types.MakeNullable(want))
		}
	} else {
		v.Type = types.Invalid
	}
	c.varDirectives(v)
	c.scope, c.fn = saveScope, saveFn
	v.state = 2
	return v.Type
}

func hasEmbed(d *syntax.PropDecl) bool {
	for _, dir := range d.Directives {
		if dir.Name() == "embed" {
			return true
		}
	}
	return false
}

// varDirectives checks the directives of a package-level var: //go:embed
// fills a string, []byte, or embed.FS variable with files of the package
// directory when the program is built, as in Go.
func (c *Checker) varDirectives(v *VarSym) {
	d := v.Decl
	for _, dir := range d.Directives {
		if dir.Name() != "embed" {
			c.errorf(diag.EInvalidUse, dir.S, "//go:%s does not apply to a variable", dir.Name()).
				WithHint("the directive a package-level var takes is //go:embed")
			continue
		}
		if dir.Args() == "" {
			c.errorf(diag.EInvalidUse, dir.S, "//go:embed needs a file pattern: //go:embed config.json")
		}
	}
	if !hasEmbed(d) {
		return
	}
	if d.Value != nil {
		typ := "string"
		if d.Type != nil {
			typ = d.Type.Span().Text()
		}
		c.errorf(diag.EInvalidUse, d.Value.Span(), "a //go:embed variable has no initializer: the build fills it with the files").
			WithEdit(source.Span{File: d.S.File, Start: d.Name.S.End, End: d.Value.Span().End}, ": "+typ, "write var %s: %s", v.name, typ)
		return
	}
	if v.Type != types.Invalid && !isEmbedType(v.Type) {
		c.errorf(diag.ETypeMismatch, d.Type.Span(), "a //go:embed variable has type string, []byte, or embed.FS, not %s", v.Type)
	}
	// The generated file imports embed; a Lyth import of it is used.
	for _, sym := range c.scopeFor(d.S).Symbols() {
		if p, ok := sym.(*PkgSym); ok && p.Pkg != nil && p.Pkg.Path == "embed" {
			p.Used = true
		}
	}
}

func isEmbedType(t types.Type) bool {
	g := types.Lower(t)
	if b, ok := g.(*gotypes.Basic); ok && b.Kind() == gotypes.String {
		return true
	}
	if s, ok := g.(*gotypes.Slice); ok {
		b, ok := s.Elem().(*gotypes.Basic)
		return ok && b.Kind() == gotypes.Byte
	}
	if n, ok := gotypes.Unalias(g).(*gotypes.Named); ok {
		obj := n.Obj()
		return obj.Pkg() != nil && obj.Pkg().Path() == "embed" && obj.Name() == "FS"
	}
	return false
}

// funcDirectives checks the directives of a function. Those that tell the
// Go compiler how to compile it are copied to the generated declaration.
func (c *Checker) funcDirectives(d *syntax.FunDecl) {
	for _, dir := range d.Directives {
		switch name := dir.Name(); name {
		case "noinline", "nosplit", "norace", "nocheckptr":
			if dir.Args() != "" {
				c.errorf(diag.EInvalidUse, dir.S, "//go:%s takes no arguments", name)
			}
		case "embed":
			c.errorf(diag.EInvalidUse, dir.S, "//go:embed applies to a package-level var, not to a function").
				WithHint("declare the variable: var content: string")
		default:
			c.errorf(diag.EUnsupported, dir.S, "//go:%s is not supported in Lyth", name).
				WithHint("the directives a function takes are //go:noinline, //go:nosplit, //go:norace, and //go:nocheckptr")
		}
	}
}

// localConst declares a constant in a function body. Its value is checked
// in the enclosing scope, so it may refer to earlier local constants.
func (c *Checker) localConst(d *syntax.ConstDecl) {
	k := &ConstSym{name: d.Name.Name, Span: d.Name.S, GoName: d.Name.Name, Decl: d}
	c.constInit(k)
	k.state = 2
	c.info.Defs[d.Name] = k
	if d.Name.Name == "_" {
		return
	}
	if old := c.scope.LookupLocal(d.Name.Name); old != nil {
		c.errorf(diag.EDuplicate, d.Name.S, "%s is already declared in this scope", d.Name.Name).
			WithRelated(old.DeclSpan(), "previous declaration")
		return
	}
	c.scope.Insert(k)
}

// constValue resolves a constant's type and value.
func (c *Checker) constValue(k *ConstSym) {
	switch k.state {
	case 2:
		return
	case 1:
		c.errorf(diag.EInvalidUse, k.Span, "constant %s refers to itself", k.name)
		k.Type = types.Invalid
		return
	}
	k.state = 1
	d := k.Decl
	saveScope, saveFn := c.scope, c.fn
	c.scope = newScope(c.scopeFor(d.S), FuncScope)
	c.fn = c.initCtx(d.S)
	c.constInit(k)
	c.scope, c.fn = saveScope, saveFn
	k.state = 2
}

// constInit checks a constant's initializer in the current scope and sets
// its type and value.
func (c *Checker) constInit(k *ConstSym) {
	d := k.Decl
	var want types.Type
	if d.Type != nil {
		want = c.resolveType(d.Type)
	}
	x := c.expr(d.Value, want)
	switch {
	case x.mode == ModeInvalid:
		k.Type = types.Invalid
	case x.val == nil:
		dg := c.errorf(diag.EInvalidUse, d.Value.Span(), "const initializer must be a constant expression")
		if k.Pkg == nil {
			dg.WithHint("use '%s := ...' for values computed at run time", k.name)
		} else {
			dg.WithHint("use 'var %s = ...' for values computed at run time", k.name)
		}
		k.Type = types.Invalid
	default:
		if want != nil {
			if b := types.BasicOf(want); b == nil || types.MayBeNull(want) {
				c.errorf(diag.ETypeMismatch, d.Type.Span(), "constants must have a basic type, not %s", want)
				want = types.Invalid
			} else {
				c.assign(x, want, "constant "+k.name)
				x.val = c.convertConst(x.val, want)
			}
			k.Type = want
		} else {
			k.Type = x.typ
		}
		k.Val = x.val
	}
}

func (c *Checker) convertConst(v constant.Value, t types.Type) constant.Value {
	switch {
	case types.IsInteger(t):
		return constant.ToInt(v)
	case types.IsFloat(t):
		return constant.ToFloat(v)
	}
	return v
}

// ---------------------------------------------------------------------------
// Bodies

func (c *Checker) checkBodies() {
	for _, fs := range c.pkg.Funcs {
		if fs.Sig == nil || fs.Decl == nil || fs.bodyChecked {
			continue
		}
		c.funcBody(fs)
	}
}

func (c *Checker) funcBody(sym *FuncSym) {
	d := sym.Decl
	parent := c.scopeFor(d.S)
	if sym.tscope != nil {
		parent = sym.tscope
	}
	scope := newScope(parent, FuncScope)
	fn := &funcCtx{sig: sym.Sig, name: sym.name, isMain: sym.IsMain, declSpan: d.Name.S, throwsSpan: d.ThrowsSpan}
	fn.fallible, fn.rawTuple = fallibleResults(sym.Sig)
	scope.fn = fn
	info := &FuncInfo{Sym: sym}
	if sym.ExtRecv != nil {
		rv := &VarSym{name: "self", Type: sym.ExtRecv, Kind: ReceiverVar, Span: d.Recv.S, Implicit: true, Used: true}
		scope.Insert(rv)
		fn.recv = rv // members through this; there is no implicit member scope
		info.Recv = rv
	}
	if sym.Recv != nil {
		var rt types.Type = &types.Named{Decl: sym.Recv, Args: sym.RecvArgs}
		if sym.PtrRecv {
			rt = &types.Pointer{Elem: rt}
		}
		rv := &VarSym{name: "self", Type: rt, Kind: ReceiverVar, Span: d.Recv.S, Implicit: true, Used: true}
		scope.Insert(rv)
		fn.recv, fn.recvDecl = rv, sym.Recv
		info.Recv = rv
	}
	for i, p := range d.Params {
		pv := &VarSym{name: p.Name.Name, Type: sym.Sig.Params[i], Kind: ParamVar, Span: p.Name.S, Used: true, owner: fn}
		if p.Name.Name != "_" {
			c.declare(scope, pv, p.Name.S)
		}
		c.info.Defs[p.Name] = pv
		info.Params = append(info.Params, pv)
	}
	var params []string
	for _, p := range d.Params {
		params = append(params, p.Name.Name)
	}
	if d.Body != nil {
		fn.reassigned = reassignedNames(params, d.Body)
	} else if d.ExprBody != nil {
		fn.reassigned = reassignedNames(params, d.ExprBody)
	}
	saveScope, saveFn := c.scope, c.fn
	c.scope, c.fn = scope, fn
	if d.Body != nil {
		terminates := c.stmtList(d.Body.Stmts)
		if len(sym.Sig.Results) > 0 && !terminates {
			c.errorf(diag.EMissingReturn, d.Body.Rbrace, "missing return in %s", sym.name).
				WithRelated(d.Result.S, "declared to return %s", resultString(sym.Sig))
		}
	} else if d.ExprBody != nil {
		if sym.infer == inferRunning {
			c.inferBody(d.ExprBody, sym)
		} else {
			c.exprBody(d.ExprBody, sym)
		}
	}
	info.Unused = c.finishLocals(fn)
	c.scope, c.fn = saveScope, saveFn
	c.info.Funcs[d] = info
}

// inferBody checks the body of a function whose result type it infers.
func (c *Checker) inferBody(e syntax.Expr, sym *FuncSym) {
	x := c.expr(e, nil)
	setResult := func(t types.Type) {
		if !sym.inferCycle {
			sym.Sig.Results = []types.Type{t}
		}
	}
	switch {
	case x.mode == ModeInvalid:
		setResult(types.Invalid)
	case x.mode == ModeNoValue || x.typ == types.Unit || isNothing(x.typ):
		// no result
	default:
		switch t := x.typ.(type) {
		case *types.Result:
			c.errorf(diag.ECannotInfer, e.Span(), "%s can fail, so it declares its result type with throws", sym.name).
				WithHint("write func %s(...): %s throws = ...", sym.name, resultHint(t))
			setResult(types.Invalid)
		case *types.Tuple:
			c.errorf(diag.ECannotInfer, e.Span(), "%s returns several values; declare them: func %s(...): %s = ...", sym.name, sym.name, t)
			setResult(types.Invalid)
		default:
			rt := c.defaultValueType(x, "the result of "+sym.name)
			if sym.Pub && rt != types.Invalid {
				c.errorf(diag.ECannotInfer, e.Span(), "declare the result type of this expression-bodied function").
					WithHint("write func %s(...): %s = ...", sym.name, rt).
					WithNote("an exported function's result type is part of its API; unexported functions may leave it out")
			}
			setResult(rt)
		}
	}
}

func resultHint(r *types.Result) string {
	switch len(r.Success) {
	case 0:
		return "unit"
	case 1:
		return r.Success[0].String()
	}
	return (&types.Tuple{Elems: r.Success}).String()
}

// exprBody checks `= expr` function bodies.
func (c *Checker) exprBody(e syntax.Expr, sym *FuncSym) {
	sig := sym.Sig
	switch len(sig.Results) {
	case 0:
		x := c.expr(e, nil)
		if c.passThrough(x, nil) {
			return
		}
		if x.mode == ModeValue && !isNothing(x.typ) && x.typ != types.Invalid {
			if r, ok := x.typ.(*types.Result); ok && len(r.Success) == 0 {
				if call, ok := syntax.Unparen(e).(*syntax.CallExpr); ok {
					if !c.errorIgnorable(call) {
						c.unhandledError(call, r)
					}
					break
				}
			}
			d := c.errorf(diag.ECannotInfer, e.Span(), "declare the result type of this expression-bodied function").
				WithHint("write func %s(...): %s = ...", sym.name, c.defaultValueType(x, ""))
			if sym.Pub {
				d.WithNote("an exported function's result type is part of its API; unexported functions may leave it out")
			}
		}
	case 1:
		x := c.expr(e, sig.Results[0])
		if c.passThrough(x, sig.Results) || c.notPassedThrough(x, sig.Results) {
			return
		}
		c.assign(c.singleValue(x), sig.Results[0], "return value")
	default:
		c.errorf(diag.EInvalidUse, e.Span(), "functions with multiple results need a block body")
	}
}

// fallibleResults reports whether a function with sig may use `?`/throw,
// and whether it does so through a raw tuple ending in Error.
func fallibleResults(sig *types.Signature) (fallible, raw bool) {
	if sig.Throws {
		return true, false
	}
	if n := len(sig.Results); n > 0 && types.IsErrorType(sig.Results[n-1]) && n > 1 {
		return true, true
	}
	return false, false
}

func resultString(sig *types.Signature) string {
	switch len(sig.Results) {
	case 0:
		return "unit"
	case 1:
		return sig.Results[0].String()
	}
	return (&types.Tuple{Elems: sig.Results}).String()
}

// finishLocals reports unused locals and returns them.
func (c *Checker) finishLocals(fn *funcCtx) []*VarSym {
	var unused []*VarSym
	for _, v := range fn.locals {
		if !v.Used && v.name != "_" && v.name != "it" && !v.Implicit {
			unused = append(unused, v)
			c.info.Unused[v] = true
			c.warnf(diag.WUnusedVar, v.Span, "%s is declared but never used", v.name).
				WithHint("remove it, or name it _ to discard the value")
		}
	}
	return unused
}

// checkConformance checks `struct T : I1, I2` declarations once every
// method is known: T or *T must implement each interface. The check costs
// nothing at run time; it reports a missing method at the declaration
// instead of at some distant use, and documents intent.
func (c *Checker) checkConformance() {
	for _, ts := range c.pkg.Types {
		d, ok := ts.syn.(*syntax.StructDecl)
		if !ok || len(d.Implements) == 0 || ts.Decl == nil {
			continue
		}
		self := ts.Type
		scope := c.scopeFor(d.S)
		if len(ts.Decl.TypeParams) > 0 {
			self = &types.Named{Decl: ts.Decl, Args: typeParamTypes(ts.Decl.TypeParams)}
			scope = ts.tscope
		}
		c.withScope(scope, func() {
			ptr := &types.Pointer{Elem: self}
			seen := map[string]bool{}
			for _, te := range d.Implements {
				it := c.resolveType(te)
				switch {
				case it == types.Invalid: // already reported
				case !types.IsInterface(it) || types.MayBeNull(it):
					c.errorf(diag.ETypeMismatch, te.Span(), "%s is not an interface; a struct can only declare interfaces it implements", it).
						WithNote("Lyth has no inheritance: share behavior through interfaces and composition")
				case seen[it.String()]:
					c.errorf(diag.EDuplicate, te.Span(), "%s is already listed", it)
				case !c.joinSealed(ts, it, te.Span()):
				case c.implements(self, it):
					c.info.Conforms[d] = append(c.info.Conforms[d], Conformance{Iface: it, Span: te.Span()})
					if sd := types.SealedOf(it); sd != nil {
						sd.Members = append(sd.Members, ts.Type)
					}
				case c.implements(ptr, it):
					c.info.Conforms[d] = append(c.info.Conforms[d], Conformance{Iface: it, Ptr: true, Span: te.Span()})
					if sd := types.SealedOf(it); sd != nil {
						sd.Members = append(sd.Members, ptr)
					}
				default:
					note, cascade := conformanceNote(ts.Decl, it)
					if cascade {
						break
					}
					if note == "" {
						_, _, note = c.assignable(ptr, it)
					}
					dg := c.errorf(diag.ENotImplement, te.Span(), "%s does not implement %s", ts.Decl.Name, it)
					if note != "" {
						dg.WithNote("%s", note)
					}
				}
				seen[it.String()] = true
			}
		})
	}
}

// joinSealed makes the struct ts a member of it when it is a sealed
// interface: the struct gets the interface's unexported marker method (a
// value receiver, so T and *T both have it). Only types of the interface's
// own package can join, as Go's unexported method requires. It reports
// false after an error.
func (c *Checker) joinSealed(ts *TypeSym, it types.Type, span source.Span) bool {
	sd := types.SealedOf(it)
	if sd == nil {
		return true
	}
	if sd.PkgPath != c.pkg.Path {
		c.errorf(diag.ENotImplement, span, "%s is sealed: only types declared in package %s can implement it", span.Text(), sd.PkgName)
		return false
	}
	for _, m := range ts.Decl.Methods {
		if m.GoName == sd.Marker {
			c.errorf(diag.EDuplicate, m.Span, "method %s conflicts with the marker method that sealed interface %s gives its members", m.Name, it)
			return false
		}
	}
	recv := gotypes.NewVar(token.NoPos, c.shadow, "", ts.Decl.Shadow)
	ts.Decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, sd.Marker, gotypes.NewSignatureType(recv, nil, nil, nil, nil, false)))
	return true
}

// implements reports whether a value of type t can be used as iface.
func (c *Checker) implements(t, iface types.Type) bool {
	ok, _, _ := c.assignable(t, iface)
	return ok
}

// conformanceNote names the method that keeps *T (and so T) from
// implementing iface, the signature iface needs, and why: the method is
// missing, has another signature, or is not pub. The note is "" when the Go
// method sets agree and only Lyth-level types differ. cascade reports a
// method whose signature already failed to resolve.
func conformanceNote(decl *types.TypeDecl, iface types.Type) (note string, cascade bool) {
	gi, ok := types.Lower(iface).Underlying().(*gotypes.Interface)
	if !ok || decl.Shadow == nil {
		return "", false
	}
	m, _ := gotypes.MissingMethod(gotypes.NewPointer(decl.Shadow), gi, true)
	if m == nil {
		return "", false
	}
	name := m.Name()
	var want *types.Signature
	if n, ok := types.Base(iface).(*types.Named); ok && n.Decl != nil {
		for _, im := range n.Decl.Methods {
			if im.GoName == m.Name() {
				name, want = im.Name, im.Sig
			}
		}
	}
	if want == nil {
		// A Go method returning a trailing error is a throws method in Lyth.
		want = types.LiftSignature(m.Signature())
		if k := len(want.Results); k > 0 && types.IsErrorType(want.Results[k-1]) {
			want.Results, want.Throws = want.Results[:k-1], true
		}
	}
	for _, dm := range decl.Methods {
		switch {
		case !strings.EqualFold(dm.Name, name):
		case slices.Contains(dm.Sig.Params, types.Invalid) || slices.Contains(dm.Sig.Results, types.Invalid):
			return "", true
		case dm.GoName == m.Name():
			return fmt.Sprintf("method %s has type %s, but %s needs %s", dm.Name, dm.Sig, iface, want), false
		default:
			return fmt.Sprintf("method %s must be named %s to implement %s", dm.Name, name, iface), false
		}
	}
	return fmt.Sprintf("missing method %s: %s", name, want), false
}

// checkEmbedCycles reports interfaces of this package that embed
// themselves, directly or through other interfaces (Go: invalid recursive
// type), and removes the embedding that closes each cycle.
func (c *Checker) checkEmbedCycles() {
	state := map[*types.TypeDecl]int{} // 1 visiting, 2 done
	var visit func(d *types.TypeDecl) bool
	visit = func(d *types.TypeDecl) bool {
		switch state[d] {
		case 1:
			return true
		case 2:
			return false
		}
		state[d] = 1
		kept := d.Embeds[:0]
		for _, e := range d.Embeds {
			if n, ok := types.Base(e).(*types.Named); ok && n.Decl != nil && n.Decl.Kind == types.InterfaceDecl && visit(n.Decl) {
				c.errorf(diag.EInvalidUse, d.Span, "interface %s embeds itself through %s", d.Name, n.Decl.Name)
				continue
			}
			kept = append(kept, e)
		}
		d.Embeds = kept
		state[d] = 2
		return false
	}
	for _, ts := range c.pkg.Types {
		if ts.Decl != nil && ts.Decl.Kind == types.InterfaceDecl {
			visit(ts.Decl)
		}
	}
}
