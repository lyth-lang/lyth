#!/usr/bin/env python3
"""Measures how well a model writes Lyth, with and without help.

Every task in evals/tasks asks for a program. A run gives one task to Claude Code (`claude -p`)
in a fresh Lyth module, then builds the .lyth files it wrote in a clean module of its own and
compares the output with the task's expected.out. See evals/README.md.
"""

import argparse
import concurrent.futures
import datetime
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TASKS = ROOT / "evals" / "tasks"
RESULTS = ROOT / "evals" / "results"

# The module path of every project; tasks with packages import "app/<name>".
MODULE = "app"

# What the agent gets besides the task, per condition.
CONDITIONS = {
    "skill": "the Lyth skill is installed; it sends the agent to `lyth guide`",
    "agents-md": "the module has the AGENTS.md and CLAUDE.md that `lyth init` writes",
    "cli": "no help, but `lyth help` lists `lyth guide`",
    "bare": "no help, and `lyth guide` is hidden",
}
ONESHOT_CONDITIONS = {
    "skill": "the system prompt holds what `lyth guide` prints",
    "bare": "only the task",
}

# Appended to Claude Code's system prompt in agent runs.
AGENT_RULES = f"""\
The current directory is a Lyth module made with `lyth init {MODULE}`: go.mod is set up, and \
the program goes in .lyth files, the main package at the root (main.lyth) and other packages \
in subdirectories, imported as "{MODULE}/<dir>". Lyth is a programming language that compiles \
to Go; the `lyth` command builds and runs it.

This is an evaluation of writing Lyth. Stay in the current directory, and don't look for Lyth \
documentation, examples or source code anywhere else on this computer or online: work from \
what you know, the skills you have, and what the `lyth` command tells you. Write the program \
in Lyth, not Go: no .go files. The program must compute its output rather than print it \
literally. You are done when `lyth run` prints exactly what the task asks for.

Change files with the Write and Edit tools. Bash may run `lyth`, `go doc`, and commands that \
only read; anything else is denied."""

# The system prompt of one-shot runs, which have no tools.
ONESHOT_RULES = f"""\
You write programs in Lyth, a programming language that compiles to Go. Answer with the \
complete contents of every file of the program: main.lyth, and the .lyth files of any other \
package, in a directory named after it (imported as "{MODULE}/<dir>"). Put each file in its \
own code block whose first line is a comment with the file's path, like `// main.lyth`. Write \
Lyth, not Go, and compute the output rather than printing it literally."""

# `lyth` without `lyth guide`, for the "bare" runs.
BARE_LYTH = """#!/bin/sh
case "$1" in
  guide) echo "lyth: unknown command \\"guide\\"" >&2; exit 2 ;;
  help|--help|-help|-h) "{lyth}" "$@" | grep -v '^  lyth guide '; exit 0 ;;
  "") "{lyth}" 2>&1 | grep -v '^  lyth guide ' >&2; exit 2 ;;
esac
exec "{lyth}" "$@"
"""

# Go and Kotlin written where Lyth has its own form; counted, not failed. Matched in code
# without comments and strings.
FOREIGN = {
    # Go habits
    "func (r T)": r"\bfunc\s*\(\s*\w+\s+\*?\w+(?:\[[^\]]*\])?\s*\)",
    "range": r"\brange\b",
    "<- operator": r"<-(?!\s*chan\b)",
    "nil": r"\bnil\b",
    "if err != nil": r"\bif\s+err\s*!=\s*(?:nil|null)\b",
    "T{F: v}": r"\b[A-Z]\w*\{\s*[A-Z]\w*\s*:",
    "for ;;": r"\bfor\s+[^{\n]*;",
    "if init;": r"\bif\s+[^{\n]*:=[^{\n]*;",
    "[T C]": r"\[\s*[A-Z]\w*\s+(?:any|comparable|\w+\.\w+)\s*\]",
    # Kotlin habits
    "fun": r"\bfun\b",
    "val": r"\bval\b",
    "this": r"\bthis\b",
    "Int/String": r"(?<![.\w])(?:Int|Long|Double|Float|Boolean|String|Unit|Any)\b(?!\s*\()",
    "List<T>": r"\b(?:List|MutableList|Map|MutableMap|Set|Array)\s*<",
    "<T>": r"\b[A-Z]\w*<[A-Z]\w*(?:\s*,\s*[A-Z]\w*)*>",
    "listOf": r"\b(?:listOf|mutableListOf|mapOf|mutableMapOf|setOf|arrayOf)\s*\(",
    "for (x in": r"\bfor\s*\(\s*\w+(?:\s*,\s*\w+)*\s+in\b",
}
# Matched in code without comments, strings kept.
FOREIGN_IN_STRINGS = {"\"$name\"": r"\"(?:[^\"\\\n]|\\.)*(?<!\\)\$[A-Za-z_]"}

