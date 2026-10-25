// Package types defines Lyth's semantic types and their relationship to Go's
// representation types.
//
// Every Lyth type lowers to exactly one go/types type (Lower). Types that come
// from Go packages enter Lyth through Lift, which marks nil-able positions as
// platform types. Lift never simplifies: Lower(Lift(t)) is identical to t.
package types

import (
	gotypes "go/types"
	"strconv"
	"strings"

	"github.com/lyth-lang/lyth/internal/source"
)

// Type is a Lyth type.
type Type interface {
	String() string
	isType()
}

// ---------------------------------------------------------------------------
// Basic types

// BasicKind enumerates basic types, including untyped constant kinds.
type BasicKind int

const (
	InvalidKind BasicKind = iota
	BoolKind
	IntKind
	Int8Kind
	Int16Kind
	Int32Kind
	Int64Kind
	UIntKind
	UInt8Kind
	UInt16Kind
	UInt32Kind
	UInt64Kind
	UIntPtrKind
	Float32Kind
	Float64Kind
	Complex64Kind
	Complex128Kind
	StringKind
	UnsafePointerKind

	UntypedBoolKind
	UntypedIntKind
	UntypedRuneKind
	UntypedFloatKind
	UntypedStringKind
)

// Basic is a predeclared basic type.
type Basic struct {
	Kind BasicKind
	Name string // Go spelling ("byte" and "rune" are aliases of uint8/int32)
}

func (b *Basic) String() string { return b.Name }
func (*Basic) isType()          {}

var (
	Bool          = &Basic{BoolKind, "bool"}
	Int           = &Basic{IntKind, "int"}
	Int8          = &Basic{Int8Kind, "int8"}
	Int16         = &Basic{Int16Kind, "int16"}
	Int32         = &Basic{Int32Kind, "int32"}
	Int64         = &Basic{Int64Kind, "int64"}
	UInt          = &Basic{UIntKind, "uint"}
	UInt8         = &Basic{UInt8Kind, "uint8"}
	UInt16        = &Basic{UInt16Kind, "uint16"}
	UInt32        = &Basic{UInt32Kind, "uint32"}
	UInt64        = &Basic{UInt64Kind, "uint64"}
	UIntPtr       = &Basic{UIntPtrKind, "uintptr"}
	Float32       = &Basic{Float32Kind, "float32"}
	Float64       = &Basic{Float64Kind, "float64"}
	Complex64     = &Basic{Complex64Kind, "complex64"}
	Complex128    = &Basic{Complex128Kind, "complex128"}
	String        = &Basic{StringKind, "string"}
	UnsafePointer = &Basic{UnsafePointerKind, "unsafe.Pointer"}
	Byte          = &Basic{UInt8Kind, "byte"}
	Rune          = &Basic{Int32Kind, "rune"}

	UntypedBool   = &Basic{UntypedBoolKind, "untyped bool"}
	UntypedInt    = &Basic{UntypedIntKind, "untyped int"}
	UntypedRune   = &Basic{UntypedRuneKind, "untyped rune"}
	UntypedFloat  = &Basic{UntypedFloatKind, "untyped float"}
	UntypedString = &Basic{UntypedStringKind, "untyped string"}
)

// BasicTypes lists the basic types visible in Lyth's universe scope by name.
var BasicTypes = []*Basic{Bool, Int, Int8, Int16, Int32, Int64, UInt, UInt8, UInt16, UInt32, UInt64,
	UIntPtr, Float32, Float64, Complex64, Complex128, String, Byte, Rune}

// ---------------------------------------------------------------------------
// Special types

type special struct{ name string }

func (s *special) String() string { return s.name }
func (*special) isType()          {}

var (
	// Unit is the result type of functions that return nothing.
	Unit Type = &special{"unit"}
	// Nothing is the type of expressions that never complete normally.
	Nothing Type = &special{"nothing"}
	// Null is the type of the null literal.
	Null Type = &special{"null"}
	// Invalid marks expressions that failed to type-check.
	Invalid Type = &special{"invalid type"}
)

// AnyType is Lyth's Any (Go's any / interface{}).
type AnyType struct{}

func (*AnyType) String() string { return "any" }
func (*AnyType) isType()        {}

// Any is the singleton Any type.
var Any = &AnyType{}

// ---------------------------------------------------------------------------
// Named types

