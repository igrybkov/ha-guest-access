// Run with: node --test internal/web/ui/admin/lib_test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  wildcardOnly, patternProblem, selectorProblems, matchPattern, pickedEntities, actionChoices, searchOptions,
  preset, toLocalInput, fromLocalInput, buildTemplate, buildLink, buildRule, ruleState, linkForm, cleanAccess,
  parseVarValue, slugifyID, normalizeTemplatePath, parentPath, fieldFor, groupProblems,
  approvalMode, splitTemplates, templateRows, moveItem, linkBadges, rotateWarning, grantSummary,
  auditQuery, auditKeep, auditWho, auditWhat, auditResult, paths, settingsSections, settingsWarnings, addMonth, PRESETS,
  linkProblems, removePasswordPrompt, wallToUTC, wallParts, usableZone, browserZone, zoneNote, fmtTime, fmtWindow,
  PENDING_ROUTE, wantsPending, haRoutePath, pollMs, ago, pendingRows, pendingBadge,
} from './lib.js';

// ---- Patterns ----

test('wildcard only mirrors model.WildcardOnly', () => {
  for (const p of ['*', '*.*', '**', '.', '', '*..*']) assert.equal(wildcardOnly(p), true, p);
  for (const p of ['light.*', '*.guest_*', 'a', '*a*', '* ']) assert.equal(wildcardOnly(p), false, p);
});

test('pattern problems come in the server order', () => {
  assert.equal(patternProblem('', true), 'is empty');
  assert.equal(patternProblem('', false), 'is empty');
  assert.equal(patternProblem(' *', true), 'has leading or trailing spaces');
  assert.match(patternProblem('*.*', true), /wildcard only/);
  assert.equal(patternProblem('*.*', false), '', 'exclude allows wildcard only');
  assert.equal(patternProblem('light.guest_*', true), '');
});

test('selector problems carry field paths', () => {
  const got = selectorProblems({ entities: ['light.a', '*'], devices: ['*.*'] }, true, 'rules[1].select');
  assert.deepEqual(got.map((p) => p.path), ['rules[1].select.entities[1]', 'rules[1].select.devices[0]']);
  assert.deepEqual(selectorProblems({ entities: ['*'] }, false, 'x'), []);
});

test('matchPattern: star is any run, the rest literal and case-sensitive', () => {
  assert.ok(matchPattern('light.guest_*', 'light.guest_room'));
  assert.ok(matchPattern('light.guest_*', 'light.guest_'));
  assert.ok(matchPattern('*.guest_*_lamp', 'light.guest_a_b_lamp'));
  assert.ok(!matchPattern('light.guest_*', 'Light.guest_room'));
  assert.ok(!matchPattern('light.a', 'lightxa'), 'a dot is literal in an exact id');
  assert.ok(!matchPattern('light.a*', 'lightxa'), 'a dot is literal in a pattern');
  assert.ok(!matchPattern('a(b|c)*', 'ab'), 'regex syntax is literal');
  assert.ok(matchPattern('a(b|c)*', 'a(b|c)x'));
});

// ---- Domain filter ----

const inv = {
  entities: [
    { entity_id: 'light.guest', device_id: 'd1', area_id: 'guest_room', labels: [] },
    { entity_id: 'fan.guest', area_id: 'guest_room', labels: [] },
    { entity_id: 'lock.front', area_id: 'hall', labels: ['guest_ok'] },
    { entity_id: 'sensor.guest_temp', area_id: 'guest_room', labels: [], entity_category: 'diagnostic' },
    { entity_id: 'switch.hidden', area_id: 'guest_room', labels: [], hidden: true },
    { entity_id: 'media_player.sonos', device_id: 'd2', area_id: 'living', labels: [] },
    { entity_id: 'light.living', area_id: 'living', labels: [] },
  ],
  areas: [{ area_id: 'guest_room', name: 'Guest room', labels: [] }, { area_id: 'living', name: 'Living', labels: ['music'] }],
  labels: [{ label_id: 'guest_ok', name: 'Guest OK' }],
  devices: [{ id: 'd1', name: 'Guest lamp', labels: [] }, { id: 'd2', name: 'Sonos One', labels: [] }],
};
const ids = (rule) => pickedEntities(rule, inv).map((e) => e.entity_id).sort();

test('area expansion skips hidden and categorized entities', () => {
  assert.deepEqual(ids({ select: { areas: ['guest_room'] } }), ['fan.guest', 'light.guest']);
});

test('an exact entity id includes a hidden entity; a pattern does not', () => {
  assert.deepEqual(ids({ select: { entities: ['switch.hidden'] } }), ['switch.hidden']);
  assert.deepEqual(ids({ select: { entities: ['switch.*'] } }), []);
});

test('labels expand on entities, devices and areas; devices match by name', () => {
  assert.deepEqual(ids({ select: { labels: ['guest_ok'] } }), ['lock.front']);
  assert.deepEqual(ids({ select: { labels: ['music'] } }), ['light.living', 'media_player.sonos']);
  assert.deepEqual(ids({ select: { devices: ['Sonos*'] } }), ['media_player.sonos']);
});

