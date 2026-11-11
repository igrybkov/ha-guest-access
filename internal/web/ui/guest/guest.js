// Guest page: the flow from the first load to the controls.
//
//   /state 200            -> main view, cards, live stream
//   /state 401 password   -> password form
//   /state 403 status     -> device approval
//   /state 410            -> "link no longer active"
//
// Text goes in with textContent only. POSTs send JSON; the browser adds
// the Origin header, which the server checks.
import {
  linkBase, cards, mergeState, errorText, tryAgainText, formatUntil, parseColor, textOn,
  parseRetryAfter, sliderInterval, deviceNext, shouldPoll, POLL_MS, geoErrorText, whereErrorText,
  approvalsOn, approvalsDelay,
} from './lib.js';
import { el, icon } from './dom.js';
import { makeCard } from './controls.js';

const base = linkBase(location.pathname);
const $ = (id) => document.getElementById(id);
const VIEWS = ['loading', 'password', 'device', 'main', 'ended', 'error'];

let view = null; // the last /state reply, with SSE states merged in
const cardsById = new Map();
let current = 'loading';

// ---- HTTP ----

// call sends one request and never throws. status 0 means no answer.
async function call(method, path, body) {
  let res;
  try {
    res = await fetch(base + path, {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      headers: body ? { 'Content-Type': 'application/json' } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    return { status: 0, body: {}, retry: null };
  }
  const data = await res.json().catch(() => ({}));
  return {
    status: res.status,
    body: data && typeof data === 'object' ? data : {},
    retry: parseRetryAfter(res.headers.get('Retry-After'), Date.now()),
  };
}

// ---- views and toasts ----

function show(name) {
  current = name;
  for (const v of VIEWS) $('view-' + v).hidden = v !== name;
  if (name !== 'main') {
    stopStream();
    stopApprovals();
  }
  if (name !== 'device') stopPoll();
}

let toastTimer = null;

// toast shows a short message. Long messages stay up longer.
function toast(text) {
  const t = $('toast');
  t.textContent = text;
  t.classList.add('shown');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('shown'), Math.min(12000, 2500 + text.length * 60));
}

function showError(text) {
  $('error-text').textContent = text;
  show('error');
}

function showEnded() {
  view = null;
  show('ended');
}

function applyBranding(g) {
  const guest = g || {};
  const title = typeof guest.title === 'string' && guest.title ? guest.title : 'Welcome';
  $('title').textContent = title;
  document.title = title;
  const w = $('welcome');
  w.textContent = typeof guest.welcome === 'string' ? guest.welcome : '';
  w.hidden = !w.textContent;
  const root = document.documentElement.style;
  const c = parseColor(guest.accent_color);
  if (c) {
    root.setProperty('--accent', c);
    root.setProperty('--on-accent', textOn(c));
  } else {
    root.removeProperty('--accent');
    root.removeProperty('--on-accent');
  }
}

// ---- loading /state ----

let loading = null;

// load fetches /state and shows the view that fits the answer. Calls
// that come in while one runs share it.
function load() {
  if (!loading) {
    loading = call('GET', '/state').then(route).finally(() => { loading = null; });
  }
  return loading;
}

function route(r) {
  if (r.status === 200) return showMain(r.body);
  if (r.status === 401 && r.body.need === 'password') return showPassword();
  if (r.status === 403 && (r.body.status === 'approval_required' || r.body.status === 'pending')) {
    return showDevice(deviceNext(device, r));
  }
  if (r.status === 410) return showEnded();
  if (r.status === 404) return showError('This link does not work. Check that you have the whole link.');
  return showError(errorText(r.status, r.body, r.retry));
}

$('error-retry').addEventListener('click', () => load());

// ---- password ----

function showPassword() {
  show('password');
  $('pw').focus();
}

let pwUnlock = null;

$('password').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const input = $('pw');
  const msg = $('pw-msg');
  const submit = $('pw-submit');
  if (!input.value) {
    msg.textContent = 'Type the password first.';
    input.focus();
    return;
  }
  submit.disabled = true;
  msg.textContent = '';
  const r = await call('POST', '/password', { password: input.value });
  input.value = '';
  submit.disabled = false;
  if (r.status === 200) {
    load();
    return;
  }
  if (r.status === 403) msg.textContent = 'That password did not work. Try again.';
  else if (r.status === 410) return showEnded();
  else msg.textContent = errorText(r.status, r.body, r.retry);
  if (r.status === 429 && r.retry) {
    // Keep the form shut until the wait is over (timers cap at ~24 days).
    submit.disabled = true;
    clearTimeout(pwUnlock);
    pwUnlock = setTimeout(() => { submit.disabled = false; msg.textContent = ''; }, Math.min(r.retry, 86400) * 1000);
  }
  input.focus();
});

