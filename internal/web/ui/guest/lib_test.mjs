import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  linkBase, actionLabel, stateLabel, cards, splitActions, pickControl, toggleFor, lockActions,
  alarmActions, scriptView, mediaInfo, sliderBounds, clamp, brightnessPct, supportsColorTemp,
  parseColor, textOn, parseRetryAfter, formatWait, tryAgainText, errorText, mergeState,
  formatUntil, confirmTap, confirmExpired, CONFIRM_MS, deviceNext, shouldPoll, geoErrorText,
  whereErrorText, sliderSender, sliderInterval, SLIDER_MS,
  approvalsOn, approvalsDelay, APPROVALS_MS,
} from './lib.js';

test('linkBase', () => {
  assert.equal(linkBase('/g/abc_DEF-123'), '/g/abc_DEF-123');
  assert.equal(linkBase('/g/abc/'), '/g/abc');
  assert.equal(linkBase('/g/abc/state'), '');
  assert.equal(linkBase('/x/abc'), '');
  assert.equal(linkBase('/g/a.b'), '');
});

test('actionLabel', () => {
  assert.equal(actionLabel('light.turn_on'), 'Turn on');
  assert.equal(actionLabel('lock.unlock'), 'Unlock');
  assert.equal(actionLabel('alarm_control_panel.alarm_disarm'), 'Disarm');
  assert.equal(actionLabel('vacuum.return_to_base'), 'Return to base');
  assert.equal(actionLabel('odd'), 'odd');
});

test('stateLabel', () => {
  assert.equal(stateLabel('lock', 'locked'), 'Locked');
  assert.equal(stateLabel('alarm_control_panel', 'armed_away'), 'Armed (away)');
  assert.equal(stateLabel('script', 'on'), 'Running…');
  assert.equal(stateLabel('script', 'off'), '');
  assert.equal(stateLabel('lock', undefined), '');
  assert.equal(stateLabel('vacuum', 'returning_home'), 'returning home');
});

test('cards keeps every action, in group order', () => {
  const view = {
    groups: [{ name: 'Entry', entities: ['lock.front', 'light.hall', 'light.gone'] }, { name: '', entities: ['switch.fan'] }, { name: 'Empty', entities: [] }],
    entities: {
      'lock.front': { name: 'Front door', domain: 'lock', actions: [{ id: 'lock.unlock', ui: 'confirm' }] },
      'light.hall': { name: 'Hall', domain: 'light', actions: [{ id: 'light.turn_on', ui: 'slider', fields: { brightness_pct: { kind: 'int', min: 1, max: 100 } } }] },
      'switch.fan': { actions: [{ id: 'switch.turn_on', ui: 'toggle' }] },
    },
  };
  const out = cards(view);
  assert.deepEqual(out.map((g) => g.name), ['Entry', 'More']);
  assert.deepEqual(out[0].entities.map((e) => e.id), ['lock.front', 'light.hall']);
  assert.equal(out[0].entities[1].actions[0].fields.brightness_pct.max, 100);
  assert.deepEqual(out[1].entities[0], { id: 'switch.fan', name: 'switch.fan', domain: 'switch', actions: [{ id: 'switch.turn_on', ui: 'toggle' }] });
  assert.deepEqual(cards({}), []);
  assert.deepEqual(cards(null), []);
});

test('splitActions', () => {
  const r = splitActions([{ id: 'a' }, { id: 'b', needs_location: true }]);
  assert.deepEqual(r.now.map((a) => a.id), ['a']);
  assert.deepEqual(r.later.map((a) => a.id), ['b']);
});

test('pickControl picks one control per entity', () => {
  const a = (...ids) => ids.map((id) => ({ id }));
  const cases = [
    ['lock', a('lock.unlock'), 'lock'],
    ['alarm_control_panel', a('alarm_control_panel.alarm_disarm'), 'alarm'],
    ['light', a('light.turn_on', 'light.turn_off'), 'light'],
    ['light', a('light.toggle'), 'light'],
    ['fan', a('fan.set_percentage'), 'fan'],
    ['media_player', a('media_player.volume_set'), 'media'],
    ['switch', a('switch.turn_off'), 'switch'],
    ['script', a('script.run', 'script.cancel'), 'script'],
    ['script', a('script.cancel'), 'script'],
    ['vacuum', a('vacuum.start'), 'buttons'],
    ['light', a('light.flash'), 'buttons'],
    ['lock', [], 'buttons'],
  ];
  for (const [domain, actions, want] of cases) {
    assert.equal(pickControl(domain, actions), want, domain + ' ' + JSON.stringify(actions));
  }
});