test('exclude wins and also removes hidden entities', () => {
  assert.deepEqual(ids({ select: { areas: ['living'] }, exclude: { entities: ['light.*'] } }), ['media_player.sonos']);
});

test('actions are filtered to matched domains, ticked ones stay', () => {
  const actions = [{ id: 'light.turn_on' }, { id: 'fan.turn_on' }, { id: 'lock.unlock' }, { id: 'script.run' }];
  const got = actionChoices(actions, new Set(['light', 'fan']), ['lock.unlock', 'gone.action']);
  assert.deepEqual(got.map((a) => [a.id, a.unmatched]), [
    ['light.turn_on', false], ['fan.turn_on', false], ['lock.unlock', true], ['gone.action', true],
  ]);
  assert.equal(got.at(-1).unknown, true);
  assert.equal(actionChoices(actions, null).length, 4, 'unknown domains offer everything');
  assert.deepEqual(actionChoices(actions, new Set()), []);
});

test('search matches id or name, exact id first', () => {
  const opts = [{ id: 'light.kitchen_main', name: 'Kitchen' }, { id: 'light.k', name: 'Kitchen 2' }, { id: 'fan.x', name: 'Desk' }];
  assert.deepEqual(searchOptions(opts, 'KITCHEN').map((o) => o.id), ['light.kitchen_main', 'light.k']);
  assert.deepEqual(searchOptions(opts, 'light.k').map((o) => o.id)[0], 'light.k');
  assert.deepEqual(searchOptions(opts, 'desk').map((o) => o.id), ['fan.x']);
  assert.equal(searchOptions(opts, '', 2).length, 2);
});

// ---- Dates in HA's zone ----
// Every helper gets the zone explicitly, and the tests check exact UTC
// instants, so they pass in any machine zone. make test-ui runs them
// with TZ=UTC and TZ=Asia/Tokyo.

const CHI = 'America/Chicago';
const KYIV = 'Europe/Kyiv';
const iso = (d) => d.toISOString();
const utc = (s) => new Date(s);

test('wallToUTC on ordinary days', () => {
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 7, d: 4, h: 12 }, CHI))), '2026-07-04T17:00:00.000Z');
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 11, d: 11, h: 12 }, CHI))), '2026-11-11T18:00:00.000Z');
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 7, d: 4, h: 12 }, KYIV))), '2026-07-04T09:00:00.000Z');
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 11, d: 11, h: 12 }, KYIV))), '2026-11-11T10:00:00.000Z');
  // Fields may overflow, as with Date.UTC.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 11, d: 31 }, CHI))), '2026-12-01T06:00:00.000Z');
});

test('wallToUTC: a time in the spring-forward gap moves forward', () => {
  // Chicago skips 02:00 to 03:00 on 8 March 2026. 02:30 becomes 03:30 CDT.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 3, d: 8, h: 2, mi: 30 }, CHI))), '2026-03-08T08:30:00.000Z');
  // Kyiv skips 03:00 to 04:00 on 29 March 2026. 03:30 becomes 04:30 EEST.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 3, d: 29, h: 3, mi: 30 }, KYIV))), '2026-03-29T01:30:00.000Z');
  // The edges of the gap.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 3, d: 8, h: 1, mi: 59 }, CHI))), '2026-03-08T07:59:00.000Z');
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 3, d: 8, h: 3 }, CHI))), '2026-03-08T08:00:00.000Z');
});

test('wallToUTC: a time in the fall-back overlap takes the first occurrence', () => {
  // Chicago shows 01:00 to 02:00 twice on 1 November 2026: CDT, then CST.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 11, d: 1, h: 1, mi: 30 }, CHI))), '2026-11-01T06:30:00.000Z');
  // Kyiv shows 03:00 to 04:00 twice on 25 October 2026: EEST, then EET.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 10, d: 25, h: 3, mi: 30 }, KYIV))), '2026-10-25T00:30:00.000Z');
  // Just after the overlap there is one answer.
  assert.equal(iso(new Date(wallToUTC({ y: 2026, mo: 11, d: 1, h: 2 }, CHI))), '2026-11-01T08:00:00.000Z');
});

test('wallParts reads the wall clock and weekday in a zone', () => {
  const t = Date.parse('2026-11-14T00:00:00Z');
  assert.deepEqual(wallParts(t, CHI), { y: 2026, mo: 11, d: 13, h: 18, mi: 0, s: 0, dow: 5 });
  assert.deepEqual(wallParts(t, KYIV), { y: 2026, mo: 11, d: 14, h: 2, mi: 0, s: 0, dow: 6 });
  // Midnight is hour 0, never 24.
  assert.equal(wallParts(Date.parse('2026-11-12T06:00:00Z'), CHI).h, 0);
});

