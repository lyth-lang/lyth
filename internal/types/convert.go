package types

import (
	gotypes "go/types"
)

var (
	goAny        = gotypes.Universe.Lookup("any").Type()
	goError      = gotypes.Universe.Lookup("error").Type().(*gotypes.Named)
	goComparable = gotypes.Universe.Lookup("comparable").Type().(*gotypes.Named)
)

// Error is Lyth's Error type (Go's predeclared error interface), non-null.
var Error = &Named{Go: goError}

// Comparable is Go's predeclared comparable constraint.
var Comparable = &Named{Go: goComparable}

// GoError returns Go's predeclared error type.
func GoError() gotypes.Type { return goError }

// Lift converts a Go type to the Lyth type describing it. Nil-able positions
// become platform types (unknown nullability); nothing is simplified.
func Lift(t gotypes.Type) Type { return Lifter{}.Lift(t) }

// LiftSignature converts a Go signature (receiver ignored).
func LiftSignature(s *gotypes.Signature) *Signature { return Lifter{}.LiftSignature(s) }

// A Lifter converts Go types to Lyth types. Decl, when set, maps a Go named
// type that is the Go view of a Lyth declaration (a Go package of the module
// that uses a Lyth package's types) back to that declaration, so the Lyth
// type keeps its identity, nullability, and members.
type Lifter struct {
	Decl func(*gotypes.Named) *TypeDecl
}

// Lift converts a Go type to the Lyth type describing it.
func (l Lifter) Lift(t gotypes.Type) Type {
	switch t := t.(type) {
	case nil:
		return Invalid
	case *gotypes.Basic:
		return liftBasic(t)
	case *gotypes.Alias:
		lt := l.Lift(gotypes.Unalias(t))
		if t.TypeArgs() != nil || t.Obj().Pkg() == nil {
			return lt
		}
		switch x := lt.(type) {
		case *Named:
			if x.Go != nil {
				return &Named{Go: x.Go, Args: x.Args, Alias: t}
			}
		case *Platform:
			if n, ok := x.Elem.(*Named); ok && n.Go != nil {
				return &Platform{Elem: &Named{Go: n.Go, Args: n.Args, Alias: t}}
			}
		}
		return lt
	case *gotypes.Named:
		n := &Named{Go: t}
		if l.Decl != nil {
			if d := l.Decl(t); d != nil {
				n = &Named{Decl: d}
			}
		}
		if args := t.TypeArgs(); args != nil {
			for i := 0; i < args.Len(); i++ {
				n.Args = append(n.Args, l.Lift(args.At(i)))
			}
		}
		if _, ok := t.Underlying().(*gotypes.Interface); ok {
			return &Platform{Elem: n}
		}
		return n
	case *gotypes.Pointer:
		return &Platform{Elem: &Pointer{Elem: l.Lift(t.Elem())}}
	case *gotypes.Slice:
		return &Slice{Elem: l.Lift(t.Elem())}
	case *gotypes.Array:
		return &Array{Len: t.Len(), Elem: l.Lift(t.Elem())}
	case *gotypes.Map:
		return &Map{Key: l.Lift(t.Key()), Elem: l.Lift(t.Elem())}
	case *gotypes.Chan:
		dir := SendRecv
		switch t.Dir() {
		case gotypes.SendOnly:
			dir = SendOnly
		case gotypes.RecvOnly:
			dir = RecvOnly
		}
		return &Platform{Elem: &Chan{Dir: dir, Elem: l.Lift(t.Elem())}}
	case *gotypes.Signature:
		return &Platform{Elem: l.LiftSignature(t)}
	case *gotypes.Interface:
		if t.Empty() {
			return &Platform{Elem: Any}
		}
		return &Platform{Elem: &GoType{T: t}}
	case *gotypes.TypeParam:
		return &TypeParam{Go: t, Name: t.Obj().Name()}
	case *gotypes.Tuple:
		tup := &Tuple{}
		for i := 0; i < t.Len(); i++ {
			tup.Elems = append(tup.Elems, l.Lift(t.At(i).Type()))
		}
		return tup
	}
	return &GoType{T: t}
}

