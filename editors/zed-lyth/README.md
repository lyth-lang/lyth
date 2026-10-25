# Lyth for Zed

Support for the [Lyth programming language](../../README.md) in the [Zed](https://zed.dev) editor:

* syntax highlighting, outline, bracket matching, indentation and text objects (tree-sitter grammar from
  `../tree-sitter-lyth`);
* the Lyth language server: diagnostics as you type and `go vet` on save, completion, hover with the generated Go,
  go to definition (also into Go sources), references, rename, signature help, inlay hints, quick fixes;
* ▶ run buttons on `func main` and on each test function of a `_test.lyth` file, and `lyth` tasks (`lyth run`,
  `lyth test`, `lyth gen` to see the generated Go);
* snippets.

## Install

1. Install the `lyth` command (the language server is `lyth lsp`):

   ```bash
   brew install lyth-lang/tap/lyth
   ```

   or `go install github.com/lyth-lang/lyth/cmd/lyth@latest`, with Go's `bin` directory on your shell's `PATH`.
   To use another binary, set it in Zed's settings:

   ```json
   { "lsp": { "lyth": { "binary": { "path": "/path/to/lyth", "arguments": ["lsp"] } } } }
   ```

2. In Zed: **zed: install dev extension** from the command palette, and choose this folder. Zed needs Rust installed
   with rustup for that: it compiles `src/lib.rs` (which starts `lyth lsp`) to WebAssembly, and builds the grammar from
   the commit in `extension.toml`.

## Updating

The language files and snippets are copies of the Kuana extension's (`../kuana-lyth`), so the queries live in one
place. Edit `../tree-sitter-lyth/grammar.js` or its `queries/`, run `../build-kuana-extension.sh` (which runs
`../build-zed-extension.sh` at the end), commit the grammar, and run `../build-zed-extension.sh` again: Zed builds the
grammar from the repository, so `extension.toml` pins the last commit that changed `editors/tree-sitter-lyth`. Then
click **Rebuild** on the extension in Zed's Extensions page.