test('datetime-local values are wall-clock time in the zone', () => {
  const d = utc('2026-11-14T00:00:00Z');
  assert.equal(toLocalInput(d, CHI), '2026-11-13T18:00');
  assert.equal(toLocalInput(d, KYIV), '2026-11-14T02:00');
  assert.equal(iso(fromLocalInput('2026-11-13T18:00', CHI)), '2026-11-14T00:00:00.000Z');
  assert.equal(iso(fromLocalInput('2026-11-14T02:00', KYIV)), '2026-11-14T00:00:00.000Z');
  assert.equal(iso(fromLocalInput('2026-03-08T02:30', CHI)), '2026-03-08T08:30:00.000Z', 'gap');
  assert.equal(iso(fromLocalInput('2026-11-01T01:30', CHI)), '2026-11-01T06:30:00.000Z', 'overlap');
  assert.equal(fromLocalInput('', CHI), null);
  assert.equal(fromLocalInput('nope', CHI), null);
  assert.equal(toLocalInput(new Date('x'), CHI), '');
});

test('today: now until midnight tonight in the zone', () => {
  const w = preset('today', utc('2026-11-11T20:30:00Z'), CHI); // 14:30 CST
  assert.equal(w.from, null);
  assert.equal(iso(w.until), '2026-11-12T06:00:00.000Z');
  // 23:30 UTC is already tomorrow in Kyiv.
  assert.equal(iso(preset('today', utc('2026-11-11T23:30:00Z'), KYIV).until), '2026-11-12T22:00:00.000Z');
});

test('1 week keeps the time of day in the zone across a DST change', () => {
  const w = preset('week', utc('2026-11-11T20:30:00Z'), CHI);
  assert.equal(w.from, null);
  assert.equal(iso(w.until), '2026-11-18T20:30:00.000Z');
  // 14:30 CDT on 28 October gives 14:30 CST on 4 November: 7 days and 1 hour.
  assert.equal(iso(preset('week', utc('2026-10-28T19:30:00Z'), CHI).until), '2026-11-04T20:30:00.000Z');
});

test('weekend on a weekday: Friday 17:00 to Monday 00:00 in the zone', () => {
  const w = preset('weekend', utc('2026-11-11T15:00:00Z'), CHI); // Wednesday 09:00 CST
  assert.equal(iso(w.from), '2026-11-13T23:00:00.000Z');
  assert.equal(iso(w.until), '2026-11-16T06:00:00.000Z');
  const k = preset('weekend', utc('2026-11-11T07:00:00Z'), KYIV); // Wednesday 09:00 EET
  assert.equal(iso(k.from), '2026-11-13T15:00:00.000Z');
  assert.equal(iso(k.until), '2026-11-15T22:00:00.000Z');
});

test('weekend on Friday evening in the zone, while UTC and Tokyo already have Saturday', () => {
  // Friday 18:00 CST is Saturday 00:00 UTC and Saturday 09:00 in Tokyo.
  const w = preset('weekend', utc('2026-11-14T00:00:00Z'), CHI);
  assert.equal(w.from, null);
  assert.equal(iso(w.until), '2026-11-16T06:00:00.000Z');
  // Friday 09:00 CST: the weekend has not started.
  assert.equal(iso(preset('weekend', utc('2026-11-13T15:00:00Z'), CHI).from), '2026-11-13T23:00:00.000Z');
  // Saturday and the last minute of Sunday start now.
  for (const now of ['2026-11-14T18:00:00Z', '2026-11-16T05:59:00Z']) {
    const x = preset('weekend', utc(now), CHI);
    assert.equal(x.from, null, now);
    assert.equal(iso(x.until), '2026-11-16T06:00:00.000Z', now);
  }
  // Friday 18:00 in Kyiv is still Friday 10:00 in Chicago.
  const k = preset('weekend', utc('2026-11-13T16:00:00Z'), KYIV);
  assert.equal(k.from, null);
  assert.equal(iso(k.until), '2026-11-15T22:00:00.000Z');
});

test('weekend crosses a month end', () => {
  const w = preset('weekend', utc('2026-01-28T16:00:00Z'), CHI); // Wednesday 10:00 CST
  assert.equal(iso(w.from), '2026-01-30T23:00:00.000Z');
  assert.equal(iso(w.until), '2026-02-02T06:00:00.000Z');
});

test('custom is no preset', () => {
  assert.equal(preset('custom', new Date(), CHI), null);
});

test('an empty or unknown zone falls back to the browser zone', () => {
  const now = utc('2026-11-14T00:00:00Z');
  const here = browserZone();
  assert.ok(here);
  assert.equal(usableZone(''), here);
  assert.equal(usableZone('Mars/Olympus_Mons'), here);
  assert.equal(usableZone(CHI), CHI);
  for (const p of ['today', 'weekend', 'week', 'month']) {
    assert.deepEqual(preset(p, now, ''), preset(p, now, here), p);
  }
  assert.equal(toLocalInput(now, ''), toLocalInput(now, here));
  assert.equal(iso(fromLocalInput('2026-11-13T18:00', '')), iso(fromLocalInput('2026-11-13T18:00', here)));
  assert.equal(fmtTime('2026-11-14T00:00:00Z', ''), fmtTime('2026-11-14T00:00:00Z', here));
});

test('zoneNote names HA\'s zone, or says the browser zone is used', () => {
  assert.equal(zoneNote(CHI, 'Asia/Tokyo'), 'Times are in America/Chicago (HA).');
  assert.equal(zoneNote('', 'Asia/Tokyo'), 'Times are in Asia/Tokyo (this browser). HA has not reported a time zone.');
  assert.match(zoneNote('Mars/Olympus_Mons', 'Asia/Tokyo'), /Asia\/Tokyo \(this browser\)/);
});

