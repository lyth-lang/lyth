# Releasing

Where Lyth and its editor extensions are published, and how each is released.

## Where things are published

| What | Public page | Managed at |
|---|---|---|
| Lyth releases (source, notes) | https://github.com/lyth-lang/lyth/releases | tags `vX.Y.Z` in this repository |
| Homebrew formula (`brew install lyth-lang/tap/lyth`) | https://github.com/lyth-lang/homebrew-tap | `Formula/lyth.rb`, written by the release workflow |
| Go module (`go install github.com/lyth-lang/lyth/cmd/lyth@latest`) | https://pkg.go.dev/github.com/lyth-lang/lyth | nothing to do: the Go proxy picks up tags |
| Documentation | https://lyth.bkymz.net | Netlify, built from `website/` on every push |
| VS Code extension, VS Code Marketplace | https://marketplace.visualstudio.com/items?itemName=lyth-lang.lyth | https://marketplace.visualstudio.com/manage/publishers/lyth-lang (publisher `lyth-lang`) |
| VS Code extension, Open VSX (VSCodium, Cursor, Windsurf, Gitpod) | https://open-vsx.org/extension/lyth-lang/lyth | https://open-vsx.org/user-settings/namespaces/lyth-lang (namespace `lyth-lang`; ownership claim: https://github.com/EclipseFdn/open-vsx.org/issues/14684) |
| Claude Code skill (`claude plugin install lyth --marketplace lyth-lang/lyth`) | `skills/lyth` in this repository | `.claude-plugin/marketplace.json`; nothing to publish |
| VS Code extension, each `.vsix` | https://github.com/lyth-lang/lyth/releases (tags `vscode-vX.Y.Z`) | the extension release workflow |

The Kuana extension (`editors/kuana-lyth`, regenerated with `editors/build-kuana-extension.sh`)
is not published anywhere yet.

## Release notes

Both release workflows write the notes with `.github/release-notes.sh`: every commit since the
previous release tag, whether pushed to `main` or merged from a pull request, grouped by its
Conventional Commits type (`feat` Features, `fix` Fixes, `perf`, `docs`, `test`, `build`/`ci`,
anything else Other changes), with the scope in bold and a compare link. A commit marked
breaking (`feat(syntax)!: ...`, or a `BREAKING CHANGE:` line in its body) goes under "Breaking
changes". So the notes are only as good as the commit subjects: write them for users. The
extension's notes list only commits that touch `editors/vscode-lyth` or the grammar.

Preview the notes of the next release before tagging it:

```bash
.github/release-notes.sh v0.3.0
.github/release-notes.sh vscode-v0.2.0 'vscode-v*' editors/vscode-lyth website/src/lyth-grammar.mjs
```

To change the notes after the fact, edit the release on GitHub.

## Lyth

1. Make sure `main` builds and passes `go test ./...`, and preview the release notes.
2. Tag and push: `git tag -a v0.3.0 -m "Lyth 0.3.0" && git push origin v0.3.0`.
3. `.github/workflows/release.yml` runs the tests, creates the GitHub release, and commits the
   new formula to the Homebrew tap (repository secret `HOMEBREW_TAP_TOKEN`: a fine-grained
   token with read and write access to the contents of `lyth-lang/homebrew-tap`).
4. Bump `devVersion` in `cmd/lyth/main.go` to the next `-dev` version. The documentation site
   reads it, and the newest `v*` tag, to say which version its specification pages describe
   (`versions()` in `website/scripts/sync-docs.mjs`); nothing in `docs/` names a version.

## VS Code extension

The extension needs a `lyth` that has `lyth lsp` (0.2.0 or newer), so release Lyth first when
the language server changes.

1. Bump `version` in `editors/vscode-lyth/package.json` and commit.
2. Tag and push the same version: `git tag vscode-v0.1.1 && git push origin vscode-v0.1.1`.
3. `.github/workflows/vscode-release.yml` runs the extension's tests, packages
   `lyth-0.1.1.vsix`, attaches it to a GitHub release, and publishes it to Open VSX. The run's
   summary links to every page above.
4. VS Code Marketplace, by hand: download the `.vsix` from the GitHub release, open
   https://marketplace.visualstudio.com/manage/publishers/lyth-lang, choose **Update** in the
   extension's `...` menu, and upload it. Microsoft verifies it within a few minutes.

To check a change without publishing, run the workflow by hand (Actions, "Release VS Code
extension", Run workflow): it runs the tests and keeps the `.vsix` as an artifact.

### Open VSX trusted publishing

Open VSX publishes without a stored token: the workflow is registered as the trusted
publisher of `lyth-lang.lyth` at https://open-vsx.org/user-settings/trusted-publishers
(repository `lyth-lang/lyth`, workflow `vscode-release.yml`), and authenticates with GitHub's
short-lived OIDC token (`ovsx publish --trusted-publishing`). Registering needs the owner role
in the `lyth-lang` namespace, which comes with the approved ownership claim. Until then, or if
the registration is ever lost, upload the `.vsix` at https://open-vsx.org/publish, or add an
`OVSX_PAT` secret (a token from https://open-vsx.org/user-settings/tokens), which the workflow
uses instead.

### Why the Marketplace upload is manual

Automatic Marketplace publishing needs either an Azure DevOps personal access token (global
tokens, the kind it needs, are retired on December 1, 2026) or a managed identity with workload
identity federation, and new Azure DevOps organizations require an Azure subscription. If that
is set up, add the repository secret `VSCE_PAT` (or change the step to
`vsce publish --azure-credential`) and the workflow publishes to the Marketplace too.
