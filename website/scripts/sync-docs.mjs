// Generates the site's pages in src/content/docs from the Markdown in this repository, so the
// Markdown stays the only copy of the documentation:
//
//   docs/guide/*.md, docs/spec/*.md, docs/design/*.md, docs/adr/*.md   pages
//   examples/todoapi/README.md                                          the REST API example page
//   README.md                                                            parts of the home page
//
// Every Lyth example (a ```kotlin or ```lyth block) is highlighted with src/lyth-grammar.mjs, and one
// that compiles on its own gets a Go tab with what `lyth gen` produces for it from this checkout's
// compiler, and an Output tab with the output recorded in src/data/doc-outputs.json by the
// doc-example test (rerecord with `LYTH_BLESS=1 go test ./tests -run TestDocExamples`).

import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const website = path.join(repo, 'website');
const outDir = path.join(website, 'src/content/docs');
const partsDir = path.join(website, 'src/generated');
const outputsFile = path.join(website, 'src/data/doc-outputs.json');

/** Where links to repository files (not pages) point, e.g. https://github.com/owner/lyth. Without
 * it, such links render as plain text. */
export const repoUrl = (process.env.LYTH_REPO_URL ?? '').replace(/\/$/, '');

/** Sidebar groups. Every Markdown file under docs/guide, docs/spec, and docs/design must appear
 * here (ADRs are listed automatically). */
export const groups = [
	{ label: 'Learn', pages: ['guide/tour', 'guide/lyth-for-go-programmers', 'guide/cheatsheet'] },
	{ label: 'Using Lyth', pages: ['guide/go-interop', 'guide/testing', 'guide/tooling', 'guide/writing-lyth', 'examples/todoapi'] },
	{ label: 'Language reference', pages: ['spec/language', 'spec/types', 'spec/errors'] },
	{
		label: 'Design',
		pages: ['design/overview', 'design/compiler', 'design/interop', 'design/diagnostics', 'design/collections', 'design/benchmarks'],
	},
];

export const sidebar = [
	...groups.map((g) => ({ label: g.label, items: g.pages })),
	{ label: 'Decisions (ADRs)', collapsed: true, items: [{ autogenerate: { directory: 'adr' } }] },
];

const docDirs = ['guide', 'spec', 'design', 'adr'];

/** The Markdown files pages are generated from, as [file, slug]. */
function pageSources() {
	const out = [];
	for (const dir of docDirs) {
		const abs = path.join(repo, 'docs', dir);
		if (!fs.existsSync(abs)) continue;
		for (const f of fs.readdirSync(abs).filter((f) => f.endsWith('.md')).sort()) {
			out.push([path.join(abs, f), f === 'README.md' ? dir : `${dir}/${f.slice(0, -3)}`]);
		}
	}
	out.push([path.join(repo, 'examples/todoapi/README.md'), 'examples/todoapi']);
	return out;
}

/** Files the pages are generated from: the dev server regenerates the site when they change. */
export function sources() {
	return [...pageSources().map(([f]) => f), path.join(repo, 'README.md'), outputsFile];
}

let lythBin;

/** The `lyth` binary of this checkout (built into .cache/), or `$LYTH_BIN`. */
function lyth() {
	if (lythBin) return lythBin;
	if (process.env.LYTH_BIN) return (lythBin = process.env.LYTH_BIN);
	const bin = path.join(website, '.cache', process.platform === 'win32' ? 'lyth.exe' : 'lyth');
	try {
		execFileSync('go', ['build', '-o', bin, './cmd/lyth'], { cwd: repo, stdio: 'inherit' });
	} catch (e) {
		throw new Error(`building lyth failed (${e.message}); the docs need Go, or LYTH_BIN set to a lyth binary`);
	}
	return (lythBin = bin);
}

const generated = new Map();
let tmpDir;

/** The Go `lyth gen` produces for an example, without the file header; null if the example is a
 * fragment or an example of an error, which doesn't compile on its own. */
