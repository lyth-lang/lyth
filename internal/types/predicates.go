package types

import (
	"fmt"
	gotypes "go/types"
)

// Base strips Nullable and Platform wrappers.
func Base(t Type) Type {
	for {
		switch tt := t.(type) {
		case *Nullable:
			t = tt.Elem
		case *Platform:
			t = tt.Elem
		default:
			return t
		}
	}
}

// IsNullable reports whether t is T?.
func IsNullable(t Type) bool { _, ok := t.(*Nullable); return ok }

// IsPlatform reports whether t is a platform type.
func IsPlatform(t Type) bool { _, ok := t.(*Platform); return ok }

// MayBeNull reports whether a value of type t may be null.
func MayBeNull(t Type) bool {
	switch t.(type) {
	case *Nullable, *Platform:
		return true
	}
	return t == Null
}

// MakeNullable returns T? for t (idempotent; platform becomes nullable).
// Slices and maps are never nullable: their empty value is the absent one,
// so null of a slice type is the nil slice.
func MakeNullable(t Type) Type {
	switch Base(t).(type) {
	case *Slice, *Map:
		return Base(t)
	}
	switch tt := t.(type) {
	case *Nullable:
		return t
	case *Platform:
		return &Nullable{Elem: tt.Elem}
	}
	if t == Null || t == Nothing || t == Invalid {
		return t
	}
	return &Nullable{Elem: t}
}

// IsNilable reports whether Go allows nil for the representation of t's
// base type: pointers, interfaces, functions, and channels. (Slices and maps
// are nil-able in Go but are never null in Lyth.)
func IsNilable(t Type) bool {
	switch b := Base(t).(type) {
	case *Pointer, *Signature, *Chan, *AnyType:
		return true
	case *Named:
		if b.Go != nil {
			// context.CancelFunc, a named func type, is nil-able like func().
			switch b.Go.Underlying().(type) {
			case *gotypes.Pointer, *gotypes.Signature, *gotypes.Chan:
				return true
			}
		}
		if u := DefinedUnder(b); u != nil {
			return IsNilable(u)
		}
		return IsInterface(b)
	case *GoType:
		_, ok := b.T.Underlying().(*gotypes.Interface)
		return ok
	case *Basic:
		return b.Kind == UnsafePointerKind
	}
	return false
}

// IsInterface reports whether t's base is an interface type.
func IsInterface(t Type) bool {
	switch b := Base(t).(type) {
	case *AnyType:
		return true
	case *Named:
		if b.Decl != nil {
			return b.Decl.Kind == InterfaceDecl
		}
		_, ok := b.Go.Underlying().(*gotypes.Interface)
		return ok
	case *GoType:
		_, ok := b.T.Underlying().(*gotypes.Interface)
		return ok
	}
	return false
}

// IsErrorType reports whether t's base is Go's predeclared error interface.
func IsErrorType(t Type) bool {
	if n, ok := Base(t).(*Named); ok && n.Go != nil {
		return n.Go.Obj() == goError.Obj()
	}
	return false
}

// IsStruct reports whether t's base is a struct type (Lyth or Go).
func IsStruct(t Type) bool {
	switch b := Base(t).(type) {
	case *Named:
		if b.Decl != nil {
			return b.Decl.Kind == StructDecl
		}
		_, ok := b.Go.Underlying().(*gotypes.Struct)
		return ok
	case *GoType:
		_, ok := b.T.Underlying().(*gotypes.Struct)
		return ok
	}
	return false
}

// BasicOf returns the basic type underlying t (through Go named types such as
// time.Duration), or nil.
func BasicOf(t Type) *Basic {
	switch b := Base(t).(type) {
	case *Basic:
		return b
	case *Named:
		if b.Go != nil {
			if gb, ok := b.Go.Underlying().(*gotypes.Basic); ok {
				if nb, ok := liftBasic(gb).(*Basic); ok {
					return nb
				}
			}
		}
		if u := DefinedUnder(b); u != nil {
			return BasicOf(u)
		}
	}
	return nil
}

// IsUntyped reports whether t is an untyped constant type.
func IsUntyped(t Type) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind >= UntypedBoolKind
}

func kindIs(t Type, f func(BasicKind) bool) bool {
	if _, ok := Base(t).(*TypeParam); ok {
		return TermsAll(t, func(term Type) bool { return kindIs(term, f) })
	}
	b := BasicOf(t)
	return b != nil && f(b.Kind)
}

