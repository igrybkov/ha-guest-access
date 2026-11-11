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
//
// The admin page shows and reads every time as wall-clock time in HA's
// time zone, not the browser's. Each helper takes the zone as an IANA
// name. An empty or unknown name means the browser's zone.

const pad = (n) => String(n).padStart(2, '0');
const DAY = 864e5;

// browserZone is the zone this browser runs in.
export function browserZone() {
  return new Intl.DateTimeFormat().resolvedOptions().timeZone;
}

// usableZone returns tz when this browser knows it, else the browser's
// zone.
export function usableZone(tz) {
  if (!tz) return browserZone();
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz });
    return tz;
  } catch {
    return browserZone();
  }
}

const partsFormats = new Map();

// wallParts returns the wall-clock time of instant ms in tz:
// {y, mo, d, h, mi, s, dow}. mo is 1 to 12. dow is 0 (Sunday) to 6.
export function wallParts(ms, tz) {
  const zone = usableZone(tz);
  let f = partsFormats.get(zone);
  if (!f) {
    // h23, so midnight is 00 and never 24.
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: zone, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric',
      hour: 'numeric', minute: 'numeric', second: 'numeric', weekday: 'short',
    });
    partsFormats.set(zone, f);
  }
  const p = {};
  for (const { type, value } of f.formatToParts(new Date(ms))) p[type] = value;
  return {
    y: +p.year, mo: +p.month, d: +p.day, h: +p.hour, mi: +p.minute, s: +p.second,
    dow: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(p.weekday),
  };
}

// wallAsUTC reads wall-clock fields as if they were UTC. Day and month
// overflow roll over, as with Date.UTC.
const wallAsUTC = (w) => Date.UTC(w.y, w.mo - 1, w.d, w.h || 0, w.mi || 0, w.s || 0);

// offsetAt is tz's offset from UTC at instant ms, in ms.
const offsetAt = (ms, tz) => wallAsUTC(wallParts(ms, tz)) - Math.floor(ms / 1000) * 1000;

// wallToUTC returns the instant (ms) of a wall-clock time in tz. Fields
// are {y, mo, d, h, mi, s}; missing time fields are 0. Fields may
// overflow (d: 32 is the next month's first day).
//
// Around a DST change a wall time can be missing or twice:
//   - In a gap (spring forward), a nonexistent time moves forward by the
//     gap: 02:30 on a day that jumps from 02:00 to 03:00 gives 03:30.
//   - In an overlap (fall back), an ambiguous time takes the first
//     occurrence, the one before the clocks go back.
export function wallToUTC(w, tz) {
  const guess = wallAsUTC(w);
  // Real zones change offset at most once a day, so the offsets a day
  // before and a day after cover both sides of any change.
  const before = offsetAt(guess - DAY, tz);
  const after = offsetAt(guess + DAY, tz);
  const hits = [...new Set([before, after])]
    .map((o) => guess - o)
    .filter((t) => wallAsUTC(wallParts(t, tz)) === guess);
  if (hits.length) return Math.min(...hits);
  return guess - before;
}

// toLocalInput formats instant d (a Date) for <input type=datetime-local>
// as wall-clock time in tz.
export function toLocalInput(d, tz) {
  if (!d || Number.isNaN(d.getTime())) return '';
  const w = wallParts(d.getTime(), tz);
  return `${String(w.y).padStart(4, '0')}-${pad(w.mo)}-${pad(w.d)}T${pad(w.h)}:${pad(w.mi)}`;
}

// fromLocalInput reads a datetime-local value as wall-clock time in tz.
// "" → null.
export function fromLocalInput(s, tz) {
  if (!s) return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(s);
  if (!m) return null;
  return new Date(wallToUTC({ y: +m[1], mo: +m[2], d: +m[3], h: +m[4], mi: +m[5] }, tz));
}

// daysInMonth is the number of days in month mo (1 to 12) of year y.
const daysInMonth = (y, mo) => new Date(Date.UTC(y, mo, 0)).getUTCDate();

