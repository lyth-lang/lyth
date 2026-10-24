# ADR 0006: Overlay builds; in-tree generation on request

**Status:** Accepted

## Context
The build must not rewrite user files, but Go must compile generated files as
members of the user's package directories (relative imports, embedding of
package-local files, module membership).

## Decision
`lyth build/run/vet/test` write generated files under `<module>/.lyth/gen/` and
pass `-overlay=.lyth/overlay.json`, mapping virtual paths
`<pkgdir>/<name>.lyth.go` to them. `lyth gen` writes the same files into the
source tree for inspection, committing, and publishing.

## Alternatives
- **Always write next to sources**: simple and Go-native, but mutates the tree
  on every build.
- **Separate output module (copy everything)**: breaks mixed packages, module
  paths, embeds, and tooling.

## Consequences
Verified: `go build`, `go vet`, `go test`, and `go list -export` honor overlays;
the tree stays untouched; `.lyth/` is ignored by `./...` patterns. File names
keep Go's `_test`/GOOS/GOARCH rules (compiler.md §6.2, verified with `go list`).
