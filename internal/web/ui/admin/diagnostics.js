// Diagnostics screen: how the app saw recent guest requests. Peer and
// headers come from whoever sent the request, so they go in as text only.
import { h, fill, get, table, docsLink } from './dom.js';
import { paths, fmtTime } from './lib.js';

function flags(e) {
  const out = [];
  if (e.untrusted_forwarding) out.push(['Untrusted forwarding', 'bad']);
  if (e.unresolved) out.push(['Unresolved client', 'bad']);
  if (e.via_proxy) out.push(['Via proxy', '']);
  if (e.local) out.push(['Local', '']);
  if (e.https) out.push(['HTTPS', '']);
  return out.map(([t, c]) => h('span', { class: `badge ${c}`, text: t }));
}

function headers(hs) {
  const entries = Object.entries(hs || {});
  if (!entries.length) return h('span', { class: 'muted', text: 'None' });
  return h('ul', { class: 'plain-list small' }, entries.map(([k, v]) => h('li', {}, h('code', { text: `${k}: ${v}` }))));
}

export async function render(view) {
  const d = await get(paths.diagnostics);
  const reqs = d.requests || [];
  fill(view, 
    h('div', { class: 'toolbar' }, h('h1', { class: 'grow', text: 'Diagnostics' }),
      h('button', { type: 'button', text: 'Refresh', onclick: () => render(view) })),
    (d.warnings || []).length > 0 && h('ul', { class: 'warning' }, d.warnings.map((w) => h('li', { text: w }))),
    h('details', { class: 'note' }, h('summary', { text: 'How to set trusted proxies' }),
      h('ol', { class: 'small' },
        h('li', { text: 'Open a guest link once on home Wi-Fi and once on mobile data.' }),
        h('li', { text: 'Look at Peer below. If guest requests reach the app through a reverse proxy or a tunnel (such as Nginx Proxy Manager or cloudflared), Peer is that proxy.' }),
        h('li', { text: 'Add each such peer to trusted_proxies in Settings → Apps → Guest Access → Configuration. Trust both the proxy and the tunnel.' }),
        h('li', { text: 'Check again: Client should now show the guest IP, and "Untrusted forwarding" should be gone.' })),
      h('p', { class: 'small' }, 'Never trust a peer that the internet can reach directly. ', docsLink())),
    reqs.length
      ? table(['Time', 'Route', 'Peer', 'Client', 'Headers', 'Flags'], reqs.map((e) => h('tr', {},
        h('td', { class: 'small', text: fmtTime(e.at) }),
        h('td', {}, h('code', { text: e.route })),
        h('td', {}, h('code', { text: e.peer })),
        h('td', {}, h('code', { text: e.client || '?' })),
        h('td', {}, headers(e.headers)),
        h('td', {}, h('div', { class: 'chips' }, flags(e))))))
      : h('p', { class: 'muted', text: 'No guest requests yet. Open a guest link, then refresh.' }));
}