test('toggleFor', () => {
  const on = { id: 'light.turn_on' };
  const off = { id: 'light.turn_off' };
  const tg = { id: 'light.toggle' };
  assert.deepEqual(toggleFor('light', [on, off], 'on'), { on: true, next: 'light.turn_off' });
  assert.deepEqual(toggleFor('light', [on, off], 'off'), { on: false, next: 'light.turn_on' });
  assert.deepEqual(toggleFor('light', [on], 'on'), { on: true, next: null });
  assert.deepEqual(toggleFor('light', [on, tg], 'on'), { on: true, next: 'light.toggle' });
  assert.deepEqual(toggleFor('light', [on, off], 'unavailable'), { on: null, next: null });
  assert.deepEqual(toggleFor('light', [tg], undefined), { on: null, next: 'light.toggle' });
  assert.deepEqual(toggleFor('switch', [{ id: 'switch.turn_on', used: true }], 'off'), { on: false, next: null });
});

test('lockActions shows the action that changes the state', () => {
  const all = [{ id: 'lock.lock' }, { id: 'lock.unlock' }, { id: 'lock.open' }];
  const ids = (s) => lockActions(all, s).map((a) => a.id);
  assert.deepEqual(ids('locked'), ['lock.unlock', 'lock.open']);
  assert.deepEqual(ids('unlocked'), ['lock.lock', 'lock.open']);
  assert.deepEqual(ids('jammed'), ['lock.lock', 'lock.unlock', 'lock.open']);
  assert.deepEqual(ids(undefined), ['lock.lock', 'lock.unlock', 'lock.open']);
});

test('alarmActions', () => {
  const all = ['alarm_disarm', 'alarm_arm_home', 'alarm_arm_away'].map((s) => ({ id: 'alarm_control_panel.' + s }));
  const ids = (s) => alarmActions(all, s).map((a) => a.id.split('.')[1]);
  assert.deepEqual(ids('disarmed'), ['alarm_arm_home', 'alarm_arm_away']);
  assert.deepEqual(ids('armed_away'), ['alarm_disarm']);
  assert.deepEqual(ids('triggered'), ['alarm_disarm']);
  assert.deepEqual(ids('arming'), ['alarm_disarm']);
  assert.deepEqual(ids(undefined), ['alarm_disarm', 'alarm_arm_home', 'alarm_arm_away']);
});

test('scriptView shows cancel only while running', () => {
  const run = { id: 'script.run' };
  const cancel = { id: 'script.cancel' };
  assert.deepEqual(scriptView([run, cancel], 'off'), { running: false, run, cancel: null });
  assert.deepEqual(scriptView([run, cancel], 'on'), { running: true, run, cancel });
  assert.deepEqual(scriptView([run], 'on'), { running: true, run, cancel: null });
});

test('mediaInfo', () => {
  assert.deepEqual(mediaInfo({ state: 'playing', attributes: { media_title: 'Song', media_artist: 'Band', volume_level: 0.456, is_volume_muted: true } }),
    { playing: true, title: 'Song', artist: 'Band', volume: 46, muted: true });
  assert.deepEqual(mediaInfo(undefined), { playing: false, title: '', artist: '', volume: null, muted: false });
  assert.equal(mediaInfo({ state: 'paused', attributes: { volume_level: 7, media_title: 5 } }).volume, 100);
  assert.equal(mediaInfo({ state: 'paused', attributes: { media_title: 5 } }).title, '');
});

