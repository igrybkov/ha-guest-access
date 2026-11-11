// The rule editor, shared by templates and links: select and exclude
// pickers, actions filtered to the domains the rule matches, access and
// script variables. Plus the problem slots that map server problems to
// fields.
import { h, fill, get, check, localEnabled, docsLink } from './dom.js';
import {
  paths, SELECTOR_KEYS, patternProblem, searchOptions, pickedEntities, actionChoices,
  domainOf, ruleState, buildRule, groupProblems, isExact, effectiveOrder, moveItem,
} from './lib.js';

// ---- Problem slots ----

// Slots maps problem paths to places in the form. Register a slot per
// field; show() fills them, and anything unmatched goes to the top slot.
export class Slots {
  constructor() {
    this.map = new Map([['', h('div', { class: 'slot' })]]);
  }

  slot(key) {
    const el = h('div', { class: 'slot' });
    this.map.set(key, el);
    return el;
  }

  get top() {
    return this.map.get('');
  }

  // drop forgets slots under a prefix, when a rule is removed.
  drop(prefix) {
    for (const k of [...this.map.keys()]) if (k && k.startsWith(prefix)) this.map.delete(k);
  }

  show(problems, normalize) {
    const grouped = groupProblems(problems, new Set([...this.map.keys()].filter(Boolean)), normalize);
    for (const [key, el] of this.map) {
      const msgs = grouped.get(key) || [];
      fill(el, ...(msgs.length ? [h('ul', { class: 'error small problems' }, msgs.map((m) => h('li', { text: m })))] : []));
    }
    return grouped;
  }
}

// ---- Inventory ----

// loadInventory fetches the picker lists. On failure (HA down) it returns
// null and the editor offers every action.
export async function loadInventory() {
  try {
    const [entities, areas, labels, devices] = await Promise.all([
      get(paths.entities), get(paths.areas), get(paths.labels), get(paths.devices),
    ]);
    return { entities, areas, labels, devices, byId: new Map(entities.map((e) => [e.entity_id, e])) };
  } catch {
    return null;
  }
}

// options lists the picker choices for one selector key.
function optionsFor(inv, key) {
  if (!inv) return [];
  switch (key) {
    case 'entities': return inv.entities.map((e) => ({ id: e.entity_id, name: e.name }));
    case 'areas': return inv.areas.map((a) => ({ id: a.area_id, name: a.name }));
    case 'labels': return inv.labels.map((l) => ({ id: l.label_id, name: l.name }));
    default: return inv.devices.map((d) => ({ id: d.id, name: d.name }));
  }
}

const KEY_LABELS = { entities: 'Entities', areas: 'Areas', labels: 'Labels', devices: 'Devices' };

// patternList edits one list of patterns: chips plus a search box with
// suggestions. Enter adds the typed text, so "light.guest_*" works too.
function patternList({ values, options, inSelect, label, onChange }) {
  const names = new Map(options.map((o) => [o.id, o.name]));
  const chips = h('div', { class: 'chips' });
  const hint = h('div', { class: 'error small' });
  const sugg = h('ul', { class: 'list suggest', role: 'listbox', hidden: true });
  const input = h('input', { type: 'search', placeholder: `Add ${label.toLowerCase()} or a pattern with *`, autocomplete: 'off', 'aria-label': `Add ${label.toLowerCase()}` });

  const add = (v) => {
    const p = v.trim();
    if (!p || values.includes(p)) return;
    values.push(p);
    input.value = '';
    draw();
    onChange();
  };

  function draw() {
    fill(chips, ...values.map((v, i) => {
      const bad = patternProblem(v, inSelect);
      const name = isExact(v) ? names.get(v) : '';
      return h('span', { class: `chip${bad ? ' bad' : ''}`, title: bad || v },
        name ? h('span', {}, h('span', { text: name }), ' ', h('code', { class: 'muted', text: v })) : h('code', { text: v }),
        h('button', { type: 'button', class: 'icon', text: '×', 'aria-label': `Remove ${v}`, onclick: () => { values.splice(i, 1); draw(); onChange(); } }));
    }));
    const msgs = values.map((v) => [v, patternProblem(v, inSelect)]).filter(([, m]) => m);
    fill(hint, ...msgs.map(([v, m]) => h('div', { text: `"${v}" ${m}` })));
  }

  function suggest() {
    const hits = searchOptions(options.filter((o) => !values.includes(o.id)), input.value, 20);
    sugg.hidden = !input.value.trim() || !hits.length;
    fill(sugg, ...hits.map((o) => h('li', {
      role: 'option',
      onmousedown: (e) => e.preventDefault(),
      onclick: () => { add(o.id); sugg.hidden = true; input.focus(); },
    }, h('span', { text: o.name || o.id }), ' ', h('code', { class: 'muted small', text: o.id }))));
  }

  input.addEventListener('input', suggest);
  input.addEventListener('blur', () => { sugg.hidden = true; });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); add(input.value); sugg.hidden = true; }
    if (e.key === 'Escape') sugg.hidden = true;
  });
  draw();
  return h('div', { class: 'patterns' }, h('div', { class: 'small muted', text: label }), chips, h('div', { class: 'suggest-wrap' }, input, sugg), hint);
}