// Named is a defined type: either a Go named type or a Lyth-declared type.
type Named struct {
	Go   *gotypes.Named // set for Go-declared types (possibly instantiated)
	Decl *TypeDecl      // set for Lyth-declared types
	Args []Type         // type arguments
	// Alias is the Go alias the type was named by (json.RawMessage for
	// jsontext.Value), kept so that diagnostics and generated code use it.
	Alias *gotypes.Alias
}

func (*Named) isType() {}

func (n *Named) String() string {
	var b strings.Builder
	if n.Decl != nil {
		b.WriteString(n.Decl.Name)
	} else if n.Alias != nil {
		obj := n.Alias.Obj()
		if obj.Pkg() != nil {
			b.WriteString(obj.Pkg().Name() + ".")
		}
		b.WriteString(obj.Name())
	} else {
		obj := n.Go.Obj()
		if obj.Pkg() == nil && obj.Name() == "error" {
			return "error"
		}
		if obj.Pkg() == nil && obj.Name() == "comparable" {
			return "comparable"
		}
		if obj.Pkg() != nil {
			b.WriteString(obj.Pkg().Name() + ".")
		}
		b.WriteString(obj.Name())
	}
	if len(n.Args) > 0 {
		b.WriteString("[")
		for i, a := range n.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(a.String())
		}
		b.WriteString("]")
	}
	return b.String()
}

// DeclKind distinguishes Lyth-declared type kinds.
type DeclKind int

const (
	StructDecl DeclKind = iota
	InterfaceDecl
	EnumDecl // a named Int with one constant per entry (ADR 0025)
)

// TypeDecl describes a Lyth-declared named type.
type TypeDecl struct {
	Name    string // Lyth name
	GoName  string
	PkgPath string
	PkgName string
	Pub     bool
	Kind    DeclKind
	// TypeParams are the type parameters of a generic struct or interface.
	TypeParams []*TypeParam
	Fields     []*Field  // struct fields
	Methods    []*Method // declared methods (structs) or interface methods
	Embeds     []Type    // embedded interfaces
	Span       source.Span
	Shadow     *gotypes.Named // the shadow go/types object for Go-level questions

	Entries []*EnumEntry // enum entries, in declaration order

	// Sealed interfaces: only types of the same package that declare the
	// interface implement it, through the unexported Marker method; Members
	// are those types (T, or *T when only the pointer implements it).
	Sealed  bool
	Marker  string
	Members []Type
}

// EnumEntry is one entry of an enum: a Go constant named GoName (the type's
// Go name followed by the entry name) with value Value.
type EnumEntry struct {
	Name   string
	GoName string
	Value  int
	Decl   *TypeDecl
	Span   source.Span
}

