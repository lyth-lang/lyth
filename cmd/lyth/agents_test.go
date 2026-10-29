package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/docs"
	"github.com/lyth-lang/lyth/internal/build"
)

func TestGuideListsEveryTopic(t *testing.T) {
	guide, err := fs.ReadFile(docs.FS, guideFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range guideTopics {
		if !strings.Contains(string(guide), "- `"+topic.name+"` — ") {
			t.Errorf("%s does not list the topic %q", guideFile, topic.name)
		}
		var out, errOut bytes.Buffer
		if code := run([]string{"guide", topic.name}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "# ") {
			t.Errorf("lyth guide %s: exit %d, %q", topic.name, code, errOut.String())
		}
	}
}

func TestGuide(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"guide"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "This is the guide to lyth "+version+", the Lyth installed here.\n\n# Writing Lyth\n") {
		t.Errorf("unexpected start:\n%.200s", out.String())
	}
	out.Reset()
	if code := run([]string{"guide", "topics"}, &out, &errOut); code != 0 || strings.Count(out.String(), "\n") != len(guideTopics) {
		t.Errorf("lyth guide topics: exit %d\n%s", code, out.String())
	}
	errOut.Reset()
	if code := run([]string{"guide", "nope"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "the topics are tour, go,") {
		t.Errorf("lyth guide nope: exit %d, %q", code, errOut.String())
	}
}

// TestAgentsMDHasNoLanguageRules keeps Lyth code out of the AGENTS.md that
// lyth init writes: it outlives the Lyth version that wrote it, so it may
// only point at commands.
func TestAgentsMDHasNoLanguageRules(t *testing.T) {
	for _, code := range []string{"func ", ":=", "throws", "${", "`struct", "```"} {
		if strings.Contains(agentsMD, code) {
			t.Errorf("AGENTS.md contains %q", code)
		}
	}
	for _, cmd := range []string{"`lyth guide`", "`lyth check`", "`lyth fix`"} {
		if !strings.Contains(agentsMD, cmd) {
			t.Errorf("AGENTS.md does not mention %s", cmd)
		}
	}
}

func TestInit(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go command")
	}
	dir := filepath.Join(t.TempDir(), "hello")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"init", "example.com/hello"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for name, want := range map[string]string{
		"go.mod":     "module example.com/hello\n",
		"main.lyth":  helloLyth,
		"AGENTS.md":  agentsMD,
		"CLAUDE.md":  claudeMD,
		".gitignore": ".lyth/\n",
	} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if !strings.HasPrefix(string(got), want) {
			t.Errorf("%s:\n%s\nwant it to start with:\n%s", name, got, want)
		}
	}

	if got, _ := os.ReadFile("go.mod"); !strings.HasSuffix(string(got), "\n\n//lyth:version "+build.LangVersion+"\n") {
		t.Errorf("go.mod does not declare the Lyth version:\n%s", got)
	}

	// A second init keeps what exists, and adds what is missing.
	os.WriteFile("AGENTS.md", []byte("mine\n"), 0o644)
	os.Remove("CLAUDE.md")
	out.Reset()
	if code := run([]string{"init"}, &out, &errOut); code != 0 {
		t.Fatalf("second init: exit %d: %s", code, errOut.String())
	}
	if out.String() != "lyth: created CLAUDE.md\n" {
		t.Errorf("second init printed %q", out.String())
	}
	if got, _ := os.ReadFile("AGENTS.md"); string(got) != "mine\n" {
		t.Errorf("AGENTS.md was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(".gitignore"); string(got) != ".lyth/\n" {
		t.Errorf(".gitignore: %q", got)
	}
	if code := run([]string{"init", "other.com/x"}, &out, &errOut); code != 1 {
		t.Errorf("init with another module path: exit %d", code)
	}
}

// TestLangVersionFollowsDevVersion keeps the Lyth version a module declares
// in step with the version of this checkout.
func TestLangVersionFollowsDevVersion(t *testing.T) {
	if !strings.HasPrefix(devVersion, build.LangVersion+".") {
		t.Errorf("build.LangVersion is %s but devVersion is %s: bump LangVersion with devVersion", build.LangVersion, devVersion)
	}
}

func TestFixLang(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go command")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	write := func(gomod string) {
		t.Helper()
		if err := os.WriteFile("go.mod", []byte(gomod), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile("main.lyth", []byte("func main() {\n    println(\"hi\")\n}\n"), 0o644)
	gomod := func() string {
		data, _ := os.ReadFile("go.mod")
		return string(data)
	}
	for _, tc := range []struct {
		before, lang string
		code         int
		out, after   string
	}{
		{"module m\n\ngo 1.26\n", build.LangVersion, 0, "lyth: go.mod now declares Lyth " + build.LangVersion + "\n", "module m\n\ngo 1.26\n\n//lyth:version " + build.LangVersion + "\n"},
		{"module m\n\ngo 1.26\n\n//lyth:version 0.1\n\n// kept\n", build.LangVersion, 0, "lyth: raised the Lyth version in go.mod from 0.1 to " + build.LangVersion + "\n", "module m\n\ngo 1.26\n\n//lyth:version " + build.LangVersion + "\n\n// kept\n"},
		{"module m\n\ngo 1.26\n\n//lyth:version " + build.LangVersion + "\n", "0.1", 1, "", ""},
		{"module m\n\ngo 1.26\n", "99.0", 1, "", ""},
		{"module m\n\ngo 1.26\n", "1", 1, "", ""},
	} {
		write(tc.before)
		var out, errOut bytes.Buffer
		code := run([]string{"fix", "-lang=" + tc.lang}, &out, &errOut)
		if code != tc.code {
			t.Errorf("%q, -lang=%s: exit %d, want %d: %s", tc.before, tc.lang, code, tc.code, errOut.String())
			continue
		}
		if tc.code != 0 {
			if gomod() != tc.before {
				t.Errorf("-lang=%s changed go.mod after an error:\n%s", tc.lang, gomod())
			}
			continue
		}
		if out.String() != tc.out {
			t.Errorf("-lang=%s printed %q, want %q", tc.lang, out.String(), tc.out)
		}
		if gomod() != tc.after {
			t.Errorf("-lang=%s: go.mod is\n%s\nwant\n%s", tc.lang, gomod(), tc.after)
		}
	}
}
