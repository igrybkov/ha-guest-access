// Pure helpers for the guest page. No DOM here, so node --test can run
// them. Everything the page decides (which control, which bounds, which
// text) lives here; guest.js and controls.js only draw it.

// linkBase returns the link's API base from the page path: "/g/abc" and
// "/g/abc/" both give "/g/abc". Anything else gives "".
export function linkBase(pathname) {
  const m = /^(\/g\/[A-Za-z0-9_-]+)\/?$/.exec(pathname);
  return m ? m[1] : '';
}

const LABELS = {
  'lock.lock': 'Lock',
  'lock.unlock': 'Unlock',
  'lock.open': 'Open the door',
  'alarm_control_panel.alarm_disarm': 'Disarm',
  'alarm_control_panel.alarm_arm_home': 'Arm (home)',
  'alarm_control_panel.alarm_arm_away': 'Arm (away)',
  'alarm_control_panel.alarm_arm_night': 'Arm (night)',
  'media_player.media_play_pause': 'Play or pause',
  'media_player.media_next_track': 'Next',
  'media_player.media_previous_track': 'Previous',
  'media_player.volume_mute': 'Mute',
  'script.run': 'Run',
  'script.cancel': 'Cancel',
};

// actionLabel gives a short button text: "lock.unlock" gives "Unlock",
// and an unknown "light.turn_on" gives "Turn on".
export function actionLabel(id) {
  if (LABELS[id]) return LABELS[id];
  const name = String(id).split('.').slice(1).join('.')
    .replace(/^media_/, '').replace(/^alarm_/, '').replace(/_/g, ' ');
  return name ? name[0].toUpperCase() + name.slice(1) : String(id);
}

// confirmLabel is the text a confirm button shows after the first tap.
export function confirmLabel(id) {
  return 'Tap again to ' + actionLabel(id).toLowerCase();
}

const STATES = {
  locked: 'Locked', unlocked: 'Unlocked', locking: 'Locking…', unlocking: 'Unlocking…',
  jammed: 'Jammed', open: 'Open', opening: 'Opening…',
  disarmed: 'Disarmed', armed_home: 'Armed (home)', armed_away: 'Armed (away)',
  armed_night: 'Armed (night)', armed_vacation: 'Armed (vacation)',
  armed_custom_bypass: 'Armed', arming: 'Arming…', disarming: 'Disarming…',
  pending: 'About to go off', triggered: 'Alarm is going off',
  playing: 'Playing', paused: 'Paused', idle: 'Idle', standby: 'Standby', buffering: 'Loading…',
  on: 'On', off: 'Off', unavailable: 'Not available', unknown: 'Unknown',
};

// stateLabel turns an HA state into plain words. A script that is on is
// running.
export function stateLabel(domain, state) {
  if (state === undefined || state === null || state === '') return '';
  if (domain === 'script') return state === 'on' ? 'Running…' : '';
  return STATES[state] || String(state).replace(/_/g, ' ');
}

// cards returns the groups of a /state reply as
// [{name, entities: [{id, name, domain, actions}]}], in reply order.
// Entities with no actions are left out.
export function cards(view) {
  const out = [];
  for (const g of (view && view.groups) || []) {
    const entities = [];
    for (const id of g.entities || []) {
      const e = (view.entities || {})[id];
      if (!e || !Array.isArray(e.actions) || e.actions.length === 0) continue;
      entities.push({ id, name: e.name || id, domain: e.domain || id.split('.')[0], actions: e.actions });
    }
    if (entities.length > 0) out.push({ name: g.name || 'More', entities });
  }
  return out;
}

// splitActions splits an entity's actions into the ones usable now and
// the ones that wait for a shared location.
export function splitActions(actions) {
  const now = [];
  const later = [];
  for (const a of actions || []) (a.needs_location ? later : now).push(a);
  return { now, later };
}

