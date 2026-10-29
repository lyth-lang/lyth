package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// The Lyth version a module is written for (ADR 0054): a //lyth:version
// comment in its go.mod, which the go command keeps. A module is checked with
// the rules of the version it declares; one without the line is written for
// 1.0. Before 1.0 nothing depends on the version, which is only recorded.

// LangVersion is the Lyth version this lyth compiles: the major and minor of
// its release (cmd/lyth's devVersion, which a test keeps in step).
const LangVersion = "0.9"

const langDirective = "//lyth:version"

// LythDirective returns the version of go.mod's //lyth:version line ("1.0")
// and its line (1-based), or "" and 0 when there is none. A line whose
// version is not major.minor, or a second line, is an error.
func LythDirective(gomod string) (string, int, error) {
	v, at := "", 0
	for i, line := range strings.Split(gomod, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), langDirective)
		if !ok || rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			continue
		}
		if at > 0 {
			return v, i + 1, fmt.Errorf("go.mod has a second %s line (the first is line %d)", langDirective, at)
		}
		v, at = strings.TrimSpace(rest), i+1
		if _, _, ok := parseLang(v); !ok {
			return v, at, fmt.Errorf("go.mod has a malformed %s line: want a version such as %s", langDirective, LangVersion)
		}
	}
	return v, at, nil
}

// parseLang parses a language version, major.minor.
func parseLang(v string) (major, minor int, ok bool) {
	a, b, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || major < 0 || minor < 0 || a[0] == '+' || b[0] == '+' {
		return 0, 0, false
	}
	return major, minor, true
}

// langBefore reports whether version a is older than b; both are valid.
func langBefore(a, b string) bool {
	am, an, _ := parseLang(a)
	bm, bn, _ := parseLang(b)
	return am < bm || am == bm && an < bn
}

// newerLang says why a module whose go.mod is gomod cannot be compiled with
// this lyth: its //lyth:version line names a newer Lyth, or is malformed. It
// returns "" when the module can be, and the line.
func newerLang(gomod string) (string, int) {
	v, line, err := LythDirective(gomod)
	switch {
	case err != nil:
		return "its go.mod has a " + strings.TrimPrefix(err.Error(), "go.mod has a "), line
	case v != "" && langBefore(LangVersion, v):
		return fmt.Sprintf("it is written for Lyth %s, newer than this lyth (%s)", v, LangVersion), line
	}
	return "", 0
}

// checkLythDirective reports, as E0602 at the line, a module compiled with
// the program whose go.mod declares a Lyth version this lyth does not know,
// or declares it badly. Dependencies from the module cache are settled with
// their packages (settleExternal).
func (s *Session) checkLythDirective() bool {
	if s.Mod.Synthetic {
		return true
	}
	ok := true
	for _, root := range append([]string{s.Mod.Root}, s.withModules...) {
		path := filepath.Join(root, "go.mod")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, line, err := LythDirective(string(data))
		if err == nil && (v == "" || !langBefore(LangVersion, v)) {
			continue
		}
		f := source.NewFile(path, displayName(path, s.cfg.WorkDir), data)
		start := f.LineStart(line)
		span := source.Span{File: f, Start: start, End: start + len(strings.TrimRight(f.LineText(line), "\r"))}
		switch {
		case err != nil && strings.Contains(err.Error(), "second"):
			s.Sink.Errorf(diag.EToolchain, span, "%v", err).WithHint("keep one %s line", langDirective)
		case err != nil:
			s.Sink.Errorf(diag.EToolchain, span, "%v", err).
				WithHint("write %s %s, or remove the line (a module without it is written for 1.0)", langDirective, LangVersion)
		default:
			s.Sink.Errorf(diag.EToolchain, span, "the module is written for Lyth %s, newer than this lyth (%s)", v, LangVersion).
				WithNote("a module is checked with the rules of the Lyth version it declares, and this lyth knows them up to %s", LangVersion).
				WithHint("upgrade lyth (lyth version prints the installed one)")
		}
		ok = false
	}
	return ok
}

// SetLythDirective writes version v into the //lyth:version line of the
// go.mod at path, adding the line after the go directive when there is none,
// and returns the version it declared before ("" for none). It only raises
// the version, and only up to LangVersion.
func SetLythDirective(path, v string) (string, error) {
	if _, _, ok := parseLang(v); !ok {
		return "", fmt.Errorf("-lang=%s: want a Lyth version such as %s", v, LangVersion)
	}
	if langBefore(LangVersion, v) {
		return "", fmt.Errorf("-lang=%s: this lyth knows Lyth up to %s", v, LangVersion)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	old, line, err := LythDirective(string(data))
	if err != nil {
		return old, fmt.Errorf("%s:%d: %v", path, line, err)
	}
	if old != "" && langBefore(v, old) {
		return old, fmt.Errorf("-lang=%s: go.mod declares Lyth %s; lyth fix only raises the version", v, old)
	}
	if old == v {
		return old, nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	directive := langDirective + " " + v
	if line > 0 {
		eol := lines[line-1][len(strings.TrimRight(lines[line-1], "\r\n")):]
		lines[line-1] = directive + eol
	} else {
		mf, err := modfile.Parse(path, data, nil)
		if err != nil {
			return "", err
		}
		at := len(lines) // after the module directive, or at the end
		if mf.Go != nil {
			at = mf.Go.Syntax.End.Line
		} else if mf.Module != nil {
			at = mf.Module.Syntax.End.Line
		}
		if at == len(lines) && !strings.HasSuffix(string(data), "\n") && len(data) > 0 {
			lines[at-1] += "\n"
		}
		lines = append(lines[:at], append([]string{"\n", directive + "\n"}, lines[at:]...)...)
	}
	info, err := os.Stat(path)
	if err != nil {
		return old, err
	}
	return old, os.WriteFile(path, []byte(strings.Join(lines, "")), info.Mode().Perm())
}