function selectorBox(title, sel, inSelect, inv, base, slots, onChange) {
  return h('fieldset', { class: 'selector' }, h('legend', { text: title }),
    SELECTOR_KEYS.map((k) => h('div', {},
      patternList({ values: sel[k], options: optionsFor(inv, k), inSelect, label: KEY_LABELS[k], onChange }),
      slots.slot(`${base}.${k}`))));
}

// ---- Entity order ----

// orderEditor lists entities in guest page order with up and down
// buttons. form.entity_order is the saved order. set() redraws the list
// from the preview's entities ([{entity_id, name}]). A move writes the
// whole shown order into form.entity_order. Order changes no grant, so a
// move needs no new preview.
export function orderEditor(form, empty) {
  const box = h('div', { class: 'stack tight' });
  let shown = [];
  // A redraw drops focus, so give it back to the button with the same
  // label. That is the same button on the moved row.
  const draw = (focusLabel) => {
    if (!shown.length) {
      fill(box, h('p', { class: 'muted small', text: empty }));
      return;
    }
    const n = shown.length;
    const move = (i, d) => (ev) => {
      const label = ev.currentTarget.getAttribute('aria-label');
      const ids = moveItem(shown.map((e) => e.entity_id), i, d);
      form.entity_order = ids;
      shown = effectiveOrder(ids, shown);
      draw(label);
    };
    fill(box, ...shown.map((e, i) => h('div', { class: 'tpl-row' },
      h('span', { class: 'check' }, h('span', { text: e.name }), h('code', { class: 'muted small', text: e.entity_id })),
      n > 1 && h('span', { class: 'tpl-move' },
        h('button', { type: 'button', class: 'icon', text: '↑', disabled: i === 0, 'aria-label': `Move ${e.name} up`, onclick: move(i, -1) }),
        h('button', { type: 'button', class: 'icon', text: '↓', disabled: i === n - 1, 'aria-label': `Move ${e.name} down`, onclick: move(i, 1) })))));
    const btn = focusLabel && [...box.querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === focusLabel);
    if (btn && !btn.disabled) btn.focus();
  };
  return {
    el: box,
    set(entities) {
      const active = box.contains(document.activeElement) ? document.activeElement.getAttribute('aria-label') : null;
      shown = effectiveOrder(form.entity_order, entities);
      draw(active);
    },
  };
}

// LOCAL_LIMITS is the short warning next to a local-only choice.
export const LOCAL_LIMITS = 'Guests on your Wi-Fi may still count as outside: Private Relay, custom DNS, VPN. They lose these controls; nobody gains any.';

// localOffNote explains local-only while it is off.
export function localOffNote() {
  return h('p', { class: 'note small' },
    'Local only is off. It is opt-in and best effort: guests on your Wi-Fi can count as outside (Private Relay, custom DNS, VPN). ',
    'Turn on local_access in Settings → Apps → Guest Access → Configuration. ', docsLink());
}

