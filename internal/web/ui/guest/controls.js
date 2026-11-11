// Entity cards. Each card draws one control, picked by lib.pickControl,
// and redraws itself on state changes. A card never redraws while the
// guest drags one of its sliders or has a confirm tap waiting, so live
// updates do not fight the guest's finger.
import { el, icon } from './dom.js';
import {
  actionLabel, confirmLabel, stateLabel, splitActions, pickControl, findAction,
  toggleFor, lockActions, alarmActions, scriptView, mediaInfo, sliderBounds, clamp,
  brightnessPct, supportsColorTemp, fanBounds, confirmTap, CONFIRM_MS, sliderSender,
  coverView, coverStateText, coverConfirmText, COVER_CONFIRM_MS,
} from './lib.js';

// The one armed confirm tap on the page: {key, until, reset}.
let armed = null;

function disarm() {
  if (!armed) return;
  const a = armed;
  armed = null;
  clearTimeout(a.timer);
  a.reset();
}

// ctx is {act(entityId, actionId, data) -> Promise<boolean>,
// shareLocation(entityId), sliderMs}. sliderMs is the least gap between
// slider sends. makeCard returns {node, update(state)}.
export function makeCard(entity, state, ctx) {
  const headId = 'card-' + entity.id.replace(/[^A-Za-z0-9_-]/g, '_');
  const name = el('h3', { className: 'name', id: headId }, entity.name);
  const kind = pickControl(entity.domain, splitActions(entity.actions).now);
  const live = kind === 'lock' || kind === 'alarm' || kind === 'script' || kind === 'cover';
  const stateText = el('span', { className: 'state', attrs: live ? { 'aria-live': 'polite' } : {} });
  const body = el('div', { className: 'body' });
  const node = el('section', { className: 'card', attrs: { 'aria-labelledby': headId } },
    el('div', { className: 'head' }, name, stateText), body);

  let current = state;
  let holding = 0; // sliders under the guest's finger
  let dirty = false;

  const card = {
    entity,
    hold() { holding++; },
    release() {
      holding = Math.max(0, holding - 1);
      if (holding === 0 && dirty) draw();
    },
    send(actionId, data, button) {
      if (button) {
        button.disabled = true;
        button.setAttribute('aria-busy', 'true');
      }
      return ctx.act(entity.id, actionId, data).finally(() => {
        if (button && button.isConnected) {
          button.disabled = false;
          button.removeAttribute('aria-busy');
        }
      });
    },
    ctx,
  };

  function draw() {
    if (holding > 0 || (armed && armed.card === card)) {
      dirty = true;
      return;
    }
    dirty = false;
    const focusKey = node.contains(document.activeElement) ? document.activeElement.dataset.key : '';
    const st = current || null;
    const s = st ? st.state : undefined;
    stateText.textContent = kind === 'cover' ? coverStateText(st) : stateLabel(entity.domain, s);
    const { now, later } = splitActions(entity.actions);
    const parts = now.length > 0 ? DRAW[kind](card, now, st) : [];
    const off = s === 'unavailable';
    if (off) for (const p of parts) disableAll(p);
    if (later.length > 0) parts.push(locationRow(entity, now.length === 0, ctx));
    body.replaceChildren(...parts);
    if (focusKey) {
      const again = body.querySelector('[data-key="' + focusKey + '"]');
      if (again) again.focus();
    }
  }

  draw();
  return {
    node,
    update(st) {
      current = st;
      draw();
    },
  };
}

function disableAll(node) {
  for (const c of node.querySelectorAll('button, input')) c.disabled = true;
}

function locationRow(entity, only, ctx) {
  return el('div', { className: 'loc' },
    el('p', {}, only ? 'Share your location to use this.' : 'Share your location for more controls.'),
    el('button', {
      type: 'button', className: 'secondary',
      on: { click: () => ctx.shareLocation(entity.id) },
    }, icon('pin'), 'Share location'));
}

