// DOM helpers shared by the screens: element building, API calls, toasts,
// the confirm dialog and form fields. Text always goes in with
// textContent, never as HTML.

// h builds an element. attrs: class, text, on<event>, and any property or
// attribute. Children may be nodes, strings, arrays, null or false.
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value' || k === 'checked' || k === 'selected' || k === 'disabled') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  append(el, children);
  return el;
}

// fill replaces el's children. Like h, it skips null and false, so
// "cond && node" is safe. Plain replaceChildren would print "false".
export function fill(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c == null || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else el.append(c instanceof Node ? c : String(c));
  }
}

// APIError carries the status and any validation problems.
export class APIError extends Error {
  constructor(status, message, problems) {
    super(message);
    this.status = status;
    this.problems = problems || [];
  }
}

// api calls the admin API. Paths are relative, so they work under
// ingress. Every non-GET sends JSON, even an empty {}: the server needs
// the Content-Type.
export async function api(method, path, body, signal) {
  const opts = { method, headers: {}, signal };
  if (method !== 'GET') {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body ?? {});
  }
  const res = await fetch(path, opts);
  const isJSON = (res.headers.get('Content-Type') || '').includes('application/json');
  const data = isJSON ? await res.json().catch(() => null) : null;
  if (!res.ok) throw new APIError(res.status, (data && data.error) || `${res.status} ${res.statusText}`, data && data.problems);
  return data;
}

export const get = (p, signal) => api('GET', p, undefined, signal);
export const post = (p, b, signal) => api('POST', p, b, signal);
export const put = (p, b) => api('PUT', p, b);
export const del = (p) => api('DELETE', p);

export function toast(msg, kind = 'ok') {
  const el = h('div', { class: `toast ${kind}`, role: 'status', text: msg });
  document.getElementById('toasts').append(el);
  setTimeout(() => el.remove(), kind === 'error' ? 6000 : 2500);
}

// run wraps an async action: shows errors as toasts and disables the button.
export async function run(btn, fn) {
  if (btn) btn.disabled = true;
  try {
    return await fn();
  } catch (e) {
    toast(e.message, 'error');
    return undefined;
  } finally {
    if (btn) btn.disabled = false;
  }
}

// ask shows the confirm dialog and resolves to true on OK. It never uses
// window.confirm, which HA's iframe may block.
export function ask(title, message, { ok = 'OK', danger = false } = {}) {
  const dlg = document.getElementById('confirm');
  const opener = document.activeElement;
  const okBtn = h('button', { type: 'submit', value: 'ok', class: danger ? 'danger' : 'primary', text: ok });
  dlg.className = '';
  dlg.oncancel = null;
  dlg.setAttribute('aria-label', title);
  dlg.replaceChildren(h('form', { method: 'dialog', class: 'stack' },
    h('h2', { text: title }),
    message && h('p', { text: message }),
    h('div', { class: 'actions' }, okBtn, h('button', { type: 'submit', value: 'cancel', text: 'Cancel' }))));
  dlg.returnValue = '';
  return new Promise((resolve) => {
    dlg.addEventListener('close', () => {
      returnFocus(opener);
      resolve(dlg.returnValue === 'ok');
    }, { once: true });
    dlg.showModal();
    okBtn.focus();
  });
}

// returnFocus moves focus back to the control that opened a dialog, if
// it is still on the page.
function returnFocus(el) {
  if (el && el.isConnected && typeof el.focus === 'function') el.focus();
}