// addMonth returns the same day and wall-clock time in tz one month
// after d. When the next month is shorter, it uses that month's last
// day: Jan 31 gives Feb 28, or Feb 29 in a leap year.
export function addMonth(d, tz) {
  const ms = d.getTime();
  const w = wallParts(ms, tz);
  const y = w.mo === 12 ? w.y + 1 : w.y;
  const mo = w.mo === 12 ? 1 : w.mo + 1;
  const day = Math.min(w.d, daysInMonth(y, mo));
  return new Date(wallToUTC({ ...w, y, mo, d: day }, tz) + (ms % 1000 + 1000) % 1000);
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
// Days, weekdays and times of day are wall-clock time in tz.
//   - today: now until midnight tonight.
//   - weekend: Friday 17:00 until Monday 00:00. From Friday 17:00 to the
//     end of Sunday it starts now.
//   - week: now until the same time of day 7 days later.
//   - month: now until the same day and time next month (addMonth).
export function preset(name, now, tz) {
  const ms = now.getTime();
  const w = wallParts(ms, tz);
  const at = (addDays, h = 0) => new Date(wallToUTC({ y: w.y, mo: w.mo, d: w.d + addDays, h }, tz));
  switch (name) {
    case 'month':
      return { from: null, until: addMonth(now, tz) };
    case 'today':
      return { from: null, until: at(1) };
    case 'week':
      return { from: null, until: new Date(wallToUTC({ ...w, d: w.d + 7 }, tz) + (ms % 1000 + 1000) % 1000) };
    case 'weekend': {
      const toMonday = w.dow === 0 ? 1 : 8 - w.dow;
      const until = at(toMonday);
      const friday = at(toMonday - 3, 17);
      return { from: now >= friday ? null : friday, until };
    }
    default:
      return null;
  }
}

// zoneNote says which zone the date inputs use. tz is HA's zone, or ""
// when HA has not reported one.
export function zoneNote(tz, browser = browserZone()) {
  if (tz && usableZone(tz) === tz) return `Times are in ${tz} (HA).`;
  return `Times are in ${browser} (this browser). HA has not reported a time zone.`;
}

// fmtTime shows a time as a short date and time in tz.
export function fmtTime(iso, tz) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 1900) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: usableZone(tz) });
}

// fmtWindow describes a link's window in tz.
export function fmtWindow(link, tz) {
  const from = fmtTime(link.valid_from, tz);
  const until = fmtTime(link.valid_until, tz);
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
    valid_from: v.valid_from && new Date(v.valid_from).getUTCFullYear() > 1 ? v.valid_from : null,
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

// brokenLinkNote is what the admin sees on a link that does not
// validate (a hand edit of links.yaml broke it).
export const brokenLinkNote = 'This link does not validate, so guests get "not found". Fix these and save:';

// linkProblems turns a link's problems into lines like
// "extra[0].access.network: ...". An empty list means the link is fine.
export function linkProblems(l) {
  return (l.problems || []).map((p) => (p.path ? `${p.path}: ${p.message}` : p.message));
}

// removePasswordPrompt is the confirm dialog for removing password p of
// link l. Removing the last one opens the link to anyone with the URL,
// so that case says so.
export function removePasswordPrompt(l, p) {
  const others = (l.passwords || []).filter((x) => x.id !== p.id).length;
  if (others === 0) {
    return {
      title: `Remove the last password, for ${p.label}?`,
      body: 'The link then has no password, so anyone with the URL can open it.',
      ok: 'Remove and open the link',
    };
  }
  return { title: `Remove the password for ${p.label}?`, body: 'Guests signed in with it lose access at once.', ok: 'Remove' };
}

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
      ['Token check (min, 0 = at start only)', t.ma_check_interval_min],
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
      ['IPv6 prefix length per client', r.ipv6_prefix],
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
      ['Open pages per address, per link', t.stream_max_per_ip],
    ] },
    { id: 'approval', title: 'Device approval', rows: [
      ['Device request expires (min)', a.pending_ttl_min],
      ['Pending requests per link', a.max_pending],
      ['Devices per link', a.max_devices],
      ['Device events per hour', a.events_per_hour],
      ['Device last seen update (min)', a.touch_throttle_min],
      ['HA notification per request', onOff(a.persistent_notification)],
      ['Waiting list refresh (s)', a.admin_poll_s],
    ] },
    { id: 'audit', title: 'Activity log', rows: [
      ['Log file', onOff(t.audit_enabled)],
      ['Max file size (KiB)', t.audit_max_file_kib],
      ['Refusals recorded per minute', t.audit_max_denials_per_min],
    ] },
    { id: 'tunables', title: 'Other', rows: [
      ['Location valid for (min)', t.location_ttl_min],
      ['Max location accuracy (m)', t.max_accuracy_m],
      ['Password window per IP (min)', t.password_window_per_ip_min],
      ['Password window per link (min)', t.password_window_per_link_min],
      ['Custom slug length', t.custom_slug_min_len != null ? `${t.custom_slug_min_len} to ${t.custom_slug_max_len}` : null],
      ['Short custom slug warning below', t.custom_slug_warn_len],
      ['Guest body limit (KiB)', t.guest_body_kib],
      ['Admin body limit (KiB)', t.admin_body_kib],
      ['Request read timeout (s)', t.http_read_timeout_s],
      ['Header limit (KiB)', t.max_header_kib],
      ['Diagnostics kept', t.diagnostics_requests],
    ] },
  ];
}