export function goFor(source) {
	if (generated.has(source)) return generated.get(source);
	// a directory named like a package, outside any Go module, so lyth compiles the file alone
	tmpDir ??= fs.mkdtempSync(path.join(os.tmpdir(), 'lyth-docs-'));
	fs.mkdirSync(path.join(tmpDir, 'example'), { recursive: true });
	// examples declaring go test functions are compiled as test files
	const test = /^func (Test|Benchmark|Fuzz|Example)\w*\(/m.test(source);
	for (const f of fs.readdirSync(path.join(tmpDir, 'example'))) fs.rmSync(path.join(tmpDir, 'example', f), { recursive: true });
	const file = path.join(tmpDir, 'example', test ? 'example_test.lyth' : 'example.lyth');
	fs.writeFileSync(file, source);
	// outside the try below: a failed build of lyth stops the site build
	const bin = lyth();
	let go = null;
	try {
		const out = execFileSync(bin, ['gen', '-stdout', '-no-line-directives', file], {
			cwd: path.dirname(file),
			encoding: 'utf8',
			stdio: ['ignore', 'pipe', 'ignore'],
		});
		const lines = out.split('\n');
		const pkg = lines.findIndex((l) => l.startsWith('package '));
		go = lines.slice(pkg + 1).join('\n').trim() + '\n';
	} catch (e) {
		if (typeof e.status !== 'number') throw new Error(`running ${bin} failed: ${e.message}`);
	}
	generated.set(source, go);
	return go;
}

function recordedOutputs() {
	try {
		return new Map(JSON.parse(fs.readFileSync(outputsFile, 'utf8')).map((o) => [o.source, o.output]));
	} catch {
		return new Map();
	}
}

function fence(lang, code, title) {
	const ticks = code.includes('```') ? '````' : '```';
	return `${ticks}${lang}${title ? ` title=${JSON.stringify(title)}` : ''}\n${code.endsWith('\n') ? code : code + '\n'}${ticks}`;
}

let exampleId = 0;

/** Tabs with the example, its Go and its output (radio buttons, so they work without JS). */
function example(source, outputs) {
	const tabs = [['lyth', 'Lyth', fence('lyth', source)]];
	const go = goFor(source);
	if (go) tabs.push(['go', 'Go', fence('go', go)]);
	const output = outputs.get(source);
	if (output) tabs.push(['output', 'Output', fence('text', output)]);
	if (tabs.length === 1) return tabs[0][2];
	const id = `ex${++exampleId}`;
	const inputs = tabs.map(([key, label], i) => `<input type="radio" name="${id}" id="${id}-${key}" value="${key}"${i === 0 ? ' checked' : ''}><label for="${id}-${key}">${label}</label>`);
	const panels = tabs.map(([key, , code]) => `<div class="lyth-panel" data-panel="${key}">\n\n${code}\n\n</div>`);
	return `<div class="lyth-example">\n<div class="lyth-tabs">${inputs.join('')}</div>\n${panels.join('\n')}\n</div>`;
}

/** The site path of a repository Markdown file, or null if it is not a page. */
function pageFor(rel) {
	if (rel === 'README.md') return '';
	if (rel === 'examples/todoapi/README.md') return 'examples/todoapi';
	const m = rel.match(/^docs\/(guide|spec|design|adr)\/([\w.-]+)\.md$/);
	if (!m) return null;
	return m[2] === 'README' ? m[1] : `${m[1]}/${m[2]}`;
}

/** Points links at site pages (`../spec/types.md#x` → `/spec/types/#x`) or at files in the
 * repository (`repoUrl`), or keeps just their text when no repository URL is configured. */
function rewriteLinks(text, srcDir) {
	return text.replace(/(!?)\[([^\]]*)\]\(([^)\s]+)\)/g, (all, bang, label, target) => {
		if (bang || /^([a-z]+:|#)/.test(target)) return all;
		const [file, hash = ''] = target.split('#');
		const rel = path.relative(repo, path.resolve(srcDir, file)).replaceAll('\\', '/');
		const page = pageFor(rel);
		if (page !== null) return `[${label}](/${page}${page ? '/' : ''}${hash ? '#' + hash : ''})`;
		if (!repoUrl) return label;
		const kind = /\.\w+$/.test(rel) ? 'blob' : 'tree';
		return `[${label}](${repoUrl}/${kind}/main/${rel}${hash ? '#' + hash : ''})`;
	});
}

/** Plain text of the first paragraph, for the page's meta description. */
function description(lines) {
	const start = lines.findIndex((l) => l.trim() && !l.startsWith('#') && !l.startsWith('>') && !l.startsWith('|'));
	if (start < 0) return '';
	const para = [];
	for (const l of lines.slice(start)) {
		if (!l.trim() || l.startsWith('```')) break;
		para.push(l.trim());
	}
	return para.join(' ').replace(/\[([^\]]*)\]\([^)]*\)/g, '$1').replace(/[`*]/g, '');
}

/** The Markdown of a page body: examples get their tabs, links point at the site. */
function convert(rest, srcDir, outputs) {
	const body = [];
	for (let i = 0; i < rest.length; i++) {
		const open = rest[i].match(/^```(kotlin|lyth)\s*$/);
		if (open) {
			const code = [];
			while (++i < rest.length && !rest[i].startsWith('```')) code.push(rest[i] + '\n');
			body.push(example(code.join(''), outputs));
		} else if (rest[i].startsWith('```')) {
			// other code blocks are copied as they are, links included
			body.push(rest[i]);
			while (++i < rest.length && !rest[i].startsWith('```')) body.push(rest[i]);
			body.push(rest[i]);
		} else {
			// an indented Lyth block (in a list item) is highlighted as Lyth, without tabs
			body.push(rewriteLinks(rest[i].replace(/^(\s+)```kotlin(?=\s*$)/, '$1```lyth'), srcDir));
		}
	}
	return body.join('\n');
}

function page(srcFile, slug, outputs) {
	const lines = fs.readFileSync(srcFile, 'utf8').split('\n');
	const titleLine = lines.findIndex((l) => l.startsWith('# '));
	const title = lines[titleLine].slice(2).trim();
	const rest = lines.slice(titleLine + 1);
	const rel = path.relative(repo, srcFile).replaceAll('\\', '/');
	const frontmatter = ['---', `title: ${JSON.stringify(title)}`, `description: ${JSON.stringify(description(rest))}`];
	if (repoUrl) frontmatter.push(`editUrl: ${repoUrl}/edit/main/${rel}`);
	frontmatter.push('---', '', `<!-- Generated from ${rel} by website/scripts/sync-docs.mjs. Edit that file instead. -->`);
	const file = slug.includes('/') ? path.join(outDir, `${slug}.md`) : path.join(outDir, slug, 'index.md');
	return { file, text: frontmatter.join('\n') + '\n' + convert(rest, path.dirname(srcFile), outputs) };
}

/** A `## ` section of the README, for the home page. */
function readmeSection(heading, name, outputs) {
	const lines = fs.readFileSync(path.join(repo, 'README.md'), 'utf8').split('\n');
	const start = lines.indexOf(`## ${heading}`);
	if (start < 0) throw new Error(`README.md has no "## ${heading}" section for the home page`);
	const end = lines.findIndex((l, i) => i > start && l.startsWith('## '));
	return { file: path.join(partsDir, `readme-${name}.md`), text: convert(lines.slice(start, end < 0 ? undefined : end), repo, outputs) };
}

/** The README's first example and its Go, which the home page shows side by side. */
function homeExample() {
	const source = fs.readFileSync(path.join(repo, 'README.md'), 'utf8').match(/^```kotlin\n([\s\S]*?)^```/m)[1];
	const go = goFor(source);
	if (!go) throw new Error("the README's first example doesn't compile with `lyth gen`");
	return { file: path.join(partsDir, 'home-example.json'), text: JSON.stringify({ source, go }, null, '\t') + '\n' };
}

/** Writes the generated pages, touching only the files whose content changed. */
export function syncDocs() {
	const outputs = recordedOutputs();
	exampleId = 0;
	const srcs = pageSources();
	const listed = new Set(groups.flatMap((g) => g.pages));
	const missing = srcs.map(([, slug]) => slug).filter((slug) => !slug.startsWith('adr') && !listed.has(slug));
	if (missing.length) throw new Error(`add ${missing.join(', ')} to the sidebar groups in website/scripts/sync-docs.mjs`);
	const pages = [
		...srcs.map(([file, slug]) => page(file, slug, outputs)),
		readmeSection('Why Lyth', 'why', outputs),
		readmeSection('What Lyth changes, and what it does not', 'changes', outputs),
		readmeSection('Quick start', 'quickstart', outputs),
		homeExample(),
	];
	// pages whose source was removed go too
	const keep = new Set(pages.map((p) => p.file));
	for (const dir of [outDir]) {
		if (!fs.existsSync(dir)) continue;
		for (const f of fs.readdirSync(dir, { recursive: true })) {
			const abs = path.join(dir, f);
			if (abs.endsWith('.md') && !keep.has(abs) && path.basename(abs) !== 'index.mdx') fs.rmSync(abs);
		}
	}
	for (const p of pages) {
		fs.mkdirSync(path.dirname(p.file), { recursive: true });
		if (fs.existsSync(p.file) && fs.readFileSync(p.file, 'utf8') === p.text) continue;
		fs.writeFileSync(p.file, p.text);
	}
}

if (process.argv[1] === fileURLToPath(import.meta.url)) syncDocs();