test('fmtTime and fmtWindow show times in the zone', () => {
  const t = '2026-11-14T00:00:00Z';
  const opts = { dateStyle: 'medium', timeStyle: 'short' };
  assert.equal(fmtTime(t, CHI), new Date(t).toLocaleString(undefined, { ...opts, timeZone: CHI }));
  assert.notEqual(fmtTime(t, CHI), fmtTime(t, KYIV));
  assert.equal(fmtTime('', CHI), '');
  assert.equal(fmtTime('0001-01-01T00:00:00Z', CHI), '');
  assert.equal(fmtWindow({ valid_until: t }, CHI), `until ${fmtTime(t, CHI)}`);
});

// ---- Builders ----

const TEMPLATE_KEYS = ['id', 'name', 'icon', 'access', 'rules', 'party'];
const RULE_KEYS = ['select', 'exclude', 'actions', 'access', 'variables'];
const LINK_EDIT_KEYS = ['name', 'note', 'valid_from', 'valid_until', 'templates', 'extra', 'one_time', 'approval', 'slug'];
const only = (obj, keys) => Object.keys(obj).forEach((k) => assert.ok(keys.includes(k), `unexpected key ${k}`));

test('buildTemplate sends only model fields, and drops empty parts', () => {
  const st = ruleState({ select: { entities: ['light.a'] }, actions: ['light.turn_on'] });
  st.select.areas.push('');
  const t = buildTemplate({ id: 'guest', name: ' Guest room ', icon: '', access: { network: 'any', near_home_m: 0 }, party: false, rules: [st] });
  only(t, TEMPLATE_KEYS);
  assert.deepEqual(t, { id: 'guest', name: 'Guest room', rules: [{ select: { entities: ['light.a'] }, actions: ['light.turn_on'] }] });
});

test('buildTemplate keeps access, icon and party when set', () => {
  const t = buildTemplate({ id: 'm', name: 'M', icon: 'mdi:music', access: { network: 'local', near_home_m: '300' }, party: true, rules: [] });
  assert.deepEqual(t.access, { network: 'local', near_home_m: 300 });
  assert.equal(t.icon, 'mdi:music');
  assert.equal(t.party, true);
});

test('rule access override: on gives an object (even empty), off leaves it out', () => {
  const st = ruleState({ select: { entities: ['a.b'] }, actions: ['a.c'] });
  assert.equal('access' in buildRule(st), false);
  st.ownAccess = true;
  assert.deepEqual(buildRule(st).access, {}, 'own access with no limits replaces the template default');
  st.access = { network: 'local', near_home_m: 50 };
  assert.deepEqual(buildRule(st).access, { network: 'local', near_home_m: 50 });
});

test('script variables only go with script.run, and parse as JSON when they can', () => {
  const st = ruleState({ select: { entities: ['script.x'] }, actions: ['script.run'], variables: { n: 5, who: 'guest' } });
  st.vars.push(['flag', 'true'], ['  ', 'ignored']);
  const r = buildRule(st);
  only(r, RULE_KEYS);
  assert.deepEqual(r.variables, { n: 5, who: 'guest', flag: true });
  st.actions = ['script.cancel'];
  assert.equal('variables' in buildRule(st), false);
  assert.equal(parseVarValue('hello'), 'hello');
  assert.deepEqual(parseVarValue('{"a":1}'), { a: 1 });
});

test('cleanAccess', () => {
  assert.equal(cleanAccess(null), null);
  assert.deepEqual(cleanAccess({ network: 'any', near_home_m: -5 }), {});
  assert.deepEqual(cleanAccess({ network: 'local', near_home_m: 'x' }), { network: 'local' });
});

// A LinkView as the server sends it, with read-only fields the strict
// decoder would refuse.
const linkView = {
  id: 'abc', name: 'Weekend', note: 'Anna', slug: 'secret-slug-123', created_at: '2026-11-01T10:00:00Z',
  valid_until: '2026-11-16T06:00:00Z', templates: ['guest'],
  extra: [{ select: { entities: ['lock.front'] }, actions: ['lock.unlock'], access: { near_home_m: 200 } }],
  one_time: true, passwords: [{ id: 'p1', label: 'Anna', created_at: '2026-11-01T10:00:00Z' }],
  used: [], use_count: 3, last_used_at: '2026-11-02T10:00:00Z', revoked: false,
  approval: { manual: false, self_service: true }, devices: [{ id: 'd1' }],
  status: 'active', path: '/g/secret-slug-123', url: 'https://x/g/secret-slug-123', warnings: [],
};

test('buildLink from a LinkView sends only editable fields', () => {
  const body = buildLink(linkForm(linkView));
  only(body, LINK_EDIT_KEYS);
  assert.equal('slug' in body, false, 'slug only on create');
  assert.equal('valid_from' in body, false, 'empty valid_from is left out');
  assert.equal(body.valid_until, '2026-11-16T06:00:00.000Z');
  assert.deepEqual(body.templates, ['guest']);
  assert.deepEqual(body.extra, [{ select: { entities: ['lock.front'] }, actions: ['lock.unlock'], access: { near_home_m: 200 } }]);
  assert.deepEqual(body.approval, { manual: false, self_service: true });
  assert.equal(body.one_time, true);
  assert.equal(body.note, 'Anna');
});