// TermsAll reports whether f holds for t or, when t is a type parameter, for
// every type its constraint allows: the terms of a type-set constraint such
// as cmp.Ordered (~int | ... | ~string), taken by their underlying types. A
// constraint without terms (methods only, Any, Comparable) allows types f
// cannot know, so it reports false.
func TermsAll(t Type, f func(Type) bool) bool {
	tp, ok := Base(t).(*TypeParam)
	if !ok {
		return f(t)
	}
	terms := typeSetTerms(tp.Go.Constraint())
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !f(Lift(term.Underlying())) {
			return false
		}
	}
	return true
}

// typeSetTerms returns the types of the first union restricting constraint's
// type set (the set is a subset of every such union), expanding unions of
// interfaces, or nil when there is none.
func typeSetTerms(constraint gotypes.Type) []gotypes.Type {
	if constraint == nil {
		return nil // a type parameter whose constraint is still being resolved
	}
	iface, ok := constraint.Underlying().(*gotypes.Interface)
	if !ok {
		return nil
	}
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		var terms []gotypes.Type
		switch e := iface.EmbeddedType(i).(type) {
		case *gotypes.Union:
			for j := 0; j < e.Len(); j++ {
				if sub := typeSetTerms(e.Term(j).Type()); sub != nil {
					terms = append(terms, sub...)
				} else if _, isIface := e.Term(j).Type().Underlying().(*gotypes.Interface); !isIface {
					terms = append(terms, e.Term(j).Type())
				} else {
					return nil // a term without a type set: any type
				}
			}
		default:
			if _, isIface := e.Underlying().(*gotypes.Interface); isIface {
				terms = typeSetTerms(e)
			} else {
				terms = []gotypes.Type{e}
			}
		}
		if len(terms) > 0 {
			return terms
		}
	}
	return nil
}

// IsInteger reports whether t is an integer type (typed or untyped).
func IsInteger(t Type) bool {
	return kindIs(t, func(k BasicKind) bool {
		return k >= IntKind && k <= UIntPtrKind || k == UntypedIntKind || k == UntypedRuneKind
	})
}

// IsUnsigned reports whether t is an unsigned integer type.
func IsUnsigned(t Type) bool {
	return kindIs(t, func(k BasicKind) bool { return k >= UIntKind && k <= UIntPtrKind })
}

// IsFloat reports whether t is a floating-point type.
func IsFloat(t Type) bool {
	return kindIs(t, func(k BasicKind) bool { return k == Float32Kind || k == Float64Kind || k == UntypedFloatKind })
}

// IsComplex reports whether t is a complex type.
func IsComplex(t Type) bool {
	return kindIs(t, func(k BasicKind) bool { return k == Complex64Kind || k == Complex128Kind || k == UntypedComplexKind })
}

// IsNumeric reports whether t is a numeric type.
func IsNumeric(t Type) bool {
	if _, ok := Base(t).(*TypeParam); ok {
		// int | float64: every type the parameter allows is numeric, though
		// not all are integers or all floats.
		return TermsAll(t, IsNumeric)
	}
	return IsInteger(t) || IsFloat(t) || IsComplex(t)
}

// IsString reports whether t is a string type.
func IsString(t Type) bool {
	return kindIs(t, func(k BasicKind) bool { return k == StringKind || k == UntypedStringKind })
}

// IsBoolean reports whether t is a boolean type.
func IsBoolean(t Type) bool {
	return kindIs(t, func(k BasicKind) bool { return k == BoolKind || k == UntypedBoolKind })
}

// DefaultType returns the default type of an untyped constant type.
func DefaultType(t Type) Type {
	b, ok := t.(*Basic)
	if !ok {
		return t
	}
	switch b.Kind {
	case UntypedBoolKind:
		return Bool
	case UntypedIntKind:
		return Int
	case UntypedRuneKind:
		return Rune
	case UntypedFloatKind:
		return Float64
	case UntypedComplexKind:
		return Complex128
	case UntypedStringKind:
		return String
	}
	return t
}

// Identical reports whether a and b are the same Lyth type, including
// nullability.
func Identical(a, b Type) bool {
	return identical(a, b, false)
}

