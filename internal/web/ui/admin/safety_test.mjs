// Run with: node --test internal/web/ui/admin/safety_test.mjs
//
// A lint for the admin page. The admin UI shows data that guests control
// (headers, device names, audit fields), so it must never parse data as
// HTML or code. The CSP is default-src 'self', so inline script, inline
// handlers and inline styles would also break the page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url));
const files = readdirSync(dir).filter((f) => !f.endsWith('_test.mjs'));
const read = (f) => readFileSync(join(dir, f), 'utf8');

// Comments may name the banned APIs; code may not.
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1');

const JS_BANNED = [
  [/\binnerHTML\b/, 'innerHTML'],
  [/\bouterHTML\b/, 'outerHTML'],
  [/\binsertAdjacentHTML\b/, 'insertAdjacentHTML'],
  [/\bdocument\s*\.\s*write(ln)?\b/, 'document.write'],
  [/\beval\s*\(/, 'eval('],
  [/\bnew\s+Function\s*\(/, 'new Function('],
  [/\bcreateContextualFragment\b/, 'createContextualFragment'],
  [/\bDOMParser\b/, 'DOMParser'],
  [/\bsrcdoc\b/, 'srcdoc'],
  [/\bconfirm\s*\(/, 'confirm( (use ask)'],
  [/\b(alert|prompt)\s*\(/, 'alert/prompt'],
  [/setAttribute\(\s*['"](style|on\w+)['"]/, 'setAttribute(style|on…)'],
  [/['"]javascript:/i, 'javascript: URL'],
  [/\bset(Timeout|Interval)\(\s*['"`]/, 'string timer'],
];

test('there are admin files to check', () => {
  assert.ok(files.includes('index.html'));
  assert.ok(files.filter((f) => f.endsWith('.js')).length >= 5);
});

for (const f of files.filter((x) => x.endsWith('.js'))) {
  test(`${f}: no HTML parsing or code from strings`, () => {
    const src = stripComments(read(f));
    for (const [re, name] of JS_BANNED) assert.doesNotMatch(src, re, `${f} uses ${name}`);
  });
}

test('index.html: no inline script, handlers or styles', () => {
  const html = read('index.html');
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>/i, 'inline <script>');
  assert.doesNotMatch(html, /\son\w+\s*=/i, 'on…= attribute');
  assert.doesNotMatch(html, /\sstyle\s*=/i, 'style= attribute');
  assert.doesNotMatch(html, /<style\b/i, '<style> block');
  assert.doesNotMatch(html, /\b(src|href)\s*=\s*["']?(https?:|\/\/|data:)/i, 'external or data: resource');
});

test('every script and style is a relative URL', () => {
  const html = read('index.html');
  for (const m of html.matchAll(/\b(?:src|href)="([^"]+)"/g)) {
    assert.doesNotMatch(m[1], /^\//, `absolute path ${m[1]} breaks under ingress`);
  }
});

test('fetches go through relative API paths', () => {
  for (const f of files.filter((x) => x.endsWith('.js'))) {
    const src = stripComments(read(f));
    assert.doesNotMatch(src, /['"`]\/api\//, `${f} uses an absolute /api path`);
  }
});

// The guard must catch what it claims to catch.
test('the lint catches each banned pattern', () => {
  const bad = ['el.innerHTML = x', 'el.outerHTML', 'el.insertAdjacentHTML("x", y)', 'document.write(x)', 'eval(x)',
    'new Function("x")', 'window.confirm("x")', 'confirm("x")', 'el.setAttribute("style", x)', 'el.setAttribute(\'onclick\', x)',
    'a.href = "javascript:x"', 'setTimeout("x()", 1)'];
  for (const s of bad) assert.ok(JS_BANNED.some(([re]) => re.test(s)), s);
  assert.ok(!JS_BANNED.some(([re]) => re.test('retrieval(x); ask("x"); el.textContent = y')));
});