test('sliderBounds narrows by attributes and refuses crossed bounds', () => {
  const ct = { kind: 'int', min: 1500, max: 9000, min_attr: 'min_color_temp_kelvin', max_attr: 'max_color_temp_kelvin' };
  assert.deepEqual(sliderBounds(ct, {}), { min: 1500, max: 9000, step: 1 });
  assert.deepEqual(sliderBounds(ct, { min_color_temp_kelvin: 2000, max_color_temp_kelvin: 6500 }), { min: 2000, max: 6500, step: 1 });
  // Attributes only narrow, never widen.
  assert.deepEqual(sliderBounds(ct, { min_color_temp_kelvin: 1000, max_color_temp_kelvin: 12000 }), { min: 1500, max: 9000, step: 1 });
  // Non-numbers are ignored.
  assert.deepEqual(sliderBounds(ct, { min_color_temp_kelvin: '2000', max_color_temp_kelvin: null }), { min: 1500, max: 9000, step: 1 });
  assert.equal(sliderBounds(ct, { min_color_temp_kelvin: 7000, max_color_temp_kelvin: 3000 }), null);
  // min 0 is left out of the JSON (omitempty).
  assert.deepEqual(sliderBounds({ kind: 'int', max: 100 }, {}), { min: 0, max: 100, step: 1 });
  assert.deepEqual(sliderBounds({ kind: 'float', max: 1 }, {}), { min: 0, max: 1, step: 0.01 });
  assert.equal(sliderBounds(undefined, {}), null);
});

test('clamp', () => {
  const b = { min: 10, max: 20 };
  assert.equal(clamp(5, b), 10);
  assert.equal(clamp(25, b), 20);
  assert.equal(clamp(15, b), 15);
  assert.equal(clamp(undefined, b), 10);
  assert.equal(clamp(NaN, b), 10);
});

test('brightnessPct and supportsColorTemp', () => {
  assert.equal(brightnessPct({ brightness: 255 }), 100);
  assert.equal(brightnessPct({ brightness: 1 }), 1);
  assert.equal(brightnessPct({ brightness: 128 }), 50);
  assert.equal(brightnessPct({ brightness: 0 }), null);
  assert.equal(brightnessPct({}), null);
  assert.equal(supportsColorTemp({ supported_color_modes: ['color_temp', 'xy'] }), true);
  assert.equal(supportsColorTemp({ supported_color_modes: ['brightness'], min_color_temp_kelvin: 2000, max_color_temp_kelvin: 6500 }), false);
  assert.equal(supportsColorTemp({ min_color_temp_kelvin: 2000, max_color_temp_kelvin: 6500 }), true);
  assert.equal(supportsColorTemp({}), false);
});

test('parseColor', () => {
  assert.equal(parseColor('#1E88E5'), '#1e88e5');
  assert.equal(parseColor('#abc'), '#aabbcc');
  assert.equal(parseColor(' #abcdef '), '#abcdef');
  for (const bad of ['', 'red', '#12345', '#1234567', 'url(x)', '#ggg', '1e88e5', null, 5, '#fff;background:red']) {
    assert.equal(parseColor(bad), null, String(bad));
  }
});

test('textOn picks the text color with more contrast', () => {
  assert.equal(textOn('#000000'), '#ffffff');
  assert.equal(textOn('#ffffff'), '#000000');
  assert.equal(textOn('#ffeb3b'), '#000000'); // yellow
  assert.equal(textOn('#1e6fd9'), '#ffffff'); // mid blue
  assert.equal(textOn('#00ff00'), '#000000'); // bright green
  assert.equal(textOn('#b3261e'), '#ffffff'); // dark red
  assert.equal(textOn('#777777'), '#000000'); // mid grey, black wins narrowly
  assert.equal(textOn('nope'), '#ffffff');
});

test('parseRetryAfter reads seconds and HTTP dates', () => {
  const now = Date.parse('2026-11-11T12:00:00Z');
  assert.equal(parseRetryAfter('120', now), 120);
  assert.equal(parseRetryAfter(' 0 ', now), 0);
  assert.equal(parseRetryAfter('Wed, 11 Nov 2026 12:01:30 GMT', now), 90);
  assert.equal(parseRetryAfter('Wed, 11 Nov 2026 11:00:00 GMT', now), 0);
  for (const bad of [null, undefined, '', '-5', '1.5', 'soon', 'Wed, 99 Nov 2026 xx GMT']) {
    assert.equal(parseRetryAfter(bad, now), null, String(bad));
  }
});

test('formatWait and tryAgainText', () => {
  assert.equal(formatWait(1), '1 second');
  assert.equal(formatWait(45), '45 seconds');
  assert.equal(formatWait(60), '1 minute');
  assert.equal(formatWait(61), '2 minutes');
  assert.equal(formatWait(3600), '1 hour');
  assert.equal(formatWait(3601), '2 hours');
  assert.equal(formatWait(0), 'a moment');
  assert.equal(formatWait(null), 'a moment');
  assert.equal(tryAgainText(90), 'Too many tries. Try again in 2 minutes.');
  assert.equal(tryAgainText(null), 'Too many tries. Try again later.');
});

