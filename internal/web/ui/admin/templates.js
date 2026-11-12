// Templates screen: the list and the editor. The editor previews each
// rule's entities on the server as you type.
import {
  h, fill, get, post, put, del, toast, run, ask, field, check, back, debounce,
  localEnabled, maEnabled, APIError,
} from './dom.js';
import { paths, buildTemplate, buildRule, slugifyID, normalizeTemplatePath, grantedEntities } from './lib.js';
import { Slots, ruleEditor, loadInventory, accessEditor, localOffNote, entityEditor, moveRule } from './rules.js';
import { loadEntityTexts } from './entitytext.js';

export async function render(view, rest) {
  const data = await get(paths.templates);
  const list = data.templates || [];
  if (rest[0] === 'new') return editor(view, null, list);
  if (rest[0] === 'edit' && rest[1]) return editor(view, list.find((t) => t.id === rest[1]), list);

  fill(view, 
    h('div', { class: 'toolbar' }, h('h1', { text: 'Templates' }),
      h('a', { class: 'button primary', href: '#/templates/new', text: 'New template' })),
    data.error && h('p', { class: 'warning', text: `templates.yaml: ${data.error}` }),
    (data.problems || []).length > 0 && h('div', { class: 'warning' },
      h('p', { text: 'Some templates have problems. Rules with problems grant nothing.' }),
      h('ul', {}, data.problems.map((p) => h('li', { text: p.path ? `${p.path}: ${p.message}` : p.message })))),
    list.length
      ? h('ul', { class: 'list plain' }, list.map((t) => h('li', {}, h('div', { class: 'row' },
        h('div', { class: 'grow' },
          h('a', { class: 'title', href: `#/templates/edit/${encodeURIComponent(t.id)}`, text: t.name }),
          h('div', { class: 'muted small' },
            `${t.id} · ${ruleCount((t.rules || []).length)}`,
            t.access?.network === 'local' && h('span', { class: 'badge', text: 'Home network' }),
            t.access?.near_home_m > 0 && h('span', { class: 'badge', text: `Near home ${t.access.near_home_m} m` }),
            t.party && h('span', { class: 'badge', text: 'Party' })))))))
      : h('p', { class: 'muted', text: 'No templates yet. A template is a set of rules that pick entities and allowed actions.' }));
  return null;
}

// ruleCount words a number of rules. A template may have none.
const ruleCount = (n) => (n === 0 ? 'no rules yet' : n === 1 ? '1 rule' : `${n} rules`);