// ---- device approval ----

let device = null; // {phase, code?, wait?, error?}
let pollTimer = null;
let waitTimer = null;

const DEVICE_TEXT = {
  required: ['This link needs approval for new devices', 'Ask your host to let this phone in.', 'Request access'],
  pending: ['Waiting for your host', '', ''],
  denied: ['Your host did not let this device in', 'You can ask again if this was a mistake.', 'Ask again'],
  expired: ['The request ran out of time', 'You can ask again.', 'Ask again'],
  wait: ['Please wait a bit', '', 'Request access'],
};

function showDevice(next) {
  device = next;
  if (device.phase === 'approved') {
    device = null;
    load();
    return;
  }
  if (device.phase === 'ended') return showEnded();
  show('device');
  const [title, text, button] = DEVICE_TEXT[device.phase] || DEVICE_TEXT.required;
  $('device-title').textContent = title;
  $('device-text').textContent = device.phase === 'pending' ? 'Show this code to your host:'
    : device.phase === 'wait' ? tryAgainText(device.wait) : text;
  const code = $('device-code');
  code.hidden = device.phase !== 'pending';
  code.textContent = device.code || '';
  code.setAttribute('aria-label', 'Code: ' + (device.code || '').split('').join(' '));
  $('device-msg').textContent = device.error || (device.phase === 'pending' ? 'This page updates by itself.' : '');
  const btn = $('device-request');
  btn.hidden = device.phase === 'pending';
  btn.textContent = button || 'Request access';
  btn.disabled = device.phase === 'wait';
  clearTimeout(waitTimer);
  if (device.phase === 'wait') {
    const sec = Math.min(device.wait ?? 60, 86400);
    waitTimer = setTimeout(() => showDevice({ phase: 'required' }), sec * 1000);
  }
  stopPoll();
  if (shouldPoll(device.phase)) schedulePoll();
}

$('device-request').addEventListener('click', async () => {
  const btn = $('device-request');
  btn.disabled = true;
  const r = await call('POST', '/device/request', {});
  btn.disabled = false;
  showDevice(deviceNext({ ...device, error: undefined }, r));
});

function schedulePoll() {
  stopPoll();
  pollTimer = setTimeout(async () => {
    pollTimer = null;
    if (document.hidden || current !== 'device') return; // resumes on visible
    const r = await call('GET', '/device/status');
    if (current !== 'device') return;
    showDevice(deviceNext(device, r));
  }, POLL_MS);
}

function stopPoll() {
  clearTimeout(pollTimer);
  pollTimer = null;
}

// ---- main view ----

function showMain(data) {
  view = data;
  applyBranding(data.guest);
  show('main');
  updateUntil();
  drawCards();
  resetParty(!!data.party);
  startStream();
  startApprovals();
}

function updateUntil() {
  if (!view) return;
  $('until').textContent = formatUntil(view.link && view.link.valid_until, Date.now());
}
setInterval(updateUntil, 60000);

function drawCards() {
  cardsById.clear();
  const root = $('groups');
  const groups = cards(view);
  const nodes = [];
  for (const g of groups) {
    const section = el('section', { className: 'group' }, el('h2', {}, g.name));
    for (const e of g.entities) {
      const c = makeCard(e, (view.states || {})[e.id], { act, shareLocation, sliderMs: sliderInterval(view.guest) });
      cardsById.set(e.id, c);
      section.append(c.node);
    }
    nodes.push(section);
  }
  root.replaceChildren(...nodes);
  $('empty').hidden = groups.length > 0;
}

