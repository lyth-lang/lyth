// Package lsp is Lyth's language server (lyth lsp): the Language Server
// Protocol over stdio, answered from the compiler's own analysis.
//
// Every change re-checks the edited file's package with the editor's unsaved
// contents and publishes Lyth's diagnostics, including the in-process
// go/types validation of the generated Go. On save, go vet runs in the
// background and its findings are mapped back to .lyth lines. Hover, go to
// definition (into Go sources too), references, rename, and highlights use
// the checker's resolution of each identifier, not its text.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// Options configure a server.
type Options struct {
	Version string // reported as serverInfo.version
	// Vet runs go vet on save; the initialize request's
	// initializationOptions {"vet": false} turns it off.
	Vet bool
	// Debounce is how long the server waits after a change before checking.
	Debounce time.Duration
}

// Server is a Lyth language server.
type Server struct {
	opts  Options
	out   io.Writer
	outMu sync.Mutex

	mu                  sync.Mutex
	docs                map[string]string // open documents by canonical path
	gen                 int               // bumped on every change to docs
	cache               map[string]*analysis
	aliases             [][2]string // (canonical dir, editor's dir)
	timers              map[string]*time.Timer
	lythDiags, vetDiags map[string][]Diagnostic
	shutdown            bool

	compileMu sync.Mutex // one compilation at a time
	vetMu     sync.Mutex // one go vet at a time
}

// Run serves the protocol on r and w until the client exits.
func Run(r io.Reader, w io.Writer, opts Options) error {
	if opts.Debounce == 0 {
		opts.Debounce = 200 * time.Millisecond
	}
	s := &Server{
		opts: opts, out: w, docs: map[string]string{}, cache: map[string]*analysis{},
		timers: map[string]*time.Timer{}, lythDiags: map[string][]Diagnostic{}, vetDiags: map[string][]Diagnostic{},
	}
	in := bufio.NewReader(r)
	for {
		m, err := readMessage(in)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Method == "exit" {
			return nil
		}
		s.dispatch(m)
	}
}

func (s *Server) send(v any) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = writeMessage(s.out, v)
}

func (s *Server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *Server) respond(id json.RawMessage, result any, err error) {
	if err != nil {
		e, ok := err.(*rpcError)
		if !ok {
			e = &rpcError{Code: codeRequestFailed, Message: err.Error()}
		}
		s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
		return
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) logf(format string, args ...any) {
	s.notify("window/logMessage", map[string]any{"type": 3, "message": fmt.Sprintf(format, args...)})
}

func (s *Server) dispatch(m *message) {
	if m.ID == nil {
		s.notification(m.Method, m.Params)
		return
	}
	var result any
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.logf("lyth lsp: %s failed: %v\n%s", m.Method, r, debug.Stack())
				err = &rpcError{Code: codeInternal, Message: fmt.Sprintf("internal error: %v", r)}
			}
		}()
		result, err = s.request(m.Method, m.Params)
	}()
	s.respond(m.ID, result, err)
}