// actionButton makes a button for one action. A used one-time action
// shows as done. ui "confirm" or opts.confirm asks for a second tap.
function actionButton(card, a, opts = {}) {
  const label = opts.label || actionLabel(a.id);
  if (a.used) {
    return el('button', { type: 'button', disabled: true, className: 'done' }, icon('check'), label + ': done');
  }
  const b = el('button', { type: 'button', className: opts.className || '' }, label);
  b.dataset.key = a.id;
  const data = opts.data || {};
  if (a.ui === 'confirm' || opts.confirm) {
    b.addEventListener('click', () => {
      const now = Date.now();
      const r = confirmTap(armed && armed.card === card ? armed : null, a.id, now, CONFIRM_MS);
      if (r.fire) {
        const sent = card.send(a.id, data, b);
        disarm();
        return sent;
      }
      disarm();
      // Disarming may redraw the card and replace this button. Arming a
      // button that is gone would leave its fresh copy one tap from firing.
      if (!b.isConnected) return undefined;
      b.textContent = confirmLabel(a.id);
      b.classList.add('armed');
      armed = {
        ...r.armed, card,
        timer: setTimeout(disarm, CONFIRM_MS),
        reset() {
          b.textContent = label;
          b.classList.remove('armed');
          card.release(); // redraw if a state came in meanwhile
        },
      };
      card.hold();
    });
  } else {
    b.addEventListener('click', () => card.send(a.id, data, b));
  }
  return b;
}

function row(...children) {
  return el('div', { className: 'actions' }, ...children);
}

function toggle(card, domain, actions, st) {
  const t = toggleFor(domain, actions, st ? st.state : undefined);
  const b = el('button', {
    type: 'button', className: 'toggle', disabled: !t.next,
    attrs: { role: 'switch', 'aria-checked': t.on === true ? 'true' : 'false', 'aria-label': card.entity.name },
  }, el('span', { className: 'track' }, el('span', { className: 'thumb' })),
  el('span', { className: 'toggle-text' }, t.on === true ? 'On' : t.on === false ? 'Off' : ''));
  b.dataset.key = 'toggle';
  if (t.next) b.addEventListener('click', () => card.send(t.next, {}, b));
  return b;
}

// slider makes a labelled range input. It sends while the guest drags,
// throttled by sliderSender, and always sends the final value on
// release. onSend sends one value.
function slider(card, key, labelText, b, value, format, onSend) {
  const sender = sliderSender({ send: onSend, intervalMs: card.ctx.sliderMs });
  const id = 'r-' + card.entity.id.replace(/[^A-Za-z0-9_-]/g, '_') + '-' + key;
  const out = el('span', { className: 'value' }, format(value));
  const input = el('input', {
    type: 'range', id, min: String(b.min), max: String(b.max), step: String(b.step), value: String(value),
  });
  input.dataset.key = key;
  input.setAttribute('aria-valuetext', format(value));
  let held = false;
  const hold = () => {
    if (!held) {
      held = true;
      card.hold();
    }
  };
  const release = () => {
    if (held) {
      held = false;
      card.release();
    }
  };
  input.addEventListener('pointerdown', hold);
  input.addEventListener('touchstart', hold, { passive: true });
  input.addEventListener('input', () => {
    const v = Number(input.value);
    out.textContent = format(v);
    input.setAttribute('aria-valuetext', format(v));
    sender.input(v);
  });
  input.addEventListener('change', () => {
    sender.release(Number(input.value));
    release();
  });
  // A drag that ends without a change (same value) must still let go.
  input.addEventListener('pointerup', () => setTimeout(release, 0));
  input.addEventListener('pointercancel', release);
  input.addEventListener('blur', release);
  return el('div', { className: 'slider' },
    el('label', { htmlFor: id }, labelText, ' ', out), input);
}