BUILD_COMMAND = re.compile(r"\blyth\s+(?:run|build|check|test|fix)\b")


class Task:
    def __init__(self, path: Path):
        self.name = path.name
        self.prompt = (path / "prompt.md").read_text()
        self.expected = (path / "expected.out").read_text()
        spec = tomllib.loads((path / "task.toml").read_text()) if (path / "task.toml").exists() else {}
        self.features = spec.get("features", [])
        self.requires = spec.get("requires", [])
        self.forbids = spec.get("forbids", [])
        self.files = spec.get("files", [])
        self.tests = spec.get("tests", 0)
        self.starter = path / "starter"
        self.solution = path / "solution"


def load_tasks(names):
    tasks = [Task(p) for p in sorted(TASKS.iterdir()) if (p / "prompt.md").exists()]
    if names:
        unknown = set(names) - {t.name for t in tasks}
        if unknown:
            sys.exit(f"unknown tasks: {', '.join(sorted(unknown))}")
        tasks = [t for t in tasks if t.name in names]
    return tasks


def lyth_files(src: Path) -> list:
    """The .lyth files of the module at `src`, relative to it, outside .lyth/ and hidden
    directories."""
    if not src.exists():
        return []
    out = []
    for p in src.rglob("*.lyth"):
        rel = p.relative_to(src)
        # .lyth/ itself matches, once a build has made it
        if p.is_file() and not any(part.startswith(".") for part in rel.parts):
            out.append(rel.as_posix())
    return sorted(out)


