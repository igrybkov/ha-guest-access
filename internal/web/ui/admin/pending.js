// The "Waiting to be admitted" panel. It sits above every screen and
// lists the device requests of all links, with Approve and Deny. It asks
// the server every approval.admin_poll_s seconds while the tab is
// visible, and stops while it is hidden. With nothing waiting, the panel
// hides, unless the page was opened on it (see wantsPending in lib.js).
import { h, fill, get, post, toast, table, settings } from './dom.js';
import { paths, pollMs, pendingRows, pendingBadge, wantsPending, haRoutePath } from './lib.js';

let timer = null;
let loading = null; // the fetch in flight, so polls never overlap
let opened = false; // the page was opened on the panel
let haPath = null; // the last route path HA sent

const panel = () => document.getElementById('pending');

// start draws the panel and starts polling. Call it once, after settings
// are loaded.
export function start() {
  document.addEventListener('visibilitychange', onVisibility);
  window.addEventListener('hashchange', () => {
    if (wantsPending(location.hash)) open();
  });
  listenToHA();
  if (wantsPending(location.hash)) opened = true;
  onVisibility();
}

function onVisibility() {
  clearTimeout(timer);
  timer = null;
  if (document.visibilityState !== 'visible') return;
  refresh().then(schedule);
}

function schedule() {
  clearTimeout(timer);
  timer = null;
  if (document.visibilityState !== 'visible') return;
  timer = setTimeout(() => refresh().then(schedule), pollMs(settings()));
}

// refresh loads the list and draws it. A failed load keeps the last list:
// the next poll tries again.
function refresh() {
  if (!loading) {
    loading = get(paths.pending())
      .then((res) => draw(res.requests || []))
      .catch(() => {})
      .finally(() => { loading = null; });
  }
  return loading;
}

// fresh loads the list after any fetch in flight, so it shows a change
// made just now.
async function fresh() {
  if (loading) await loading;
  return refresh();
}

// open shows the panel even when it is empty, and brings it into view.
function open() {
  opened = true;
  fresh().then(() => {
    const el = panel();
    el.scrollIntoView({ block: 'start' });
    el.querySelector('h2')?.focus();
  });
}

// listenToHA asks HA's frontend for the page's route, when the page runs
// in HA's iframe. A notification links to /app/<slug>/pending, and HA
// sends "/pending" here as the route. Only messages from the parent
// window on this origin count, and only the exact marker opens the panel.
function listenToHA() {
  if (window.parent === window) return;
  window.addEventListener('message', (ev) => {
    if (ev.source !== window.parent || ev.origin !== location.origin) return;
    const path = haRoutePath(ev.data);
    if (path === null || path === haPath) return;
    haPath = path;
    if (wantsPending('', path)) open();
  });
  try {
    window.parent.postMessage({ type: 'home-assistant/subscribe-properties' }, location.origin);
  } catch {
    // Not in HA's frontend; the hash marker still works.
  }
}

function draw(reqs) {
  const rows = pendingRows(reqs, Date.now());
  const badge = pendingBadge(rows.length);
  const nav = document.getElementById('pending-nav');
  nav.hidden = !badge;
  document.getElementById('pending-count').textContent = badge;
  const el = panel();
  el.hidden = !rows.length && !opened;
  fill(el,
    h('div', { class: 'toolbar' },
      h('h2', { class: 'grow', id: 'pending-title', tabindex: '-1', text: 'Waiting to be admitted' }),
      opened && h('button', { type: 'button', text: 'Close', onclick: close })),
    rows.length
      ? table(['Link', 'Guest', 'Device', 'Code', 'Asked', ''], rows.map(row))
      : h('p', { class: 'muted', text: 'Nobody is waiting. The request may be decided already, or it expired.' }));
}

function close() {
  opened = false;
  if (wantsPending(location.hash)) location.hash = '#/links';
  fresh();
}

function row(r) {
  return h('tr', {},
    h('td', { text: r.link }),
    h('td', { text: r.guest }),
    h('td', { class: 'small', text: r.device }),
    h('td', { class: 'nowrap' }, h('code', { class: 'code', text: r.code })),
    h('td', { class: 'small nowrap', text: r.asked }),
    h('td', {}, h('div', { class: 'actions' },
      h('button', { type: 'button', class: 'primary', text: 'Approve', onclick: decide(r, true) }),
      h('button', { type: 'button', class: 'danger', text: 'Deny', onclick: decide(r, false) }))));
}

// decide sends the decision. Another admin, HA or a guest device may have
// decided first; the request is then gone, and the list says so.
function decide(r, approve) {
  return async (ev) => {
    const btns = ev.currentTarget.parentElement.querySelectorAll('button');
    for (const b of btns) b.disabled = true;
    try {
      await post(paths.decide(r.id), { approve });
      toast(approve ? `Approved ${r.guest || r.device}` : `Denied ${r.guest || r.device}`);
    } catch (e) {
      toast(e.status === 404 ? 'This request was decided already, or it expired.' : e.message, 'error');
    }
    await fresh();
    for (const b of btns) b.disabled = false;
  };
}