const KNOWN = {
  lock: ['lock.lock', 'lock.unlock', 'lock.open'],
  alarm: ['alarm_control_panel.alarm_disarm', 'alarm_control_panel.alarm_arm_home',
    'alarm_control_panel.alarm_arm_away', 'alarm_control_panel.alarm_arm_night'],
  light: ['light.turn_on', 'light.turn_off', 'light.toggle'],
  fan: ['fan.turn_on', 'fan.turn_off', 'fan.set_percentage'],
  media: ['media_player.media_play_pause', 'media_player.media_next_track',
    'media_player.media_previous_track', 'media_player.volume_set', 'media_player.volume_mute'],
  switch: ['switch.turn_on', 'switch.turn_off'],
  script: ['script.run', 'script.cancel'],
};

const KINDS = {
  lock: 'lock', alarm_control_panel: 'alarm', light: 'light', fan: 'fan',
  media_player: 'media', switch: 'switch', script: 'script',
};

// pickControl names the control for an entity: lock, alarm, light, fan,
// media, switch, script, or buttons for anything else. A known domain
// with no action this page knows how to draw also gets buttons.
export function pickControl(domain, actions) {
  const kind = KINDS[domain];
  const ids = new Set((actions || []).map((a) => a.id));
  if (!kind || ids.size === 0) return 'buttons';
  return KNOWN[kind].some((id) => ids.has(id)) ? kind : 'buttons';
}

// findAction returns the action with id, or null.
export function findAction(actions, id) {
  return (actions || []).find((a) => a.id === id) || null;
}

// toggleFor works out an on/off switch for a light, fan or switch.
// It returns {on, next}: on is true, false or null (unknown), and next is
// the action a tap sends, or null when no granted action fits. A used
// one-time action is never next.
export function toggleFor(domain, actions, state) {
  const on = state === 'on' ? true : state === 'off' ? false : null;
  const usable = (id) => {
    const a = findAction(actions, id);
    return a && !a.used ? a : null;
  };
  const toggle = usable(domain + '.toggle');
  let next = null;
  if (on === true) next = usable(domain + '.turn_off') || toggle;
  else if (on === false) next = usable(domain + '.turn_on') || toggle;
  else next = toggle;
  return { on, next: next ? next.id : null };
}

// lockActions lists the lock actions to show for a state. Lock is hidden
// while locked, unlock while unlocked. Open always shows.
export function lockActions(actions, state) {
  return (actions || []).filter((a) => {
    if (a.id === 'lock.lock') return state !== 'locked' && state !== 'locking';
    if (a.id === 'lock.unlock') return state !== 'unlocked' && state !== 'unlocking' && state !== 'open';
    return a.id === 'lock.open';
  });
}

// alarmActions lists the alarm actions to show for a state: disarm when
// armed or going off, arm when disarmed, and all of them when the state
// is unknown.
export function alarmActions(actions, state) {
  const disarmed = state === 'disarmed';
  const armed = typeof state === 'string' && /^(armed_|arming|pending|triggered)/.test(state);
  return (actions || []).filter((a) => {
    if (!a.id.startsWith('alarm_control_panel.')) return false;
    if (a.id === 'alarm_control_panel.alarm_disarm') return !disarmed;
    return !armed;
  });
}

// scriptView says what a script card shows: running while its state is
// on, the run action, and cancel only while running.
export function scriptView(actions, state) {
  const running = state === 'on';
  return {
    running,
    run: findAction(actions, 'script.run'),
    cancel: running ? findAction(actions, 'script.cancel') : null,
  };
}

// mediaInfo reads what a media card shows from a filtered state. Volume
// is 0 to 100.
export function mediaInfo(st) {
  const attrs = (st && st.attributes) || {};
  const vol = attrs.volume_level;
  return {
    playing: !!st && st.state === 'playing',
    title: typeof attrs.media_title === 'string' ? attrs.media_title : '',
    artist: typeof attrs.media_artist === 'string' ? attrs.media_artist : '',
    volume: finite(vol) ? Math.round(Math.min(1, Math.max(0, vol)) * 100) : null,
    muted: attrs.is_volume_muted === true,
  };
}

function finite(x) {
  return typeof x === 'number' && Number.isFinite(x);
}

