// Links screen: the list and the editor. After a save the editor also
// shows the URL and QR code, passwords, devices and recent activity.
import {
  h, fill, get, post, put, del, toast, run, ask, field, check, back, debounce, table, copyText, settings, APIError,
} from './dom.js';
import {
  paths, PRESETS, preset, toLocalInput, fromLocalInput, fmtTime, fmtWindow, buildLink, linkForm,
  approvalMode, splitTemplates, linkBadges, rotateWarning, STATUS_LABELS, grantSummary, auditQuery, auditWho, auditWhat, auditResult, auditOffText,
} from './lib.js';
import { Slots, ruleEditor, loadInventory } from './rules.js';

export async function render(view, rest) {
  if (rest[0] === 'new') return editor(view, null);
  if (rest[0] === 'edit' && rest[1]) return editor(view, rest[1]);
  return list(view);
}

// ---- Shared actions ----

async function revoke(l, btn, after) {
  if (!(await ask(`Revoke ${l.name}?`, 'The link stops working at once. This cannot be undone.', { ok: 'Revoke', danger: true }))) return;
  await run(btn, async () => {
    await post(paths.revoke(l.id), {});
    toast(`Revoked ${l.name}`);
    after();
  });
}

async function rotate(l, btn, after) {
  const warn = rotateWarning(l);
  const msg = `The old URL stops working at once. Send the new one to your guests.${warn ? ` ${warn}` : ''}`;
  if (!(await ask(`New URL for ${l.name}?`, msg, { ok: 'Make new URL', danger: true }))) return;
  await run(btn, async () => {
    await post(paths.rotate(l.id), {});
    toast('New URL made');
    after();
  });
}

async function remove(l, btn, after) {
  if (!(await ask(`Delete ${l.name}?`, 'The link stops working and its history is gone. This cannot be undone.', { ok: 'Delete', danger: true }))) return;
  await run(btn, async () => {
    await del(paths.link(l.id));
    toast(`Deleted ${l.name}`);
    after();
  });
}

// linkActions are the buttons for one link. afterDelete defaults to after.
function linkActions(l, after, { open = true, afterDelete = after } = {}) {
  const live = l.status !== 'revoked';
  return h('div', { class: 'actions' },
    open && h('a', { class: 'button', href: `#/links/edit/${encodeURIComponent(l.id)}`, text: 'Open' }),
    live && h('button', { type: 'button', text: 'New URL', onclick: (e) => rotate(l, e.currentTarget, after) }),
    live && h('button', { type: 'button', class: 'danger', text: 'Revoke', onclick: (e) => revoke(l, e.currentTarget, after) }),
    h('button', { type: 'button', class: 'danger', text: 'Delete', onclick: (e) => remove(l, e.currentTarget, afterDelete) }));
}

// ---- List ----

async function list(view) {
  const [{ links }, tpls] = await Promise.all([get(paths.links), get(paths.templates).catch(() => ({ templates: [] }))]);
  const names = new Map((tpls.templates || []).map((t) => [t.id, t.name]));
  const reload = () => list(view);
  const rows = links.map((l) => h('tr', {},
    h('td', {}, h('span', { class: `status ${l.status}`, text: STATUS_LABELS[l.status] || l.status })),
    h('td', {},
      h('a', { class: 'title', href: `#/links/edit/${encodeURIComponent(l.id)}`, text: l.name }),
      h('div', {}, linkBadges(l).map((b) => h('span', { class: 'badge', text: b }))),
      (l.warnings || []).length > 0 && h('div', { class: 'error small', text: `${l.warnings.length} warning${l.warnings.length > 1 ? 's' : ''}` })),
    h('td', { class: 'small', text: fmtWindow(l) }),
    h('td', { class: 'small' },
      (l.templates || []).map((id) => names.get(id) || id).join(', '),
      (l.extra || []).length > 0 && h('div', { class: 'muted', text: `+ ${l.extra.length} extra rule${l.extra.length > 1 ? 's' : ''}` })),
    h('td', { class: 'num', text: String(l.use_count || 0) }),
    h('td', { class: 'small', text: l.last_used_at ? fmtTime(l.last_used_at) : 'Never' }),
    h('td', {}, linkActions(l, reload, { open: false }))));
  fill(view, 
    h('div', { class: 'toolbar' }, h('h1', { text: 'Links' }),
      h('a', { class: 'button primary', href: '#/links/new', text: 'New link' })),
    links.length
      ? table(['Status', 'Name', 'Window', 'Templates', 'Uses', 'Last use', ''], rows)
      : h('p', { class: 'muted', text: 'No links yet. A link gives a guest a page with the controls you pick.' }));
}

