# tree-sitter-lyth

A [tree-sitter](https://tree-sitter.github.io) grammar for the Lyth programming language, made for editors: it
recognizes declarations (functions and methods, structs, interfaces, enums, type aliases, constants, variables,
imports), parameters and fields with their types, calls, member accesses, lambdas, control-flow headers and
strings with `${}` templates, and groups everything else by brackets, so it stays small, fast and tolerant of
incomplete code. The Lyth compiler has its own complete parser.

```bash
npm install          # installs the tree-sitter CLI
npx tree-sitter generate
npx tree-sitter parse ../../examples/todoapi/main.lyth
```

Queries for highlighting, outline (Zed/Kuana format), indentation, brackets, text objects and runnables are in
`queries/`. Every `.lyth` file of the repository and every Lyth block of the docs parses without errors; check
that after changing the grammar.