func (s *Server) request(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.initialize(params), nil
	case "shutdown":
		s.mu.Lock()
		s.shutdown = true
		s.mu.Unlock()
		return nil, nil
	case "textDocument/hover":
		return s.hover(params)
	case "textDocument/definition", "textDocument/declaration", "textDocument/typeDefinition", "textDocument/implementation":
		return s.definition(method, params)
	case "textDocument/references":
		return s.references(params)
	case "textDocument/documentHighlight":
		return s.highlight(params)
	case "textDocument/prepareRename":
		return s.prepareRename(params)
	case "textDocument/rename":
		return s.rename(params)
	case "textDocument/documentSymbol":
		return s.documentSymbols(params)
	case "workspace/symbol":
		return s.workspaceSymbols(params)
	case "textDocument/completion":
		return s.completion(params)
	case "completionItem/resolve":
		var item any
		_ = json.Unmarshal(params, &item)
		return item, nil
	case "textDocument/signatureHelp":
		return s.signatureHelp(params)
	case "textDocument/inlayHint":
		return s.inlayHints(params)
	case "textDocument/codeAction":
		return s.codeActions(params)
	case "textDocument/formatting":
		return s.formatting(params)
	case "workspace/executeCommand":
		return s.executeCommand(params)
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "unsupported method " + method}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		InitializationOptions struct {
			Vet *bool `json:"vet"`
		} `json:"initializationOptions"`
	}
	_ = json.Unmarshal(params, &p)
	if p.InitializationOptions.Vet != nil {
		s.opts.Vet = *p.InitializationOptions.Vet
	}
	return map[string]any{
		"capabilities": map[string]any{
			"textDocumentSync":           map[string]any{"openClose": true, "change": 1, "save": map[string]any{"includeText": false}},
			"hoverProvider":              true,
			"definitionProvider":         true,
			"declarationProvider":        true,
			"typeDefinitionProvider":     true,
			"referencesProvider":         true,
			"documentHighlightProvider":  true,
			"renameProvider":             map[string]any{"prepareProvider": true},
			"documentSymbolProvider":     true,
			"workspaceSymbolProvider":    true,
			"completionProvider":         map[string]any{"triggerCharacters": []string{".", "\""}, "resolveProvider": false},
			"signatureHelpProvider":      map[string]any{"triggerCharacters": []string{"(", ","}, "retriggerCharacters": []string{","}},
			"inlayHintProvider":          true,
			"codeActionProvider":         map[string]any{"codeActionKinds": []string{"quickfix"}},
			"documentFormattingProvider": true,
			"executeCommandProvider":     map[string]any{"commands": []string{"lyth.showGo"}},
		},
		"serverInfo": map[string]any{"name": "lyth", "version": s.opts.Version},
	}
}

// ---------------------------------------------------------------------------
// documents

func (s *Server) notification(method string, params json.RawMessage) {
	defer s.survive(method)
	var p struct {
		TextDocument struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case "textDocument/didOpen":
		path := s.open(p.TextDocument.URI)
		s.setDoc(path, p.TextDocument.Text)
		s.check(path)
		s.vet(path)
	case "textDocument/didChange":
		if len(p.ContentChanges) == 0 {
			return
		}
		path := canonical(uriToPath(p.TextDocument.URI))
		s.setDoc(path, p.ContentChanges[len(p.ContentChanges)-1].Text)
		s.schedule(path)
	case "textDocument/didSave":
		path := canonical(uriToPath(p.TextDocument.URI))
		s.vet(path)
	case "textDocument/didClose":
		path := canonical(uriToPath(p.TextDocument.URI))
		s.mu.Lock()
		delete(s.docs, path)
		s.gen++
		s.mu.Unlock()
	}
}

// open remembers the editor's spelling of a document's directory, which may
// reach it through a symlink (macOS's /tmp is /private/tmp): locations sent
// back use the editor's path, or the editor would open the file twice.
func (s *Server) open(uri string) string {
	editor := uriToPath(uri)
	path := canonical(editor)
	ed, real := filepath.Dir(editor), filepath.Dir(path)
	for filepath.Base(ed) == filepath.Base(real) && ed != filepath.Dir(ed) && real != filepath.Dir(real) {
		ed, real = filepath.Dir(ed), filepath.Dir(real)
	}
	if ed != real {
		s.mu.Lock()
		known := false
		for _, a := range s.aliases {
			known = known || a[0] == real
		}
		if !known {
			s.aliases = append(s.aliases, [2]string{real, ed})
		}
		s.mu.Unlock()
	}
	return path
}

func (s *Server) setDoc(path, text string) {
	s.mu.Lock()
	s.docs[path] = text
	s.gen++
	s.mu.Unlock()
}

// uri returns the URI the editor knows a file by.
func (s *Server) uri(path string) string {
	s.mu.Lock()
	var best [2]string
	for _, a := range s.aliases {
		if (path == a[0] || strings.HasPrefix(path, a[0]+string(filepath.Separator))) && len(a[0]) > len(best[0]) {
			best = a
		}
	}
	s.mu.Unlock()
	if best[0] != "" {
		// filepath.Join, not concatenation: the editor's side may be the root
		// (/private/var reached as /var gives the alias /private -> /).
		path = filepath.Join(best[1], strings.TrimPrefix(path, best[0]))
	}
	return pathToURI(path)
}

