// Entry point: loads settings once, shows the limits banner, starts the
// pending panel, and routes by hash. The hash #/pending shows the links
// screen with the pending panel open (see pending.js). Each screen module
// exports render(view, rest, params) and may return a cleanup function.
import * as links from './links.js';
import * as templates from './templates.js';
import * as audit from './audit.js';
import * as diagnostics from './diagnostics.js';
import * as settingsScreen from './settings.js';
import * as pending from './pending.js';
import { h, get, setSettings } from './dom.js';
import { paths } from './lib.js';

const screens = { links, templates, audit, diagnostics, settings: settingsScreen };
let cleanup = null;
let seq = 0;

// parseHash turns "#/links/edit/x?a=b" into {screen, rest, params}.
function parseHash() {
  const [path, query = ''] = location.hash.replace(/^#\/?/, '').split('?');
  const [screen, ...rest] = path.split('/').map(decodeURIComponent);
  return { screen: screen || 'links', rest, params: new URLSearchParams(query) };
}

// route renders the screen for the hash. Each route gets its own
// container, so a slow earlier route that resolves late writes into a
// detached node and never replaces the newer screen.
async function route() {
  const my = ++seq;
  const { screen, rest, params } = parseHash();
  const mod = screens[screen] || links;
  if (cleanup) cleanup();
  cleanup = null;
  for (const a of document.querySelectorAll('#nav a')) {
    a.toggleAttribute('aria-current', a.dataset.screen === (screens[screen] ? screen : 'links'));
  }
  const view = h('div', { class: 'screen' }, h('p', { class: 'muted', text: 'Loading…' }));
  document.getElementById('view').replaceChildren(view);
  let done = null;
  try {
    done = (await mod.render(view, rest, params)) || null;
  } catch (e) {
    if (my === seq) view.replaceChildren(h('p', { class: 'error', text: `Could not load: ${e.message}` }));
    return;
  }
  if (my === seq) cleanup = done;
  else if (done) done();
}

// showBanner warns on every screen while rate limits are off.
function showBanner(s) {
  const el = document.getElementById('banner');
  el.hidden = !s.limits_disabled;
  el.replaceChildren(h('strong', { text: 'Rate limits are off. ' }),
    'Guests can guess passwords and slugs without limit. Turn rate_limits.enabled back on in Settings → Apps → Guest Access → Configuration.');
}

async function boot() {
  try {
    const s = await get(paths.settings);
    setSettings(s);
    showBanner(s);
  } catch {
    // Screens still work; they treat missing settings as defaults.
  }
  window.addEventListener('hashchange', route);
  route();
  pending.start();
}

boot();
