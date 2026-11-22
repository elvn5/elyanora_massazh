import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// Production CSP disallows inline JS, including Angular's async CSS onload handler.
const output = new URL('../dist/web/browser/', import.meta.url);
const html = await readFile(new URL('index.html', output), 'utf8');
const links = [...html.matchAll(/<link\b[^>]*rel="stylesheet"[^>]*>/g)].map(([tag]) => tag);
assert(links.length > 0, 'Production HTML must link a stylesheet');
for (const tag of links) {
  assert(!/\bonload\s*=/.test(tag), 'Inline CSS load handlers are blocked by production CSP');
  assert(!/\bmedia="print"/.test(tag), 'The main stylesheet must apply to screens immediately');
  const href = tag.match(/\bhref="([^"]+)"/)?.[1];
  assert(href && !href.includes('..') && !href.includes(':'), 'Expected a local stylesheet');
  const css = await readFile(new URL(href, output), 'utf8');
  for (const selector of ['.admin-sidebar', '.admin-panel', '.primary']) {
    assert(css.includes(selector), `Missing shared UI styles: ${selector}`);
  }
  const definitions = new Set([...css.matchAll(/(--color-[\w-]+)\s*:/g)].map((m) => m[1]));
  for (const [, token] of css.matchAll(/var\((--color-[\w-]+)\)/g)) {
    assert(definitions.has(token), `Undefined color token: ${token}`);
  }
  assert(definitions.has('--color-brand'), 'Shared theme is missing');
}
console.log('Production CSS: synchronous loading, shared theme and component styles verified.');