// canonical resolves symlinks, as the compiler does for the files it reads.
func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// A file not saved yet: resolve its directory.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// overlay returns the open documents' contents. The caller holds s.mu.
func (s *Server) overlayLocked() map[string][]byte {
	o := make(map[string][]byte, len(s.docs))
	for p, t := range s.docs {
		o[p] = []byte(t)
	}
	return o
}

// analyze returns an up-to-date analysis of the unit of path.
func (s *Server) analyze(path string, wholeModule bool) *analysis {
	u := unitFor(path, wholeModule)
	s.mu.Lock()
	if a := s.cache[u.key]; a != nil && a.gen == s.gen {
		s.mu.Unlock()
		return a
	}
	gen, overlay := s.gen, s.overlayLocked()
	s.mu.Unlock()
	var a *analysis
	s.compiling(func() { a = compile(u, overlay) })
	a.gen = gen
	s.mu.Lock()
	s.cache[u.key] = a
	s.mu.Unlock()
	return a
}

// analyzeWith compiles path's unit with one document replaced, for
// completion and signature help on code that does not parse yet.
func (s *Server) analyzeWith(path, text string) *analysis {
	u := unitFor(path, false)
	s.mu.Lock()
	overlay := s.overlayLocked()
	s.mu.Unlock()
	overlay[path] = []byte(text)
	s.compileMu.Lock()
	defer s.compileMu.Unlock()
	return compile(u, overlay)
}

func (s *Server) doc(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.docs[path]
	if !ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false
		}
		return string(data), true
	}
	return t, ok
}

// ---------------------------------------------------------------------------
// diagnostics

// schedule checks path once the edits pause.
func (s *Server) schedule(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[path]; t != nil {
		t.Stop()
	}
	s.timers[path] = time.AfterFunc(s.opts.Debounce, func() { s.check(path) })
}

// check analyzes path's package and publishes the diagnostics of its files.
// compiling runs fn holding the compilation lock, which a panic in fn
// releases too: the next request must not wait forever.
func (s *Server) compiling(fn func()) {
	s.compileMu.Lock()
	defer s.compileMu.Unlock()
	fn()
}

// survive logs a panic of a background task instead of ending the server:
// one bad edit must not take the editor's language support down.
func (s *Server) survive(task string) {
	if r := recover(); r != nil {
		s.logf("lyth lsp: %s failed: %v\n%s", task, r, debug.Stack())
	}
}

func (s *Server) check(path string) {
	defer s.survive("checking " + path)
	a := s.analyze(path, false)
	byFile := map[string][]Diagnostic{}
	for p := range a.files {
		byFile[p] = nil
	}
	byFile[path] = nil
	if a.sess != nil {
		for _, d := range a.sess.Sink.All() {
			file := path
			if d.Span.IsValid() {
				file = d.Span.File.Path
			}
			if _, ok := byFile[file]; !ok && file != path {
				continue
			}
			byFile[file] = append(byFile[file], s.diagnostic(d, "lyth"))
		}
	}
	if a.err != nil {
		byFile[path] = append(byFile[path], Diagnostic{Severity: 1, Source: "lyth", Message: a.err.Error()})
	}
	s.mu.Lock()
	for f, list := range byFile {
		if _, open := s.docs[f]; open || f == path || len(list) > 0 || len(s.lythDiags[f]) > 0 {
			s.lythDiags[f] = list
		}
	}
	s.mu.Unlock()
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	s.publish(files)
}

// publish sends the Lyth and vet diagnostics of files.
func (s *Server) publish(files []string) {
	sort.Strings(files)
	for _, f := range files {
		s.mu.Lock()
		_, lyth := s.lythDiags[f]
		_, vet := s.vetDiags[f]
		list := append(append([]Diagnostic{}, s.lythDiags[f]...), s.vetDiags[f]...)
		s.mu.Unlock()
		if !lyth && !vet {
			continue
		}
		s.notify("textDocument/publishDiagnostics", map[string]any{"uri": s.uri(f), "diagnostics": list})
	}
}