// LookupEntry returns the enum entry named name, or nil.
func (d *TypeDecl) LookupEntry(name string) *EnumEntry {
	for _, e := range d.Entries {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// EnumOf returns the enum declaration of t's base type, or nil.
func EnumOf(t Type) *TypeDecl {
	if n, ok := Base(t).(*Named); ok && n.Decl != nil && n.Decl.Kind == EnumDecl {
		return n.Decl
	}
	return nil
}

// SealedOf returns the sealed interface declaration of t's base type, or nil.
func SealedOf(t Type) *TypeDecl {
	if n, ok := Base(t).(*Named); ok && n.Decl != nil && n.Decl.Sealed {
		return n.Decl
	}
	return nil
}

// Field is a struct field.
type Field struct {
	Name    string // Lyth name
	GoName  string
	Type    Type
	Private bool
	Tag     string // final Go tag (including automatic json key)
	Span    source.Span
}

// Method is a method of a Lyth type or interface.
type Method struct {
	Name    string // Lyth name
	GoName  string
	Sig     *Signature
	PtrRecv bool
	Pub     bool
	Span    source.Span
	Sym     any // the checker's symbol for this method
}

// LookupField returns the field named name (Lyth name), or nil.
func (d *TypeDecl) LookupField(name string) *Field {
	for _, f := range d.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// LookupMethod returns the declared method named name (Lyth name), or nil.
func (d *TypeDecl) LookupMethod(name string) *Method {
	for _, m := range d.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Composite types

// Pointer is *T.
type Pointer struct{ Elem Type }

// Slice is []T (a Go slice).
type Slice struct{ Elem Type }

// Array is [N]T.
type Array struct {
	Len  int64
	Elem Type
}

// Map is Map<K, V>.
type Map struct{ Key, Elem Type }

// ChanDir is a channel direction.
type ChanDir int

const (
	SendRecv ChanDir = iota
	SendOnly
	RecvOnly
)

// Chan is a channel type.
type Chan struct {
	Dir  ChanDir
	Elem Type
}

// Signature is a function type. For fallible functions, Results holds the
// success results and Throws is set; the Go form has an extra error result.
type Signature struct {
	Params     []Type
	ParamNames []string
	Variadic   bool // the last parameter is a Slice holding the variadic args
	Results    []Type
	Throws     bool
	TypeParams []*TypeParam       // non-empty for uninstantiated generic Go functions
	GoSig      *gotypes.Signature // original Go signature (for generic instantiation)
}

// Tuple is a multi-value result. It is not a first-class type.
type Tuple struct{ Elems []Type }

// Result is the type of a call to a fallible function: its last Go result
// is an error. Success holds the success values as seen through `?` and
// `catch`; Raw holds the Go-shaped components as seen by destructuring.
// Like Tuple, it is not first-class, except that a Result whose only
// component is the error can be used as a value of that error type.
type Result struct {
	Success []Type
	Raw     []Type
}

// Nullable is T?.
type Nullable struct{ Elem Type }

// Platform is T! — a nil-able value from Go with unknown nullability.
type Platform struct{ Elem Type }

// TypeParam is a Go type parameter appearing in an uninstantiated signature.
type TypeParam struct {
	Go         *gotypes.TypeParam
	Name       string
	Constraint Type // the constraint as a Lyth type; nil for type parameters of Go declarations
}

// GoType wraps a Go type that Lyth does not model structurally (anonymous
// struct and interface types). Lyth questions about it go to go/types.
type GoType struct{ T gotypes.Type }

func (*Pointer) isType()   {}
func (*Slice) isType()     {}
func (*Array) isType()     {}
func (*Map) isType()       {}
func (*Chan) isType()      {}
func (*Signature) isType() {}
func (*Tuple) isType()     {}
func (*Result) isType()    {}
func (*Nullable) isType()  {}
func (*Platform) isType()  {}
func (*TypeParam) isType() {}
func (*GoType) isType()    {}

func (p *Pointer) String() string { return "*" + p.Elem.String() }
func (s *Slice) String() string   { return "[]" + s.Elem.String() }
func (a *Array) String() string   { return "[" + strconv.FormatInt(a.Len, 10) + "]" + a.Elem.String() }
func (m *Map) String() string     { return "map[" + m.Key.String() + "]" + m.Elem.String() }
func (c *Chan) String() string {
	switch c.Dir {
	case SendOnly:
		return "chan<- " + c.Elem.String()
	case RecvOnly:
		return "<-chan " + c.Elem.String()
	}
	return "chan " + c.Elem.String()
}

func (s *Signature) String() string {
	var b strings.Builder
	b.WriteString("(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if s.Variadic && i == len(s.Params)-1 {
			if sl, ok := p.(*Slice); ok {
				b.WriteString("..." + sl.Elem.String())
				continue
			}
		}
		b.WriteString(p.String())
	}
	b.WriteString(") -> ")
	switch len(s.Results) {
	case 0:
		b.WriteString("unit")
	case 1:
		b.WriteString(s.Results[0].String())
	default:
		b.WriteString("(")
		for i, r := range s.Results {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(r.String())
		}
		b.WriteString(")")
	}
	if s.Throws {
		b.WriteString(" throws")
	}
	return b.String()
}

func (t *Tuple) String() string {
	parts := make([]string, len(t.Elems))
	for i, e := range t.Elems {
		parts[i] = e.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (r *Result) String() string {
	parts := make([]string, len(r.Raw))
	for i, e := range r.Raw {
		parts[i] = e.String()
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (n *Nullable) String() string {
	switch n.Elem.(type) {
	case *Signature, *Slice, *Array, *Map, *Chan:
		// []T? would read as a slice of nullable elements.
		return "(" + n.Elem.String() + ")?"
	}
	return n.Elem.String() + "?"
}

func (p *Platform) String() string {
	if _, ok := p.Elem.(*Signature); ok {
		return "(" + p.Elem.String() + ")!"
	}
	return p.Elem.String() + "!"
}

func (t *TypeParam) String() string { return t.Name }

func (g *GoType) String() string {
	return gotypes.TypeString(g.T, func(p *gotypes.Package) string { return p.Name() })
}