class Toolchain:
    """`lyth` built from this checkout, outside the repository, so the runs use this checkout's
    compiler without pointing at the repository and its docs."""

    def __init__(self, work: Path):
        self.bin = work / "bin"
        self.bin.mkdir()
        self.lyth = self.bin / "lyth"
        subprocess.run(["go", "build", "-o", str(self.lyth), "./cmd/lyth"], cwd=ROOT, check=True)
        self.bare_bin = work / "bin-bare"
        self.bare_bin.mkdir()
        (self.bare_bin / "lyth").write_text(BARE_LYTH.replace("{lyth}", str(self.lyth)))
        (self.bare_bin / "lyth").chmod(0o755)
        self.version = subprocess.run([self.lyth, "version"], capture_output=True, text=True).stdout.strip()
        self.guide = subprocess.run([self.lyth, "guide"], capture_output=True, text=True, cwd=work).stdout

    def env(self, hide_guide=False):
        env = {k: v for k, v in os.environ.items() if not k.startswith("LYTH_")}
        env["PATH"] = f"{self.bare_bin if hide_guide else self.bin}{os.pathsep}{env.get('PATH', '')}"
        env["NO_COLOR"] = "1"
        env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
        return env

    def project(self, parent: Path, starter: Path | None = None) -> Path:
        """A module made by `lyth init`, after the task's starter files (if any) were copied in,
        so it writes main.lyth only for tasks without them."""
        project = parent / MODULE
        project.mkdir(parents=True)
        if starter and starter.exists():
            shutil.copytree(starter, project, dirs_exist_ok=True)
        subprocess.run([self.lyth, "init", MODULE], cwd=project, env=self.env(), check=True, capture_output=True)
        return project

    def run(self, args, cwd: Path, timeout: int):
        try:
            return subprocess.run([self.lyth, *args], cwd=cwd, env=self.env(), stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            return subprocess.CompletedProcess(args, -1, "", f"timed out after {timeout}s")


def strip_code(code: str, strings: bool) -> str:
    """`code` with comments (and, when `strings` is set, the text of string, raw string and rune
    literals) blanked out, keeping its lines."""
    out, i, n = [], 0, len(code)
    spaces = lambda s: re.sub(r"[^\n]", " ", s)
    # a literal keeps its quotes, so `"a" || "b"` doesn't read as `||` after blanking
    blank = lambda s: s[0] + spaces(s[1:-1]) + s[-1] if len(s) > 1 and s[0] in "\"'`" else spaces(s)
    while i < n:
        c = code[i]
        if code.startswith("//", i):
            j = code.find("\n", i)
            j = n if j < 0 else j
            out.append(spaces(code[i:j]))
            i = j
        elif code.startswith("/*", i):
            j = code.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append(spaces(code[i:j]))
            i = j
        elif c == "`":
            j = code.find("`", i + 1)
            j = n if j < 0 else j + 1
            out.append(blank(code[i:j]) if strings else code[i:j])
            i = j
        elif c == '"':
            j = string_end(code, i)
            out.append(blank(code[i:j]) if strings else code[i:j])
            i = j
        elif c == "'" and re.match(r"'(?:\\.|\\u[0-9a-fA-F]{4}|[^\\'\n])'", code[i:]):
            j = i + re.match(r"'(?:\\.|\\u[0-9a-fA-F]{4}|[^\\'\n])'", code[i:]).end()
            out.append(blank(code[i:j]) if strings else code[i:j])
            i = j
        else:
            out.append(c)
            i += 1
    return "".join(out)


def string_end(code: str, i: int) -> int:
    """The index after the `"..."` literal at `i`, whose `${...}` templates may hold strings."""
    j, n = i + 1, len(code)
    while j < n:
        if code[j] == "\\":
            j += 2
        elif code[j] == '"' or code[j] == "\n":
            return j + 1
        elif code.startswith("${", j):
            depth, j = 1, j + 2
            while j < n and depth:
                if code[j] == '"':
                    j = string_end(code, j)
                    continue
                depth += {"{": 1, "}": -1}.get(code[j], 0)
                j += 1
        else:
            j += 1
    return n


def foreign_isms(code: str) -> dict:
    """Go and Kotlin spellings in `code`, counted by kind."""
    bare, with_strings = strip_code(code, strings=True), strip_code(code, strings=False)
    found = {name: len(re.findall(pattern, bare, re.M)) for name, pattern in FOREIGN.items()}
    found.update({name: len(re.findall(pattern, with_strings, re.M)) for name, pattern in FOREIGN_IN_STRINGS.items()})
    return {name: n for name, n in found.items() if n}


def normalized(text: str) -> str:
    return "\n".join(line.rstrip() for line in text.strip("\n").splitlines())


def first_difference(expected: str, actual: str) -> str:
    want, got = normalized(expected).splitlines(), normalized(actual).splitlines()
    for i in range(max(len(want), len(got))):
        w = want[i] if i < len(want) else "(nothing)"
        g = got[i] if i < len(got) else "(nothing)"
        if w != g:
            return f"line {i + 1}: expected `{w}`, got `{g}`"
    return ""


def grade(task: Task, src: Path, work: Path, toolchain: Toolchain) -> dict:
    """Builds the .lyth files of the module at `src` in a clean module, runs them, and checks
    them against the task."""
    files = lyth_files(src)
    g = {"pass": False, "files": files, "problems": [], "built": False, "foreign": {}}
    if not any("/" not in f and not f.endswith("_test.lyth") for f in files):
        g["problems"].append("no .lyth file in the module's root")
        return g
    code = "\n".join((src / f).read_text() for f in files)
    without_comments, bare = strip_code(code, strings=False), strip_code(code, strings=True)
    g["foreign"] = foreign_isms(code)
    g["problems"] += [f"doesn't match `{p}`" for p in task.requires if not re.search(p, without_comments)]
    g["problems"] += [f"matches forbidden `{p}`" for p in task.forbids if re.search(p, bare, re.M)]
    g["problems"] += [f"has no {f}" for f in task.files if not any(x == f or x.startswith(f) for x in files)]
    lines = [line.strip() for line in task.expected.splitlines() if len(line.strip()) >= 8]
    if len(lines) >= 2 and 2 * sum(line in without_comments for line in lines) > len(lines):
        g["problems"].append("prints the expected output literally")

    project = toolchain.project(work)
    for name in ("main.lyth", "AGENTS.md", "CLAUDE.md"):
        (project / name).unlink(missing_ok=True)
    for f in files:
        (project / f).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src / f, project / f)
    check = toolchain.run(["check"], project, 600)
    g["built"] = check.returncode == 0
    if not g["built"]:
        errors = [line for line in check.stderr.splitlines() if re.search(r"\berror(\[\w+\])?:", line)]
        g["problems"].append("doesn't build: " + (errors[0] if errors else check.stderr.strip()[-300:]))
        g["build_errors"] = errors[:20]
        return g
    run = toolchain.run(["run"], project, 120)
    g["output"] = run.stdout
    if run.returncode != 0:
        g["problems"].append(f"exits with status {run.returncode}: {run.stderr.strip()[-300:]}")
    elif normalized(run.stdout) != normalized(task.expected):
        g["problems"].append("output differs at " + first_difference(task.expected, run.stdout))
    if task.tests:
        test = toolchain.run(["test", "-v", "./..."], project, 600)
        passed = len(re.findall(r"^\s*--- PASS:", test.stdout, re.M))
        g["tests_passed"] = passed
        if test.returncode != 0:
            g["problems"].append("`lyth test` fails")
        elif passed < task.tests:
            g["problems"].append(f"{passed} tests pass, the task asks for {task.tests}")
    g["pass"] = not g["problems"]
    return g