// LiftSignature converts a Go signature (receiver ignored).
func (l Lifter) LiftSignature(s *gotypes.Signature) *Signature {
	sig := &Signature{Variadic: s.Variadic(), GoSig: s}
	for i := 0; i < s.Params().Len(); i++ {
		v := s.Params().At(i)
		sig.Params = append(sig.Params, l.Lift(v.Type()))
		sig.ParamNames = append(sig.ParamNames, v.Name())
	}
	for i := 0; i < s.Results().Len(); i++ {
		sig.Results = append(sig.Results, l.Lift(s.Results().At(i).Type()))
	}
	if tps := s.TypeParams(); tps != nil {
		for i := 0; i < tps.Len(); i++ {
			tp := tps.At(i)
			sig.TypeParams = append(sig.TypeParams, &TypeParam{Go: tp, Name: tp.Obj().Name()})
		}
	}
	return sig
}

func liftBasic(b *gotypes.Basic) Type {
	switch b.Kind() {
	case gotypes.Bool:
		return Bool
	case gotypes.Int:
		return Int
	case gotypes.Int8:
		return Int8
	case gotypes.Int16:
		return Int16
	case gotypes.Int32:
		if b.Name() == "rune" {
			return Rune
		}
		return Int32
	case gotypes.Int64:
		return Int64
	case gotypes.Uint:
		return UInt
	case gotypes.Uint8:
		if b.Name() == "byte" {
			return Byte
		}
		return UInt8
	case gotypes.Uint16:
		return UInt16
	case gotypes.Uint32:
		return UInt32
	case gotypes.Uint64:
		return UInt64
	case gotypes.Uintptr:
		return UIntPtr
	case gotypes.Float32:
		return Float32
	case gotypes.Float64:
		return Float64
	case gotypes.Complex64:
		return Complex64
	case gotypes.Complex128:
		return Complex128
	case gotypes.String:
		return String
	case gotypes.UnsafePointer:
		return UnsafePointer
	case gotypes.UntypedBool:
		return UntypedBool
	case gotypes.UntypedInt:
		return UntypedInt
	case gotypes.UntypedRune:
		return UntypedRune
	case gotypes.UntypedFloat:
		return UntypedFloat
	case gotypes.UntypedString:
		return UntypedString
	case gotypes.UntypedNil:
		return Null
	}
	return Invalid
}

// GoBasic returns the go/types basic type for a Lyth basic type.
// goByte and goRune are Go's named aliases. gotypes.Typ[gotypes.Byte] is
// uint8 itself (the kind constants are equal), which would print as uint8.
var (
	goByte = gotypes.Universe.Lookup("byte").Type().(*gotypes.Basic)
	goRune = gotypes.Universe.Lookup("rune").Type().(*gotypes.Basic)
)

func GoBasic(b *Basic) *gotypes.Basic {
	switch b {
	case Byte:
		return goByte
	case Rune:
		return goRune
	}
	switch b.Kind {
	case BoolKind:
		return gotypes.Typ[gotypes.Bool]
	case IntKind:
		return gotypes.Typ[gotypes.Int]
	case Int8Kind:
		return gotypes.Typ[gotypes.Int8]
	case Int16Kind:
		return gotypes.Typ[gotypes.Int16]
	case Int32Kind:
		return gotypes.Typ[gotypes.Int32]
	case Int64Kind:
		return gotypes.Typ[gotypes.Int64]
	case UIntKind:
		return gotypes.Typ[gotypes.Uint]
	case UInt8Kind:
		return gotypes.Typ[gotypes.Uint8]
	case UInt16Kind:
		return gotypes.Typ[gotypes.Uint16]
	case UInt32Kind:
		return gotypes.Typ[gotypes.Uint32]
	case UInt64Kind:
		return gotypes.Typ[gotypes.Uint64]
	case UIntPtrKind:
		return gotypes.Typ[gotypes.Uintptr]
	case Float32Kind:
		return gotypes.Typ[gotypes.Float32]
	case Float64Kind:
		return gotypes.Typ[gotypes.Float64]
	case Complex64Kind:
		return gotypes.Typ[gotypes.Complex64]
	case Complex128Kind:
		return gotypes.Typ[gotypes.Complex128]
	case StringKind:
		return gotypes.Typ[gotypes.String]
	case UnsafePointerKind:
		return gotypes.Typ[gotypes.UnsafePointer]
	case UntypedBoolKind:
		return gotypes.Typ[gotypes.UntypedBool]
	case UntypedIntKind:
		return gotypes.Typ[gotypes.UntypedInt]
	case UntypedRuneKind:
		return gotypes.Typ[gotypes.UntypedRune]
	case UntypedFloatKind:
		return gotypes.Typ[gotypes.UntypedFloat]
	case UntypedStringKind:
		return gotypes.Typ[gotypes.UntypedString]
	}
	return gotypes.Typ[gotypes.Invalid]
}