// act sends one action. It resolves to true on success. State comes back
// over the stream, so success only shows a toast for plain buttons.
async function act(entityId, actionId, data) {
  const r = await call('POST', '/act', { entity_id: entityId, action: actionId, data: data || {} });
  if (r.status === 200) {
    const e = view && view.entities && view.entities[entityId];
    const a = e && e.actions.find((x) => x.id === actionId);
    if (a && (a.ui === 'button' || a.ui === 'confirm')) toast('Done.');
    if (view && view.link && view.link.one_time) load();
    return true;
  }
  switch (r.status) {
    case 401:
      load();
      break;
    case 403:
      // A device gate answer, or access that changed (an expired
      // location). Either way /state knows what is allowed now.
      if (!r.body.status) toast(errorText(403, r.body));
      load();
      break;
    case 409:
      toast(errorText(409, r.body));
      load();
      break;
    case 410:
      showEnded();
      break;
    default:
      toast(errorText(r.status, r.body, r.retry));
  }
  return false;
}

// ---- location ----

let locating = false;

function shareLocation(entityId) {
  if (locating) return;
  if (!window.isSecureContext || !('geolocation' in navigator)) {
    toast(geoErrorText(0));
    return;
  }
  locating = true;
  toast('Finding your location…');
  navigator.geolocation.getCurrentPosition(async (pos) => {
    const r = await call('POST', '/where', {
      lat: pos.coords.latitude, lon: pos.coords.longitude, accuracy: pos.coords.accuracy,
    });
    locating = false;
    if (r.status === 410) return showEnded();
    if (r.status !== 200) {
      toast(whereErrorText(r.status, r.body, r.retry));
      return;
    }
    // The stream picks its entities when it connects, so load() starts a
    // new one with the location cookie.
    await load();
    const e = view && view.entities && view.entities[entityId];
    if (e && e.actions.some((a) => a.needs_location)) {
      toast('You seem to be too far from home for this. Try again when you are closer.');
    } else {
      toast('Thanks. You can use it now.');
    }
  }, (err) => {
    locating = false;
    toast(geoErrorText(err && err.code));
  }, { enableHighAccuracy: true, timeout: 20000, maximumAge: 0 });
}

// ---- live updates ----

let stream = null;
let failures = 0;
let reconnectTimer = null;

function startStream() {
  stopStream();
  if (document.hidden) return; // starts on visible
  const es = new EventSource(base + '/events');
  stream = es;
  es.addEventListener('state', (ev) => {
    if (stream !== es) return;
    let st;
    try { st = JSON.parse(ev.data); } catch { return; }
    const next = mergeState(view, st);
    if (next === view) return;
    view = next;
    const c = cardsById.get(st.entity_id);
    if (c) c.update(st);
  });
  es.addEventListener('ready', () => {
    if (stream !== es) return;
    failures = 0;
    $('live').textContent = '';
  });
  es.addEventListener('reload', () => { if (stream === es) { stopStream(); load(); } });
  es.addEventListener('end', () => { if (stream === es) { stopStream(); load(); } });
  // EventSource gives up for good on HTTP errors (429, 401, 403, 410), so
  // the page runs its own backoff. Each try loads /state first, which
  // also shows the right view if the link changed.
  es.addEventListener('error', () => {
    if (stream !== es) return;
    stopStream();
    $('live').textContent = 'Reconnecting…';
    const delay = Math.min(30000, 1000 * 2 ** failures) * (0.75 + Math.random() * 0.5);
    failures = Math.min(failures + 1, 10);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!document.hidden && current === 'main') load();
    }, delay);
  });
}

function stopStream() {
  clearTimeout(reconnectTimer);
  reconnectTimer = null;
  if (stream) {
    stream.close();
    stream = null;
  }
}

// ---- self-service approvals ----
// /state says can_approve when this device may let new devices in. Only
// then does the page poll device/pending.