// sliderBounds gives {min, max, step} for a catalog field, narrowed by the
// entity's attributes named in min_attr and max_attr, the same way the
// server checks. It returns null when the bounds cross, since the server
// then refuses every value.
export function sliderBounds(field, attrs) {
  if (!field) return null;
  const a = attrs || {};
  let min = finite(field.min) ? field.min : 0;
  let max = finite(field.max) ? field.max : 100;
  if (field.min_attr && finite(a[field.min_attr])) min = Math.max(min, a[field.min_attr]);
  if (field.max_attr && finite(a[field.max_attr])) max = Math.min(max, a[field.max_attr]);
  if (min > max) return null;
  const step = field.kind === 'int' ? 1 : (max - min) / 100 || 0.01;
  return { min, max, step };
}

// clamp keeps x in [b.min, b.max]. A missing x gives b.min.
export function clamp(x, b) {
  if (!finite(x)) return b.min;
  return Math.min(b.max, Math.max(b.min, x));
}

// brightnessPct turns HA's 0-255 brightness into 1-100, or null when the
// light is off or the value is missing.
export function brightnessPct(attrs) {
  const b = attrs && attrs.brightness;
  if (!finite(b) || b <= 0) return null;
  return Math.min(100, Math.max(1, Math.round((b / 255) * 100)));
}

// supportsColorTemp reports whether a light can change its white tone.
export function supportsColorTemp(attrs) {
  const modes = attrs && attrs.supported_color_modes;
  if (Array.isArray(modes)) return modes.includes('color_temp');
  return !!attrs && finite(attrs.min_color_temp_kelvin) && finite(attrs.max_color_temp_kelvin);
}

// parseColor checks a "#rrggbb" or "#rgb" color and returns it as
// lowercase "#rrggbb", or null.
export function parseColor(s) {
  if (typeof s !== 'string') return null;
  const t = s.trim();
  let m = /^#([0-9a-f]{6})$/i.exec(t);
  if (m) return '#' + m[1].toLowerCase();
  m = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(t);
  if (m) return ('#' + m[1] + m[1] + m[2] + m[2] + m[3] + m[3]).toLowerCase();
  return null;
}

