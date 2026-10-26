package build

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/emit"
	"github.com/lyth-lang/lyth/internal/source"
)

type importerFunc func(path string) (*gotypes.Package, error)

func (f importerFunc) Import(path string) (*gotypes.Package, error) { return f(path) }

// validate type-checks the generated Go of every package in-process
// (ADR 0004). Errors indicate gaps in Lyth's checker; they are mapped to Lyth
// spans through the exact source maps.
func (s *Session) validate() {
	for _, p := range s.Packages {
		s.validatePkg(p)
	}
}

// goTypesByPath indexes the loaded Go packages and their dependencies.
func (s *Session) goTypesByPath() map[string]*gotypes.Package {
	if s.goTypes != nil && s.goTypesN == len(s.goPkgs) {
		return s.goTypes
	}
	goTypes := map[string]*gotypes.Package{}
	var add func(p *gotypes.Package)
	add = func(p *gotypes.Package) {
		if p == nil || goTypes[p.Path()] != nil {
			return
		}
		goTypes[p.Path()] = p
		for _, dep := range p.Imports() {
			add(dep)
		}
	}
	for _, gp := range s.goPkgs {
		if gp != nil && gp.Types != nil {
			add(gp.Types)
		}
	}
	s.goTypes, s.goTypesN = goTypes, len(s.goPkgs)
	return goTypes
}

// validatePkg type-checks the generated Go of one package, once.
func (s *Session) validatePkg(p *Package) {
	if len(p.Generated) == 0 || p.validated {
		return
	}
	p.validated = true
	goTypes := s.goTypesByPath()
	fset := token.NewFileSet()
	var files []*ast.File
	byName := map[string]*emit.Result{}
	for _, g := range p.Generated {
		f, err := parser.ParseFile(fset, g.Name, g.Code, parser.SkipObjectResolution)
		if err != nil {
			s.Sink.Errorf(diag.EInternal, source.Span{File: g.Map.Src}, "internal compiler error: generated Go does not parse: %v", err)
			continue
		}
		files = append(files, f)
		byName[g.Name] = g
	}
	var errs []gotypes.Error
	conf := gotypes.Config{
		Importer: importerFunc(func(path string) (*gotypes.Package, error) {
			if np := s.byPath[path]; np != nil {
				if np.GoTypes != nil {
					return np.GoTypes, nil
				}
				return nil, fmt.Errorf("package %s has no type information", path)
			}
			if b := s.bridges[path]; b != nil && b.types != nil {
				return b.types, nil
			}
			if t := goTypes[path]; t != nil {
				return s.loader.Complete(t)
			}
			return nil, fmt.Errorf("package %s not loaded", path)
		}),
		Sizes:     s.loader.Sizes(),
		GoVersion: "go1.26",
		Error: func(err error) {
			var te gotypes.Error
			if errors.As(err, &te) && len(errs) < 20 {
				errs = append(errs, te)
			}
		},
	}
	pkg, _ := conf.Check(p.Dir.ImportPath, fset, files, nil)
	p.GoTypes = pkg
	for _, te := range errs {
		s.reportGoTypeError(fset, te, byName)
	}
}

func (s *Session) reportGoTypeError(fset *token.FileSet, te gotypes.Error, byName map[string]*emit.Result) {
	raw := fset.PositionFor(te.Pos, false)
	g := byName[raw.Filename]
	cause := &diag.GoCause{Message: te.Msg, Position: emit.Describe(raw.Filename, raw.Line, raw.Column)}
	var span source.Span
	if g != nil {
		if sp, _, ok := g.Map.Lookup(raw.Offset); ok {
			span = sp
			cause.Mapping = "exact"
		} else if line := g.Map.LookupLine(raw.Line); line > 0 {
			start := g.Map.Src.LineStart(line)
			span = source.Span{File: g.Map.Src, Start: start, End: start}
			cause.Mapping = "line"
		} else {
			span = source.Span{File: g.Map.Src}
			cause.Mapping = "approximate"
		}
	}
	d := s.Sink.Errorf(diag.EGoTypeCheck, span, "%s", check.TranslateGoMessage(te.Msg))
	d.Cause = cause
	d.WithNote("the generated Go code did not type-check, so Lyth's checker missed this problem; please report it")
}