test('errorText shows our words, never raw text for known codes', () => {
  assert.equal(errorText(409, { error: 'x' }), 'Already used.');
  assert.equal(errorText(502, { error: 'HA said: secret' }), 'Home is not answering, try again.');
  assert.equal(errorText(503, {}), 'Home is not answering, try again.');
  assert.equal(errorText(410, {}), 'This link is no longer active.');
  assert.equal(errorText(429, {}, 30), 'Too many tries. Try again in 30 seconds.');
  assert.equal(errorText(0), 'Could not connect. Check your internet and try again.');
  assert.equal(errorText(413, { error: 'Request too large.' }), 'Request too large.');
  assert.equal(errorText(500, { error: 'x'.repeat(500) }), 'Something went wrong. Try again.');
  assert.equal(errorText(500, { error: { nested: true } }), 'Something went wrong. Try again.');
  assert.equal(errorText(500, null), 'Something went wrong. Try again.');
});

test('mergeState', () => {
  const view = { entities: { 'light.a': {} }, states: { 'light.a': { entity_id: 'light.a', state: 'off' } } };
  const next = mergeState(view, { entity_id: 'light.a', state: 'on' });
  assert.notEqual(next, view);
  assert.equal(next.states['light.a'].state, 'on');
  assert.equal(view.states['light.a'].state, 'off', 'the old view stays as it was');
  // Unknown entities, bad input and prototype keys change nothing.
  assert.equal(mergeState(view, { entity_id: 'lock.b', state: 'unlocked' }), view);
  assert.equal(mergeState(view, { entity_id: '__proto__', state: 'x' }), view);
  assert.equal(mergeState(view, { state: 'on' }), view);
  assert.equal(mergeState(view, null), view);
  assert.equal(mergeState(null, { entity_id: 'light.a' }), null);
  // A view with no states yet.
  assert.equal(mergeState({ entities: { 'light.a': {} } }, { entity_id: 'light.a', state: 'on' }).states['light.a'].state, 'on');
});

test('formatUntil', () => {
  const o = { locale: 'en-GB', timeZone: 'UTC' };
  const now = Date.parse('2026-11-11T08:00:00Z'); // a Wednesday
  assert.equal(formatUntil('2026-11-11T18:00:00Z', now, o), 'Works until 18:00 today');
  assert.equal(formatUntil('2026-11-12T09:30:00Z', now, o), 'Works until 09:30 tomorrow');
  assert.equal(formatUntil('2026-11-14T10:00:00Z', now, o), 'Works until Sat 10:00');
  assert.equal(formatUntil('2026-12-03T10:00:00Z', now, o), 'Works until 3 Dec, 10:00');
  assert.equal(formatUntil('2026-11-11T07:00:00Z', now, o), 'This link has ended.');
  assert.equal(formatUntil('', now, o), '');
  assert.equal(formatUntil(undefined, now, o), '');
  assert.equal(formatUntil('garbage', now, o), '');
  // Days follow the given time zone: 23:00 UTC is already tomorrow in Kyiv.
  assert.equal(formatUntil('2026-11-11T23:00:00Z', now, { locale: 'en-GB', timeZone: 'Europe/Kyiv' }), 'Works until 01:00 tomorrow');
});

test('confirmTap needs a second tap on the same button in time', () => {
  let r = confirmTap(null, 'lock.unlock', 1000);
  assert.deepEqual(r, { fire: false, armed: { key: 'lock.unlock', until: 1000 + CONFIRM_MS } });
  const armed = r.armed;
  assert.deepEqual(confirmTap(armed, 'lock.unlock', 1000 + CONFIRM_MS), { fire: true, armed: null });
  // Too late: arms again.
  r = confirmTap(armed, 'lock.unlock', 1001 + CONFIRM_MS);
  assert.equal(r.fire, false);
  assert.equal(r.armed.until, 1001 + 2 * CONFIRM_MS);
  // Another button: arms that one instead.
  r = confirmTap(armed, 'lock.lock', 1500);
  assert.deepEqual(r, { fire: false, armed: { key: 'lock.lock', until: 1500 + CONFIRM_MS } });
  assert.equal(confirmExpired(armed, 1000 + CONFIRM_MS), false);
  assert.equal(confirmExpired(armed, 1001 + CONFIRM_MS), true);
  assert.equal(confirmExpired(null, 0), false);
});

