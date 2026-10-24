# Lyth for Visual Studio Code

Support for [Lyth](https://github.com/lyth-lang/lyth), a programming language that compiles to readable Go:
all of Go, in fewer lines.

## Features

* **Syntax highlighting** with the same grammar as the Lyth docs, so code reads the same in the editor and on
  [lyth.bkymz.net](https://lyth.bkymz.net), including string templates (`"hi ${name}"`) and ` ```lyth ` code
  blocks in Markdown.
* **The Lyth language server** (`lyth lsp`), answering from the compiler itself:
  * Lyth's errors and warnings as you type, including the check of the generated Go, with quick fixes from the
    compiler's suggested edits. On save, `go vet`'s findings on the `.lyth` lines.
  * Hover with the declaration, its doc comment and the Go it compiles to; Go declarations show their Go doc.
  * Go to definition (also into the Go standard library and modules), type definition, references, highlights,
    and rename across the module, following what each name resolves to.
  * Completion of members (fields, methods, `lyth/coll`'s extension functions, packages' exports), names in
    scope and import paths; signature help; document and workspace symbols.
  * Inlay hints with inferred types: `x := f()`, loop variables, lambda parameters, `func f() = ...` results.
* **Run and test**: ▶ Run above `func main`, ▶ Run Test above each test function of a `_test.lyth` file, and Run
  File and Run Tests in Package in the editor's run menu. They run as tasks in the terminal.
* **Show Generated Go** (the preview button in the editor title): the Go for the current file beside it, updated
  as you type.
* **Tasks** for Lyth modules: `lyth build`, `run`, `test`, `check` and `vet` under Terminal → Run Task. In
  `tasks.json`, use `{"type": "lyth", "command": "build", "args": ["./..."]}`.
* **Snippets**: `main`, `func`, `method`, `struct`, `interface`, `enum`, `when`, `for`, `catch`, `test`, ...

## Requirements

Lyth needs [Go](https://go.dev/dl/) 1.26 or newer and the `lyth` command:

```bash
brew install lyth-lang/tap/lyth
# or
go install github.com/lyth-lang/lyth/cmd/lyth@latest
```

If `lyth` isn't on the `PATH` (or in Go's bin directory, `~/go/bin`), the extension offers to install it with
`go install`. Set `lyth.path` to use another build, such as one from a checkout of the Lyth repository.

## Settings

| Setting | Default | |
|---|---|---|
| `lyth.path` | `""` | The `lyth` command for the language server and for running files |
| `lyth.vet` | `true` | Run `go vet` on save and show its findings |
| `lyth.trace.server` | `"off"` | Log the language server messages (`messages`, `verbose`) |

## Restricted Mode

In an untrusted workspace only highlighting and snippets work: the language server and the run commands run the
Go toolchain on the code.

## Development

The extension lives in `editors/vscode-lyth` of the Lyth repository. Building and testing it needs Node.js 22 or
newer.

```bash
npm install
npm run build      # bundles src/ into out/extension.js and writes the grammars
npm test           # grammar tests, then integration tests in VS Code
npm run package    # writes lyth-<version>.vsix
```

The TextMate grammar is generated from the docs site's (`website/src/lyth-grammar.mjs`); change it there. Open
this folder in VS Code and press F5 to try the extension in a new window. The integration tests download VS Code
unless `VSCODE_EXECUTABLE` points to one, and build `lyth` from the repository unless `LYTH_PATH` is set. Grammar
tests are in `test/grammar`, in the [vscode-tmgrammar-test](https://github.com/PanAeon/vscode-tmgrammar-test)
format.
