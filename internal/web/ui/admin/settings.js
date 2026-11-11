// Settings screen: read-only. Settings are app options, edited in the
// Supervisor Configuration tab.
import { h, fill, get, docsLink } from './dom.js';
import { paths } from './lib.js';

const where = 'Change these in Settings → Apps → Guest Access → Configuration. Saving there restarts the app.';

// rows renders [label, value] pairs as a definition list.
function dl(rows) {
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: v === '' || v == null ? 'Not set' : String(v) })]));
}

const list = (xs) => (xs && xs.length ? xs.join(', ') : 'None');
const yes = (b) => (b ? 'On' : 'Off');

export async function render(view) {
  const s = await get(paths.settings);
  const r = s.rate_limits || {};
  const t = s.tunables || {};
  const a = t.approval || {};
  fill(view, 
    h('div', { class: 'toolbar' }, h('h1', { text: 'Settings' })),
    h('p', { class: 'note', text: where }),
    h('section', { class: 'card' }, h('h2', { text: 'General' }), dl([
      ['Public base URL', s.public_base_url],
      ['Home radius default (m)', s.home_radius_default_m],
      ['Trusted proxies', list(s.trusted_proxies)],
    ])),
    h('section', { class: 'card' }, h('h2', { text: 'Local access' }), dl([
      ['Local only', yes(s.local_access?.enabled)],
      ['Local networks', list(s.local_access?.networks)],
    ]),
    h('p', { class: 'muted small' },
      'Local only is best effort. Guests on your Wi-Fi may still count as outside: iCloud Private Relay, custom DNS or a VPN. ',
      'They lose those controls; nobody gains any. ', docsLink())),
    h('section', { class: 'card' }, h('h2', { text: 'Music Assistant' }), dl([
      ['URL', s.music_assistant?.url],
      ['Token', s.ma_token?.set ? 'Set' : 'Not set'],
      ['Timeout (ms)', t.ma_timeout_ms],
    ])),
    h('section', { class: 'card' }, h('h2', { text: 'Guest page' }), dl([
      ['Title', s.guest?.title],
      ['Welcome', s.guest?.welcome],
      ['Accent color', s.guest?.accent_color],
    ])),
    h('section', { class: 'card' }, h('h2', { text: 'Rate limits' }),
      s.limits_disabled && h('p', { class: 'banner-inline', text: 'Rate limits are off. Not recommended.' }),
      dl([
        ['Guest requests per minute', r.guest_per_min],
        ['Guest burst', r.guest_burst],
        ['Actions per minute', r.actions_per_min],
        ['Actions burst', r.actions_burst],
        ['Open streams per link', r.max_streams_per_link],
        ['Slug misses per hour per IP', r.misses_per_hour_per_ip],
        ['Slug misses per hour, all IPs', r.misses_per_hour_global],
        ['Password fails per IP', r.password_fails_per_ip],
        ['Password fails per link', r.password_fails_per_link],
        ['Password lockout (min)', r.password_lockout_min],
        ['Admin requests per minute', r.admin_per_min],
      ])),
    h('section', { class: 'card' }, h('h2', { text: 'Tunables' }), dl([
      ['Location valid for (min)', t.location_ttl_min],
      ['Max location accuracy (m)', t.max_accuracy_m],
      ['Password window per IP (min)', t.password_window_per_ip_min],
      ['Password window per link (min)', t.password_window_per_link_min],
      ['Custom slug length', t.custom_slug_min_len != null ? `${t.custom_slug_min_len} to ${t.custom_slug_max_len}` : null],
      ['Guest body limit (KiB)', t.guest_body_kib],
      ['Admin body limit (KiB)', t.admin_body_kib],
      ['Diagnostics kept', t.diagnostics_requests],
      ['Inventory max age (s)', t.inventory_max_age_s],
      ['Device request expires (min)', a.pending_ttl_min],
      ['Pending requests per link', a.max_pending],
      ['Devices per link', a.max_devices],
      ['Device events per hour', a.events_per_hour],
      ['Device last seen update (min)', a.touch_throttle_min],
    ])));
}