// dialog shows a modal form in the shared dialog. body is the form's
// content. onOk runs on submit; if it throws, the message shows in the
// dialog and it stays open. It resolves to onOk's result, or undefined
// on Cancel or Escape. The native modal keeps focus inside and Escape
// cancels; focus goes back to the opener on close.
export function dialog({ title, body, ok = 'OK', danger = false, onOk, wide = false }) {
  const dlg = document.getElementById('confirm');
  const opener = document.activeElement;
  const err = h('p', { class: 'error small', role: 'alert', hidden: true });
  const okBtn = h('button', { type: 'submit', class: danger ? 'danger' : 'primary', text: ok });
  const cancel = h('button', { type: 'button', text: 'Cancel', onclick: () => dlg.close('cancel') });
  let result;
  let busy = false;
  const form = h('form', { class: 'stack', novalidate: true, onsubmit: async (ev) => {
    ev.preventDefault();
    if (busy) return;
    err.hidden = true;
    busy = okBtn.disabled = cancel.disabled = true;
    try {
      result = await onOk();
      dlg.close('ok');
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    } finally {
      busy = okBtn.disabled = cancel.disabled = false;
    }
  } }, h('h2', { text: title }), body, err, h('div', { class: 'actions' }, okBtn, cancel));
  // Escape cancels, but not while the call runs: its answer must land.
  dlg.oncancel = (e) => { if (busy) e.preventDefault(); };
  dlg.className = wide ? 'wide' : '';
  dlg.setAttribute('aria-label', title);
  dlg.replaceChildren(form);
  dlg.returnValue = '';
  return new Promise((resolve) => {
    dlg.addEventListener('close', () => {
      returnFocus(opener);
      resolve(dlg.returnValue === 'ok' ? result : undefined);
    }, { once: true });
    dlg.showModal();
    (form.querySelector('input:checked') || okBtn).focus();
  });
}

// field wraps a control with a label and a slot for problems.
export function field(label, control, hint) {
  return h('label', { class: 'field' }, h('span', { text: label }), control, hint && h('small', { class: 'muted', text: hint }));
}

export function check(label, input, hint) {
  return h('div', {}, h('label', { class: 'check' }, input, h('span', { text: label })), hint && h('small', { class: 'muted hint', text: hint }));
}

export function select(options, value, attrs = {}) {
  return h('select', attrs, options.map(([v, label]) => h('option', { value: v, selected: v === value }, label)));
}

// problemList renders messages as a list, or nothing.
export function problemList(msgs) {
  if (!msgs || !msgs.length) return null;
  return h('ul', { class: 'error small problems' }, msgs.map((m) => h('li', { text: m })));
}

// debounce delays fn until calls stop for ms.
export function debounce(fn, ms) {
  let t = null;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

// copyText copies to the clipboard. HA's iframe may block the clipboard
// API, so it falls back to selecting the input for a manual copy.
export async function copyText(text, input) {
  try {
    await navigator.clipboard.writeText(text);
    toast('Copied');
  } catch {
    if (input) {
      input.focus();
      input.select();
      toast('Press Ctrl+C or Cmd+C to copy', 'error');
    } else {
      // No field to select: show the text so it can still be copied.
      toast(`Copy did not work. ${text}`, 'error');
    }
  }
}

export function table(head, rows) {
  return h('div', { class: 'table-wrap' }, h('table', {},
    h('thead', {}, h('tr', {}, head.map((t) => h('th', { scope: 'col', text: t })))),
    h('tbody', {}, rows)));
}

// back is the "← Screen" button in an editor's toolbar.
export const back = (href, label) => h('a', { href, class: 'button', text: `← ${label}` });

// settings is fetched once at boot by app.js.
let settingsCache = null;
export function setSettings(s) {
  settingsCache = s;
}
export const settings = () => settingsCache || {};
export const localEnabled = () => !!settings().local_access?.enabled;
export const maEnabled = () => !!settings().music_assistant?.url;
// haZone is HA's time zone, or "" when HA has not reported one.
export const haZone = () => settings().time_zone || '';

export const DOCS_URL = 'https://github.com/igrybkov/ha-guest-access/blob/main/guest_access/DOCS.md';
export const docsLink = (text = 'Read the docs') => h('a', { href: DOCS_URL, target: '_blank', rel: 'noopener noreferrer', text });