let approvalsTimer = null;
let approvalsSeq = 0; // a newer start or stop makes older polls drop out

function startApprovals() {
  stopApprovals();
  if (!approvalsOn(view, document.hidden)) {
    drawApprovals([]);
    return;
  }
  checkApprovals(approvalsSeq);
}

function stopApprovals() {
  approvalsSeq++;
  clearTimeout(approvalsTimer);
  approvalsTimer = null;
}

async function checkApprovals(seq) {
  const r = await call('GET', '/device/pending');
  if (seq !== approvalsSeq || current !== 'main') return;
  if (r.status === 200 && Array.isArray(r.body.requests)) {
    drawApprovals(r.body.requests);
  } else if (r.status === 401 || r.status === 403 || r.status === 410) {
    // Access changed. /state knows what holds now.
    drawApprovals([]);
    load();
    return;
  }
  approvalsTimer = setTimeout(() => checkApprovals(seq), approvalsDelay(r.status, r.retry));
}

function drawApprovals(list) {
  const items = list.filter((q) => q && typeof q.request_id === 'string');
  $('approvals').hidden = items.length === 0;
  $('approvals-list').replaceChildren(...items.map((q) => {
    const approve = el('button', { type: 'button' }, 'Let in');
    const deny = el('button', { type: 'button', className: 'secondary' }, 'Say no');
    const decide = async (yes) => {
      approve.disabled = true;
      deny.disabled = true;
      const r = await call('POST', '/device/decide', { request_id: q.request_id, approve: yes });
      if (r.status === 200) toast(yes ? 'Done. They are in.' : 'Done. They were not let in.');
      else if (r.status === 404) toast('That request has run out.');
      else if (r.status === 409) toast('This link has all the devices it can have. Ask your host.');
      else toast(errorText(r.status, r.body, r.retry));
      startApprovals();
    };
    approve.addEventListener('click', () => decide(true));
    deny.addEventListener('click', () => decide(false));
    const who = [q.label, q.device].filter((x) => typeof x === 'string' && x).join(', ');
    return el('div', { className: 'card request' },
      el('p', { className: 'code small' }, typeof q.code === 'string' ? q.code : ''),
      who ? el('p', { className: 'muted' }, who) : null,
      el('div', { className: 'actions' }, approve, deny));
  }));
}

// ---- music party ----

function resetParty(on) {
  $('party').hidden = !on;
  $('party-msg').textContent = '';
  const b = el('button', { type: 'button', className: 'big' }, icon('music'), 'Join the music party');
  b.addEventListener('click', () => joinParty(b));
  $('party-slot').replaceChildren(b);
}

function safeURL(s) {
  try {
    const u = new URL(s);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : '';
  } catch {
    return '';
  }
}

// joinParty fetches a fresh join URL. Some phones block a new tab that
// opens after a network wait, so the button also turns into a plain link
// the guest can tap.
async function joinParty(button) {
  button.disabled = true;
  const r = await call('GET', '/party');
  const url = r.status === 200 ? safeURL(r.body.url) : '';
  if (!url) {
    button.disabled = false;
    if (r.status === 404 || r.status === 503 || r.status === 200) {
      $('party-slot').replaceChildren();
      $('party-msg').textContent = 'The music party is not available right now.';
    } else {
      toast(errorText(r.status, r.body, r.retry));
    }
    return;
  }
  const a = el('a', { className: 'button big', href: url, target: '_blank', rel: 'noopener noreferrer' },
    icon('music'), 'Open the music party');
  $('party-slot').replaceChildren(a);
  window.open(url, '_blank', 'noopener');
}

// ---- page life ----

document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    stopStream();
    stopApprovals();
    stopPoll();
    return;
  }
  if (current === 'main') load();
  else if (current === 'device' && device && shouldPoll(device.phase)) schedulePoll();
});

window.addEventListener('pageshow', (ev) => {
  if (ev.persisted && current === 'main') load();
});

if (base) {
  load();
} else {
  showError('This link does not work. Check that you have the whole link.');
  $('error-retry').hidden = true;
}