// ---- Editor ----

async function editor(view, id) {
  const isNew = !id;
  const [linkView, tpls, catalog, inv] = await Promise.all([
    isNew ? null : get(paths.link(id)),
    get(paths.templates),
    get(paths.catalog),
    loadInventory(),
  ]);
  const templates = tpls.templates || [];
  const f = linkForm(linkView || {});
  // A template the link names but that no longer exists is dropped from
  // the form, or every save would fail.
  const { keep, missing: missingTpls } = splitTemplates(f.templates, templates);
  f.templates = keep;
  const slots = new Slots();
  const reload = () => editor(view, id);

  // Name and note.
  const name = h('input', { value: f.name, required: true });
  name.addEventListener('input', () => { f.name = name.value; schedule(); });
  const note = h('textarea', { rows: 2, placeholder: 'Only you see this.' });
  note.value = f.note;
  note.addEventListener('input', () => { f.note = note.value; });

  // Window with presets.
  const from = h('input', { type: 'datetime-local', value: f.valid_from ? toLocalInput(new Date(f.valid_from)) : '' });
  const until = h('input', { type: 'datetime-local', value: f.valid_until ? toLocalInput(new Date(f.valid_until)) : '' });
  const presetBtns = h('div', { class: 'chips', role: 'group', 'aria-label': 'Window presets' });
  let picked = 'custom';
  const markPreset = () => {
    for (const b of presetBtns.children) b.setAttribute('aria-pressed', String(b.dataset.preset === picked));
  };
  const applyPreset = (p) => {
    picked = p;
    const w = preset(p, new Date());
    if (w) {
      f.valid_from = w.from ? w.from.toISOString() : null;
      f.valid_until = w.until.toISOString();
      from.value = w.from ? toLocalInput(w.from) : '';
      until.value = toLocalInput(w.until);
      schedule();
    }
    markPreset();
  };
  presetBtns.append(...PRESETS.map(([p, label]) => h('button', { type: 'button', 'data-preset': p, text: label, onclick: () => applyPreset(p) })));
  const readWindow = () => {
    picked = 'custom';
    markPreset();
    const a = fromLocalInput(from.value);
    const b = fromLocalInput(until.value);
    f.valid_from = a ? a.toISOString() : null;
    f.valid_until = b ? b.toISOString() : null;
    schedule();
  };
  from.addEventListener('change', readWindow);
  until.addEventListener('change', readWindow);

  // Templates to tick.
  const tplBox = h('div', { class: 'stack tight' }, templates.length
    ? templates.map((t) => {
      const cb = h('input', { type: 'checkbox', checked: f.templates.includes(t.id) });
      cb.addEventListener('change', () => {
        if (cb.checked) f.templates.push(t.id);
        else f.templates.splice(f.templates.indexOf(t.id), 1);
        schedule();
      });
      return h('label', { class: 'check' }, cb, h('span', { text: t.name }), h('code', { class: 'muted small', text: t.id }));
    })
    : h('p', { class: 'muted' }, 'No templates yet. ', h('a', { href: '#/templates/new', text: 'Make one' }), ' or add a rule below.'));
  for (const tid of missingTpls) {
    tplBox.append(h('p', { class: 'warning small', text: `Template "${tid}" no longer exists. It is left out when you save.` }));
  }

  // Extra rules.
  const rulesBox = h('div', { class: 'stack' });
  let editors = [];
  const current = () => editors.map((e) => e.st);
  function drawRules(rules) {
    slots.drop('extra[');
    editors = rules.map((r, i) => ruleEditor(r, `extra[${i}]`, `Extra rule ${i + 1}`, {
      catalog, inv, slots, forceAccess: true, onChange: schedule,
      onRemove: () => { const next = rulesFromState(); next.splice(i, 1); drawRules(next); schedule(); },
    }));
    fill(rulesBox, ...editors.map((e) => e.el));
  }
  const rulesFromState = () => buildLink({ ...f, extra: current() }).extra;

  // Options.
  const oneTime = h('input', { type: 'checkbox', checked: f.one_time });
  oneTime.addEventListener('change', () => { f.one_time = oneTime.checked; });
  const customSlug = h('input', { type: 'checkbox' });
  const minSlug = settings().tunables?.custom_slug_min_len;
  const slug = h('input', { placeholder: minSlug ? `at least ${minSlug} of a-z, 0-9 and -` : 'a-z, 0-9 and -', hidden: true, autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Custom slug' });
  const slugWarn = h('p', { class: 'warning small', hidden: true, text: 'A custom slug is easier to guess than a random one. Use a long one, and add a password.' });
  customSlug.addEventListener('change', () => {
    f.customSlug = customSlug.checked;
    slug.hidden = slugWarn.hidden = !customSlug.checked;
  });
  slug.addEventListener('input', () => { f.slug = slug.value; });
  const manual = h('input', { type: 'checkbox', checked: f.approval.manual });
  const self = h('input', { type: 'checkbox', checked: f.approval.self_service });
  const claimNote = h('p', { class: 'note small' });
  const drawClaim = () => {
    const mode = approvalMode(f.approval);
    claimNote.hidden = mode === 'off';
    claimNote.textContent = {
      manual: 'Each new device waits until you approve it here or from the HA notification.',
      claim: 'Claim mode: the first device to open the link gets in. Later devices need that device to approve them.',
      both: 'A new device waits until you, or a device already approved, lets it in.',
    }[mode] || '';
  };
  manual.addEventListener('change', () => { f.approval.manual = manual.checked; drawClaim(); });
  self.addEventListener('change', () => { f.approval.self_service = self.checked; drawClaim(); });
  drawClaim();

  // Live grants preview.
  const grantsBox = h('div', { class: 'grants' }, h('p', { class: 'muted', text: 'Loading…' }));
  let seq = 0;
  const preview = debounce(async () => {
    const my = ++seq;
    const draft = buildLink({ ...f, extra: current() });
    try {
      const res = await post(paths.preview, { link: draft });
      if (my !== seq) return;
      const byRule = new Map(res.rules.filter((r) => !r.template).map((r) => [r.rule, r.entities]));
      editors.forEach((e, i) => e.setResolved(byRule.get(i) ?? []));
      const groups = grantSummary(res.grants);
      fill(grantsBox, 
        groups.length
          ? h('ul', { class: 'plain-list' }, groups.map((g) => h('li', {},
            h('strong', { text: g.name }), ' ', h('code', { class: 'muted small', text: g.entity_id }),
            h('div', { class: 'small', text: g.actions.join(', ') }))))
          : h('p', { class: 'muted', text: 'Nothing yet. Tick a template or add a rule.' }),
        res.warnings.length > 0 && h('ul', { class: 'warning small' }, res.warnings.map((w) => h('li', { text: w.template ? `${w.template}: ${w.message}` : w.message }))));
      slots.show(res.problems);
    } catch (e) {
      if (my !== seq) return;
      editors.forEach((ed) => ed.setResolved(null));
      fill(grantsBox, h('p', { class: 'error', text: e instanceof APIError && e.status === 503 ? 'Home Assistant is not reachable, so the preview is off.' : `Preview failed: ${e.message}` }));
    }
  }, 400);
  const schedule = () => preview();

  const save = h('button', { type: 'submit', class: 'primary', text: isNew ? 'Create link' : 'Save' });
  const form = h('form', { class: 'editor', novalidate: true, onsubmit: (ev) => {
    ev.preventDefault();
    run(save, async () => {
      const body = buildLink({ ...f, extra: current() }, { create: isNew });
      if (!body.valid_until) {
        slots.show([{ path: 'valid_until', message: 'Pick when the link ends.' }]);
        throw new Error('Pick when the link ends.');
      }
      try {
        const saved = isNew ? await post(paths.links, body) : await put(paths.link(id), body);
        toast(`Saved ${saved.name}`);
        if (isNew) location.hash = `#/links/edit/${encodeURIComponent(saved.id)}`;
        else reload();
      } catch (e) {
        if (e instanceof APIError && e.problems.length) {
          slots.show(e.problems);
          throw new Error('Fix the marked fields.');
        }
        throw e;
      }
    });
  } },
  h('div', { class: 'toolbar' }, back('#/links', 'Links'),
    h('h1', { text: isNew ? 'New link' : f.name }),
    linkView && h('span', { class: `status ${linkView.status}`, text: STATUS_LABELS[linkView.status] || linkView.status })),
  linkView && (linkView.warnings || []).length > 0 && h('ul', { class: 'warning' }, linkView.warnings.map((w) => h('li', { text: w.message }))),
  slots.top,
  h('div', { class: 'cols' },
    h('div', { class: 'stack' },
      h('div', {}, field('Name', name, 'Guests see it on their page.'), slots.slot('name')),
      h('div', {}, field('Note', note), slots.slot('note')),
      h('fieldset', {}, h('legend', { text: 'When it works' }),
        presetBtns,
        h('div', { class: 'grid2' },
          h('div', {}, field('From', from, 'Empty means now.'), slots.slot('valid_from')),
          h('div', {}, field('Until', until), slots.slot('valid_until')))),
      h('fieldset', {}, h('legend', { text: 'Templates' }), tplBox, slots.slot('templates')),
      h('h2', { text: 'Extra rules' }),
      h('p', { class: 'muted small', text: 'Rules for this link only. Each has its own access.' }),
      rulesBox,
      h('div', {}, h('button', { type: 'button', text: 'Add rule', onclick: () => {
        drawRules([...rulesFromState(), { select: {}, actions: [] }]);
        schedule();
      } })),
      h('fieldset', {}, h('legend', { text: 'Options' }),
        check('One-time', oneTime, 'Each action on each entity works once.'),
        isNew && check('Custom slug', customSlug, 'Random by default.'),
        isNew && slug, isNew && slugWarn, slots.slot('slug'),
        check('Approve devices myself', manual),
        check('Approved devices can let others in', self),
        claimNote, slots.slot('approval'))),
    h('aside', { class: 'stack' },
      h('section', { class: 'card' }, h('h2', { text: 'This link can…' }), grantsBox))),
  h('div', { class: 'actions sticky' }, save));

  const parts = [form];
  if (linkView) {
    parts.push(
      shareSection(linkView),
      passwordsSection(linkView, reload),
      devicesSection(linkView, reload),
      activitySection(linkView),
      h('section', { class: 'card' }, h('h2', { text: 'Link actions' }),
        linkActions(linkView, reload, { open: false, afterDelete: () => { location.hash = '#/links'; } })));
  }
  drawRules(linkView?.extra || []);
  if (isNew) applyPreset('week');
  else markPreset();
  fill(view, ...parts);
  schedule();
  return null;
}

// ---- After save ----

function shareSection(l) {
  const url = l.url || l.path;
  const input = h('input', { readonly: true, value: url, 'aria-label': 'Guest URL', class: 'grow' });
  input.addEventListener('focus', () => input.select());
  return h('section', { class: 'card share' }, h('h2', { text: 'Share' }),
    h('div', { class: 'inline' }, input,
      h('button', { type: 'button', text: 'Copy', onclick: () => copyText(url, input) })),
    !l.url && h('p', { class: 'warning small', text: 'This is only the path. Set public_base_url in Settings → Apps → Guest Access → Configuration to get a full URL and a QR code.' }),
    l.url && h('img', { class: 'qr', src: `${paths.qrSVG(l.id)}?v=${Date.now()}`, alt: 'QR code of the guest URL', width: 200, height: 200 }));
}

function passwordsSection(l, reload) {
  const label = h('input', { placeholder: 'Guest name', autocomplete: 'off', 'aria-label': 'Label' });
  const pw = h('input', { type: 'password', autocomplete: 'new-password', 'aria-label': 'Password' });
  const add = h('button', { type: 'submit', text: 'Add password' });
  const form = h('form', { class: 'grid3', onsubmit: (ev) => {
    ev.preventDefault();
    const body = { label: label.value, password: pw.value };
    pw.value = '';
    run(add, async () => {
      await post(paths.passwords(l.id), body);
      toast('Password added');
      reload();
    });
  } }, field('Label', label), field('Password', pw), h('div', {}, add));
  const pws = l.passwords || [];
  return h('section', { class: 'card' }, h('h2', { text: 'Passwords' }),
    h('p', { class: 'muted small', text: 'With a password, guests must enter it first. The exact text counts: spaces and case too. You can\'t see a password again after you add it.' }),
    pws.length
      ? h('ul', { class: 'list plain' }, pws.map((p) => h('li', { class: 'row' },
        h('span', { class: 'grow' }, h('span', { text: p.label }), ' ', h('span', { class: 'muted small', text: `added ${fmtTime(p.created_at)}` })),
        h('button', { type: 'button', class: 'danger', text: 'Remove', onclick: async (ev) => {
          const btn = ev.currentTarget;
          if (!(await ask(`Remove the password for ${p.label}?`, 'Guests signed in with it lose access at once.', { ok: 'Remove', danger: true }))) return;
          run(btn, async () => { await del(paths.password(l.id, p.id)); toast('Password removed'); reload(); });
        } }))))
      : h('p', { class: 'muted', text: 'No passwords. The URL alone opens the link.' }),
    form);
}

function devicesSection(l, reload) {
  const devices = l.devices || [];
  const mode = approvalMode(l.approval);
  if (mode === 'off' && !devices.length && !(l.pending || []).length) return null;
  const pendingBox = h('div', {});
  drawPending(l.pending || [], pendingBox, reload);
  const refresh = h('button', { type: 'button', text: 'Refresh', onclick: (ev) => run(ev.currentTarget, async () => {
    const res = await get(paths.pending(l.id));
    drawPending(res.requests || [], pendingBox, reload);
  }) });
  return h('section', { class: 'card' }, h('h2', { text: 'Devices' }),
    mode === 'off' && h('p', { class: 'muted small', text: 'Device approval is off, so these devices no longer matter.' }),
    h('div', { class: 'toolbar' }, h('h3', { class: 'grow', text: 'Waiting for approval' }), refresh), pendingBox,
    h('h3', { text: 'Approved' }),
    devices.length
      ? table(['Device', 'Approved', 'By', 'Last seen', ''], devices.map((d) => h('tr', {},
        h('td', {}, h('div', { text: d.label || d.device || d.id }), d.label && h('div', { class: 'muted small', text: d.device })),
        h('td', { class: 'small', text: fmtTime(d.approved_at) }),
        h('td', { class: 'small', text: d.approved_by || '' }),
        h('td', { class: 'small', text: d.last_seen_at ? fmtTime(d.last_seen_at) : 'Never' }),
        h('td', {}, h('button', { type: 'button', class: 'danger', text: 'Revoke', onclick: async (ev) => {
          const btn = ev.currentTarget;
          if (!(await ask('Revoke this device?', 'It loses access at once.', { ok: 'Revoke', danger: true }))) return;
          run(btn, async () => { await del(paths.linkDevice(l.id, d.id)); toast('Device revoked'); reload(); });
        } })))))
      : h('p', { class: 'muted', text: 'No approved devices.' }),
    h('div', { class: 'actions' }, h('button', { type: 'button', class: 'danger', text: 'Reset devices', onclick: async (ev) => {
      const btn = ev.currentTarget;
      const msg = mode === 'claim'
        ? 'All devices and requests are removed. The next device to open the link claims it.'
        : 'All devices and requests are removed. Every device must be approved again.';
      if (!(await ask('Reset devices?', msg, { ok: 'Reset', danger: true }))) return;
      run(btn, async () => { await post(paths.resetDevices(l.id), {}); toast('Devices reset'); reload(); });
    } })));
}

// drawPending lists the link's pending device requests with approve and
// deny buttons.
function drawPending(reqs, box, reload) {
  if (!reqs.length) {
    fill(box, h('p', { class: 'muted', text: 'No requests.' }));
    return;
  }
  const decide = (r, approve) => (ev) => {
    run(ev.currentTarget, async () => {
      await post(paths.decide(r.request_id), { approve });
      toast(approve ? 'Approved' : 'Denied');
      reload();
    });
  };
  fill(box, table(['Code', 'Guest', 'Device', 'Asked', ''], reqs.map((r) => h('tr', {},
    h('td', {}, h('code', { class: 'code', text: r.code })),
    h('td', { text: r.label || '' }),
    h('td', { class: 'small', text: r.device || '' }),
    h('td', { class: 'small', text: fmtTime(r.created_at) }),
    h('td', {}, h('div', { class: 'actions' },
      h('button', { type: 'button', class: 'primary', text: 'Approve', onclick: decide(r, true) }),
      h('button', { type: 'button', class: 'danger', text: 'Deny', onclick: decide(r, false) })))))));
}

function activitySection(l) {
  const box = h('div', {}, h('p', { class: 'muted small', text: 'Loading…' }));
  get(paths.audit(auditQuery({ link: l.id, limit: 10 }))).then((res) => {
    if (!res.file) {
      fill(box, h('p', { class: 'muted small', text: auditOffText }));
      return;
    }
    const recs = res.records || [];
    fill(box, recs.length
      ? h('ul', { class: 'plain-list small' }, recs.map((r) => h('li', {},
        h('span', { class: 'muted', text: fmtTime(r.time) }), ' ',
        h('code', { text: r.event }), ' ',
        h('span', { text: [auditWho(r), auditWhat(r), auditResult(r)].filter(Boolean).join(' · ') }))))
      : h('p', { class: 'muted', text: 'No activity yet.' }));
  }).catch((e) => {
    fill(box, h('p', { class: 'muted small', text: `Could not load activity: ${e.message}` }));
  });
  return h('section', { class: 'card' },
    h('div', { class: 'toolbar' }, h('h2', { class: 'grow', text: 'Recent activity' }),
      h('a', { href: `#/audit?link=${encodeURIComponent(l.id)}`, text: 'All activity' })),
    box);
}