// confirmSlider is a slider that sends nothing while the guest drags.
// On release it asks ask(v) in a confirm row, and sends v only on Yes.
// Cancel, a timeout or another confirm tap puts the slider back. The
// row is the page's one armed confirm, so the card does not redraw
// under it.
function confirmSlider(card, key, labelText, b, value, format, ask, onSend) {
  const id = 'r-' + card.entity.id.replace(/[^A-Za-z0-9_-]/g, '_') + '-' + key;
  let start = value;
  const out = el('span', { className: 'value' }, format(value));
  const input = el('input', {
    type: 'range', id, min: String(b.min), max: String(b.max), step: String(b.step), value: String(value),
  });
  input.dataset.key = key;
  input.setAttribute('aria-valuetext', format(value));
  const question = el('p', { className: 'question', attrs: { 'aria-live': 'polite' } });
  const yes = el('button', { type: 'button', className: 'armed' }, 'Yes');
  const no = el('button', { type: 'button', className: 'secondary' }, 'Cancel');
  const prompt = el('div', { className: 'confirm-row', hidden: true }, question, row(yes, no));
  const show = (v) => {
    out.textContent = format(v);
    input.setAttribute('aria-valuetext', format(v));
  };

  let held = false;
  const hold = () => {
    if (!held) {
      held = true;
      card.hold();
    }
  };
  const letGo = () => {
    if (held) {
      held = false;
      card.release();
    }
  };
  input.addEventListener('pointerdown', hold);
  input.addEventListener('touchstart', hold, { passive: true });
  input.addEventListener('input', () => show(Number(input.value)));
  input.addEventListener('change', () => {
    const v = Number(input.value);
    disarm();
    // Disarming may redraw the card and replace this slider. Asking from
    // a slider that is gone would leave a row nobody sees.
    if (!input.isConnected) {
      letGo();
      return;
    }
    input.value = String(v);
    show(v);
    if (v === start) {
      letGo();
      return;
    }
    question.textContent = ask(v);
    prompt.hidden = false;
    armed = {
      key: 'slider:' + key, until: Date.now() + COVER_CONFIRM_MS, card,
      timer: setTimeout(disarm, COVER_CONFIRM_MS),
      reset() {
        prompt.hidden = true;
        input.value = String(start);
        show(start);
        card.release();
      },
    };
    card.hold();
    letGo();
  });
  input.addEventListener('pointerup', () => setTimeout(letGo, 0));
  input.addEventListener('pointercancel', letGo);
  input.addEventListener('blur', letGo);
  yes.addEventListener('click', () => {
    if (!armed || armed.card !== card || armed.key !== 'slider:' + key) return;
    const v = Number(input.value);
    // Keep the new value through the reset, until the state catches up.
    start = v;
    disarm();
    onSend(v);
  });
  no.addEventListener('click', disarm);
  return el('div', { className: 'slider' },
    el('label', { htmlFor: id }, labelText, ' ', out), input, prompt);
}

const pct = (v) => Math.round(v) + '%';

