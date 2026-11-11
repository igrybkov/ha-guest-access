// Activity screen: the audit log, newest first. Most fields come from
// guests, so everything goes in as text only.
import { h, fill, get, table, select } from './dom.js';
import { paths, fmtTime, AUDIT_GROUPS, auditQuery, auditKeep, auditWho, auditWhat, auditResult, isDenied, auditOffText } from './lib.js';

const PAGE = 50;

export async function render(view, rest, params) {
  const state = { link: params.get('link') || '', group: params.get('group') || '', limit: PAGE };
  const { links } = await get(paths.links).catch(() => ({ links: [] }));
  const linkSel = select([['', 'All links'], ...links.map((l) => [l.id, l.name])], state.link, { 'aria-label': 'Link' });
  const groupSel = select(AUDIT_GROUPS, state.group, { 'aria-label': 'Events' });
  const body = h('div', {}, h('p', { class: 'muted', text: 'Loading…' }));
  const more = h('button', { type: 'button', text: 'Load more', hidden: true });
  let seq = 0;

  async function load() {
    const my = ++seq;
    more.disabled = true;
    let res;
    try {
      res = await get(paths.audit(auditQuery(state)));
    } catch (e) {
      if (my !== seq) return;
      fill(body, h('p', { class: 'error', text: `Could not load: ${e.message}` }));
      more.hidden = true;
      return;
    }
    if (my !== seq) return;
    if (!res.file) {
      fill(body, h('p', { class: 'note', text: auditOffText }));
      more.hidden = true;
      return;
    }
    const all = res.records || [];
    const recs = all.filter((r) => auditKeep(state.group, r));
    fill(body, recs.length
      ? table(['Time', 'Event', 'Link', 'Who', 'What', 'Result', 'Client IP'], recs.map((r) => h('tr', { class: isDenied(r) ? 'denied' : '' },
        h('td', { class: 'small nowrap', text: fmtTime(r.time) }),
        h('td', {}, h('code', { text: r.event || '' })),
        h('td', { class: 'small', text: r.link_name || r.link_id || '' }),
        h('td', { class: 'small', text: auditWho(r) }),
        h('td', { class: 'small', text: auditWhat(r) }),
        h('td', { class: 'small', text: auditResult(r) }),
        h('td', {}, h('code', { class: 'small', text: r.client_ip || '' })))))
      : h('p', { class: 'muted', text: 'Nothing here.' }));
    // A full page means there may be more.
    more.hidden = all.length < state.limit;
    more.disabled = false;
  }

  const refilter = () => {
    state.link = linkSel.value;
    state.group = groupSel.value;
    state.limit = PAGE;
    load();
  };
  linkSel.addEventListener('change', refilter);
  groupSel.addEventListener('change', refilter);
  more.addEventListener('click', () => { state.limit += PAGE; load(); });

  fill(view, 
    h('div', { class: 'toolbar' }, h('h1', { class: 'grow', text: 'Activity' }), linkSel, groupSel,
      h('button', { type: 'button', text: 'Refresh', onclick: load })),
    h('p', { class: 'muted small', text: 'Times are in this browser\'s time zone.' }),
    body,
    h('div', { class: 'actions' }, more));
  load();
}