async function editor(view, tpl, all) {
  const isNew = !tpl;
  if (!isNew && !tpl.id) throw new Error('no such template');
  const [catalog, inv, texts] = await Promise.all([get(paths.catalog), loadInventory(), loadEntityTexts()]);
  const src = tpl || { id: '', name: '', rules: [] };
  const f = {
    id: src.id,
    name: src.name,
    icon: src.icon || '',
    access: { network: src.access?.network || 'any', near_home_m: src.access?.near_home_m || 0 },
    party: !!src.party,
    entity_order: [...(src.entity_order || [])],
  };
  const slots = new Slots();
  // The server only resolves a template that passes Validate, so a new
  // template gets a stand-in id until it has one.
  const probeID = () => f.id || 'new-template';
  const entities = entityEditor({
    form: f, inv, catalog, texts,
    rules: () => editors.map((e) => e.st),
    group: probeID,
    empty: 'Nothing yet. Entities this template grants show here.',
    onRulesChange: (changed) => {
      for (const i of changed) editors[i]?.syncExclude();
      schedule();
    },
  });
  let idTouched = !isNew;

  const name = h('input', { value: f.name, required: true });
  const id = h('input', { value: f.id, disabled: !isNew, pattern: '[a-z0-9][a-z0-9_\\-]*', placeholder: 'from the name' });
  name.addEventListener('input', () => {
    f.name = name.value;
    if (!idTouched) { f.id = slugifyID(name.value); id.value = f.id; }
    schedule();
  });
  id.addEventListener('input', () => { idTouched = true; f.id = id.value; schedule(); });
  const icon = h('input', { value: f.icon, placeholder: 'mdi:door' });
  icon.addEventListener('input', () => { f.icon = icon.value; });
  const party = h('input', { type: 'checkbox', checked: f.party });
  party.addEventListener('change', () => { f.party = party.checked; });

  // Rules: the cards rebuild only when a rule is added or removed. A sort
  // moves the cards with their state. Each editor holds its own state;
  // draft() reads it.
  const rulesBox = h('div', { class: 'stack' });
  let editors = [];
  const current = () => editors.map((e) => buildRule(e.st));
  const sortRule = (from, to) => {
    editors = moveRule(editors, from, to, { box: rulesBox, slots, prefix: 'rules', title: (i) => `Rule ${i + 1}` });
    schedule();
  };
  function drawRules(rules) {
    slots.drop('rules[');
    editors = rules.map((r, i) => {
      const ed = ruleEditor(r, `rules[${i}]`, `Rule ${i + 1}`, {
        catalog, inv, slots, onChange: schedule,
        onRemove: () => { const next = current(); next.splice(editors.indexOf(ed), 1); drawRules(next); schedule(); },
        sort: { box: rulesBox, index: () => editors.indexOf(ed), count: () => editors.length, onMove: sortRule },
      });
      return ed;
    });
    fill(rulesBox, ...(editors.length
      ? editors.map((e) => e.el)
      : [h('p', { class: 'muted', text: 'No rules yet. The template grants nothing until you add one.' })]));
  }
  const draft = () => buildTemplate({ ...f, rules: editors.map((e) => e.st) });

  // Live preview: debounced, newest answer wins.
  let seq = 0;
  const preview = debounce(async () => {
    const my = ++seq;
    const t = draft();
    const probe = { ...t, id: probeID(), name: t.name || 'New template' };
    try {
      const res = await post(paths.preview, { template: probe });
      if (my !== seq) return;
      const byRule = new Map(res.rules.filter((r) => r.template === probe.id).map((r) => [r.rule, r.entities]));
      editors.forEach((e, i) => e.setResolved(byRule.get(i) ?? []));
      entities.set(res.grants, grantedEntities(res.grants));
      const probs = [...res.problems.filter((p) => !(t.id === '' && p.path === 'id'))];
      slots.show(probs, normalizeTemplatePath);
      fill(warnings, ...res.warnings.map((w) => h('li', { text: w.rule >= 0 ? `Rule ${w.rule + 1}: ${w.message}` : w.message })));
    } catch (e) {
      if (my !== seq) return;
      editors.forEach((ed) => ed.setResolved(null));
      slots.show([{ path: '', message: e instanceof APIError && e.status === 503 ? 'Home Assistant is not reachable, so the preview is off.' : `Preview failed: ${e.message}` }]);
    }
  }, 400);
  const schedule = () => preview();
  const warnings = h('ul', { class: 'warning-list small' });

  const save = h('button', { type: 'submit', class: 'primary', text: isNew ? 'Create' : 'Save' });
  const form = h('form', { class: 'editor', novalidate: true, onsubmit: (ev) => {
    ev.preventDefault();
    run(save, async () => {
      const t = draft();
      try {
        const saved = isNew ? await post(paths.templates, t) : await put(paths.template(t.id), t);
        toast(`Saved ${saved.name}`);
        location.hash = `#/templates/edit/${encodeURIComponent(saved.id)}`;
      } catch (e) {
        if (e instanceof APIError && e.problems.length) {
          slots.show(e.problems, normalizeTemplatePath);
          slots.top.scrollIntoView({ block: 'nearest' });
          throw new Error('Fix the marked fields.');
        }
        throw e;
      }
    });
  } },
  h('div', { class: 'toolbar' }, back('#/templates', 'Templates'), h('h1', { text: isNew ? 'New template' : f.name })),
  slots.top,
  h('div', { class: 'grid2' },
    h('div', {}, field('Name', name), slots.slot('name')),
    h('div', {}, field('ID', id, isNew ? 'Lower case, digits, _ and -. Fixed after create.' : 'Fixed.'), slots.slot('id'))),
  h('div', { class: 'grid2' }, field('Icon', icon, 'An MDI icon name. Display only.'),
    maEnabled() ? check('Music Assistant party', party, 'Shows the party link on the guest page.') : h('div')),
  h('fieldset', {}, h('legend', { text: 'Default access' }),
    h('p', { class: 'muted small', text: 'Applies to rules that have no access of their own.' }),
    accessEditor(f.access, 'access', slots, schedule),
    !localEnabled() && localOffNote()),
  h('h2', { text: 'Rules' }),
  rulesBox,
  h('div', {}, h('button', { type: 'button', text: 'Add rule', onclick: () => {
    drawRules([...current(), { select: {}, actions: [] }]);
    schedule();
  } })),
  warnings,
  h('fieldset', {}, h('legend', { text: 'Entities on the guest page' }),
    h('p', { class: 'muted small', text: 'Guests see Shown in this order. Drag by the handle, or use Alt+Up and Alt+Down. Until you move one, they go by name. Removing an entity takes its access away: it goes on the exclude list of the rules that pick it. The ✎ button sets the name and description guests see; it saves at once, for every template and link.' }),
    entities.el, slots.slot('entity_order')),
  h('div', { class: 'actions sticky' },
    save,
    !isNew && h('button', { type: 'button', class: 'danger', text: 'Delete', onclick: async (ev) => {
      const users = await linksUsing(tpl.id);
      const msg = users.length
        ? `Links that use it lose its rules: ${users.join(', ')}.`
        : 'No link uses it.';
      if (!(await ask(`Delete ${tpl.name}?`, msg, { ok: 'Delete', danger: true }))) return;
      run(ev.target, async () => {
        await del(paths.template(tpl.id));
        toast(`Deleted ${tpl.name}`);
        location.hash = '#/templates';
      });
    } })));
  drawRules(src.rules || []);
  fill(view, form);
  if (isNew && all.length === 0) name.focus();
  schedule();
  return null;
}

async function linksUsing(id) {
  try {
    const { links } = await get(paths.links);
    return links.filter((l) => (l.templates || []).includes(id)).map((l) => l.name);
  } catch {
    return [];
  }
}
