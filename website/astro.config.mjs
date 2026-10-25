// @ts-check
import fs from 'node:fs';
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';
import starlightLinksValidator from 'starlight-links-validator';
import starlightThemeNext from 'starlight-theme-next';
import { repo, repoUrl, sidebar, sources, syncDocs } from './scripts/sync-docs.mjs';
import lythGrammar from './src/lyth-grammar.mjs';

// Picks the same tab (Lyth, Go or Output) in every example on the page, or Lyth where an example
// has no such tab, and remembers it, keeping the clicked example where it was on screen.
const tabScript = `
function lythShowTab(value) {
	for (const tabs of document.querySelectorAll('.lyth-tabs')) {
		const input = tabs.querySelector('input[value="' + value + '"]') || tabs.querySelector('input[value="lyth"]');
		if (input) input.checked = true;
	}
}
document.addEventListener('change', (e) => {
	const input = e.target;
	if (!(input instanceof HTMLInputElement) || !input.closest('.lyth-tabs')) return;
	const top = input.getBoundingClientRect().top;
	lythShowTab(input.value);
	window.scrollBy(0, input.getBoundingClientRect().top - top);
	try { localStorage.setItem('lyth-tab', input.value); } catch {}
});
document.addEventListener('DOMContentLoaded', () => {
	let tab = null;
	try { tab = localStorage.getItem('lyth-tab'); } catch {}
	if (tab) lythShowTab(tab);
});`;

/** Generates the pages from the repository's Markdown before the site is built, and again
 * when a source changes. */
function lythDocs() {
	return {
		name: 'lyth-docs',
		hooks: {
			'astro:config:setup': ({ config }) => {
				// Rendered pages are cached with links to the code styles, whose names change with the
				// code settings below but don't invalidate the cache, so start from a clean one.
				for (const dir of [new URL('.astro/', config.root), config.cacheDir]) fs.rmSync(new URL('data-store.json', dir), { force: true });
				syncDocs();
			},
			/** @param {{ server: import('vite').ViteDevServer }} options */
			'astro:server:setup': ({ server }) => {
				server.watcher.add(sources());
				server.watcher.on('change', (file) => sources().includes(file) && syncDocs());
			},
		},
	};
}

// The specification's grammar blocks: terminals and operators.
const ebnfGrammar = {
	name: 'ebnf',
	scopeName: 'source.ebnf',
	patterns: [
		{ match: '"[^"]*"', name: 'string.quoted.double.ebnf' },
		{ match: '[=|.\\[\\]{}()]', name: 'keyword.operator.ebnf' },
		{ match: '\\b[a-z_]+\\b', name: 'variable.other.ebnf' },
	],
};

export default defineConfig({
	// set LYTH_SITE_URL (and LYTH_REPO_URL for source links) when publishing
	site: process.env.LYTH_SITE_URL,
	integrations: [
		lythDocs(),
		starlight({
			title: 'Lyth',
			logo: { src: './src/assets/logo.svg' },
			description: "Lyth compiles to readable Go: less boilerplate, simpler error handling, nil safety at compile time, and shorter code that is easier to review, with every Go package and Go's speed.",
			favicon: '/favicon.svg',
			social: repoUrl ? [{ icon: 'github', label: 'Source', href: repoUrl }] : [],
			customCss: ['@fontsource-variable/instrument-sans', '@fontsource/commit-mono', './src/styles/custom.css'],
			expressiveCode: {
				themes: ['one-dark-pro', 'one-light'],
				// the default (5.5) washes the theme's colors out; 4.5 is WCAG AA
				minSyntaxHighlightingColorContrast: 4.5,
				shiki: { langs: [lythGrammar, ebnfGrammar] },
			},
			head: [{ tag: 'script', content: tabScript }],
			sidebar,
			plugins: [starlightThemeNext(), starlightLinksValidator()],
		}),
	],
	vite: { server: { fs: { allow: [repo] } } },
});