// ---- Waiting to be admitted ----

// PENDING_ROUTE opens the admin page on its pending panel. It is the same
// value as web.PendingRoute in Go.
//
// HA's frontend loads this page in an iframe from the app's ingress URL
// and drops the path tail, query and hash of its own URL. It passes the
// path after /app/<slug> to the page only as the route of a
// "home-assistant/properties" message, which it sends after the page
// asks with "home-assistant/subscribe-properties". So the notification
// links to /app/<slug>/pending, and the page reads the route from that
// message. Opened outside HA (the demo, or run-dev), the hash #/pending
// does the same.
export const PENDING_ROUTE = '/pending';

// wantsPending reports whether the page should open on the pending panel,
// from its hash (such as "#/pending") or the route path HA sent (such as
// "/pending"). Only the exact marker counts, with or without a trailing
// slash.
export function wantsPending(hash, routePath) {
  const isMarker = (p) => p === PENDING_ROUTE || p === `${PENDING_ROUTE}/`;
  const h = typeof hash === 'string' ? hash.replace(/^#/, '').split('?')[0] : '';
  return isMarker(h) || (typeof routePath === 'string' && isMarker(routePath));
}

// haRoutePath returns route.path of a "home-assistant/properties" message
// from HA's frontend, or null for any other message.
export function haRoutePath(data) {
  if (!data || typeof data !== 'object' || data.type !== 'home-assistant/properties') return null;
  const p = data.route && data.route.path;
  return typeof p === 'string' ? p : null;
}

// pollMs is how often the page asks for pending requests, from the
// approval.admin_poll_s option. Missing or bad values use the option's
// default.
export function pollMs(s) {
  const v = s && s.tunables && s.tunables.approval && s.tunables.approval.admin_poll_s;
  return Number.isInteger(v) && v >= 1 ? v * 1000 : 5000;
}

// ago says how long ago iso was, from nowMs. It counts elapsed time
// only, so the time zone does not matter.
export function ago(iso, nowMs) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.max(0, Math.floor((nowMs - t) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ago`;
  const hrs = Math.floor(m / 60);
  if (hrs < 24) return `${hrs} h ago`;
  return `${Math.floor(hrs / 24)} d ago`;
}

// pendingRows turns GET api/devices/pending into the rows of the pending
// panel, oldest first as the server sends them.
export function pendingRows(reqs, nowMs) {
  return (Array.isArray(reqs) ? reqs : []).map((r) => ({
    id: r.request_id,
    link: r.link_name || r.link_id || '',
    guest: r.label || '',
    device: r.device || '',
    code: r.code || '',
    asked: ago(r.created_at, nowMs),
  }));
}

// pendingBadge is the nav badge's text, or "" to hide it.
export function pendingBadge(n) {
  if (!Number.isInteger(n) || n <= 0) return '';
  return n > 99 ? '99+' : String(n);
}
