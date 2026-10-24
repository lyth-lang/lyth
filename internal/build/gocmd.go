package build

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// buildEvent is the go build -json event format (go help buildjson).
type buildEvent struct {
	ImportPath string
	Action     string
	Output     string
}

func (s *Session) goCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Dir = s.Mod.Root
	cmd.Env = append(append(os.Environ(), "PWD="+s.Mod.Root), s.cfg.Env...)
	return cmd
}

func (s *Session) overlayArgs() []string {
	var args []string
	if s.Overlay != "" {
		args = append(args, "-overlay="+s.Overlay)
	}
	if s.ModFile != "" {
		args = append(args, "-modfile="+s.ModFile)
	}
	return args
}

// GoBuild runs `go build` on the given packages (import paths). It reports
// toolchain errors as diagnostics and returns whether the build succeeded.
func (s *Session) GoBuild(output string, pkgs []string, flags []string) (bool, error) {
	args := []string{"build", "-json"}
	args = append(args, s.overlayArgs()...)
	if len(s.cfg.Tags) > 0 {
		args = append(args, "-tags="+strings.Join(s.cfg.Tags, ","))
	}
	if output != "" {
		args = append(args, "-o", output)
	}
	args = append(args, flags...)
	args = append(args, pkgs...)
	cmd := s.goCmd(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	s.phase("go build", start)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false, fmt.Errorf("running go build: %w", err)
	}
	var out strings.Builder
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var ev buildEvent
		if json.Unmarshal(line, &ev) == nil && ev.Action == "build-output" {
			out.WriteString(ev.Output)
		} else if len(line) > 0 && line[0] != '{' {
			out.Write(line)
			out.WriteByte('\n')
		}
	}
	out.WriteString(stderr.String())
	s.mapToolchainOutput(out.String())
	if !s.Sink.HasErrors() {
		s.Sink.Errorf(diag.EGoLink, source.Span{}, "go build failed").WithNote("%s", strings.TrimSpace(out.String()))
	}
	return false, nil
}

// mapToolchainOutput turns go command output into diagnostics. Positions
// already refer to .lyth files through //line directives; positions inside
// generated files are mapped through the line map.
func (s *Session) mapToolchainOutput(text string) {
	var pending []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "# ") {
			continue
		}
		file, ln, col, msg, ok := parsePosLine(trimmed)
		if !ok {
			pending = append(pending, trimmed)
			continue
		}
		span, mapping := s.resolvePos(file, ln, col)
		code := diag.EGoCompile
		d := s.Sink.Errorf(code, span, "%s", check.TranslateGoMessage(msg))
		d.Cause = &diag.GoCause{Message: msg, Position: fmt.Sprintf("%s:%d:%d", file, ln, col), Mapping: mapping}
		if mapping != "exact" && mapping != "line" {
			d.WithNote("location approximate: %s", trimmed)
		}
	}
	if len(pending) > 0 {
		joined := strings.Join(pending, "\n")
		code := diag.EGoCompile
		if strings.Contains(joined, "ld:") || strings.Contains(joined, "link:") || strings.Contains(joined, "undefined reference") || strings.Contains(joined, "relocation") {
			code = diag.EGoLink
		}
		s.Sink.Errorf(code, source.Span{}, "go toolchain reported an error").WithNote("%s", joined)
	}
}

// parsePosLine parses "file:line:col: message" or "file:line: message",
// accepting only .lyth and .go files so arbitrary text is never misread.
func parsePosLine(line string) (file string, ln, col int, msg string, ok bool) {
	for _, ext := range []string{".lyth:", ".go:"} {
		i := strings.Index(line, ext)
		if i < 0 {
			continue
		}
		file = line[:i+len(ext)-1]
		rest := line[i+len(ext):]
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) < 2 {
			return "", 0, 0, "", false
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", 0, 0, "", false
		}
		ln = n
		if c, err := strconv.Atoi(parts[1]); err == nil && len(parts) == 3 {
			col = c
			msg = strings.TrimSpace(parts[2])
		} else {
			msg = strings.TrimSpace(strings.Join(parts[1:], ":"))
		}
		file = strings.TrimPrefix(file, "vet: ")
		return file, ln, col, msg, true
	}
	return "", 0, 0, "", false
}

