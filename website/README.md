# Lyth documentation site

A [Starlight](https://starlight.astro.build) site built from the Markdown in this repository.
There is nothing to keep in sync by hand: edit the files below, and the site follows.

| Source | Pages |
|---|---|
| `docs/guide/*.md` | Learn and Using Lyth (tour, Lyth for Go programmers, cheat sheet, interop, testing, tooling) |
| `examples/todoapi/README.md` | the REST API example |
| `docs/spec/*.md`, `docs/design/*.md`, `docs/adr/*.md` | language reference, design, decisions |
| `README.md` | the home page's "What Lyth changes" and "Quick start" sections, and its first example |

- `scripts/sync-docs.mjs` generates the pages in `src/content/docs/` (ignored by git). Links
  between docs become site links. Links to other repository files go to `LYTH_REPO_URL` when it
  is set, and render as plain text otherwise. A new file in `docs/guide`, `docs/spec`, or
  `docs/design` must be added to the sidebar groups at the top of the script, or the build fails;
  ADRs are listed automatically.
- Every Lyth example (a ```` ```kotlin ```` or ```` ```lyth ```` block) that compiles on its own
  gets a **Go** tab with what `lyth gen` produces for it, from this checkout's compiler, and an
  **Output** tab with the output recorded in `src/data/doc-outputs.json`. Examples that declare
  test functions are compiled as `_test.lyth` files. Fragments and examples of errors don't
  compile and are shown without tabs.
- `go test ./tests -run TestDocExamples` (part of `go test ./...`) compiles and runs every
  example with `fun main` and fails when it doesn't compile or its output differs from the
  recorded one. After checking the difference, rerecord with
  `LYTH_BLESS=1 go test ./tests -run TestDocExamples`. Mark an example whose output changes
  from run to run with `// output varies`.
- The build fails on links to missing pages or headings (`starlight-links-validator`).

## Running it

It needs Node 22.23.4 or newer (`.nvmrc` says 24) and Go, which builds `lyth` into `.cache/`
the first time (or set `LYTH_BIN`).

```bash
npm install
npm run dev
```

`npm run build` writes the site to `dist/`. Set `LYTH_SITE_URL` (the published address) and
`LYTH_REPO_URL` (for source and edit links) when publishing. `netlify.toml` at the repository
root builds it on Netlify.

After changing the code block settings (`expressiveCode` in `astro.config.mjs`), restart the dev
server: pages it already rendered keep links to the old code styles until it starts afresh.