// IdenticalIgnoringNull reports whether a and b are identical when all
// nullability (nullable and platform) is ignored at every level.
func IdenticalIgnoringNull(a, b Type) bool {
	return identical(a, b, true)
}

func identical(a, b Type, ignoreNull bool) bool {
	if ignoreNull {
		a, b = Base(a), Base(b)
	}
	if a == b {
		return true
	}
	switch a := a.(type) {
	case *Basic:
		b, ok := b.(*Basic)
		return ok && a.Kind == b.Kind
	case *AnyType:
		_, ok := b.(*AnyType)
		return ok
	case *Named:
		b, ok := b.(*Named)
		if !ok {
			return false
		}
		if a.Decl != nil || b.Decl != nil {
			if a.Decl != b.Decl || len(a.Args) != len(b.Args) {
				return false
			}
			for i := range a.Args {
				if !identical(a.Args[i], b.Args[i], ignoreNull) {
					return false
				}
			}
			return true
		}
		return gotypes.Identical(a.Go, b.Go)
	case *Pointer:
		b, ok := b.(*Pointer)
		return ok && identical(a.Elem, b.Elem, ignoreNull)
	case *Slice:
		b, ok := b.(*Slice)
		return ok && identical(a.Elem, b.Elem, ignoreNull)
	case *Array:
		b, ok := b.(*Array)
		return ok && a.Len == b.Len && identical(a.Elem, b.Elem, ignoreNull)
	case *Map:
		b, ok := b.(*Map)
		return ok && identical(a.Key, b.Key, ignoreNull) && identical(a.Elem, b.Elem, ignoreNull)
	case *Chan:
		b, ok := b.(*Chan)
		return ok && a.Dir == b.Dir && identical(a.Elem, b.Elem, ignoreNull)
	case *Signature:
		b, ok := b.(*Signature)
		if !ok || a.Variadic != b.Variadic || len(a.Params) != len(b.Params) {
			return false
		}
		ar, br := RawResults(a), RawResults(b)
		if len(ar) != len(br) {
			return false
		}
		for i := range a.Params {
			if !identical(a.Params[i], b.Params[i], ignoreNull) {
				return false
			}
		}
		for i := range ar {
			if !identical(ar[i], br[i], ignoreNull) {
				return false
			}
		}
		return ignoreNull || a.Throws == b.Throws
	case *Tuple:
		b, ok := b.(*Tuple)
		if !ok || len(a.Elems) != len(b.Elems) {
			return false
		}
		for i := range a.Elems {
			if !identical(a.Elems[i], b.Elems[i], ignoreNull) {
				return false
			}
		}
		return true
	case *Nullable:
		b, ok := b.(*Nullable)
		return ok && identical(a.Elem, b.Elem, ignoreNull)
	case *Platform:
		b, ok := b.(*Platform)
		return ok && identical(a.Elem, b.Elem, ignoreNull)
	case *TypeParam:
		b, ok := b.(*TypeParam)
		return ok && a.Go == b.Go
	case *GoType:
		b, ok := b.(*GoType)
		return ok && gotypes.Identical(a.T, b.T)
	}
	return false
}

// RawResults returns the Go-shaped result list of a signature: for fallible
// functions, the success results (zero values on error, so non-zeroable
// types become nullable) followed by a nullable Error.
func RawResults(s *Signature) []Type {
	if !s.Throws {
		return s.Results
	}
	out := make([]Type, 0, len(s.Results)+1)
	for _, r := range s.Results {
		if Zeroable(r) {
			out = append(out, r)
		} else {
			out = append(out, MakeNullable(r))
		}
	}
	return append(out, &Nullable{Elem: Error})
}

// Zeroable reports whether the Go zero value of t is a valid Lyth value of t.
func Zeroable(t Type) bool {
	return zeroable(t, map[*TypeDecl]bool{})
}

// NoZeroReason explains why a type that is not Zeroable has no zero value,
// down to the field that causes it: "User has no zero value: its field
// Plan has type Plan, a string-backed enum without a "" entry".
func NoZeroReason(t Type) string {
	return t.String() + " has no zero value" + noZeroCause(t, map[*TypeDecl]bool{})
}