// Lower returns the Go representation type of a Lyth type.
func Lower(t Type) gotypes.Type {
	switch t := t.(type) {
	case *Basic:
		return GoBasic(t)
	case *AnyType:
		return goAny
	case *Named:
		if t.Alias != nil {
			return t.Alias
		}
		if t.Go != nil {
			return t.Go
		}
		if t.Decl != nil && t.Decl.Shadow != nil {
			if len(t.Args) > 0 {
				// An instance of a generic Lyth type: Page<User> is Page[User].
				args := make([]gotypes.Type, len(t.Args))
				for i, a := range t.Args {
					args[i] = Lower(a)
				}
				if inst, err := gotypes.Instantiate(nil, t.Decl.Shadow, args, false); err == nil {
					return inst
				}
			}
			return t.Decl.Shadow
		}
	case *Pointer:
		return gotypes.NewPointer(Lower(t.Elem))
	case *Slice:
		return gotypes.NewSlice(Lower(t.Elem))
	case *Array:
		return gotypes.NewArray(Lower(t.Elem), t.Len)
	case *Map:
		return gotypes.NewMap(Lower(t.Key), Lower(t.Elem))
	case *Chan:
		dir := gotypes.SendRecv
		switch t.Dir {
		case SendOnly:
			dir = gotypes.SendOnly
		case RecvOnly:
			dir = gotypes.RecvOnly
		}
		return gotypes.NewChan(dir, Lower(t.Elem))
	case *Signature:
		return LowerSignature(t, nil)
	case *Nullable:
		if IsNilable(t.Elem) {
			return Lower(t.Elem)
		}
		return gotypes.NewPointer(Lower(t.Elem))
	case *Platform:
		return Lower(t.Elem)
	case *TypeParam:
		return t.Go
	case *GoType:
		return t.T
	case *Tuple:
		vars := make([]*gotypes.Var, len(t.Elems))
		for i, e := range t.Elems {
			vars[i] = gotypes.NewParam(0, nil, "", Lower(e))
		}
		return gotypes.NewTuple(vars...)
	case *Result:
		return Lower(&Tuple{Elems: t.Raw})
	}
	if t == Null {
		return gotypes.Typ[gotypes.UntypedNil]
	}
	return gotypes.Typ[gotypes.Invalid]
}

// LowerSignature returns the Go signature, with an optional receiver.
func LowerSignature(s *Signature, recv *gotypes.Var) *gotypes.Signature {
	params := make([]*gotypes.Var, len(s.Params))
	for i, p := range s.Params {
		name := ""
		if i < len(s.ParamNames) {
			name = s.ParamNames[i]
		}
		params[i] = gotypes.NewParam(0, nil, name, Lower(p))
	}
	var results []*gotypes.Var
	for _, r := range s.Results {
		results = append(results, gotypes.NewParam(0, nil, "", Lower(r)))
	}
	if s.Throws {
		results = append(results, gotypes.NewParam(0, nil, "", goError))
	}
	return gotypes.NewSignatureType(recv, nil, nil, gotypes.NewTuple(params...), gotypes.NewTuple(results...), s.Variadic)
}
