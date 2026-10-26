package build

import (
	"fmt"
	"go/ast"
	gobuild "go/build"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/golang"
)

// A bridge is a Go package of the module that imports Lyth packages of the
// module, directly or through other Go packages of the module: Lyth code
// importing it needs the Go those Lyth packages become. Its types come from
// type-checking its sources against the generated Go of those packages,
// which is validated first, so the generated code of an importing Lyth
// package type-checks against the same objects.
type bridge struct {
	path    string
	dir     string
	files   []string  // the Go files the build uses
	imports []string  // import paths of those files
	lyth    []*pkgDir // the Lyth packages it reaches
	via     []*bridge // the bridges it imports
	types   *gotypes.Package
	errors  []string
	done    bool
}

// moduleLocal reports whether an import path names a package of the main
// module.
func (s *Session) moduleLocal(path string) bool {
	return !s.Mod.Synthetic && (path == s.Mod.Path || strings.HasPrefix(path, s.Mod.Path+"/"))
}

// bridgeFor returns the bridge for a Go import of a Lyth package, or nil
// when the path is not a Go package of the module that reaches Lyth code.
// The Lyth packages it reaches join the session.
func (s *Session) bridgeFor(path string) *bridge {
	if !s.moduleLocal(path) || s.available[path] != nil {
		return nil
	}
	if s.bridges == nil {
		s.bridges = map[string]*bridge{}
	}
	b, seen := s.bridges[path]
	if seen {
		return b
	}
	s.bridges[path] = nil // an import cycle of Go packages is Go's to report
	dir := filepath.Join(s.Mod.Root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(path, s.Mod.Path), "/")))
	ctx := gobuild.Default
	ctx.BuildTags = s.cfg.Tags
	if s.goEnv != nil {
		if v := s.goEnv.Vars["GOOS"]; v != "" {
			ctx.GOOS = v
		}
		if v := s.goEnv.Vars["GOARCH"]; v != "" {
			ctx.GOARCH = v
		}
		ctx.CgoEnabled = s.goEnv.Vars["CGO_ENABLED"] == "1"
	}
	gp, err := ctx.ImportDir(dir, 0)
	if err != nil || len(gp.GoFiles)+len(gp.CgoFiles) == 0 {
		return nil
	}
	nb := &bridge{path: path, dir: dir}
	for _, f := range append(slices.Clip(gp.GoFiles), gp.CgoFiles...) {
		nb.files = append(nb.files, filepath.Join(dir, f))
	}
	for _, imp := range gp.Imports {
		if lp := s.available[imp]; lp != nil {
			nb.lyth = append(nb.lyth, lp)
			continue
		}
		if sub := s.bridgeFor(imp); sub != nil {
			nb.via = append(nb.via, sub)
			for _, lp := range sub.lyth {
				if !slices.Contains(nb.lyth, lp) {
					nb.lyth = append(nb.lyth, lp)
				}
			}
			continue
		}
		nb.imports = append(nb.imports, imp)
	}
	if len(nb.lyth) == 0 {
		return nil // plain Go: loaded with the other Go packages
	}
	if len(gp.CgoFiles) > 0 {
		nb.errors = append(nb.errors, fmt.Sprintf("package %s uses cgo, which a Go package between Lyth packages cannot use yet", path))
	}
	s.bridges[path] = nb
	for _, lp := range nb.lyth {
		if s.byPath[lp.ImportPath] == nil {
			s.addPkg(lp)
		}
	}
	return nb
}

// bridgeImports returns the import paths the session's bridges need from
// the Go loader.
func (s *Session) bridgeImports() []string {
	var out []string
	for _, b := range s.bridges {
		if b != nil {
			out = append(out, b.imports...)
		}
	}
	return out
}

// ensureBridge type-checks a bridge (and the bridges it imports) once the
// Lyth packages it reaches are generated and validated.
func (s *Session) ensureBridge(b *bridge) {
	if b.done {
		return
	}
	b.done = true
	if len(b.errors) > 0 {
		return
	}
	for _, sub := range b.via {
		s.ensureBridge(sub)
		if sub.types == nil {
			b.errors = append(b.errors, fmt.Sprintf("its import %s failed to compile", sub.path))
			return
		}
	}
	// The Lyth packages it reaches, with what they import, are compiled
	// to Go and validated first.
	for _, lp := range b.lyth {
		p := s.byPath[lp.ImportPath]
		if p == nil || !s.compileEarly(p) {
			b.errors = append(b.errors, fmt.Sprintf("its import %s failed to compile", lp.ImportPath))
			return
		}
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range b.files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			b.errors = append(b.errors, err.Error())
			return
		}
		files = append(files, f)
	}
	var errs []string
	conf := gotypes.Config{
		Importer: importerFunc(s.bridgeImport),
		Sizes:    s.loader.Sizes(),
		Error: func(err error) {
			if len(errs) < 10 {
				errs = append(errs, err.Error())
			}
		},
	}
	pkg, _ := conf.Check(b.path, fset, files, nil)
	if len(errs) > 0 {
		b.errors = errs
		return
	}
	b.types = pkg
	s.goPkgs[b.path] = &golang.Package{Path: b.path, Name: pkg.Name(), Types: pkg}
}

// bridgeImport resolves the imports of a bridge's sources: Lyth packages
// to the types of their validated Go, bridges to their own checks, and
// everything else to the loaded Go packages.
func (s *Session) bridgeImport(path string) (*gotypes.Package, error) {
	if p := s.byPath[path]; p != nil {
		if p.GoTypes == nil {
			return nil, fmt.Errorf("package %s failed to compile", path)
		}
		return p.GoTypes, nil
	}
	if b := s.bridges[path]; b != nil {
		s.ensureBridge(b)
		if b.types == nil {
			return nil, fmt.Errorf("package %s failed to compile", path)
		}
		return b.types, nil
	}
	if path == "unsafe" {
		return gotypes.Unsafe, nil
	}
	if gp := s.goPkgs[path]; gp != nil && gp.Types != nil {
		return s.loader.Complete(gp.Types)
	}
	if t := s.goTypesByPath()[path]; t != nil {
		return s.loader.Complete(t)
	}
	return nil, fmt.Errorf("package %s not loaded", path)
}

// compileEarly lowers, emits, and validates a checked package and the
// Lyth packages it imports, for a bridge that needs its Go. It reports
// whether the package compiled.
func (s *Session) compileEarly(p *Package) bool {
	if p.GoTypes != nil {
		return true
	}
	if p.Checked == nil || s.hasErrorsIn(p) {
		return false
	}
	for _, f := range p.Syntax {
		for _, imp := range f.Imports {
			if dep := s.byPath[imp.Path]; dep != nil && !s.compileEarly(dep) {
				return false
			}
			if b := s.bridges[imp.Path]; b != nil {
				s.ensureBridge(b)
			}
		}
	}
	s.generatePkg(p, s.emitOptions())
	if len(p.Generated) == 0 {
		return false
	}
	s.validatePkg(p)
	return p.GoTypes != nil && !s.hasErrorsIn(p)
}

// hasErrorsIn reports whether an error was reported in a file of p.
func (s *Session) hasErrorsIn(p *Package) bool {
	for _, d := range s.Sink.All() {
		if d.Severity != diag.Error {
			continue
		}
		if !d.Span.IsValid() {
			return true // a session-wide error
		}
		if slices.Contains(p.Files, d.Span.File) {
			return true
		}
	}
	return false
}