def parse_transcript(text: str):
    """The tool calls (with their results) and the final message of a stream-json transcript."""
    calls, by_id, final = [], {}, {}
    for line in text.splitlines():
        try:
            m = json.loads(line)
        except ValueError:
            continue
        if m.get("type") == "assistant":
            for c in m.get("message", {}).get("content", []):
                if c.get("type") == "tool_use":
                    call = {"tool": c.get("name"), "input": c.get("input") or {}, "output": "", "error": False}
                    by_id[c.get("id")] = call
                    calls.append(call)
        elif m.get("type") == "user" and isinstance(m.get("message", {}).get("content"), list):
            for c in m["message"]["content"]:
                if c.get("type") == "tool_result" and c.get("tool_use_id") in by_id:
                    out = c.get("content")
                    if isinstance(out, list):
                        out = "\n".join(x.get("text", "") for x in out if isinstance(x, dict))
                    by_id[c["tool_use_id"]].update(output=str(out or ""), error=bool(c.get("is_error")))
        elif m.get("type") == "result":
            final = m
    return calls, final


def looked_outside(calls, allowed) -> list:
    """Tool calls that read files in the home directory outside the run (the repository, the Go
    module cache, caches) or reach the Lyth website, which the rules ask the agent not to."""
    home = Path.home().resolve()
    # Claude Code keeps long tool output there, and the agent reads it back
    allowed = [Path(a).resolve() for a in allowed] + [home / ".claude" / "projects"]

    def outside(path: str) -> bool:
        p = Path(os.path.expanduser(path)).resolve()
        return p.is_relative_to(home) and not any(p.is_relative_to(a) for a in allowed)

    found = []
    for c in calls:
        inp = c["input"]
        if c["tool"] == "Bash":
            command = inp.get("command", "")
            paths = re.findall(r"(?<![\w.$])((?:~|/)[^\s'\"|;&)<>]*)", command)
            if "lyth.bkymz.net" in command or "lyth-lang" in command or "$HOME" in command or any(outside(p) for p in paths):
                found.append(command[:200])
        else:
            for key in ("file_path", "path", "notebook_path"):
                if isinstance(inp.get(key), str) and inp[key].startswith(("/", "~")) and outside(inp[key]):
                    found.append(f"{c['tool']} {inp[key]}")
    return found