test('deviceNext walks the approval phases', () => {
  const ok = (body) => ({ status: 200, body });
  let s = deviceNext(null, { status: 403, body: { status: 'approval_required' } });
  assert.deepEqual(s, { phase: 'required' });
  s = deviceNext(s, ok({ status: 'pending', code: 'K7Q2' }));
  assert.deepEqual(s, { phase: 'pending', code: 'K7Q2' });
  assert.equal(shouldPoll(s.phase), true);
  // A passing error keeps polling with the same code.
  const flaky = deviceNext(s, { status: 0, body: {} });
  assert.equal(flaky.phase, 'pending');
  assert.equal(flaky.code, 'K7Q2');
  assert.match(flaky.error, /connect/);
  assert.deepEqual(deviceNext(s, { status: 503, body: {} }).phase, 'pending');
  // A pending reply without a code keeps the old one.
  assert.deepEqual(deviceNext(s, ok({ status: 'pending' })), { phase: 'pending', code: 'K7Q2' });
  // Outcomes.
  assert.deepEqual(deviceNext(s, ok({ status: 'approved' })), { phase: 'approved' });
  const denied = deviceNext(s, ok({ status: 'denied' }));
  assert.deepEqual(denied, { phase: 'denied' });
  assert.equal(shouldPoll(denied.phase), false);
  assert.deepEqual(deviceNext(denied, { status: 403, body: { status: 'approval_required' } }), { phase: 'denied' });
  assert.deepEqual(deviceNext(s, ok({ status: 'approval_required' })), { phase: 'expired' });
  assert.deepEqual(deviceNext(s, { status: 410, body: {} }), { phase: 'ended' });
  // Too many requests.
  const wait = deviceNext({ phase: 'required' }, { status: 429, body: { status: 'try_later' }, retry: 120 });
  assert.deepEqual(wait, { phase: 'wait', wait: 120 });
  assert.deepEqual(deviceNext({ phase: 'required' }, { status: 429, body: {} }), { phase: 'wait', wait: null });
  // Claim mode approves at once; /state can go straight to pending.
  assert.deepEqual(deviceNext({ phase: 'required' }, ok({ status: 'approved' })), { phase: 'approved' });
  assert.deepEqual(deviceNext(null, { status: 403, body: { status: 'pending', code: 'AB34' } }), { phase: 'pending', code: 'AB34' });
  // A 403 with no status is an error, not a phase.
  assert.equal(deviceNext({ phase: 'required' }, { status: 403, body: { error: 'Not allowed.' } }).phase, 'required');
});

test('geo and where messages', () => {
  assert.match(geoErrorText(0), /secure/);
  assert.match(geoErrorText(1), /Nothing is stored/);
  assert.match(geoErrorText(2), /Try again/);
  assert.match(geoErrorText(3), /too long/);
  assert.match(whereErrorText(400, {}), /precise/);
  assert.equal(whereErrorText(503, {}), 'Home is not answering, try again.');
});

test('guest text has no em dashes', async () => {
  const { readFile } = await import('node:fs/promises');
  for (const f of ['lib.js', 'guest.js', 'controls.js', 'index.html']) {
    const src = await readFile(new URL('./' + f, import.meta.url), 'utf8');
    assert.equal(src.includes('—'), false, f);
  }
});

// fakeClock drives sliderSender in tests. Sends resolve only when the
// test calls done().
function rig(intervalMs) {
  let t = 0;
  const timers = [];
  const sent = [];
  const waits = [];
  const s = sliderSender({
    intervalMs,
    now: () => t,
    setTimer: (fn, ms) => { const x = { fn, at: t + ms }; timers.push(x); return x; },
    clearTimer: (x) => { const i = timers.indexOf(x); if (i >= 0) timers.splice(i, 1); },
    send: (v) => { sent.push([v, t]); return new Promise((r) => waits.push(r)); },
  });
  const flush = () => new Promise((r) => setImmediate(r));
  return {
    s, sent,
    inFlight: () => waits.length,
    async done() { waits.shift()(true); await flush(); },
    async tick(ms) {
      t += ms;
      for (;;) {
        const due = timers.filter((x) => x.at <= t).sort((a, b) => a.at - b.at)[0];
        if (!due) break;
        timers.splice(timers.indexOf(due), 1);
        due.fn();
        await flush();
      }
    },
  };
}

