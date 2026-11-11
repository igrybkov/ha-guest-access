// Guest page. A plain first version, to be replaced by the full UI. It uses
// textContent only, never innerHTML, and sends JSON with the page's own
// origin, which the server checks.
import { linkBase, actionLabel, cards } from './lib.js';

const base = linkBase(location.pathname);
const $ = (id) => document.getElementById(id);
let view = null;
let events = null;

function status(text) { $('status').textContent = text; }

async function post(path, body) {
  return fetch(base + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  });
}

async function load() {
  const res = await fetch(base + '/state', { credentials: 'same-origin' });
  const body = await res.json().catch(() => ({}));
  if (res.status === 401 && body.need === 'password') {
    $('password').hidden = false;
    status('This link needs a password.');
    return;
  }
  if (!res.ok) {
    status(body.error || 'This link does not work.');
    return;
  }
  $('password').hidden = true;
  view = body;
  render();
  listen();
}

function render() {
  const g = view.guest || {};
  if (g.title) $('title').textContent = g.title;
  $('welcome').textContent = g.welcome || '';
  if (g.accent_color) document.documentElement.style.setProperty('--accent', g.accent_color);
  status('');
  const root = $('groups');
  root.replaceChildren();
  for (const group of cards(view)) {
    const h = document.createElement('h2');
    h.textContent = group.name;
    root.append(h);
    for (const e of group.entities) root.append(card(e));
  }
  $('party').hidden = !view.party;
}

function card(e) {
  const div = document.createElement('div');
  div.className = 'card';
  const name = document.createElement('div');
  name.textContent = e.name;
  const st = document.createElement('div');
  st.className = 'state';
  st.textContent = e.state;
  const row = document.createElement('div');
  row.className = 'actions';
  for (const a of e.actions) {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = a.needs_location ? actionLabel(a.id) + ' (share location)' : actionLabel(a.id);
    b.disabled = !!a.used;
    b.addEventListener('click', () => act(e.id, a));
    row.append(b);
  }
  div.append(name, st, row);
  return div;
}

async function act(entityId, a) {
  if (a.needs_location) {
    await shareLocation();
    return;
  }
  if (a.ui === 'confirm' && !confirm(actionLabel(a.id) + '?')) return;
  const res = await post('/act', { entity_id: entityId, action: a.id, data: {} });
  const body = await res.json().catch(() => ({}));
  status(res.ok ? 'Done.' : body.error || 'That did not work.');
  if (view.link && view.link.one_time) load();
}

function shareLocation() {
  return new Promise((resolve) => {
    if (!navigator.geolocation) {
      status('This browser can not share its location.');
      resolve();
      return;
    }
    navigator.geolocation.getCurrentPosition(async (pos) => {
      const res = await post('/where', { lat: pos.coords.latitude, lon: pos.coords.longitude, accuracy: pos.coords.accuracy });
      status(res.ok ? '' : 'Location not accurate enough.');
      await load();
      resolve();
    }, () => { status('Location not shared.'); resolve(); }, { enableHighAccuracy: true, timeout: 15000 });
  });
}

function listen() {
  if (events) return;
  events = new EventSource(base + '/events');
  events.addEventListener('state', (ev) => {
    const st = JSON.parse(ev.data);
    if (!view) return;
    view.states = view.states || {};
    view.states[st.entity_id] = st;
    render();
  });
  events.addEventListener('reload', () => { events.close(); events = null; load(); });
  events.addEventListener('end', () => { events.close(); events = null; load(); });
}

$('password').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const res = await post('/password', { password: $('pw').value });
  $('pw').value = '';
  if (res.ok) { load(); return; }
  const body = await res.json().catch(() => ({}));
  status(body.error || 'That did not work.');
});

$('party-link').addEventListener('click', async (ev) => {
  const a = ev.currentTarget;
  if (a.href) return;
  ev.preventDefault();
  const res = await fetch(base + '/party', { credentials: 'same-origin' });
  const body = await res.json().catch(() => ({}));
  if (res.ok && body.url) { a.href = body.url; window.open(body.url, '_blank', 'noopener'); }
  else status('The party is not available now.');
});

if (base) load(); else status('This link does not work.');
