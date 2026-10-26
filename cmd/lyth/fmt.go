package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lyth-lang/lyth/internal/format"
)

// cmdFmt formats .lyth files in place, as go fmt does, or lists (-l) or
// shows (-d) the files whose layout differs. With no paths it formats the
// current directory and the directories below it. -stdin formats standard
// input to standard output, for editors.
func cmdFmt(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.Bool("l", false, "list the files whose formatting differs, without changing them")
	diff := fs.Bool("d", false, "print the changes as diffs, without changing the files")
	fromStdin := fs.Bool("stdin", false, "format standard input and write the result to standard output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fromStdin {
		src, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
			return 1
		}
		out, err := format.Source("<stdin>", src)
		if err != nil {
			fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
			return 1
		}
		stdout.Write(out)
		return 0
	}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	files, err := lythFiles(paths)
	if err != nil {
		fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
		return 1
	}
	code := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
			code = 1
			continue
		}
		out, err := format.Source(path, src)
		if err != nil {
			fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
			code = 1
			continue
		}
		if bytes.Equal(src, out) {
			continue
		}
		switch {
		case *diff:
			fmt.Fprint(stdout, unifiedDiff(path, string(src), string(out)))
		case *list:
			fmt.Fprintln(stdout, path)
		default:
			info, err := os.Stat(path)
			if err == nil {
				err = os.WriteFile(path, out, info.Mode().Perm())
			}
			if err != nil {
				fmt.Fprintf(stderr, "lyth fmt: %v\n", err)
				code = 1
				continue
			}
			fmt.Fprintln(stdout, path)
		}
	}
	return code
}

// lythFiles expands paths (files, directories, and dir/... patterns) to the
// .lyth files they hold, skipping the directories the go command ignores
// (testdata, vendor, and names starting with . or _).
func lythFiles(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		p = strings.TrimSuffix(strings.TrimSuffix(p, "..."), "/")
		if p == "" {
			p = "."
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if path != p && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".lyth") && !strings.HasPrefix(name, ".") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// unifiedDiff returns a unified diff of two versions of a file.
func unifiedDiff(path, a, b string) string {
	x, y := strings.SplitAfter(a, "\n"), strings.SplitAfter(b, "\n")
	if len(x) > 0 && x[len(x)-1] == "" {
		x = x[:len(x)-1]
	}
	if len(y) > 0 && y[len(y)-1] == "" {
		y = y[:len(y)-1]
	}
	// Longest common subsequence of lines; files are formatted one at a
	// time, so the quadratic table is small enough.
	if len(x)*len(y) > 50_000_000 {
		return fmt.Sprintf("--- %s\n+++ %s (formatted)\n@@ the file differs; it is too large to diff @@\n", path, path)
	}
	lcs := make([][]int32, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int // line numbers (1-based) in a and b
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, op{' ', x[i], i + 1, j + 1})
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', x[i], i + 1, j + 1})
			i++
		default:
			ops = append(ops, op{'+', y[j], i + 1, j + 1})
			j++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s (formatted)\n", path, path)
	const ctx = 3
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(0, k-ctx)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(len(ops), end+ctx)
				break
			}
			end = run
		}
		na, nb := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				na++
			}
			if o.kind != '-' {
				nb++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", ops[start].ai, na, ops[start].bi, nb)
		for _, o := range ops[start:end] {
			text := o.text
			if !strings.HasSuffix(text, "\n") {
				text += "\n\\ No newline at end of file\n"
			}
			out.WriteString(string(o.kind) + text)
		}
		k = end
	}
	return out.String()
}
