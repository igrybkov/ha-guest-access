// Static checks on the guest page source. The page runs under a strict
// CSP and must never parse server data as markup or code.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdir, readFile } from 'node:fs/promises';

const dir = new URL('./', import.meta.url);

async function sources(ext) {
  const names = (await readdir(dir)).filter((n) => n.endsWith(ext) && !n.endsWith('_test.mjs'));
  return Promise.all(names.map(async (n) => [n, await readFile(new URL(n, dir), 'utf8')]));
}

test('guest JS never builds markup or code from strings', async () => {
  const banned = [
    /\binnerHTML\b/, /\bouterHTML\b/, /\binsertAdjacentHTML\b/, /\bdocument\.write(ln)?\b/,
    /\beval\s*\(/, /\bnew\s+Function\b/, /\bcreateContextualFragment\b/, /\bsetHTMLUnsafe\b/,
    /\bsrcdoc\b/, /\bsetTimeout\s*\(\s*['"`]/, /\bsetInterval\s*\(\s*['"`]/, /javascript:/i,
  ];
  const files = await sources('.js');
  assert.ok(files.length >= 3, 'found the guest modules');
  for (const [name, src] of files) {
    for (const re of banned) assert.equal(re.test(src), false, `${name} matches ${re}`);
  }
});

test('the pages have no inline script, style or handlers', async () => {
  const pages = await sources('.html');
  assert.deepEqual(pages.map(([n]) => n).sort(), ['index.html', 'notfound.html']);
  for (const [name, html] of pages) {
    assert.equal(/<script(?![^>]*\bsrc=)[^>]*>/i.test(html), false, `${name}: inline script`);
    assert.equal(/<style\b/i.test(html), false, `${name}: style element`);
    assert.equal(/\sstyle\s*=/i.test(html), false, `${name}: style attribute`);
    assert.equal(/\son[a-z]+\s*=/i.test(html), false, `${name}: event handler attribute`);
    assert.equal(/(src|href)\s*=\s*["']?(https?:)?\/\//i.test(html), false, `${name}: external resource`);
  }
});

test('the not-found page has no script at all', async () => {
  const html = await readFile(new URL('notfound.html', dir), 'utf8');
  assert.equal(/<script\b/i.test(html), false);
});

test('the CSS loads nothing from outside', async () => {
  for (const [name, css] of await sources('.css')) {
    assert.equal(/@import|url\s*\(/i.test(css), false, name);
  }
});