// accessEditor edits an access object in place: network and near home.
// The network choice shows only when local access is on, or when the
// value is already local (so it can be fixed).
export function accessEditor(acc, slotKey, slots, onChange) {
  const net = h('select', { 'aria-label': 'Network' },
    h('option', { value: 'any', text: 'Any network', selected: acc.network !== 'local' }),
    h('option', { value: 'local', text: 'Home network only', selected: acc.network === 'local' }));
  const warn = h('p', { class: 'warning small', text: localEnabled() ? LOCAL_LIMITS : 'Local access is off, so this rule denies every request. Pick Any network, or turn local access on.' });
  const showWarn = () => { warn.hidden = net.value !== 'local'; };
  net.addEventListener('change', () => { acc.network = net.value; showWarn(); onChange(); });
  showWarn();
  const near = h('input', { type: 'number', min: 0, max: 100000, step: 50, value: acc.near_home_m || '', placeholder: 'Off', inputmode: 'numeric' });
  near.addEventListener('input', () => { acc.near_home_m = Number(near.value) || 0; onChange(); });
  const showNet = localEnabled() || acc.network === 'local';
  return h('div', { class: 'stack' },
    h('div', { class: 'grid2' },
      showNet && h('label', { class: 'field' }, h('span', { text: 'Network' }), net),
      h('label', { class: 'field' }, h('span', { text: 'Near home (meters)' }), near,
        h('small', { class: 'muted', text: 'The guest shares their location. Empty means off.' }))),
    showNet && warn,
    slots.slot(slotKey));
}

// actionsBox lists the catalog actions as checkboxes, filtered to the
// domains the rule matches. Ticked actions always stay visible.
function actionsBox(st, catalog, onChange) {
  const box = h('div', { class: 'actions-list' });
  const note = h('p', { class: 'muted small' });
  let domains = null;

  function draw() {
    const choices = actionChoices(catalog.actions || [], domains, st.actions);
    note.textContent = domains == null
      ? 'Showing every action: the entity list is not loaded.'
      : (domains.size ? `Showing actions for: ${[...domains].sort().join(', ')}.` : 'Pick entities first. Actions show for the domains you pick.');
    const groups = new Map();
    for (const a of choices) {
      const d = domainOf(a.id);
      if (!groups.has(d)) groups.set(d, []);
      groups.get(d).push(a);
    }
    fill(box, ...[...groups].map(([d, as]) => h('div', { class: 'action-group' },
      h('div', { class: 'small muted', text: d }),
      h('div', { class: 'chips' }, as.map((a) => {
        const cb = h('input', { type: 'checkbox', checked: st.actions.includes(a.id) });
        cb.addEventListener('change', () => {
          if (cb.checked) st.actions.push(a.id);
          else st.actions.splice(st.actions.indexOf(a.id), 1);
          onChange(true);
        });
        return h('label', { class: `check chip-check${a.unmatched ? ' unmatched' : ''}`, title: a.unmatched ? 'Nothing this rule picks is in this domain' : a.id },
          cb, h('span', { text: a.id.slice(d.length + 1) }), a.unmatched && h('span', { class: 'muted small', text: a.unknown ? '(unknown)' : '(no match)' }));
      })))));
  }

  return {
    el: h('div', {}, note, box),
    setDomains(ds) { domains = ds; draw(); },
    draw,
  };
}

function varsBox(st, base, slots, onChange) {
  const rows = h('div', { class: 'stack' });
  function draw() {
    fill(rows, ...st.vars.map((pair, i) => {
      const k = h('input', { value: pair[0], placeholder: 'name', 'aria-label': 'Variable name' });
      const v = h('input', { value: pair[1], placeholder: 'value', 'aria-label': 'Variable value' });
      k.addEventListener('input', () => { pair[0] = k.value; onChange(); });
      v.addEventListener('input', () => { pair[1] = v.value; onChange(); });
      return h('div', { class: 'inline' }, k, v,
        h('button', { type: 'button', class: 'icon', text: '×', 'aria-label': 'Remove variable', onclick: () => { st.vars.splice(i, 1); draw(); onChange(); } }));
    }));
  }
  draw();
  return h('fieldset', {}, h('legend', { text: 'Script variables' }),
    h('p', { class: 'muted small', text: 'Sent with script.run. The app adds guest_link. Values are JSON when they parse, text otherwise. No codes.' }),
    rows,
    h('div', {}, h('button', { type: 'button', text: 'Add variable', onclick: () => { st.vars.push(['', '']); draw(); } })),
    slots.slot(`${base}.variables`));
}