test('extra rules always carry their own access', () => {
  const f = linkForm({ ...linkView, extra: [{ select: { entities: ['a.b'] }, actions: ['a.c'] }] });
  assert.deepEqual(buildLink(f).extra[0].access, {});
});

test('buildLink on create: custom slug only when asked', () => {
  const f = { ...linkForm(), name: 'x', valid_until: '2026-11-16T06:00:00Z', slug: 'my-custom-slug' };
  assert.equal('slug' in buildLink(f, { create: true }), false);
  f.customSlug = true;
  assert.equal(buildLink(f, { create: true }).slug, 'my-custom-slug');
  assert.equal('slug' in buildLink(f), false);
});

test('buildLink leaves out a missing end, so the form can flag it', () => {
  const body = buildLink({ ...linkForm(), name: 'x' });
  assert.equal('valid_until' in body, false);
  assert.equal('note' in body, false);
});

test('slugifyID', () => {
  assert.equal(slugifyID('Guest Room!'), 'guest_room');
  assert.equal(slugifyID('  --Music--  '), 'music');
  assert.equal(slugifyID('Café'), 'cafe');
});

// ---- Problems ----

test('template paths lose the policy prefix; others keep it', () => {
  assert.equal(normalizeTemplatePath('templates[0].rules[1].access.network'), 'rules[1].access.network');
  assert.equal(normalizeTemplatePath('templates[0].access.network'), 'access.network');
  assert.equal(normalizeTemplatePath('rules[0]'), 'rules[0]');
});

test('parentPath walks up one segment at a time', () => {
  assert.equal(parentPath('rules[0].select.entities[1]'), 'rules[0].select.entities');
  assert.equal(parentPath('rules[0].select.entities'), 'rules[0].select');
  assert.equal(parentPath('rules[0]'), 'rules');
  assert.equal(parentPath('rules'), '');
});

test('fieldFor finds the nearest registered field', () => {
  const keys = new Set(['name', 'rules[1]', 'rules[1].select.entities', 'templates']);
  assert.equal(fieldFor('rules[1].select.entities[2]', keys), 'rules[1].select.entities');
  assert.equal(fieldFor('rules[1].access.network', keys), 'rules[1]', 'no own access field: falls back to the rule');
  assert.equal(fieldFor('templates[0]', keys), 'templates');
  assert.equal(fieldFor('rules[7].actions', keys), '');
  assert.equal(fieldFor('', keys), '');
});

test('groupProblems drops nothing', () => {
  const keys = new Set(['name', 'rules[0].select.entities']);
  const g = groupProblems([
    { path: 'name', message: 'is empty' },
    { path: 'templates[0].rules[0].select.entities[1]', message: 'wildcard only' },
    { path: 'rules[9]', message: 'odd' },
    { path: '', message: 'general' },
  ], keys, normalizeTemplatePath);
  assert.deepEqual(g.get('name'), ['is empty']);
  assert.deepEqual(g.get('rules[0].select.entities'), ['Entry 2: wildcard only']);
  assert.deepEqual(g.get(''), ['rules[9]: odd', 'general']);
});

// ---- Links ----

test('approval modes, badges and the rotate warning', () => {
  assert.equal(approvalMode(undefined), 'off');
  assert.equal(approvalMode({ manual: true }), 'manual');
  assert.equal(approvalMode({ self_service: true }), 'claim');
  assert.equal(approvalMode({ manual: true, self_service: true }), 'both');
  assert.deepEqual(linkBadges(linkView), ['1 password', 'Claim mode', 'One-time']);
  assert.deepEqual(linkBadges({}), []);
  assert.match(rotateWarning(linkView), /Reset devices/);
  assert.equal(rotateWarning({}), '');
});

test('splitTemplates drops ids that no longer exist', () => {
  const ts = [{ id: 'a' }, { id: 'b' }];
  assert.deepEqual(splitTemplates(['a', 'gone', 'b'], ts), { keep: ['a', 'b'], missing: ['gone'] });
  assert.deepEqual(splitTemplates([], ts), { keep: [], missing: [] });
});

test('templateRows puts ticked templates first, in link order', () => {
  const ts = [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }];
  const rows = templateRows(['c', 'gone', 'a'], ts);
  assert.deepEqual(rows.map((r) => [r.t.id, r.ticked]), [['c', true], ['a', true], ['b', false], ['d', false]]);
  assert.deepEqual(templateRows([], ts).map((r) => r.t.id), ['a', 'b', 'c', 'd']);
});

test('moveItem swaps neighbours and ignores moves past the ends', () => {
  assert.deepEqual(moveItem(['a', 'b', 'c'], 2, -1), ['a', 'c', 'b']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 0, 1), ['b', 'a', 'c']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 0, -1), ['a', 'b', 'c']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 2, 1), ['a', 'b', 'c']);
  assert.deepEqual(moveItem([], 0, 1), []);
});

