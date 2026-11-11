// Settings screen: read-only. Settings are app options, edited in the
// Supervisor Configuration tab.
import { h, fill, get, docsLink } from './dom.js';
import { paths, settingsSections } from './lib.js';

const where = 'Change these in Settings → Apps → Guest Access → Configuration. Saving there restarts the app.';

// dl renders [label, value] pairs as a definition list.
function dl(rows) {
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: v === '' || v == null ? 'Not set' : String(v) })]));
}

// extras are the notes some sections carry under their rows.
function extras(id, s) {
  switch (id) {
    case 'local':
      return h('p', { class: 'muted small' },
        'Local only is best effort. Guests on your Wi-Fi may still count as outside: iCloud Private Relay, custom DNS or a VPN. ',
        'They lose those controls; nobody gains any. ', docsLink());
    case 'limits':
      return s.limits_disabled && h('p', { class: 'banner-inline', text: 'Rate limits are off. Not recommended.' });
    case 'guest':
      return h('p', { class: 'muted small', text: 'Sliders send while the guest drags, at most once per interval.' });
    default:
      return null;
  }
}

export async function render(view) {
  const s = await get(paths.settings);
  fill(view,
    h('div', { class: 'toolbar' }, h('h1', { text: 'Settings' })),
    h('p', { class: 'note', text: where }),
    settingsSections(s).map((sec) => h('section', { class: 'card' }, h('h2', { text: sec.title }), dl(sec.rows), extras(sec.id, s))));
}
