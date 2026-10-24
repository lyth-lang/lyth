package types

import (
	gotypes "go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestLiftLowerRoundTrip checks the interop fidelity property: lifting a Go
// type into Lyth and lowering it back yields an identical Go type, for every
// exported declaration (and method) of several large standard packages.
func TestLiftLowerRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("loads standard packages")
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, "net/http", "encoding/json", "database/sql", "context", "io", "sync", "time", "slices", "iter")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	check := func(where string, gt gotypes.Type) {
		checked++
		nt := Lift(gt)
		back := Lower(nt)
		if !gotypes.Identical(back, gt) {
			t.Errorf("%s: Lower(Lift(%s)) = %s (Lyth: %s)", where, gt, back, nt)
		}
	}
	for _, p := range pkgs {
		if p.Types == nil {
			t.Fatalf("%s: no types: %v", p.PkgPath, p.Errors)
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			where := p.PkgPath + "." + name
			switch o := obj.(type) {
			case *gotypes.Func:
				sig := o.Type().(*gotypes.Signature)
				if sig.TypeParams() == nil {
					check(where, gotypes.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic()))
				}
			case *gotypes.Var, *gotypes.Const:
				check(where, o.Type())
			case *gotypes.TypeName:
				named, ok := o.Type().(*gotypes.Named)
				if !ok || named.TypeParams() != nil {
					continue
				}
				check(where, named)
				check(where+" (underlying)", named.Underlying())
				for i := 0; i < named.NumMethods(); i++ {
					m := named.Method(i)
					if !m.Exported() {
						continue
					}
					sig := m.Type().(*gotypes.Signature)
					check(where+"."+m.Name(), gotypes.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic()))
				}
			}
		}
	}
	if checked < 500 {
		t.Errorf("only %d types checked; loading may have failed", checked)
	}
	t.Logf("checked %d Go types", checked)
}

func TestNullabilityOfLiftedTypes(t *testing.T) {
	ptr := gotypes.NewPointer(gotypes.Typ[gotypes.Int])
	if _, ok := Lift(ptr).(*Platform); !ok {
		t.Errorf("Go pointers must lift to platform types, got %s", Lift(ptr))
	}
	slice := gotypes.NewSlice(ptr)
	s, ok := Lift(slice).(*Slice)
	if !ok {
		t.Fatalf("slices are never null: got %s", Lift(slice))
	}
	if _, ok := s.Elem.(*Platform); !ok {
		t.Errorf("platform-ness is deep: element should be platform, got %s", s.Elem)
	}
	if got := Lift(GoError()).String(); got != "error!" {
		t.Errorf("error lifts to %q, want error!", got)
	}
}

func TestZeroable(t *testing.T) {
	cases := []struct {
		t    Type
		want bool
	}{
		{Int, true},
		{String, true},
		{&Pointer{Elem: Int}, false},
		{&Nullable{Elem: &Pointer{Elem: Int}}, true},
		{&Slice{Elem: &Pointer{Elem: Int}}, true},
		{Any, false},
		{&Nullable{Elem: Any}, true},
		{&Array{Len: 2, Elem: &Pointer{Elem: Int}}, false},
	}
	for _, c := range cases {
		if got := Zeroable(c.t); got != c.want {
			t.Errorf("Zeroable(%s) = %v, want %v", c.t, got, c.want)
		}
	}
}

func TestRawResultsOfThrowingFunctions(t *testing.T) {
	sig := &Signature{Results: []Type{&Pointer{Elem: Int}}, Throws: true}
	raw := RawResults(sig)
	if len(raw) != 2 || !IsNullable(raw[0]) || !IsErrorType(raw[1]) {
		t.Errorf("RawResults = %v; non-zeroable success values become nullable when destructured", raw)
	}
	if got := LowerSignature(sig, nil).String(); got != "func() (*int, error)" {
		t.Errorf("lowered signature %s", got)
	}
}