// luminance is the WCAG relative luminance of a "#rrggbb" color.
export function luminance(hex) {
  const lin = (i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(1) + 0.7152 * lin(3) + 0.0722 * lin(5);
}

// textOn picks black or white text for a background color, whichever has
// the higher WCAG contrast.
export function textOn(hex) {
  const c = parseColor(hex);
  if (!c) return '#ffffff';
  const l = luminance(c);
  const withBlack = (l + 0.05) / 0.05;
  const withWhite = 1.05 / (l + 0.05);
  return withBlack >= withWhite ? '#000000' : '#ffffff';
}

// parseRetryAfter reads a Retry-After header: whole seconds or an HTTP
// date. It returns seconds to wait (0 or more), or null.
export function parseRetryAfter(value, nowMs) {
  if (value === null || value === undefined) return null;
  const s = String(value).trim();
  if (s === '') return null;
  if (/^\d+$/.test(s)) return parseInt(s, 10);
  if (/^[A-Za-z]{3}, /.test(s)) {
    const t = Date.parse(s);
    if (Number.isNaN(t)) return null;
    return Math.max(0, Math.ceil((t - nowMs) / 1000));
  }
  return null;
}

// formatWait turns seconds into "45 seconds", "3 minutes" or "1 hour".
// It rounds up, so the guest never comes back too early.
export function formatWait(sec) {
  if (!finite(sec) || sec <= 0) return 'a moment';
  if (sec < 60) {
    const s = Math.ceil(sec);
    return s === 1 ? '1 second' : s + ' seconds';
  }
  if (sec < 3600) {
    const m = Math.ceil(sec / 60);
    return m === 1 ? '1 minute' : m + ' minutes';
  }
  const h = Math.ceil(sec / 3600);
  return h === 1 ? '1 hour' : h + ' hours';
}

// tryAgainText is the wait message for a 429.
export function tryAgainText(sec) {
  return sec === null || sec === undefined
    ? 'Too many tries. Try again later.'
    : 'Too many tries. Try again in ' + formatWait(sec) + '.';
}

// errorText gives the toast for a failed request. It shows only our own
// words, or the server's short "error" field for statuses we have no
// words for.
export function errorText(status, body, retrySec) {
  switch (status) {
    case 0: return 'Could not connect. Check your internet and try again.';
    case 400: return 'That did not work. Try again.';
    case 403: return 'This is not allowed right now.';
    case 409: return 'Already used.';
    case 410: return 'This link is no longer active.';
    case 429: return tryAgainText(retrySec);
    case 502:
    case 503: return 'Home is not answering, try again.';
  }
  const e = body && body.error;
  if (typeof e === 'string' && e.length > 0 && e.length <= 120) return e;
  return 'Something went wrong. Try again.';
}

// mergeState puts one SSE state into the view. It returns a new view, or
// the same view when the state is not for an entity on this page.
export function mergeState(view, st) {
  if (!view || !st || typeof st.entity_id !== 'string') return view;
  if (!view.entities || !Object.hasOwn(view.entities, st.entity_id)) return view;
  return { ...view, states: { ...(view.states || {}), [st.entity_id]: st } };
}

// dayNumber is the calendar day of d in timeZone, as days since 1970.
function dayNumber(d, timeZone) {
  // en-CA prints YYYY-MM-DD.
  const s = new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(d);
  return Date.UTC(+s.slice(0, 4), +s.slice(5, 7) - 1, +s.slice(8, 10)) / 86400000;
}

// formatUntil says when a link ends: "Works until 18:00 today",
// "Works until 09:00 tomorrow", "Works until Sat 10:00" within a week,
// else "Works until 12 Mar, 10:00". It uses the browser's locale and
// time zone unless opts sets them.
export function formatUntil(iso, nowMs, opts = {}) {
  const t = typeof iso === 'string' ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return '';
  if (t <= nowMs) return 'This link has ended.';
  const { locale, timeZone } = opts;
  const d = new Date(t);
  const time = new Intl.DateTimeFormat(locale, { timeZone, hour: '2-digit', minute: '2-digit' }).format(d);
  const days = dayNumber(d, timeZone) - dayNumber(new Date(nowMs), timeZone);
  if (days === 0) return 'Works until ' + time + ' today';
  if (days === 1) return 'Works until ' + time + ' tomorrow';
  if (days < 7) {
    const wd = new Intl.DateTimeFormat(locale, { timeZone, weekday: 'short' }).format(d);
    return 'Works until ' + wd + ' ' + time;
  }
  const date = new Intl.DateTimeFormat(locale, { timeZone, day: 'numeric', month: 'short' }).format(d);
  return 'Works until ' + date + ', ' + time;
}

// Confirm-twice taps. A risky button needs a second tap on the same
// button within CONFIRM_MS.
export const CONFIRM_MS = 3000;

// confirmTap handles one tap. armed is the current {key, until} or null.
// It returns {fire, armed}: fire is true on the second tap in time, and
// armed is the state to keep.
export function confirmTap(armed, key, nowMs, windowMs = CONFIRM_MS) {
  if (armed && armed.key === key && nowMs <= armed.until) return { fire: true, armed: null };
  return { fire: false, armed: { key, until: nowMs + windowMs } };
}

// confirmExpired reports whether an armed tap has run out.
export function confirmExpired(armed, nowMs) {
  return !!armed && nowMs > armed.until;
}

// Device approval. The phases are:
//   required: the device needs approval; show "Request access".
//   pending:  a request waits; show the code and poll.
//   denied:   the host said no; offer to ask again.
//   expired:  the request ran out; offer to ask again.
//   wait:     too many requests; show the wait.
//   approved: load the page.
//   ended:    the link is no longer active.
// A passing error (network, 5xx) keeps the phase and sets error.
export const POLL_MS = 3000;

// deviceNext returns the next phase after a device reply. res is
// {status, body, retry}: the HTTP status, the JSON body, and the
// Retry-After seconds. It reads /state 403 replies too.
export function deviceNext(prev, res) {
  const p = prev || { phase: 'required' };
  const body = res.body || {};
  if (res.status === 410) return { phase: 'ended' };
  if (res.status === 429) return { phase: 'wait', wait: res.retry ?? null };
  if (res.status === 200 || res.status === 403) {
    switch (body.status) {
      case 'approved': return { phase: 'approved' };
      case 'pending':
        return { phase: 'pending', code: typeof body.code === 'string' ? body.code : p.code || '' };
      case 'denied': return { phase: 'denied' };
      case 'approval_required':
        if (p.phase === 'pending') return { phase: 'expired' };
        if (p.phase === 'denied' || p.phase === 'expired') return { phase: p.phase };
        return { phase: 'required' };
    }
  }
  return { ...p, error: errorText(res.status, body, res.retry) };
}

// shouldPoll reports whether the page polls device/status in a phase.
export function shouldPoll(phase) {
  return phase === 'pending';
}

// APPROVALS_MS is how often an approver device asks for new requests.
export const APPROVALS_MS = 5000;

// approvalsOn reports whether the page shows the approver panel and
// polls device/pending: /state said can_approve and the page is visible.
export function approvalsOn(view, hidden) {
  return !hidden && !!view && view.can_approve === true;
}

// approvalsDelay is the wait before the next device/pending poll. A 429
// waits for its Retry-After; other answers keep the normal pace.
export function approvalsDelay(status, retrySec) {
  if (status === 429 && finite(retrySec) && retrySec > 0) return Math.max(APPROVALS_MS, retrySec * 1000);
  return APPROVALS_MS;
}

// geoErrorText explains a failed location request. code is the
// GeolocationPositionError code; 0 means the browser can't do it here.
export function geoErrorText(code) {
  switch (code) {
    case 0: return 'Your browser can share location only on a secure (https) link. Ask your host for that link.';
    case 1: return 'This needs your location to check that you are near the home. Nothing is stored. Allow location for this site in your settings, then try again.';
    case 3: return 'Finding your location took too long. Try again.';
    default: return 'Could not find your location. Try again.';
  }
}

// whereErrorText explains a failed POST where.
export function whereErrorText(status, body, retrySec) {
  if (status === 400) return 'Your location is not precise enough. Try again outside, or turn on Precise Location for this browser.';
  return errorText(status, body, retrySec);
}

// SLIDER_MS is the default gap between slider sends, used when /state
// has no guest.slider_interval_ms.
export const SLIDER_MS = 300;

// sliderInterval reads guest.slider_interval_ms from /state, or gives
// SLIDER_MS when it is missing or not a positive number.
export function sliderInterval(guest) {
  const ms = guest && guest.slider_interval_ms;
  return finite(ms) && ms > 0 ? ms : SLIDER_MS;
}

// sliderSender sends slider values while the guest drags. It keeps at
// most one request in flight and never sends more often than intervalMs.
// While it waits, only the newest value is kept. A value equal to the
// last one sent is skipped.
//
// opts: send(v) returns a promise; now() gives ms; setTimer(fn, ms) and
// clearTimer(t) default to the real timers. Tests pass a fake clock.
//
// It returns {input(v), release(v)}: input on every drag step, release
// on change. release sends the final value as soon as the spacing allows.
export function sliderSender(opts) {
  const {
    send, intervalMs = SLIDER_MS, now = () => Date.now(),
    setTimer = (fn, ms) => setTimeout(fn, ms), clearTimer = (t) => clearTimeout(t),
  } = opts;
  let lastSent;
  let lastAt = -Infinity;
  let inFlight = false;
  let hasPending = false;
  let pending;
  let timer = null;

  function pump() {
    if (inFlight || !hasPending) return;
    if (pending === lastSent) {
      hasPending = false;
      return;
    }
    const wait = lastAt + intervalMs - now();
    if (wait > 0) {
      if (timer === null) {
        timer = setTimer(() => {
          timer = null;
          pump();
        }, wait);
      }
      return;
    }
    const v = pending;
    hasPending = false;
    inFlight = true;
    lastSent = v;
    lastAt = now();
    let p;
    try {
      p = send(v);
    } catch {
      p = null;
    }
    Promise.resolve(p)
      .catch(() => {})
      .finally(() => {
        inFlight = false;
        pump();
      });
  }

  function push(v) {
    pending = v;
    hasPending = true;
    pump();
  }

  return {
    input: push,
    release: push,
    // stop drops a waiting value and its timer.
    stop() {
      hasPending = false;
      if (timer !== null) clearTimer(timer);
      timer = null;
    },
  };
}