test('grantSummary groups by entity', () => {
  const g = grantSummary([
    { entity_id: 'lock.front', name: 'Front door', action: 'lock.unlock' },
    { entity_id: 'lock.front', name: 'Front door', action: 'lock.lock' },
    { entity_id: 'light.a', name: '', action: 'light.turn_on' },
  ]);
  assert.deepEqual(g, [
    { entity_id: 'lock.front', name: 'Front door', actions: ['lock.unlock', 'lock.lock'] },
    { entity_id: 'light.a', name: 'light.a', actions: ['light.turn_on'] },
  ]);
});

test('paths are relative and encode ids', () => {
  assert.equal(paths.link('a/b'), 'api/links/a%2Fb');
  assert.equal(paths.decide('r 1'), 'api/devices/pending/r%201');
  assert.equal(paths.pending(), 'api/devices/pending');
  assert.equal(paths.pending('a&b'), 'api/devices/pending?link=a%26b');
  assert.equal(paths.linkDevice('l', 'd/1'), 'api/links/l/devices/d%2F1');
  assert.equal(paths.resetDevices('l'), 'api/links/l/devices/reset');
  assert.equal(paths.audit('limit=5'), 'api/audit?limit=5');
  for (const v of Object.values(paths)) {
    const p = typeof v === 'function' ? v('x', 'y') : v;
    assert.ok(!p.startsWith('/'), p);
  }
});

// ---- Audit ----

test('audit query: link, event prefix and limit; denied filters locally', () => {
  assert.equal(auditQuery({ limit: 50 }), 'limit=50');
  assert.equal(auditQuery({ link: 'abc', group: 'guest', limit: 100 }), 'link=abc&event=guest.&limit=100');
  assert.equal(auditQuery({ group: 'denied', limit: 50 }), 'limit=50');
  assert.equal(auditQuery({ link: 'a&b=c', limit: 1 }), 'link=a%26b%3Dc&limit=1');
});

test('audit keep: denied only keeps denied events or results', () => {
  assert.ok(auditKeep('denied', { event: 'guest.act.denied' }));
  assert.ok(auditKeep('denied', { event: 'guest.password', result: 'denied' }));
  assert.ok(!auditKeep('denied', { event: 'guest.act', result: 'ok' }));
  assert.ok(!auditKeep('denied', { event: 'guest.denied_thing' }));
  assert.ok(auditKeep('guest', { event: 'guest.act' }));
});

test('audit who: guest label and device, else the HA user', () => {
  assert.equal(auditWho({ password_label: 'Anna', device_label: 'iPhone' }), 'Anna (iPhone)');
  assert.equal(auditWho({ password_label: 'Anna' }), 'Anna');
  assert.equal(auditWho({ device: 'Safari on iOS' }), 'Safari on iOS');
  assert.equal(auditWho({ user: { id: 'u1', name: 'illia', display_name: 'Illia G' } }), 'Illia G');
  assert.equal(auditWho({ user: { id: 'u1', name: 'illia' } }), 'illia');
  assert.equal(auditWho({ user: { id: 'u1' } }), 'u1');
  assert.equal(auditWho({ password_label: 'Anna', user: { name: 'illia' } }), 'Anna');
  assert.equal(auditWho({}), '');
});

test('audit what and result', () => {
  assert.equal(auditWhat({ entity_id: 'lock.front', action: 'lock.unlock', route: 'g/act' }), 'lock.unlock on lock.front');
  assert.equal(auditWhat({ route: 'g/state' }), 'g/state');
  assert.equal(auditWhat({ action: 'admin.link.update' }), 'admin.link.update');
  assert.equal(auditWhat({}), '');
  assert.equal(auditResult({ result: 'denied', reason: 'outside window' }), 'denied: outside window');
  assert.equal(auditResult({ reason: 'x' }), 'x');
  assert.equal(auditResult({}), '');
});

// ---- Settings ----

// settingsSample has every key GET api/settings sends, each with a
// distinct value, so the test can tell that each one is shown.
const settingsSample = {
  public_base_url: 'https://guest.example.com', home_radius_default_m: 101, trusted_proxies: ['192.0.2.1'],
  local_access: { enabled: true, networks: ['192.0.2.0/24'] },
  music_assistant: { url: 'http://ma.example' }, ma_token: { set: true },
  guest: { title: 'Hi', welcome: 'Welcome', accent_color: '#123456', slider_interval_ms: 102 },
  rate_limits: {
    guest_per_min: 201, guest_burst: 202, actions_per_min: 203, actions_burst: 204,
    continuous_per_min: 205, continuous_burst: 206, max_streams_per_link: 207,
    misses_per_hour_per_ip: 208, misses_per_hour_global: 209,
    password_fails_per_ip: 210, password_fails_per_link: 211, password_lockout_min: 212, admin_per_min: 213,
    assets_per_min: 214, assets_burst: 215, password_link_delay_s: 216, ipv6_prefix: 217,
  },
  tunables: {
    ma_timeout_ms: 301, location_ttl_min: 302, max_accuracy_m: 303,
    password_window_per_ip_min: 304, password_window_per_link_min: 305,
    custom_slug_min_len: 306, custom_slug_max_len: 307, guest_body_kib: 308, admin_body_kib: 309,
    diagnostics_requests: 310, inventory_max_age_s: 311,
    approval: { pending_ttl_min: 312, max_pending: 313, max_devices: 314, events_per_hour: 315, touch_throttle_min: 316, persistent_notification: true, admin_poll_s: 329 },
    ha_call_timeout_s: 317, ha_event_timeout_s: 318, stream_recheck_s: 319, stream_heartbeat_s: 320,
    stream_retry_after_s: 321, audit_enabled: true, audit_max_file_kib: 322,
    ma_check_interval_min: 323, custom_slug_warn_len: 324, http_read_timeout_s: 325, max_header_kib: 326,
    stream_max_per_ip: 327, audit_max_denials_per_min: 328,
  },
};