const DRAW = {
  lock(card, actions, st) {
    return [row(...lockActions(actions, st && st.state).map((a) => actionButton(card, a, { className: 'big' })))];
  },

  alarm(card, actions, st) {
    return [row(...alarmActions(actions, st && st.state).map((a) => actionButton(card, a, { className: 'big' })))];
  },

  light(card, actions, st) {
    const parts = [row(toggle(card, 'light', actions, st))];
    const on = findAction(actions, 'light.turn_on');
    const attrs = (st && st.attributes) || {};
    if (!on || on.used || !st || st.state !== 'on') return parts;
    const fields = on.fields || {};
    const modes = attrs.supported_color_modes;
    const dims = !Array.isArray(modes) || modes.some((m) => m !== 'onoff');
    const bb = sliderBounds(fields.brightness_pct, attrs);
    if (bb && dims) {
      parts.push(slider(card, 'brightness', 'Brightness', bb, clamp(brightnessPct(attrs) ?? bb.max, bb), pct,
        (v) => card.send('light.turn_on', { brightness_pct: v })));
    }
    const cb = sliderBounds(fields.color_temp_kelvin, attrs);
    if (cb && supportsColorTemp(attrs)) {
      const mid = Math.round((cb.min + cb.max) / 2);
      parts.push(slider(card, 'ct', 'Warm to cool', cb, clamp(attrs.color_temp_kelvin ?? mid, cb), (v) => Math.round(v) + ' K',
        (v) => card.send('light.turn_on', { color_temp_kelvin: v })));
    }
    return parts;
  },

  // Like a light, the speed slider shows only while the fan is on. A fan
  // with no on/off grant keeps the slider, since it is the only way to
  // start it.
  fan(card, actions, st) {
    const parts = [];
    const canToggle = !!(findAction(actions, 'fan.turn_on') || findAction(actions, 'fan.turn_off'));
    if (canToggle) parts.push(row(toggle(card, 'fan', actions, st)));
    const sp = findAction(actions, 'fan.set_percentage');
    const attrs = (st && st.attributes) || {};
    const b = sp && !sp.used && fanBounds(sliderBounds((sp.fields || {}).percentage, {}), attrs);
    if (b && (!canToggle || (st && st.state === 'on'))) {
      // A step such as 33.33 gives fractions. The server takes whole
      // percents, and HA picks the nearest speed.
      parts.push(slider(card, 'speed', 'Speed', b, clamp(attrs.percentage, b), pct,
        (v) => card.send('fan.set_percentage', { percentage: Math.round(v) })));
    }
    return parts;
  },

  media(card, actions, st) {
    const info = mediaInfo(st);
    const parts = [];
    if (info.title || info.artist) {
      parts.push(el('p', { className: 'now-playing' },
        info.title ? el('span', { className: 'title' }, info.title) : null,
        info.artist ? el('span', { className: 'artist' }, info.artist) : null));
    }
    const buttons = [];
    const iconButton = (a, name, label, data, extra = {}) => {
      if (!a) return null;
      const b = el('button', {
        type: 'button', className: 'icon-button', disabled: !!a.used,
        attrs: { 'aria-label': label, ...extra },
      }, icon(name));
      b.dataset.key = a.id;
      if (!a.used) b.addEventListener('click', () => card.send(a.id, data, b));
      return b;
    };
    buttons.push(iconButton(findAction(actions, 'media_player.media_previous_track'), 'prev', 'Previous', {}));
    buttons.push(iconButton(findAction(actions, 'media_player.media_play_pause'),
      info.playing ? 'pause' : 'play', info.playing ? 'Pause' : 'Play', {}));
    buttons.push(iconButton(findAction(actions, 'media_player.media_next_track'), 'next', 'Next', {}));
    buttons.push(iconButton(findAction(actions, 'media_player.volume_mute'),
      info.muted ? 'muted' : 'volume', 'Mute', { is_volume_muted: !info.muted },
      { 'aria-pressed': info.muted ? 'true' : 'false' }));
    if (buttons.some(Boolean)) parts.push(row(...buttons));
    const vs = findAction(actions, 'media_player.volume_set');
    const vb = vs && !vs.used && sliderBounds((vs.fields || {}).volume_level, {});
    if (vb) {
      const b = { min: Math.ceil(vb.min * 100), max: Math.floor(vb.max * 100), step: 1 };
      if (b.min <= b.max) {
        parts.push(slider(card, 'volume', 'Volume', b, clamp(info.volume, b), pct,
          (v) => card.send('media_player.volume_set', { volume_level: Math.round(v) / 100 })));
      }
    }
    return parts;
  },

  switch(card, actions, st) {
    return [row(toggle(card, 'switch', actions, st))];
  },

  script(card, actions, st) {
    const v = scriptView(actions, st && st.state);
    const buttons = [];
    if (v.run) {
      const b = actionButton(card, v.run, { className: 'big', label: v.runLabel });
      if (v.running) b.disabled = true;
      buttons.push(b);
    }
    if (v.cancel) buttons.push(actionButton(card, v.cancel, { className: 'secondary' }));
    return [row(...buttons)];
  },

  cover(card, actions, st) {
    const v = coverView(actions, st);
    const parts = [];
    const button = (x) => actionButton(card, x.action, { confirm: x.confirm });
    if (v.buttons.length > 0) parts.push(row(...v.buttons.map(button)));
    if (v.position) {
      const p = v.position;
      const b = sliderBounds(p.field, {});
      const send = (x) => card.send(p.action.id, { position: x });
      if (b && p.confirm) {
        const dc = (st && st.attributes && st.attributes.device_class) || '';
        parts.push(confirmSlider(card, 'position', 'Position', b, clamp(p.value, b), pct,
          (x) => coverConfirmText(dc, x), send));
      } else if (b) {
        parts.push(slider(card, 'position', 'Position', b, clamp(p.value, b), pct, send));
      }
    }
    if (v.tiltButtons.length > 0) parts.push(row(...v.tiltButtons.map(button)));
    if (v.tilt) {
      const t = v.tilt;
      const b = sliderBounds(t.field, {});
      if (b) {
        parts.push(slider(card, 'tilt', 'Tilt', b, clamp(t.value, b), pct,
          (x) => card.send(t.action.id, { tilt_position: x })));
      }
    }
    return parts;
  },

  buttons(card, actions) {
    return [row(...actions.map((a) => actionButton(card, a)))];
  },
};