test('sliderInterval reads the option or falls back', () => {
  assert.equal(SLIDER_MS, 300);
  assert.equal(sliderInterval({ slider_interval_ms: 150 }), 150);
  assert.equal(sliderInterval({}), 300);
  assert.equal(sliderInterval(undefined), 300);
  assert.equal(sliderInterval({ slider_interval_ms: 0 }), 300);
  assert.equal(sliderInterval({ slider_interval_ms: '100' }), 300);
});

test('sliderSender uses the default interval when none is given', async () => {
  const sent = [];
  let t = 0;
  const timers = [];
  const s = sliderSender({ now: () => t, setTimer: (fn, ms) => timers.push(ms), send: (v) => { sent.push(v); } });
  s.input(1);
  await new Promise((r) => setImmediate(r));
  t = 10;
  s.input(2);
  assert.deepEqual(sent, [1]);
  assert.deepEqual(timers, [290]);
});

test('sliderSender spaces sends by the interval', async () => {
  const r = rig(300);
  r.s.input(10);
  assert.deepEqual(r.sent, [[10, 0]]);
  await r.done();
  await r.tick(100);
  r.s.input(20);
  assert.equal(r.sent.length, 1, 'too soon');
  await r.tick(199);
  assert.equal(r.sent.length, 1);
  await r.tick(1);
  assert.deepEqual(r.sent[1], [20, 300]);
});

test('sliderSender keeps one request in flight and the newest value wins', async () => {
  const r = rig(100);
  r.s.input(1);
  await r.tick(500);
  r.s.input(2);
  r.s.input(3);
  r.s.input(4);
  assert.equal(r.inFlight(), 1);
  assert.deepEqual(r.sent.map((x) => x[0]), [1]);
  await r.done();
  assert.deepEqual(r.sent.map((x) => x[0]), [1, 4]);
  assert.equal(r.inFlight(), 1);
});

test('sliderSender sends the final value on release and skips duplicates', async () => {
  const r = rig(300);
  r.s.input(5);
  await r.done();
  r.s.input(6);
  r.s.release(7);
  await r.tick(300);
  assert.deepEqual(r.sent.map((x) => x[0]), [5, 7]);
  await r.done();
  // Release on the value already sent sends nothing.
  await r.tick(1000);
  r.s.release(7);
  await r.tick(1000);
  assert.equal(r.sent.length, 2);
  // A drag back to the sent value while waiting sends nothing either.
  r.s.input(8);
  assert.equal(r.sent.length, 3);
  await r.done();
  r.s.input(9);
  r.s.input(8);
  await r.tick(300);
  assert.deepEqual(r.sent.map((x) => x[0]), [5, 7, 8]);
});

test('sliderSender recovers after a failed send', async () => {
  const sent = [];
  const s = sliderSender({ intervalMs: 0, send: (v) => { sent.push(v); if (v === 1) throw new Error('x'); return Promise.reject(new Error('y')); } });
  s.input(1);
  await new Promise((r) => setImmediate(r));
  s.input(2);
  await new Promise((r) => setImmediate(r));
  s.input(3);
  assert.deepEqual(sent, [1, 2, 3]);
});

test('approvals show only when /state says can_approve and the page is visible', () => {
  assert.equal(approvalsOn({ can_approve: true }, false), true);
  assert.equal(approvalsOn({ can_approve: true }, true), false);
  assert.equal(approvalsOn({ can_approve: false }, false), false);
  assert.equal(approvalsOn({}, false), false);
  assert.equal(approvalsOn({ can_approve: 'yes' }, false), false);
  assert.equal(approvalsOn(null, false), false);
});

test('approvals poll at the normal pace, slower after a 429', () => {
  assert.equal(approvalsDelay(200, null), APPROVALS_MS);
  assert.equal(approvalsDelay(0, null), APPROVALS_MS);
  assert.equal(approvalsDelay(429, 30), 30000);
  assert.equal(approvalsDelay(429, 1), APPROVALS_MS);
  assert.equal(approvalsDelay(429, null), APPROVALS_MS);
});