func noZeroCause(t Type, seen map[*TypeDecl]bool) string {
	switch t := t.(type) {
	case *Array:
		return noZeroCause(t.Elem, seen)
	case *Named:
		if u := DefinedUnder(t); u != nil {
			return noZeroCause(u, seen)
		}
		if t.Decl == nil || t.Decl.Kind == InterfaceDecl || seen[t.Decl] {
			return ""
		}
		if t.Decl.Backing != nil {
			return ": it is a string-backed enum without a \"\" entry"
		}
		seen[t.Decl] = true
		for _, f := range t.Decl.Fields {
			if zeroable(f.Type, map[*TypeDecl]bool{}) {
				continue
			}
			what := ""
			switch ft := f.Type.(type) {
			case *Pointer:
				what = ", a non-null pointer"
			case *Signature:
				what = ", a non-null function"
			case *Chan:
				what = ", a non-null channel"
			case *AnyType:
				what = ", a non-null interface"
			case *Named:
				if ft.Decl != nil && ft.Decl.Kind == InterfaceDecl {
					what = ", a non-null interface"
				} else if ft.Decl != nil && ft.Decl.Backing != nil {
					what = ", a string-backed enum without a \"\" entry"
				} else if ft.Decl == nil {
					switch ft.Go.Underlying().(type) {
					case *gotypes.Interface:
						what = ", a non-null interface"
					case *gotypes.Signature:
						what = ", a non-null function"
					case *gotypes.Pointer:
						what = ", a non-null pointer"
					case *gotypes.Chan:
						what = ", a non-null channel"
					}
				}
			case *GoType:
				what = ", a non-null interface"
			}
			return fmt.Sprintf(": its field %s has type %s%s", f.Name, f.Type, what)
		}
	}
	return ""
}

func zeroable(t Type, seen map[*TypeDecl]bool) bool {
	switch t := t.(type) {
	case *Basic:
		return true
	case *Nullable, *Platform, *Slice, *Map:
		return true
	case *AnyType, *Pointer, *Signature, *Chan:
		return false
	case *Array:
		return zeroable(t.Elem, seen)
	case *Named:
		if u := DefinedUnder(t); u != nil {
			return zeroable(u, seen)
		}
		if t.Decl != nil {
			if t.Decl.Kind == InterfaceDecl {
				return false
			}
			if t.Decl.Backing != nil {
				// A string-backed enum has a zero value only when "" is
				// one of its entries (ADR 0035).
				return t.Decl.HasEmpty
			}
			if seen[t.Decl] {
				return true
			}
			seen[t.Decl] = true
			for _, f := range t.Decl.Fields {
				if !zeroable(f.Type, seen) {
					return false
				}
			}
			return true
		}
		// A Go named type has its underlying type's zero value: a named
		// function, pointer, or channel type (context.CancelFunc,
		// http.HandlerFunc) has none in Lyth, like func() itself.
		switch t.Go.Underlying().(type) {
		case *gotypes.Interface, *gotypes.Signature, *gotypes.Pointer, *gotypes.Chan:
			return false
		}
		return true
	case *GoType:
		_, isIface := t.T.Underlying().(*gotypes.Interface)
		return !isIface
	case *TypeParam:
		// Go's zero value of the type argument; generic code returns it
		// beside a flag or an error, as Go code does.
		return true
	}
	return false
}

// FallibleShape reports whether t is the type of a fallible result (a call
// whose last result is an error) or a plain Error value. It returns the
// success types.
func FallibleShape(t Type) ([]Type, bool) {
	switch tt := t.(type) {
	case *Result:
		return tt.Success, true
	}
	if IsErrorType(t) {
		return nil, true
	}
	return nil, false
}

// ResultOf builds the Result type for calling sig, or returns nil if sig is
// not fallible.
func ResultOf(sig *Signature) *Result {
	if sig.Throws {
		return &Result{Success: sig.Results, Raw: RawResults(sig)}
	}
	n := len(sig.Results)
	if n > 0 && IsErrorType(sig.Results[n-1]) {
		return &Result{Success: sig.Results[:n-1], Raw: sig.Results}
	}
	return nil
}

// IsSmallInt reports whether t's basic type is an integer of at most 32
// bits, whose inclusive ranges are counted in int64 so that a range ending
// at the type's largest value ends (for b in byte(0)..255).
func IsSmallInt(t Type) bool {
	b := BasicOf(t)
	if b == nil {
		return false
	}
	switch b.Kind {
	case Int8Kind, Int16Kind, Int32Kind, UInt8Kind, UInt16Kind, UInt32Kind:
		return true
	}
	return false
}