def agent_metrics(calls, final, allowed) -> dict:
    ran = [c for c in calls if c["tool"] == "Bash" and not (c["error"] and c["output"].startswith("Permission for this tool use was denied"))]
    commands = [c["input"].get("command", "") for c in ran]
    builds = [c for c in ran if BUILD_COMMAND.search(c["input"].get("command", ""))]
    # a compiler error, not an `error: ...` line the program prints
    failed = re.compile(r"^\S+\.lyth:\d+:\d+: error\[|^\d+ errors?$", re.M)
    first_build_ok = not failed.search(builds[0]["output"]) if builds else None
    usage = final.get("usage") or {}
    return {
        "turns": final.get("num_turns"),
        "cost_usd": final.get("total_cost_usd"),
        "tokens": sum(usage.get(k) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens")),
        "stop": final.get("subtype") or "no result",
        "tool_calls": len(calls),
        "builds": len(builds),
        "first_build_ok": first_build_ok,
        "guide": [c for c in commands if re.search(r"\blyth\s+guide\b", c)],
        "fix": [c for c in commands if re.search(r"\blyth\s+fix\b", c)],
        "skill_used": any(c["tool"] == "Skill" and "lyth" in json.dumps(c["input"]) for c in calls),
        "denied": len(final.get("permission_denials") or []),
        "outside": looked_outside(calls, allowed),
    }


def run_process(cmd, cwd: Path, env: dict, timeout: int):
    """Runs `cmd` in a process group of its own, so a timeout also stops what it started."""
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        out, err = proc.communicate(timeout=timeout)
        return out, err, False
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        out, err = proc.communicate()
        return out, err, True


class Eval:
    def __init__(self, args, tasks, out: Path, work: Path):
        self.args, self.tasks, self.out, self.work = args, tasks, out, work
        self.toolchain = Toolchain(work)
        self.plugin = work / "plugin"
        (self.plugin / ".claude-plugin").mkdir(parents=True)
        (self.plugin / ".claude-plugin" / "plugin.json").write_text(json.dumps({"name": "lyth", "description": "The Lyth skill under evaluation"}))
        shutil.copytree(Path(args.skill).resolve(), self.plugin / "skills" / "lyth")

    def agent_command(self, task: Task, condition: str):
        a = self.args
        cmd = ["claude", "-p", "--output-format", "stream-json", "--verbose", "--model", a.model, "--max-turns", str(a.max_turns)]
        cmd += ["--setting-sources", "project", "--strict-mcp-config", "--no-session-persistence"]
        cmd += ["--tools", "Bash,Read,Write,Edit,Glob,Grep,Skill"]
        cmd += ["--allowedTools", "Bash(lyth:*)", "Bash(go doc:*)", "Bash(go version)", "Read", "Write", "Edit", "Glob", "Grep", "Skill"]
        cmd += ["--permission-prompts", "none", "--append-system-prompt", AGENT_RULES]
        if condition == "skill":
            cmd += ["--plugin-dir", str(self.plugin)]
        if a.effort:
            cmd += ["--effort", a.effort]
        return cmd + ["--", task.prompt]

    def oneshot_command(self, task: Task, condition: str):
        system = ONESHOT_RULES + ("\n\n" + self.toolchain.guide if condition == "skill" else "")
        prompt = task.prompt
        if task.starter.exists():
            for f in lyth_files(task.starter):
                prompt += f"\n\n{f} currently contains:\n\n```kotlin\n{(task.starter / f).read_text()}```"
        cmd = ["claude", "-p", "--output-format", "json", "--model", self.args.model, "--max-turns", "1", "--tools", ""]
        cmd += ["--setting-sources", "project", "--strict-mcp-config", "--no-session-persistence", "--system-prompt", system]
        if self.args.effort:
            cmd += ["--effort", self.args.effort]
        return cmd + ["--", prompt]

    def run_one(self, task: Task, condition: str, rep: int) -> dict:
        run_dir = self.work / "runs" / f"{task.name}-{condition}-{rep}"
        saved = self.out / "runs" / task.name / f"{condition}-{rep}"
        saved.mkdir(parents=True, exist_ok=True)
        record = {"task": task.name, "condition": condition, "rep": rep, "mode": self.args.mode}
        start = time.time()
        if self.args.mode == "agent":
            project = self.toolchain.project(run_dir, task.starter)
            if condition != "agents-md":
                (project / "AGENTS.md").unlink()
                (project / "CLAUDE.md").unlink()
            env = self.toolchain.env(hide_guide=condition == "bare")
            out, err, timed_out = run_process(self.agent_command(task, condition), project, env, self.args.timeout)
            (saved / "transcript.jsonl").write_text(out)
            calls, final = parse_transcript(out)
            record.update(agent_metrics(calls, final, [run_dir, self.plugin]))
            src = project
        else:
            run_dir.mkdir(parents=True)
            out, err, timed_out = run_process(self.oneshot_command(task, condition), run_dir, self.toolchain.env(), self.args.timeout)
            try:
                final = json.loads(out)
            except ValueError:
                final = {}
            answer = final.get("result") or ""
            (saved / "answer.md").write_text(answer)
            src = run_dir / "src"
            if task.starter.exists():
                shutil.copytree(task.starter, src)
            for name, text in extract_files(answer).items():
                (src / name).parent.mkdir(parents=True, exist_ok=True)
                (src / name).write_text(text)
            usage = final.get("usage") or {}
            record.update(cost_usd=final.get("total_cost_usd"), turns=final.get("num_turns"), stop=final.get("subtype") or "no result",
                          tokens=sum(usage.get(k) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens")))
        record["seconds"] = round(time.time() - start, 1)
        record["timed_out"] = timed_out
        if err.strip() and not out.strip():
            record["claude_error"] = err.strip()[-500:]
        record.update(grade(task, src, run_dir / "grade", self.toolchain))
        for f in lyth_files(src):
            (saved / "src" / f).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src / f, saved / "src" / f)
        (saved / "grade.json").write_text(json.dumps(record, indent=2) + "\n")
        if not self.args.keep:
            shutil.rmtree(run_dir, ignore_errors=True)
        # what Claude Code stored for the run's directory, even without session persistence
        for d in (run_dir / MODULE, run_dir):
            slug = re.sub(r"[^A-Za-z0-9]", "-", str(d.resolve()))
            shutil.rmtree(Path.home() / ".claude" / "projects" / slug, ignore_errors=True)
        return record


def extract_files(answer: str) -> dict:
    """The files of a one-shot answer: each code block, named by its `// path.lyth` first line."""
    files = {}
    for m in re.finditer(r"```[\w-]*\n(.*?)```", answer, re.S):
        body = m.group(1)
        first = body.split("\n", 1)[0].strip()
        named = re.match(r"//\s*([\w/.-]+\.lyth)\s*$", first)
        if named:
            files[named.group(1)] = body
        elif "main.lyth" not in files and "func main" in body:
            files["main.lyth"] = body
    return files


def mean(values):
    values = [v for v in values if v is not None]
    return sum(values) / len(values) if values else None


def duration(seconds):
    return "–" if seconds is None else f"{int(seconds // 60)}m {int(seconds % 60):02d}s"


def report(records, config) -> str:
    conditions = config["conditions"]
    by = {c: [r for r in records if r["condition"] == c] for c in conditions}
    agent = config["mode"] == "agent"

    def row(label, value):
        return f"| {label} | " + " | ".join(value(by[c]) for c in conditions) + " |"

    def count(rs, key, want=True):
        known = [r for r in rs if r.get(key) is not None]
        return f"{sum(r.get(key) == want for r in known)}/{len(known)}" if known else "–"

    def avg(rs, key, fmt):
        m = mean([r.get(key) for r in rs])
        return "–" if m is None else fmt(m)

    lines = [f"# Lyth eval: {config['model']}, {config['mode']} mode", ""]
    lines.append(f"{config['date']}. {config['lyth']} (commit {config['commit']}), Claude Code {config['claude']}. "
                 f"{len(config['tasks'])} tasks, {config['reps']} run(s) per task and condition.")
    lines += ["", *(f"* **{c}**: {(CONDITIONS if agent else ONESHOT_CONDITIONS)[c]}" for c in conditions), ""]
    lines += ["| | " + " | ".join(conditions) + " |", "|---|" + "---|" * len(conditions)]
    lines.append(row("tasks passed", lambda rs: f"{sum(r['pass'] for r in rs)}/{len(rs)} ({100 * sum(r['pass'] for r in rs) // max(len(rs), 1)}%)"))
    lines.append(row("solutions that build", lambda rs: count(rs, "built")))
    if agent:
        lines.append(row("first build succeeded", lambda rs: count(rs, "first_build_ok")))
        lines.append(row("builds per run", lambda rs: avg(rs, "builds", lambda m: f"{m:.1f}")))
        lines.append(row("turns per run", lambda rs: avg(rs, "turns", lambda m: f"{m:.1f}")))
    lines.append(row("cost per run", lambda rs: avg(rs, "cost_usd", lambda m: f"${m:.2f}")))
    lines.append(row("time per run", lambda rs: duration(mean([r.get("seconds") for r in rs]))))
    lines.append(row("Go/Kotlin-isms per solution", lambda rs: avg([dict(r, n=sum(r.get("foreign", {}).values())) for r in rs], "n", lambda m: f"{m:.1f}")))
    if agent:
        lines.append(row("ran `lyth guide`", lambda rs: f"{sum(bool(r.get('guide')) for r in rs)}/{len(rs)}"))
        lines.append(row("ran `lyth fix`", lambda rs: f"{sum(bool(r.get('fix')) for r in rs)}/{len(rs)}"))
        lines.append(row("loaded the skill", lambda rs: f"{sum(bool(r.get('skill_used')) for r in rs)}/{len(rs)}"))
        lines.append(row("looked outside the project", lambda rs: f"{sum(bool(r.get('outside')) for r in rs)}/{len(rs)}"))
        lines.append(row("commands denied per run", lambda rs: avg(rs, "denied", lambda m: f"{m:.1f}")))
        lines.append(row("hit the turn limit or timed out", lambda rs: f"{sum(r.get('timed_out') or r.get('stop') == 'error_max_turns' for r in rs)}/{len(rs)}"))
    total = sum(r.get("cost_usd") or 0 for r in records)
    lines += ["", f"Total cost: ${total:.2f}.", "", "## Tasks", ""]
    lines += ["| task | exercises | " + " | ".join(conditions) + " |", "|---|---|" + "---|" * len(conditions)]
    for task in config["tasks"]:
        cells = []
        for c in conditions:
            rs = [r for r in by[c] if r["task"] == task]
            marks = "".join("✓" if r["pass"] else "✗" for r in rs)
            extra = f" {mean([r.get('turns') for r in rs]):.0f} turns" if agent and mean([r.get("turns") for r in rs]) is not None else ""
            cells.append(f"{marks}{extra}")
        lines.append(f"| {task} | {', '.join(config.get('features', {}).get(task, []))} | " + " | ".join(cells) + " |")
    lines += ["", "## Problems", ""]
    for r in sorted(records, key=lambda r: (r["task"], conditions.index(r["condition"]), r["rep"])):
        notes = list(r.get("problems", []))
        if r.get("claude_error"):
            notes.append("claude: " + r["claude_error"].splitlines()[-1])
        if r.get("outside"):
            notes.append("looked outside: " + "; ".join(r["outside"][:3]))
        if r.get("foreign"):
            notes.append("Go/Kotlin-isms: " + ", ".join(f"{k} ×{v}" for k, v in r["foreign"].items()))
        if notes:
            lines.append(f"* **{r['task']}**, {r['condition']} #{r['rep']}: " + "; ".join(notes))
    return "\n".join(lines) + "\n"


def git(*args):
    return subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True).stdout.strip()


def check(tasks) -> int:
    """Grades every task's reference solution: it must pass its own task. A task with a starter
    must not pass with the starter alone."""
    work = Path(tempfile.mkdtemp(prefix="lyth-eval-check-"))
    try:
        toolchain = Toolchain(work)

        def one(task):
            g = grade(task, task.solution, work / task.name, toolchain)
            if task.starter.exists() and grade(task, task.starter, work / (task.name + "-starter"), toolchain)["pass"]:
                g["pass"] = False
                g["problems"].append("the starter passes as it is")
            return task, g

        failed = 0
        with concurrent.futures.ThreadPoolExecutor(max_workers=os.cpu_count() or 4) as pool:
            for task, g in pool.map(one, tasks):
                status = "ok" if g["pass"] else "FAILED: " + "; ".join(g["problems"])
                isms = f" (Go/Kotlin-isms: {g['foreign']})" if g["foreign"] else ""
                print(f"{task.name:<22} {status}{isms}")
                failed += not g["pass"]
        return 1 if failed else 0
    finally:
        shutil.rmtree(work, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description="Measure how well a model writes Lyth, with and without help.")
    parser.add_argument("--model", default="sonnet", help="model for `claude --model` (default: sonnet)")
    parser.add_argument("--mode", choices=["agent", "oneshot"], default="agent", help="agent: Claude Code with tools; oneshot: one answer, no tools")
    parser.add_argument("--conditions", default="skill,bare", help=f"comma-separated, from {', '.join(CONDITIONS)} (default: skill,bare)")
    parser.add_argument("--tasks", default="", help="comma-separated task names (default: all)")
    parser.add_argument("--reps", type=int, default=1, help="runs per task and condition (default: 1)")
    parser.add_argument("--jobs", type=int, default=4, help="runs at the same time (default: 4)")
    parser.add_argument("--max-turns", type=int, default=60, help="turn limit of an agent run (default: 60)")
    parser.add_argument("--timeout", type=int, default=1800, help="seconds before a run is stopped (default: 1800)")
    parser.add_argument("--effort", default="", help="`claude --effort` level")
    parser.add_argument("--skill", default=str(ROOT / "skills" / "lyth"), help="the skill directory to evaluate (default: skills/lyth)")
    parser.add_argument("--name", default="", help="name of the results directory")
    parser.add_argument("--keep", action="store_true", help="keep the projects the runs worked in")
    parser.add_argument("--check", action="store_true", help="only check that every task's reference solution passes")
    parser.add_argument("--report", metavar="DIR", help="rewrite DIR/report.md from DIR/results.jsonl, counting the Go/Kotlin-isms again")
    args = parser.parse_args()

    tasks = load_tasks([t for t in args.tasks.split(",") if t])
    if args.check:
        sys.exit(check(tasks))
    if args.report:
        out = Path(args.report)
        config = json.loads((out / "config.json").read_text())
        records = [json.loads(line) for line in (out / "results.jsonl").read_text().splitlines() if line.strip()]
        for r in records:  # counted again, in case the counting changed since the run
            saved = out / "runs" / r["task"] / f"{r['condition']}-{r['rep']}"
            if (saved / "src").exists():
                r["foreign"] = foreign_isms("\n".join((saved / "src" / f).read_text() for f in lyth_files(saved / "src")))
            if (saved / "transcript.jsonl").exists():
                r.update(agent_metrics(*parse_transcript((saved / "transcript.jsonl").read_text()), []))
        (out / "report.md").write_text(report(records, config))
        print(out / "report.md")
        return

    conditions = [c for c in args.conditions.split(",") if c]
    allowed = CONDITIONS if args.mode == "agent" else ONESHOT_CONDITIONS
    if not conditions or any(c not in allowed for c in conditions):
        sys.exit(f"--conditions must be from {', '.join(allowed)} in {args.mode} mode")
    if shutil.which("claude") is None:
        sys.exit("needs Claude Code: the `claude` command isn't on the PATH")

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    out = RESULTS / (args.name or f"{stamp}-{args.model}-{args.mode}")
    out.mkdir(parents=True, exist_ok=False)
    work = Path(tempfile.mkdtemp(prefix="lyth-eval-"))
    ev = Eval(args, tasks, out, work)
    dirty = " + uncommitted changes" if git("status", "--porcelain") else ""
    config = {
        "date": datetime.datetime.now().strftime("%Y-%m-%d %H:%M"),
        "model": args.model, "mode": args.mode, "conditions": conditions, "reps": args.reps,
        "tasks": [t.name for t in tasks], "features": {t.name: t.features for t in tasks},
        "max_turns": args.max_turns, "effort": args.effort,
        "lyth": ev.toolchain.version, "commit": git("rev-parse", "--short", "HEAD") + dirty,
        "claude": subprocess.run(["claude", "--version"], capture_output=True, text=True).stdout.strip(),
        "skill": str(Path(args.skill).resolve().relative_to(ROOT)) if Path(args.skill).resolve().is_relative_to(ROOT) else args.skill,
    }
    (out / "config.json").write_text(json.dumps(config, indent=2) + "\n")
    jobs = [(t, c, rep) for rep in range(1, args.reps + 1) for t in tasks for c in conditions]
    print(f"{len(jobs)} runs of {args.model} ({args.mode}) into {out.relative_to(ROOT)}", flush=True)
    records = []
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
            futures = {pool.submit(ev.run_one, *job): job for job in jobs}
            for future in concurrent.futures.as_completed(futures):
                task, condition, rep = futures[future]
                try:
                    r = future.result()
                except Exception as e:  # a broken run is reported, not fatal
                    r = {"task": task.name, "condition": condition, "rep": rep, "pass": False, "problems": [f"the harness failed: {e!r}"]}
                records.append(r)
                with open(out / "results.jsonl", "a") as f:
                    f.write(json.dumps(r) + "\n")
                cost = f", ${r['cost_usd']:.2f}" if r.get("cost_usd") is not None else ""
                turns = f", {r['turns']} turns" if r.get("turns") is not None else ""
                verdict = "pass" if r["pass"] else "FAIL: " + "; ".join(r.get("problems", []))[:160]
                print(f"[{len(records)}/{len(jobs)}] {task.name} {condition}#{rep}: {verdict}{turns}{cost}", flush=True)
    finally:
        if not args.keep:
            shutil.rmtree(work, ignore_errors=True)
    (out / "report.md").write_text(report(records, config))
    print(f"\n{out.relative_to(ROOT) / 'report.md'}")


if __name__ == "__main__":
    main()