// resolvePos maps a toolchain position to a Lyth span.
func (s *Session) resolvePos(file string, line, col int) (source.Span, string) {
	candidates := []string{file}
	if !filepath.IsAbs(file) {
		candidates = []string{filepath.Join(s.Mod.Root, file), filepath.Join(s.cfg.WorkDir, file)}
	}
	var byBase []*source.File
	for _, p := range s.Packages {
		for _, f := range p.Files {
			for _, c := range candidates {
				if f.Path == filepath.Clean(c) {
					return lineSpan(f, line, col), "line"
				}
			}
			if filepath.Base(f.Path) == filepath.Base(file) {
				byBase = append(byBase, f)
			}
		}
	}
	if len(byBase) == 1 && strings.HasSuffix(file, ".lyth") {
		return lineSpan(byBase[0], line, col), "line"
	}
	abs := filepath.Clean(candidates[0])
	if m := s.Maps[abs]; m != nil {
		if nl := m.LookupLine(line); nl > 0 {
			start := m.Src.LineStart(nl)
			return source.Span{File: m.Src, Start: start, End: start}, "line"
		}
		return source.Span{File: m.Src}, "approximate"
	}
	return source.Span{}, "none"
}

// GoRun builds the package to a temporary binary and runs it, forwarding
// standard streams. It returns the program's exit code.
func (s *Session) GoRun(pkg string, args []string, flags []string) (int, error) {
	binDir := filepath.Join(s.Mod.Root, ".lyth", "bin")
	if s.Mod.Synthetic {
		binDir = filepath.Join(s.Mod.Root, "bin")
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return 1, err
	}
	name := filepath.Base(pkg)
	if name == "." || name == "" {
		name = "main"
	}
	bin := filepath.Join(binDir, name)
	ok, err := s.GoBuild(bin, []string{pkg}, flags)
	if err != nil || !ok {
		return 1, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, s.cfg.Stdout, s.cfg.Stderr
	cmd.Dir = s.cfg.WorkDir
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// GoVet runs go vet with the overlay.
func (s *Session) GoVet(pkgs []string) (bool, error) {
	args := append([]string{"vet"}, s.overlayArgs()...)
	args = append(args, pkgs...)
	cmd := s.goCmd(args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false, err
	}
	s.mapToolchainOutput(out.String())
	return false, nil
}

// GoTest runs go test with the overlay on the given package patterns,
// resolved from the directory lyth was invoked in, like go test itself.
// Output streams to the configured writers; test failures already point at
// .lyth lines through //line directives. It returns go test's exit status.
func (s *Session) GoTest(patterns, flags []string) (int, error) {
	args := append([]string{"test"}, s.overlayArgs()...)
	if len(s.cfg.Tags) > 0 {
		args = append(args, "-tags="+strings.Join(s.cfg.Tags, ","))
	}
	args = append(args, patterns...)
	args = append(args, flags...)
	cmd := s.goCmd(args...)
	cmd.Dir = s.cfg.WorkDir
	cmd.Env = append(append(os.Environ(), "PWD="+s.cfg.WorkDir), s.cfg.Env...)
	cmd.Stdout, cmd.Stderr = s.cfg.Stdout, s.cfg.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// WriteInTree writes generated files next to their Lyth sources (lyth gen).
// It refuses to overwrite files that were not generated by lyth.
func (s *Session) WriteInTree() ([]string, error) {
	var written []string
	for _, p := range s.Packages {
		if p.Dir.Std {
			continue // the toolchain's library, not the module's code
		}
		for _, g := range p.Generated {
			path := filepath.Join(p.Dir.Dir, g.Name)
			if old, err := os.ReadFile(path); err == nil && !bytes.HasPrefix(old, []byte("// Code generated by lyth")) {
				return written, fmt.Errorf("refusing to overwrite %s: it was not generated by lyth", path)
			}
			if err := writeIfChanged(path, g.Code); err != nil {
				return written, err
			}
			written = append(written, path)
		}
	}
	return written, nil
}

func lineSpan(f *source.File, line, col int) source.Span {
	start := f.LineStart(line)
	if col > 0 {
		start += col - 1
	}
	if start > len(f.Content) {
		start = len(f.Content)
	}
	return source.Span{File: f, Start: start, End: start}
}
