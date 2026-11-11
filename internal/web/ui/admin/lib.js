// Pure helpers for the admin UI: patterns, presets, form builders, problem
// mapping and audit formatting. Nothing here touches the DOM, so node
// --test can load it.

// ---- API paths ----

// paths holds every admin endpoint. All are relative, so they work under
// the ingress path prefix. Change a route here and nowhere else.
const enc = encodeURIComponent;
export const paths = {
  settings: 'api/settings',
  catalog: 'api/catalog',
  entities: 'api/entities',
  areas: 'api/areas',
  labels: 'api/labels',
  devices: 'api/devices',
  diagnostics: 'api/diagnostics',
  preview: 'api/preview',
  templates: 'api/templates',
  template: (id) => `api/templates/${enc(id)}`,
  links: 'api/links',
  link: (id) => `api/links/${enc(id)}`,
  revoke: (id) => `api/links/${enc(id)}/revoke`,
  rotate: (id) => `api/links/${enc(id)}/rotate`,
  passwords: (id) => `api/links/${enc(id)}/passwords`,
  password: (id, pid) => `api/links/${enc(id)}/passwords/${enc(pid)}`,
  qrSVG: (id) => `api/links/${enc(id)}/qr.svg`,
  linkDevice: (id, did) => `api/links/${enc(id)}/devices/${enc(did)}`,
  resetDevices: (id) => `api/links/${enc(id)}/devices/reset`,
  pending: (link) => (link ? `api/devices/pending?link=${enc(link)}` : 'api/devices/pending'),
  decide: (rid) => `api/devices/pending/${enc(rid)}`,
  audit: (q) => `api/audit${q ? `?${q}` : ''}`,
};

// ---- Patterns ----

export const SELECTOR_KEYS = ['entities', 'areas', 'labels', 'devices'];

// domainOf returns "light" for "light.kitchen" and for "light.turn_on".
export function domainOf(id) {
  const i = id.indexOf('.');
  return i < 0 ? '' : id.slice(0, i);
}

// wildcardOnly mirrors model.WildcardOnly: a pattern made only of "*" and
// "." would expose a whole domain or the whole house.
export const wildcardOnly = (p) => /^[*.]*$/.test(p);

export const isExact = (p) => !p.includes('*');

// patternProblem mirrors model.Selector.validate, in the same order.
// It returns "" for a good pattern.
export function patternProblem(p, inSelect) {
  if (p === '') return 'is empty';
  if (p.trim() !== p) return 'has leading or trailing spaces';
  if (inSelect && wildcardOnly(p)) return 'wildcard only: name an id or add a prefix';
  return '';
}

// selectorProblems checks every pattern of a selector. base is the path
// of the selector, such as "rules[0].select".
export function selectorProblems(sel, inSelect, base) {
  const out = [];
  for (const key of SELECTOR_KEYS) {
    (sel?.[key] || []).forEach((p, i) => {
      const msg = patternProblem(p, inSelect);
      if (msg) out.push({ path: `${base}.${key}[${i}]`, message: msg });
    });
  }
  return out;
}

