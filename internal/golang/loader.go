// Package golang loads type information for Go packages through
// golang.org/x/tools/go/packages, so module resolution, build tags, cgo,
// vendoring, and workspaces behave exactly as in the go command.
package golang

import (
	"bytes"
	"encoding/json"
	"fmt"
	gotypes "go/types"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Package is a loaded Go package.
type Package struct {
	Path   string
	Name   string
	Types  *gotypes.Package
	Errors []string
}

// Loader loads Go packages relative to a module directory.
type Loader struct {
	Dir       string   // directory to run the go command in (module root)
	Env       []string // extra environment (e.g. GOOS); nil means inherit
	Tags      []string // build tags
	CacheFile string   // persistent cache of export data locations ("" disables)
	GoEnv     *Env     // go env output to key the cache with; nil runs go env
	// CacheHit reports whether the last Load was served from CacheFile.
	CacheHit bool

	cache       map[string]*Package
	sizes       gotypes.Sizes
	envKeyCache string
	goarch      string
}

// NewLoader creates a loader for dir.
func NewLoader(dir string, env, tags []string) *Loader {
	return &Loader{Dir: dir, Env: env, Tags: tags, cache: map[string]*Package{}}
}

// Sizes returns the type sizes for the target platform, once known.
func (l *Loader) Sizes() gotypes.Sizes {
	if l.sizes == nil {
		return gotypes.SizesFor("gc", "amd64")
	}
	return l.sizes
}

// Load loads the given import paths (and their dependencies' export data).
// Already-loaded paths are served from the cache.
func (l *Loader) Load(paths []string) (map[string]*Package, error) {
	var missing []string
	seen := map[string]bool{}
	for _, p := range paths {
		if _, ok := l.cache[p]; !ok && !seen[p] {
			missing = append(missing, p)
			seen[p] = true
		}
	}
	sort.Strings(missing)
	l.CacheHit = false
	if len(missing) > 0 {
		if res, ok := l.readCache(missing); ok {
			for path, p := range res {
				l.cache[path] = p
			}
			l.CacheHit = true
			missing = nil
		}
	}
	if len(missing) > 0 {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps | packages.NeedTypesSizes |
				packages.NeedExportFile | packages.NeedFiles | packages.NeedModule,
			Dir: l.Dir,
		}
		if l.Env != nil {
			cfg.Env = l.Env
		}
		if len(l.Tags) > 0 {
			cfg.BuildFlags = []string{"-tags=" + strings.Join(l.Tags, ",")}
		}
		pkgs, err := packages.Load(cfg, missing...)
		if err != nil {
			return nil, fmt.Errorf("loading Go packages: %w", err)
		}
		byPath := map[string]*packages.Package{}
		for _, p := range pkgs {
			byPath[p.PkgPath] = p
			if l.sizes == nil && p.TypesSizes != nil {
				l.sizes = p.TypesSizes
			}
		}
		for _, path := range missing {
			p, ok := byPath[path]
			res := &Package{Path: path}
			if !ok {
				// go/packages reports unresolvable patterns with an ID equal to the path.
				for _, q := range pkgs {
					if q.ID == path {
						p = q
						ok = true
						break
					}
				}
			}
			if !ok {
				res.Errors = append(res.Errors, fmt.Sprintf("package %s could not be loaded", path))
			} else {
				res.Name = p.Name
				for _, e := range p.Errors {
					res.Errors = append(res.Errors, cleanError(e))
				}
				if len(res.Errors) == 0 && p.Types != nil {
					res.Types = p.Types
				}
			}
			l.cache[path] = res
		}
		clean := true
		for _, path := range missing {
			if len(l.cache[path].Errors) > 0 {
				clean = false
			}
		}
		if clean {
			l.writeCache(missing, pkgs)
		}
	}
	out := map[string]*Package{}
	for _, p := range paths {
		out[p] = l.cache[p]
	}
	return out, nil
}

func cleanError(e packages.Error) string {
	msg := e.Msg
	// The go command prefixes errors with positions inside go.mod or the
	// package; keep only the message for import diagnostics.
	if i := strings.Index(msg, ": "); i > 0 && strings.Contains(msg[:i], "/") && strings.HasSuffix(msg[:i], ".go") {
		msg = msg[i+2:]
	}
	return strings.TrimSpace(msg)
}

// GoVersion returns the toolchain version (e.g. "go1.26.7") reported by
// `go env GOVERSION`, and its minor version number.
func GoVersion(dir string) (string, int, error) {
	env, err := GoEnv(dir, nil)
	if err != nil {
		return "", 0, err
	}
	v, minor := env.Version()
	return v, minor, nil
}

// EnvVars are the go env variables that affect type information; they key
// the persistent type cache.
var EnvVars = []string{"GOVERSION", "GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED", "GOEXPERIMENT", "GOROOT", "GOWORK"}

// Env is the output of `go env -json` for EnvVars.
type Env struct {
	Raw  []byte
	Vars map[string]string
}

// GoEnv runs `go env -json` in dir once for the toolchain check and the
// type cache key. A nil env inherits the process environment.
func GoEnv(dir string, env []string) (*Env, error) {
	cmd := exec.Command("go", append([]string{"env", "-json"}, EnvVars...)...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running go env: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	e := &Env{Raw: out.Bytes()}
	if err := json.Unmarshal(e.Raw, &e.Vars); err != nil {
		return nil, fmt.Errorf("reading go env output: %v", err)
	}
	return e, nil
}

// Version returns GOVERSION and its minor version number (go1.26.7 -> 26).
func (e *Env) Version() (string, int) {
	v := e.Vars["GOVERSION"]
	minor := 0
	if rest, ok := strings.CutPrefix(v, "go1."); ok {
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		minor, _ = strconv.Atoi(rest[:end])
	}
	return v, minor
}
