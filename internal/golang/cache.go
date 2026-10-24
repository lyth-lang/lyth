package golang

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	gotypes "go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

// The persistent cache lets warm builds skip `go list -export`, which
// dominates Lyth's build time (a process round trip of 0.2–0.5 s). A cache
// entry records, for a set of requested import paths, the export data files
// the Go build cache holds for them. Its key covers everything that can
// change the answer:
//
//   - the toolchain and environment (go env: GOVERSION, GOOS, GOARCH,
//     GOFLAGS, CGO_ENABLED, GOEXPERIMENT, GOROOT, GOWORK) and build tags,
//   - the module graph (go.mod, go.sum, go.work, go.work.sum,
//     vendor/modules.txt),
//   - the requested import paths,
//   - a fingerprint (names, sizes, modification times) of every file in each
//     local package of the dependency closure, i.e. packages of the main
//     module or of local replace directories, whose sources can change
//     without the module graph changing.
//
// Module-cache and standard-library packages are immutable for a given key.
// Export files are content-addressed by the go command, so a recorded path
// always names the same data; if one was removed (go clean -cache, cache
// trimming), the entry is ignored and the packages are loaded normally.

const cacheVersion = 1

type cacheEntry struct {
	Version  int
	Key      string
	GOARCH   string
	Packages []cachedPackage // requested packages, dependencies first
	Local    []localPackage  // fingerprints of local packages in the closure
}

type cachedPackage struct {
	Path   string
	Name   string
	Export string
}

type localPackage struct {
	Dir         string
	Fingerprint string
}

// envKey returns the toolchain and module-graph part of the cache key.
func (l *Loader) envKey() (key, goarch string, err error) {
	if l.GoEnv == nil {
		if l.GoEnv, err = GoEnv(l.Dir, l.Env); err != nil {
			return "", "", err
		}
	}
	env := l.GoEnv.Vars
	h := sha256.New()
	fmt.Fprintf(h, "lyth-gotypes-cache %d\n", cacheVersion)
	h.Write(l.GoEnv.Raw)
	fmt.Fprintf(h, "tags=%s\n", strings.Join(l.Tags, ","))
	files := []string{"go.mod", "go.sum", "go.work", "go.work.sum", filepath.Join("vendor", "modules.txt")}
	if env["GOWORK"] != "" && env["GOWORK"] != "off" {
		files = append(files, env["GOWORK"])
	}
	for _, f := range files {
		p := f
		if !filepath.IsAbs(p) {
			p = filepath.Join(l.Dir, f)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(h, "%s: absent\n", f)
			continue
		}
		fmt.Fprintf(h, "%s: %x\n", f, sha256.Sum256(data))
	}
	return hex.EncodeToString(h.Sum(nil)), env["GOARCH"], nil
}

func requestKey(envKey string, paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(envKey + "\n" + strings.Join(sorted, "\n")))
	return hex.EncodeToString(h[:])
}

// fingerprint summarizes the files of a package directory.
func fingerprint(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "missing"
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s %d %d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readCache tries to satisfy a load of paths from the persistent cache.
func (l *Loader) readCache(paths []string) (map[string]*Package, bool) {
	if l.CacheFile == "" {
		return nil, false
	}
	data, err := os.ReadFile(l.CacheFile)
	if err != nil {
		return nil, false
	}
	var entries []cacheEntry
	if json.Unmarshal(data, &entries) != nil {
		return nil, false
	}
	envKey, goarch, err := l.envKey()
	if err != nil {
		return nil, false
	}
	l.envKeyCache, l.goarch = envKey, goarch
	key := requestKey(envKey, paths)
	for _, e := range entries {
		if e.Version != cacheVersion || e.Key != key {
			continue
		}
		for _, lp := range e.Local {
			if fingerprint(lp.Dir) != lp.Fingerprint {
				return nil, false
			}
		}
		view := map[string]*gotypes.Package{}
		out := map[string]*Package{}
		for _, cp := range e.Packages {
			f, err := os.Open(cp.Export)
			if err != nil {
				return nil, false
			}
			r, err := gcexportdata.NewReader(bufio.NewReader(f))
			if err != nil {
				f.Close()
				return nil, false
			}
			pkg, err := gcexportdata.Read(r, l.Fset, view, cp.Path)
			f.Close()
			if err != nil {
				return nil, false
			}
			out[cp.Path] = &Package{Path: cp.Path, Name: cp.Name, Types: pkg}
		}
		for _, p := range paths {
			if out[p] == nil {
				return nil, false
			}
		}
		l.sizes = gotypes.SizesFor("gc", e.GOARCH)
		return out, true
	}
	return nil, false
}

// writeCache records a successful load. Only loads without errors are
// cached; the file keeps the most recent entries.
func (l *Loader) writeCache(paths []string, roots []*packages.Package) {
	if l.CacheFile == "" {
		return
	}
	envKey, goarch := l.envKeyCache, l.goarch
	if envKey == "" {
		var err error
		if envKey, goarch, err = l.envKey(); err != nil {
			return
		}
	}
	e := cacheEntry{Version: cacheVersion, Key: requestKey(envKey, paths), GOARCH: goarch}
	requested := map[string]bool{}
	for _, p := range paths {
		requested[p] = true
	}
	seen := map[*packages.Package]bool{}
	var visit func(p *packages.Package) bool
	visit = func(p *packages.Package) bool {
		if seen[p] {
			return true
		}
		seen[p] = true
		if len(p.Errors) > 0 {
			return false
		}
		paths := make([]string, 0, len(p.Imports))
		for path := range p.Imports {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if !visit(p.Imports[path]) {
				return false
			}
		}
		if isLocal(p) {
			dir := packageDir(p)
			if dir == "" {
				return false
			}
			e.Local = append(e.Local, localPackage{Dir: dir, Fingerprint: fingerprint(dir)})
		}
		if requested[p.PkgPath] {
			if p.ExportFile == "" {
				return false
			}
			e.Packages = append(e.Packages, cachedPackage{Path: p.PkgPath, Name: p.Name, Export: p.ExportFile})
		}
		return true
	}
	for _, r := range roots {
		if !visit(r) {
			return
		}
	}
	if len(e.Packages) != len(paths) {
		return
	}
	var entries []cacheEntry
	if data, err := os.ReadFile(l.CacheFile); err == nil {
		json.Unmarshal(data, &entries)
	}
	kept := []cacheEntry{e}
	for _, old := range entries {
		if old.Key != e.Key && old.Version == cacheVersion && len(kept) < 8 {
			kept = append(kept, old)
		}
	}
	data, err := json.MarshalIndent(kept, "", " ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.CacheFile), 0o755); err != nil {
		return
	}
	tmp := l.CacheFile + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, l.CacheFile)
	}
}

// isLocal reports whether p's sources can change without the module graph
// changing: packages of a main module or of a local replace directory.
func isLocal(p *packages.Package) bool {
	m := p.Module
	if m == nil {
		return false // standard library
	}
	if m.Main {
		return true
	}
	if m.Replace != nil && m.Replace.Version == "" {
		return true
	}
	return false
}

func packageDir(p *packages.Package) string {
	for _, files := range [][]string{p.GoFiles, p.CompiledGoFiles, p.OtherFiles} {
		for _, f := range files {
			return filepath.Dir(f)
		}
	}
	return ""
}