test('settings sections show every number the server sends', () => {
  const shown = new Set(settingsSections(settingsSample).flatMap((s) => s.rows.map(([, v]) => String(v))));
  const want = [];
  const walk = (o) => {
    for (const v of Object.values(o)) {
      if (typeof v === 'number') want.push(v);
      else if (v && typeof v === 'object' && !Array.isArray(v)) walk(v);
    }
  };
  walk(settingsSample);
  for (const n of want) {
    if (n === 306 || n === 307) continue; // shown together as a range
    assert.ok(shown.has(String(n)), `value ${n} is not shown`);
  }
  assert.ok(shown.has('306 to 307'));
});

test('settings sections: on/off, lists and missing values', () => {
  const rows = new Map(settingsSections(settingsSample).flatMap((s) => s.rows));
  assert.equal(rows.get('Log file'), 'On');
  assert.equal(rows.get('HA notification per request'), 'On');
  assert.equal(rows.get('Local only'), 'On');
  assert.equal(rows.get('Trusted proxies'), '192.0.2.1');
  assert.equal(rows.get('Token'), 'Set');
  const empty = new Map(settingsSections({}).flatMap((s) => s.rows));
  assert.equal(empty.get('Trusted proxies'), 'None');
  assert.equal(empty.get('Log file'), 'Off');
  assert.equal(empty.get('Custom slug length'), null);
});

test('settings: a refused Music Assistant token shows in the row and as a warning', () => {
  const ok = { ...settingsSample, ma_token: { set: true, refused: false } };
  assert.deepEqual(settingsWarnings(ok), []);
  const bad = { ...settingsSample, ma_token: { set: true, refused: true }, limits_disabled: true };
  const rows = new Map(settingsSections(bad).flatMap((s) => s.rows));
  assert.equal(rows.get('Token'), 'Refused by Music Assistant');
  const w = settingsWarnings(bad);
  assert.equal(w.length, 2);
  assert.match(w[0], /Rate limits are off/);
  assert.match(w[1], /music_assistant\.token/);
  assert.deepEqual(settingsWarnings({}), []);
});

test('link badges count waiting devices', () => {
  assert.deepEqual(linkBadges({ pending: [{}] }), ['1 device waiting']);
  assert.deepEqual(linkBadges({ pending: [{}, {}] }), ['2 devices waiting']);
});

// ---- Month preset ----

test('1 month: same day and time next month in the zone', () => {
  const now = utc('2026-05-12T20:30:00Z'); // 15:30 CDT
  assert.equal(iso(addMonth(now, CHI)), '2026-06-12T20:30:00.000Z');
  const p = preset('month', now, CHI);
  assert.equal(p.from, null);
  assert.equal(iso(p.until), '2026-06-12T20:30:00.000Z');
  assert.ok(PRESETS.some(([k]) => k === 'month'));
  // 18:00 CST in February gives 18:00 CDT in March.
  assert.equal(iso(addMonth(utc('2026-02-16T00:00:00Z'), CHI)), '2026-03-15T23:00:00.000Z');
});

test('1 month: a short next month clamps to its last day', () => {
  assert.equal(iso(addMonth(utc('2026-01-31T15:15:00Z'), CHI)), '2026-02-28T15:15:00.000Z');
  assert.equal(iso(addMonth(utc('2028-01-31T15:15:00Z'), CHI)), '2028-02-29T15:15:00.000Z', 'leap year');
  assert.equal(iso(addMonth(utc('2026-03-31T13:00:00Z'), CHI)), '2026-04-30T13:00:00.000Z');
  assert.equal(iso(addMonth(utc('2026-01-28T14:00:00Z'), CHI)), '2026-02-28T14:00:00.000Z');
  // 00:30 on 31 March in Kyiv is still 30 March in UTC. The clamp uses
  // Kyiv's date: 30 April, 00:30 EEST.
  assert.equal(iso(addMonth(utc('2026-03-30T21:30:00Z'), KYIV)), '2026-04-29T21:30:00.000Z');
});

test('1 month: December rolls into January of the next year', () => {
  assert.equal(iso(addMonth(utc('2026-12-16T00:00:00Z'), CHI)), '2027-01-16T00:00:00.000Z');
  // 23:59 CST on 31 December is already 2027 in UTC.
  assert.equal(iso(addMonth(utc('2027-01-01T05:59:00Z'), CHI)), '2027-02-01T05:59:00.000Z');
});

// ---- Link problems and passwords ----

