# Lyth for Kuana

Support for the [Lyth programming language](../../README.md) in the Kuana editor:

* syntax highlighting, outline, breadcrumbs and sticky scroll, bracket matching, indentation and text objects
  (tree-sitter grammar from `../tree-sitter-lyth`);
* the Lyth language server: diagnostics as you type and `go vet` on save, completion, hover with the generated Go,
  go to definition (also into Go sources), references, rename, signature help, inlay hints, quick fixes;
* ▶ run buttons on `func main` and on each test function of a `_test.lyth` file, which show the test's last
  result (✓/✕); `lyth gen` to see the generated Go;
* snippets.

## Install

1. Install the `lyth` command (the language server is `lyth lsp`): open a `.lyth` file and click **Install** in
   Kuana's status bar, which runs `go install`, or:

   ```bash
   brew install lyth-lang/tap/lyth
   ```

2. In Kuana: Settings → Extensions → **Install from Folder…** and choose this folder (or link it:
   `ln -s "$PWD" ~/.config/kuana/extensions/lyth`, then **Reload Extensions**).

Kuana compiles the bundled grammar (`grammars/lyth/src/parser.c`) the first time; validate the extension with
`kuana extension check editors/kuana-lyth`.

## Updating the grammar

Edit `../tree-sitter-lyth/grammar.js` or its `queries/`, then run `../build-kuana-extension.sh` and **Reload
Extensions** in Kuana.