// matchPattern mirrors authz.Match: "*" is any run of characters, the rest
// is literal and case-sensitive.
export function matchPattern(p, s) {
  if (isExact(p)) return p === s;
  const re = p.split('*').map((x) => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*');
  return new RegExp(`^${re}$`, 's').test(s);
}

const matchAny = (ps, s) => (ps || []).some((p) => matchPattern(p, s));

// pickedEntities mirrors authz expansion (select minus exclude), before
// the domain filter. inv holds the picker lists: entities (area_id is the
// effective area), areas, labels and devices. The editor uses it only to
// offer actions; the server preview is the source of truth.
export function pickedEntities(rule, inv) {
  const areas = new Map((inv.areas || []).map((a) => [a.area_id, a]));
  const devices = new Map((inv.devices || []).map((d) => [d.id, d]));
  const selects = (sel, all) => (e) => {
    if (!sel) return false;
    const ents = sel.entities || [];
    if (ents.some((p) => isExact(p) && p === e.entity_id)) return true;
    if (!all && (e.hidden || e.disabled || e.entity_category)) return false;
    if (ents.some((p) => !isExact(p) && matchPattern(p, e.entity_id))) return true;
    const labelHit = (ls) => (ls || []).some((l) => matchAny(sel.labels, l));
    if (labelHit(e.labels)) return true;
    if (e.device_id) {
      const d = devices.get(e.device_id);
      if (matchAny(sel.devices, e.device_id)) return true;
      if (d && ((d.name && matchAny(sel.devices, d.name)) || labelHit(d.labels))) return true;
    }
    if (e.area_id && (matchAny(sel.areas, e.area_id) || labelHit(areas.get(e.area_id)?.labels))) return true;
    return false;
  };
  const inc = selects(rule.select, false);
  const exc = selects(rule.exclude, true);
  return (inv.entities || []).filter((e) => inc(e) && !exc(e));
}

// actionChoices returns the catalog actions to offer for a rule: those in
// the given domains, plus any already ticked (flagged unmatched when out
// of the domains). domains null means unknown: offer everything.
export function actionChoices(actions, domains, ticked = []) {
  const tick = new Set(ticked);
  const out = [];
  for (const a of actions) {
    const inDomain = domains == null || domains.has(domainOf(a.id));
    if (inDomain || tick.has(a.id)) out.push({ ...a, unmatched: !inDomain });
  }
  // A ticked id the catalog doesn't know stays visible so it can be removed.
  const known = new Set(actions.map((a) => a.id));
  for (const id of ticked) if (!known.has(id)) out.push({ id, ui: '', unmatched: true, unknown: true });
  return out;
}

// searchOptions filters picker options by a case-insensitive substring of
// id or name. Exact id hits come first.
export function searchOptions(options, query, limit = 30) {
  const q = query.trim().toLowerCase();
  const hits = q
    ? options.filter((o) => o.id.toLowerCase().includes(q) || (o.name || '').toLowerCase().includes(q))
    : options.slice();
  hits.sort((a, b) => (b.id.toLowerCase() === q) - (a.id.toLowerCase() === q));
  return hits.slice(0, limit);
}

// ---- Dates ----

const pad = (n) => String(n).padStart(2, '0');

// toLocalInput formats a Date for <input type=datetime-local>.
export function toLocalInput(d) {
  if (!d || Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// fromLocalInput reads a datetime-local value as local time. "" → null.
export function fromLocalInput(s) {
  if (!s) return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(s);
  if (!m) return null;
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
}

const midnight = (d, addDays = 0) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + addDays);

// addMonth returns the same day and local time one month after d. When
// the next month is shorter, it uses that month's last day: Jan 31
// gives Feb 28, or Feb 29 in a leap year.
export function addMonth(d) {
  const y = d.getFullYear();
  const m = d.getMonth() + 1; // Date rolls month 12 into January
  const last = new Date(y, m + 1, 0).getDate();
  return new Date(y, m, Math.min(d.getDate(), last), d.getHours(), d.getMinutes(), d.getSeconds(), d.getMilliseconds());
}

// PRESETS are the window shortcuts. from null means "from now".
export const PRESETS = [
  ['today', 'Today'],
  ['weekend', 'This weekend'],
  ['week', '1 week'],
  ['month', '1 month'],
  ['custom', 'Custom'],
];

// preset returns {from, until} for a preset at now, or null for custom.
//   - today: now until midnight tonight.
//   - weekend: Friday 17:00 until Monday 00:00. From Friday 17:00 to the
//     end of Sunday it starts now.
//   - week: now until the same time 7 days later.
//   - month: now until the same day and time next month (addMonth).
export function preset(name, now) {
  switch (name) {
    case 'month':
      return { from: null, until: addMonth(now) };
    case 'today':
      return { from: null, until: midnight(now, 1) };
    case 'week':
      return { from: null, until: new Date(now.getTime() + 7 * 864e5) };
    case 'weekend': {
      const dow = now.getDay(); // 0 Sunday … 6 Saturday
      const toMonday = dow === 0 ? 1 : 8 - dow;
      const until = midnight(now, toMonday);
      const friday = new Date(until.getFullYear(), until.getMonth(), until.getDate() - 3, 17);
      return { from: now >= friday ? null : friday, until };
    }
    default:
      return null;
  }
}

// fmtTime shows a time as a short local date and time.
export function fmtTime(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1900) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// fmtWindow describes a link's window.
export function fmtWindow(link) {
  const from = fmtTime(link.valid_from);
  const until = fmtTime(link.valid_until);
  return from ? `${from} to ${until}` : `until ${until}`;
}

// ---- Form builders ----

const nonEmpty = (xs) => (xs || []).filter((x) => x !== '');

function cleanSelector(sel) {
  const out = {};
  for (const k of SELECTOR_KEYS) {
    const v = nonEmpty(sel?.[k]);
    if (v.length) out[k] = v;
  }
  return out;
}

// cleanAccess turns a form access into the model shape. null means the
// key is left out.
export function cleanAccess(acc) {
  if (!acc) return null;
  const out = {};
  if (acc.network === 'local') out.network = 'local';
  const m = Number(acc.near_home_m);
  if (Number.isFinite(m) && m > 0) out.near_home_m = Math.round(m);
  return out;
}

// parseVarValue reads a script variable: JSON when it parses, text
// otherwise. So 5 is a number, true a bool, and hello a string.
export function parseVarValue(s) {
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

// ruleState is a rule as the editor holds it. ownAccess says whether the
// rule replaces the template's access. vars is a list of [key, text].
export function ruleState(rule = {}, { forceAccess = false } = {}) {
  return {
    select: Object.fromEntries(SELECTOR_KEYS.map((k) => [k, [...(rule.select?.[k] || [])]])),
    exclude: Object.fromEntries(SELECTOR_KEYS.map((k) => [k, [...(rule.exclude?.[k] || [])]])),
    actions: [...(rule.actions || [])],
    ownAccess: forceAccess || rule.access != null,
    access: { network: rule.access?.network || 'any', near_home_m: rule.access?.near_home_m || 0 },
    vars: Object.entries(rule.variables || {}).map(([k, v]) => [k, typeof v === 'string' ? v : JSON.stringify(v)]),
  };
}

// buildRule turns editor state into a model.Rule.
export function buildRule(st) {
  const r = { select: cleanSelector(st.select), actions: [...st.actions] };
  const exc = cleanSelector(st.exclude);
  if (Object.keys(exc).length) r.exclude = exc;
  if (st.ownAccess) r.access = cleanAccess(st.access);
  const vars = (st.vars || []).filter(([k]) => k.trim() !== '');
  if (vars.length && st.actions.includes('script.run')) {
    r.variables = Object.fromEntries(vars.map(([k, v]) => [k.trim(), parseVarValue(v)]));
  }
  return r;
}

// slugifyID makes a template id from a name: a-z, 0-9, "_" and "-".
export function slugifyID(name) {
  return name.toLowerCase().normalize('NFKD').replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 64);
}

// buildTemplate returns exactly the model.Template fields, so the strict
// decoder on the server accepts it.
export function buildTemplate(f) {
  const t = { id: f.id, name: f.name.trim(), rules: f.rules.map(buildRule) };
  if (f.icon && f.icon.trim()) t.icon = f.icon.trim();
  const acc = cleanAccess(f.access);
  if (acc && Object.keys(acc).length) t.access = acc;
  if (f.party) t.party = true;
  return t;
}

// buildLink returns only the editable model.Link fields. A LinkView from
// the server also has status, path, url and warnings, which the server
// refuses. slug goes in only on create, and only for a custom slug.
export function buildLink(f, { create = false } = {}) {
  const l = {
    name: f.name.trim(),
    valid_until: f.valid_until ? new Date(f.valid_until).toISOString() : '',
    templates: [...(f.templates || [])],
    extra: (f.extra || []).map(buildRule),
    one_time: !!f.one_time,
    approval: { manual: !!f.approval?.manual, self_service: !!f.approval?.self_service },
  };
  if (f.note && f.note.trim()) l.note = f.note.trim();
  if (f.valid_from) l.valid_from = new Date(f.valid_from).toISOString();
  if (!l.valid_until) delete l.valid_until;
  if (create && f.customSlug && f.slug) l.slug = f.slug;
  return l;
}

// linkForm is the editor state for a LinkView (or a new link).
export function linkForm(v = {}) {
  return {
    name: v.name || '',
    note: v.note || '',
    valid_from: v.valid_from && new Date(v.valid_from).getFullYear() > 1 ? v.valid_from : null,
    valid_until: v.valid_until || null,
    templates: [...(v.templates || [])],
    extra: (v.extra || []).map((r) => ruleState(r, { forceAccess: true })),
    one_time: !!v.one_time,
    approval: { manual: !!v.approval?.manual, self_service: !!v.approval?.self_service },
    customSlug: false,
    slug: '',
  };
}

// splitTemplates splits a link's template ids into those that exist and
// those that don't. The server refuses a save that names a missing one.
export function splitTemplates(ids, templates) {
  const have = new Set(templates.map((t) => t.id));
  return { keep: ids.filter((id) => have.has(id)), missing: ids.filter((id) => !have.has(id)) };
}

// approvalMode names a link's approval setting.
export function approvalMode(a) {
  if (!a || (!a.manual && !a.self_service)) return 'off';
  if (a.manual && a.self_service) return 'both';
  return a.manual ? 'manual' : 'claim';
}

// ---- Problems ----

// normalizePath strips the "templates[N]." prefix that template policy
// checks add. Use it for template saves only: on a link, "templates[0]"
// is the link's first template id.
export const normalizeTemplatePath = (p) => (p || '').replace(/^templates\[\d+\]\.?/, '');

// parentPath drops the last segment: "a[0].b[1]" → "a[0].b" → "a[0]" → "a".
export function parentPath(p) {
  if (p.endsWith(']')) return p.slice(0, p.lastIndexOf('['));
  const i = p.lastIndexOf('.');
  return i < 0 ? '' : p.slice(0, i);
}

// fieldFor walks up a problem path until it finds a registered field.
// "" means no field: show it at the top of the form.
export function fieldFor(path, keys) {
  for (let p = path || ''; p; p = parentPath(p)) {
    if (keys.has(p)) return p;
  }
  return '';
}

// restLabel words the part of a path below its field: "[1]" is "Entry 2:",
// ".access.network" is "access.network:".
function restLabel(rest) {
  const m = /^\[(\d+)\]$/.exec(rest);
  return m ? `Entry ${Number(m[1]) + 1}:` : `${rest.replace(/^\./, '')}:`;
}

// groupProblems maps each problem to a field key. Nothing is dropped:
// a problem with no field goes under "".
export function groupProblems(problems, keys, normalize = (p) => p) {
  const out = new Map();
  for (const pr of problems || []) {
    const path = normalize(pr.path || '');
    const key = fieldFor(path, keys);
    const msg = key && key !== path ? `${restLabel(path.slice(key.length))} ${pr.message}` : pr.message;
    if (!out.has(key)) out.set(key, []);
    out.get(key).push(key ? msg : (path ? `${path}: ${pr.message}` : pr.message));
  }
  return out;
}

// ---- Links list ----

export const STATUS_LABELS = { active: 'Active', scheduled: 'Scheduled', expired: 'Expired', revoked: 'Revoked' };

// linkBadges lists the small tags on a link row.
export function linkBadges(l) {
  const out = [];
  const n = (l.passwords || []).length;
  if (n) out.push(n === 1 ? '1 password' : `${n} passwords`);
  const mode = approvalMode(l.approval);
  if (mode !== 'off') out.push({ manual: 'Manual approval', claim: 'Claim mode', both: 'Approval' }[mode]);
  if (l.one_time) out.push('One-time');
  const p = (l.pending || []).length;
  if (p) out.push(p === 1 ? '1 device waiting' : `${p} devices waiting`);
  return out;
}

// rotateWarning returns a warning for a slug rotation, or "".
export function rotateWarning(l) {
  const mode = approvalMode(l.approval);
  if (mode === 'claim') return 'Approved devices keep access after a new slug. Use Reset devices to remove them.';
  if (mode !== 'off') return 'Approved devices keep access after a new slug.';
  return '';
}

// grantSummary groups preview grants by entity: [{entity_id, name, actions}].
export function grantSummary(grants, actionUI = {}) {
  const by = new Map();
  for (const g of grants || []) {
    if (!by.has(g.entity_id)) by.set(g.entity_id, { entity_id: g.entity_id, name: g.name || g.entity_id, actions: [] });
    by.get(g.entity_id).actions.push(actionUI[g.action] || g.action);
  }
  return [...by.values()];
}

// ---- Audit ----

export const AUDIT_GROUPS = [
  ['', 'All events'],
  ['guest', 'Guest'],
  ['device', 'Device'],
  ['admin', 'Admin'],
  ['denied', 'Denied only'],
];

// auditQuery builds the query for GET api/audit. "denied" is not a
// prefix, so it filters in the browser (auditKeep) instead.
export function auditQuery({ link = '', group = '', limit = 50 } = {}) {
  const q = new URLSearchParams();
  if (link) q.set('link', link);
  if (group && group !== 'denied') q.set('event', `${group}.`);
  q.set('limit', String(limit));
  return q.toString();
}

// auditOffText explains an empty log when the audit file is off.
export const auditOffText = 'The activity log file is off, so nothing is kept here. Turn on audit.enabled in Settings → Apps → Guest Access → Configuration. The app log still has every event.';

export const isDenied = (r) => /\.denied$/.test(r.event || '') || r.result === 'denied';

// auditKeep applies the filters the server can't.
export function auditKeep(group, r) {
  return group === 'denied' ? isDenied(r) : true;
}

// auditWho names who acted: the guest (password label, device), else the
// HA user. The user is an object {id, name, display_name}.
export function auditWho(r) {
  const dev = r.device_label || r.device || '';
  if (r.password_label && dev) return `${r.password_label} (${dev})`;
  const u = r.user && typeof r.user === 'object' ? r.user.display_name || r.user.name || r.user.id || '' : '';
  return r.password_label || dev || u;
}

// auditWhat names what was done: the action on an entity, else the route.
export function auditWhat(r) {
  if (r.entity_id && r.action) return `${r.action} on ${r.entity_id}`;
  return r.entity_id || r.action || r.route || '';
}

// auditResult joins result and reason.
export function auditResult(r) {
  if (r.result && r.reason) return `${r.result}: ${r.reason}`;
  return r.result || r.reason || '';
}

// ---- Settings ----

// tokenState is the Music Assistant token row of the settings.
function tokenState(t) {
  if (!t?.set) return 'Not set';
  return t.refused ? 'Refused by Music Assistant' : 'Set';
}

// settingsWarnings lists the problems the Settings page shows at the top,
// from GET api/settings.
export function settingsWarnings(s) {
  const out = [];
  if (s.limits_disabled) out.push('Rate limits are off. Not recommended.');
  if (s.ma_token?.refused) {
    out.push('Music Assistant refused the token, so guests see no party button. ' +
      'Make a new long-lived token in Music Assistant and put it in the music_assistant.token option.');
  }
  return out;
}

// settingsSections lays out GET api/settings as [{id, title, rows}],
// where rows are [label, value]. A null or "" value shows as "Not set".
export function settingsSections(s) {
  const r = s.rate_limits || {};
  const t = s.tunables || {};
  const a = t.approval || {};
  const g = s.guest || {};
  const list = (xs) => (xs && xs.length ? xs.join(', ') : 'None');
  const onOff = (b) => (b ? 'On' : 'Off');
  return [
    { id: 'general', title: 'General', rows: [
      ['Public base URL', s.public_base_url],
      ['Home radius default (m)', s.home_radius_default_m],
      ['Trusted proxies', list(s.trusted_proxies)],
    ] },
    { id: 'local', title: 'Local access', rows: [
      ['Local only', onOff(s.local_access?.enabled)],
      ['Local networks', list(s.local_access?.networks)],
    ] },
    { id: 'ma', title: 'Music Assistant', rows: [
      ['URL', s.music_assistant?.url],
      ['Token', tokenState(s.ma_token)],
      ['Timeout (ms)', t.ma_timeout_ms],
    ] },
    { id: 'guest', title: 'Guest page', rows: [
      ['Title', g.title],
      ['Welcome', g.welcome],
      ['Accent color', g.accent_color],
      ['Slider send interval (ms)', g.slider_interval_ms],
    ] },
    { id: 'limits', title: 'Rate limits', rows: [
      ['Guest requests per minute', r.guest_per_min],
      ['Guest burst', r.guest_burst],
      ['Page files per minute', r.assets_per_min],
      ['Page files burst', r.assets_burst],
      ['Actions per minute', r.actions_per_min],
      ['Actions burst', r.actions_burst],
      ['Slider actions per minute, per entity', r.continuous_per_min],
      ['Slider actions burst', r.continuous_burst],
      ['Open streams per link', r.max_streams_per_link],
      ['Slug misses per hour per IP', r.misses_per_hour_per_ip],
      ['Slug misses per hour, all IPs', r.misses_per_hour_global],
      ['Password fails per IP', r.password_fails_per_ip],
      ['Password fails per link', r.password_fails_per_link],
      ['Password lockout per address (min)', r.password_lockout_min],
      ['Slowed link delay (s)', r.password_link_delay_s],
      ['Admin requests per minute', r.admin_per_min],
    ] },
    { id: 'ha', title: 'Home Assistant', rows: [
      ['Service call timeout (s)', t.ha_call_timeout_s],
      ['Event timeout (s)', t.ha_event_timeout_s],
      ['Inventory max age (s)', t.inventory_max_age_s],
    ] },
    { id: 'stream', title: 'Live updates', rows: [
      ['Access recheck (s)', t.stream_recheck_s],
      ['Heartbeat (s)', t.stream_heartbeat_s],
      ['Reconnect wait (s)', t.stream_retry_after_s],
    ] },
    { id: 'approval', title: 'Device approval', rows: [
      ['Device request expires (min)', a.pending_ttl_min],
      ['Pending requests per link', a.max_pending],
      ['Devices per link', a.max_devices],
      ['Device events per hour', a.events_per_hour],
      ['Device last seen update (min)', a.touch_throttle_min],
    ] },
    { id: 'audit', title: 'Activity log', rows: [
      ['Log file', onOff(t.audit_enabled)],
      ['Max file size (KiB)', t.audit_max_file_kib],
    ] },
    { id: 'tunables', title: 'Other', rows: [
      ['Location valid for (min)', t.location_ttl_min],
      ['Max location accuracy (m)', t.max_accuracy_m],
      ['Password window per IP (min)', t.password_window_per_ip_min],
      ['Password window per link (min)', t.password_window_per_link_min],
      ['Custom slug length', t.custom_slug_min_len != null ? `${t.custom_slug_min_len} to ${t.custom_slug_max_len}` : null],
      ['Guest body limit (KiB)', t.guest_body_kib],
      ['Admin body limit (KiB)', t.admin_body_kib],
      ['Diagnostics kept', t.diagnostics_requests],
    ] },
  ];
}