test('linkProblems lists path and message, and nothing for a good link', () => {
  assert.deepEqual(linkProblems({}), []);
  assert.deepEqual(linkProblems({ problems: [] }), []);
  assert.deepEqual(linkProblems({ problems: [{ path: 'extra[0].access.network', message: 'must be any or local' }, { path: '', message: 'x' }] }),
    ['extra[0].access.network: must be any or local', 'x']);
});

test('removing the last password says the link opens to anyone with the URL', () => {
  const anna = { id: 'p1', label: 'Anna' };
  const bob = { id: 'p2', label: 'Bob' };
  const last = removePasswordPrompt({ passwords: [anna] }, anna);
  assert.match(last.title, /last password/);
  assert.match(last.body, /anyone with the URL/);
  assert.notEqual(last.ok, 'Remove');
  const one = removePasswordPrompt({ passwords: [anna, bob] }, anna);
  assert.doesNotMatch(one.title, /last/);
  assert.doesNotMatch(one.body, /anyone/);
  assert.equal(one.ok, 'Remove');
});

// ---- Waiting to be admitted ----

test('wantsPending takes the hash marker or the HA route path', () => {
  assert.equal(PENDING_ROUTE, '/pending');
  for (const [hash, route] of [['#/pending', undefined], ['#/pending/', ''], ['#/pending?x=1', null], ['', '/pending'], ['#/links', '/pending/']]) {
    assert.equal(wantsPending(hash, route), true, `${hash} ${route}`);
  }
  for (const [hash, route] of [['', ''], ['#/links', '/'], ['#pending', undefined], ['#/pendingx', '/pendingx'], ['#/links/pending', '/x/pending'],
    [undefined, { path: '/pending' }], ['', '/pending/x'], ['#/PENDING', '/Pending']]) {
    assert.equal(wantsPending(hash, route), false, `${hash} ${route}`);
  }
});

test('haRoutePath reads only HA property messages', () => {
  assert.equal(haRoutePath({ type: 'home-assistant/properties', narrow: false, route: { prefix: '/app/x', path: '/pending' } }), '/pending');
  assert.equal(haRoutePath({ type: 'home-assistant/properties', route: { prefix: '/x', path: '' } }), '');
  for (const bad of [null, undefined, 'home-assistant/properties', 5, {}, { type: 'other', route: { path: '/pending' } },
    { type: 'home-assistant/properties' }, { type: 'home-assistant/properties', route: { path: 7 } },
    { type: 'home-assistant/properties', route: null }]) {
    assert.equal(haRoutePath(bad), null, JSON.stringify(bad));
  }
});

test('pollMs comes from approval.admin_poll_s with a safe default', () => {
  assert.equal(pollMs({ tunables: { approval: { admin_poll_s: 7 } } }), 7000);
  for (const s of [undefined, {}, { tunables: {} }, { tunables: { approval: { admin_poll_s: 0 } } },
    { tunables: { approval: { admin_poll_s: '5' } } }, { tunables: { approval: { admin_poll_s: 1.5 } } }]) {
    assert.equal(pollMs(s), 5000, JSON.stringify(s));
  }
});

test('ago counts elapsed time in rough units', () => {
  const now = Date.parse('2026-11-11T12:00:00Z');
  assert.equal(ago('2026-11-11T12:00:00Z', now), 'just now');
  assert.equal(ago('2026-11-11T12:00:30Z', now), 'just now', 'a clock a bit ahead is not negative');
  assert.equal(ago('2026-11-11T11:59:01Z', now), 'just now');
  assert.equal(ago('2026-11-11T11:59:00Z', now), '1 min ago');
  assert.equal(ago('2026-11-11T12:01:00+01:00', now), '59 min ago', 'the offset of the time counts');
  assert.equal(ago('2026-11-11T09:30:00Z', now), '2 h ago');
  assert.equal(ago('2026-11-08T12:00:00Z', now), '3 d ago');
  assert.equal(ago('', now), '');
  assert.equal(ago('soon', now), '');
});

test('pendingRows keeps the server order and fills gaps', () => {
  const now = Date.parse('2026-11-11T12:00:00Z');
  const rows = pendingRows([
    { request_id: 'r1', link_id: 'l1', link_name: 'Anna', label: 'Anna', device: 'iPhone, Safari', code: 'K7Q2', created_at: '2026-11-11T11:57:00Z' },
    { request_id: 'r2', link_id: 'l2', link_name: '', device: 'Android, Chrome', code: 'X3M9', created_at: '2026-11-11T11:59:30Z' },
  ], now);
  assert.deepEqual(rows, [
    { id: 'r1', link: 'Anna', guest: 'Anna', device: 'iPhone, Safari', code: 'K7Q2', asked: '3 min ago' },
    { id: 'r2', link: 'l2', guest: '', device: 'Android, Chrome', code: 'X3M9', asked: 'just now' },
  ]);
  assert.deepEqual(pendingRows(null, now), []);
});

test('pendingBadge hides at zero and caps big counts', () => {
  assert.equal(pendingBadge(0), '');
  assert.equal(pendingBadge(undefined), '');
  assert.equal(pendingBadge(2), '2');
  assert.equal(pendingBadge(150), '99+');
});