// ruleEditor builds one rule card. ctx: {catalog, inv, slots, onChange,
// onRemove, forceAccess (link extras always carry their own access)}.
// Returns {el, st, setResolved(ids)}.
export function ruleEditor(rule, base, title, ctx) {
  const st = ruleState(rule, { forceAccess: ctx.forceAccess });
  const { slots } = ctx;
  const resolved = h('div', { class: 'resolved small' }, h('span', { class: 'muted', text: 'Resolving…' }));
  let actions = null;

  const refreshDomains = () => {
    if (!ctx.inv) return actions.setDomains(null);
    const ents = pickedEntities(buildRule(st), ctx.inv);
    actions.setDomains(new Set(ents.map((e) => domainOf(e.entity_id))));
  };
  const vars = h('div');
  const drawVars = () => fill(vars, st.actions.includes('script.run') || st.vars.length ? varsBox(st, base, slots, changed) : '');
  // changed runs after any edit. actionsOnly skips the domain refresh.
  function changed(actionsOnly) {
    if (actionsOnly === true) drawVars();
    else refreshDomains();
    ctx.onChange();
  }
  actions = actionsBox(st, ctx.catalog, changed);

  const accessWrap = h('div');
  const drawAccess = () => fill(accessWrap, st.ownAccess ? accessEditor(st.access, `${base}.access`, slots, changed) : '');
  const own = h('input', { type: 'checkbox', checked: st.ownAccess });
  own.addEventListener('change', () => { st.ownAccess = own.checked; drawAccess(); changed(); });

  const el = h('section', { class: 'rule card' },
    h('div', { class: 'toolbar' }, h('h3', { class: 'grow', text: title }),
      ctx.onRemove && h('button', { type: 'button', class: 'danger', text: 'Remove rule', onclick: ctx.onRemove })),
    slots.slot(base),
    selectorBox('Select', st.select, true, ctx.inv, `${base}.select`, slots, changed),
    slots.slot(`${base}.select`),
    h('details', { class: 'exclude' }, h('summary', { text: 'Exclude' }),
      selectorBox('Exclude', st.exclude, false, ctx.inv, `${base}.exclude`, slots, changed),
      slots.slot(`${base}.exclude`)),
    h('fieldset', {}, h('legend', { text: 'Actions' }), actions.el, slots.slot(`${base}.actions`)),
    ctx.forceAccess
      ? h('fieldset', {}, h('legend', { text: 'Access' }), accessEditor(st.access, `${base}.access`, slots, changed))
      : h('fieldset', {}, h('legend', { text: 'Access' }),
        check('Own access for this rule', own, 'Replaces the template default for this rule.'), accessWrap),
    vars,
    resolved);
  if (Object.values(st.exclude).some((l) => l.length)) el.querySelector('details.exclude').setAttribute('open', '');
  drawAccess();
  drawVars();
  refreshDomains();

  return {
    el,
    st,
    // setResolved shows the entities the server preview resolved.
    setResolved(ids) {
      if (ids == null) {
        fill(resolved, h('span', { class: 'muted', text: 'Preview unavailable.' }));
        return;
      }
      const names = ctx.inv?.byId;
      const coded = ids.filter((id) => names?.get(id)?.needs_code);
      fill(resolved, 
        h('details', {}, h('summary', { text: ids.length === 1 ? 'Resolves to 1 entity' : `Resolves to ${ids.length} entities` }),
          h('ul', { class: 'plain-list' }, ids.map((id) => h('li', {},
            h('span', { text: names?.get(id)?.name || id }), ' ', h('code', { class: 'muted', text: id }))))),
        coded.length > 0 && h('p', { class: 'warning', text: `Needs a code in HA, which the app never sends: ${coded.join(', ')}. Use a script instead.` }));
    },
  };
}