// diagnostic converts a compiler diagnostic. Notes and hints are part of
// the message; hints with an edit are also quick fixes.
func (s *Server) diagnostic(d *diag.Diagnostic, src string) Diagnostic {
	out := Diagnostic{Severity: severity(d.Severity), Code: string(d.Code), Source: src}
	msg := d.Message
	if d.Label != "" {
		msg += "\n" + d.Label
	}
	for _, n := range d.Notes {
		msg += "\nnote: " + n
	}
	var fixes []fix
	for _, h := range d.Hints {
		msg += "\nhint: " + h.Message
		if h.Edit != nil && h.Edit.Span.IsValid() {
			fixes = append(fixes, fix{Title: capitalize(h.Message), Edits: []TextEdit{{Range: spanRange(h.Edit.Span), NewText: h.Edit.NewText}}})
		}
	}
	out.Message = msg
	if d.Span.IsValid() {
		out.Range = spanRange(d.Span)
	}
	for _, r := range d.Related {
		if r.Span.IsValid() {
			out.RelatedInformation = append(out.RelatedInformation, DiagnosticRelatedInformation{
				Location: Location{URI: s.uri(r.Span.File.Path), Range: spanRange(r.Span)}, Message: r.Label,
			})
		}
	}
	if len(fixes) > 0 {
		out.Data = &diagnosticData{Fixes: fixes}
	}
	return out
}

func severity(s diag.Severity) int {
	switch s {
	case diag.Error:
		return 1
	case diag.Warning:
		return 2
	}
	return 3
}

// spanRange converts a span of a source file.
func spanRange(sp source.Span) Range {
	return newLineIndex(sp.File.Content).rangeOf(sp.Start, sp.End)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// vet runs go vet on path's package in the background, after a clean
// compile, and publishes its findings on the .lyth lines.
func (s *Server) vet(path string) {
	if !s.opts.Vet {
		return
	}
	go func() {
		defer s.survive("go vet")
		s.vetMu.Lock()
		defer s.vetMu.Unlock()
		u := unitFor(path, false)
		s.notify("$/progress", map[string]any{"token": "lyth-vet", "value": map[string]any{"kind": "begin", "title": "go vet"}})
		defer s.notify("$/progress", map[string]any{"token": "lyth-vet", "value": map[string]any{"kind": "end"}})
		cfg := &build.Config{WorkDir: u.workDir, Stdout: io.Discard, Stderr: io.Discard}
		var sess *build.Session
		var err error
		s.compiling(func() { sess, err = build.Compile(cfg, u.target) })
		if err != nil || sess == nil || sess.Sink.HasErrors() {
			return // the check reports these
		}
		before := len(sess.Sink.All())
		var pkgs []string
		if sess.Mod.Synthetic {
			pkgs = []string{"."}
		} else {
			for _, p := range sess.Targets {
				pkgs = append(pkgs, p.Dir.ImportPath)
			}
		}
		if _, err := sess.GoVet(pkgs); err != nil {
			s.logf("go vet: %v", err)
			return
		}
		byFile := map[string][]Diagnostic{}
		for _, p := range sess.Targets {
			for _, f := range p.Files {
				byFile[f.Path] = nil
			}
		}
		for _, d := range sess.Sink.All()[before:] {
			if d.Span.IsValid() {
				byFile[d.Span.File.Path] = append(byFile[d.Span.File.Path], s.diagnostic(d, "vet"))
			}
		}
		s.mu.Lock()
		files := make([]string, 0, len(byFile))
		for f, list := range byFile {
			if len(list) > 0 || len(s.vetDiags[f]) > 0 {
				s.vetDiags[f] = list
				files = append(files, f)
			}
		}
		s.mu.Unlock()
		s.publish(files)
	}()
}

var (
	gorootOnce sync.Once
	gorootDir  string
)

// goroot is the Go toolchain's root, for positions in the standard library.
func goroot() string {
	gorootOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err == nil {
			gorootDir = strings.TrimSpace(string(out))
		}
	})
	return gorootDir
}
