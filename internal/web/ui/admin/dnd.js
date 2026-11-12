// Two lists with drag and drop: an ordered list ("chosen") and a list of
// the rest ("other"). The editors use it for the templates of a link and
// for the entities of a guest page group.
//
// Drag works with Pointer Events, so mouse and touch both work. Only the
// handle starts a drag, so a touch on the rest of the row still scrolls
// the page. The keyboard has the same moves: the handle is a button, and
// Alt+ArrowUp or Alt+ArrowDown moves the item in the chosen list. The +
// and − buttons move it between the lists.
//
// The list holds no state of its own. A move calls onMove; the caller
// changes its data and calls set() with the new lists.
import { h, fill } from './dom.js';

// Edge zone for auto-scroll, in px. The top one clears the sticky menu.
const EDGE_TOP = 72;
const EDGE_BOTTOM = 48;
// The pointer must move this far before a drag starts, so a tap on the
// handle only focuses it.
const START_PX = 4;

// dualList builds the control. opts:
//   - label: the name of the whole group, for screen readers.
//   - chosen: {title, empty, remove(name), edit}. remove gives the
//     aria-label of the − button. edit is optional: {label(name),
//     form(item, close)}. With it, each chosen row gets an edit button
//     that opens form's element under the row. label gives the button's
//     aria-label. form builds the element; close() shuts it. The open
//     form is kept across set(), so a redraw never loses what was typed.
//   - other: {title, empty, add(name)}. add gives the aria-label of the +
//     button.
//   - onMove(id, to, index): to is "chosen" or "other". index is the
//     place in the chosen list, counted without the item; undefined
//     means the end.
// Items are {id, name, sub, desc, locked, note}. desc is a muted line
// under the name. A locked item has no handle and no + button; note says
// why.
// Returns {el, set({chosen, other})}.
export function dualList(opts) {
  const lists = {
    chosen: { cfg: opts.chosen, items: [], body: h('ul', { class: 'dl-body', role: 'list' }), count: h('span', { class: 'dl-count muted' }) },
    other: { cfg: opts.other, items: [], body: h('ul', { class: 'dl-body', role: 'list' }), count: h('span', { class: 'dl-count muted' }) },
  };
  const status = h('div', { class: 'sr-only', 'aria-live': 'polite' });
  const section = (key) => {
    const l = lists[key];
    l.box = h('section', { class: `dl-list dl-${key}`, 'data-list': key, tabindex: -1, 'aria-label': l.cfg.title },
      h('div', { class: 'dl-head' }, h('span', { class: 'dl-title', text: l.cfg.title }), l.count),
      l.body);
    return l.box;
  };
  const el = h('div', { class: 'dl', role: 'group', 'aria-label': opts.label }, section('chosen'), section('other'), status);

  // focusNext names the control to focus after the next draw:
  // {id, role}. Without it, draw keeps the focus the page had.
  let focusNext = null;
  const announce = (msg) => { status.textContent = msg; };
  // editing is the open inline form: {id, el}, or null.
  let editing = null;
  const edit = opts.chosen.edit;
  const openEdit = (it) => {
    let form = null;
    const close = () => {
      if (editing?.el !== form) return;
      editing = null;
      focusNext = { id: it.id, role: 'edit' };
      draw();
    };
    form = edit.form(it, close);
    editing = { id: it.id, el: form };
    draw();
    form.querySelector('input, textarea, button')?.focus();
  };

  const move = (id, to, index, focus) => {
    focusNext = focus;
    opts.onMove(id, to, index);
  };

  function row(key, it, i) {
    const l = lists[key];
    const n = l.items.length;
    const handle = !it.locked && h('button', {
      type: 'button', class: 'dl-handle', 'data-role': 'handle',
      'aria-label': key === 'chosen' ? `Move ${it.name}, place ${i + 1} of ${n}` : `Move ${it.name}`,
      title: key === 'chosen' ? 'Drag to move. Alt+Up and Alt+Down move it too.' : 'Drag to move.',
    });
    const btn = key === 'chosen'
      ? h('button', { type: 'button', class: 'icon dl-btn', 'data-role': 'remove', text: '−', 'aria-label': l.cfg.remove(it.name),
        onclick: () => { announce(`${it.name} moved to ${lists.other.cfg.title}`); move(it.id, 'other', undefined, { id: it.id, role: 'add' }); } })
      : !it.locked && h('button', { type: 'button', class: 'icon dl-btn', 'data-role': 'add', text: '+', 'aria-label': l.cfg.add(it.name),
        onclick: () => { announce(`${it.name} moved to ${lists.chosen.cfg.title}`); move(it.id, 'chosen', undefined, { id: it.id, role: 'remove' }); } });
    const open = key === 'chosen' && editing?.id === it.id;
    const editBtn = key === 'chosen' && edit && h('button', {
      type: 'button', class: 'icon dl-btn dl-edit', 'data-role': 'edit', text: '✎',
      'aria-label': edit.label(it.name), 'aria-expanded': String(open), title: 'Edit the name and description guests see',
      onclick: () => {
        if (!open) return openEdit(it);
        editing = null;
        return draw();
      },
    });
    const li = h('li', { class: `dl-row${it.locked ? ' locked' : ''}${open ? ' editing' : ''}`, 'data-id': it.id, 'data-drag-row': '', tabindex: -1 },
      handle || h('span', { class: 'dl-nohandle' }),
      h('span', { class: 'dl-text' },
        h('span', { class: 'dl-name', text: it.name }),
        it.sub && h('code', { class: 'muted small', text: it.sub }),
        it.desc && h('span', { class: 'dl-desc small muted', text: it.desc }),
        it.note && h('span', { class: 'dl-note small muted', text: it.note })),
      editBtn,
      btn,
      open && editing.el);
    if (handle) dragFrom(handle, li, key, it, i);
    if (key === 'chosen') {
      li.addEventListener('keydown', (ev) => {
        if (!ev.altKey || (ev.key !== 'ArrowUp' && ev.key !== 'ArrowDown')) return;
        // Keys typed in the inline form belong to the form.
        if (editing?.el.contains(ev.target)) return;
        ev.preventDefault();
        const to = i + (ev.key === 'ArrowUp' ? -1 : 1);
        if (to < 0 || to >= n) return;
        announce(`${it.name}: place ${to + 1} of ${n}`);
        move(it.id, 'chosen', to, { id: it.id, role: ev.target.dataset?.role || 'handle' });
      });
    }
    return li;
  }

  function draw() {
    const active = el.contains(document.activeElement) ? document.activeElement : null;
    // A control in the open form keeps the focus: the form element is
    // reused, so the same control is still there after the redraw.
    const inForm = !focusNext && active && editing?.el.contains(active) ? active : null;
    const keep = focusNext || (!inForm && active && {
      id: active.closest('.dl-row')?.dataset.id, role: active.dataset.role || '', list: active.dataset.list || '',
    });
    focusNext = null;
    if (editing && !lists.chosen.items.some((it) => it.id === editing.id)) editing = null;
    for (const key of ['chosen', 'other']) {
      const l = lists[key];
      l.count.textContent = String(l.items.length);
      fill(l.body, ...(l.items.length
        ? l.items.map((it, i) => row(key, it, i))
        : [h('li', { class: 'dl-empty muted small', text: l.cfg.empty })]));
    }
    if (inForm?.isConnected) inForm.focus({ preventScroll: true });
    else if (keep) restoreFocus(keep);
  }

  // restoreFocus finds the control by dataset, not by a selector built
  // from the id, since entity ids hold dots.
  function restoreFocus({ id, role, list }) {
    if (id == null) {
      if (list && lists[list]) lists[list].box.focus();
      return;
    }
    const rowEl = [...el.querySelectorAll('.dl-row')].find((r) => r.dataset.id === id);
    if (!rowEl) {
      lists.chosen.box.focus();
      return;
    }
    const want = [...rowEl.querySelectorAll('button')];
    const target = want.find((b) => b.dataset.role === role) || want.find((b) => b.dataset.role === 'handle') || want[0] || rowEl;
    target.focus({ preventScroll: false });
  }

  function dragFrom(handle, li, from, it, fromIndex) {
    dragRow(handle, li, {
      from,
      targets: {
        chosen: { box: lists.chosen.box, body: lists.chosen.body, ordered: true },
        other: { box: lists.other.box, body: lists.other.body, ordered: false },
      },
      onDrop(to, index) {
        const focus = { id: it.id, role: 'handle' };
        if (to === 'chosen') {
          if (from === 'chosen' && index === fromIndex) return;
          announce(from === 'chosen' ? `${it.name}: place ${index + 1}` : `${it.name} moved to ${lists.chosen.cfg.title}`);
          move(it.id, 'chosen', index, focus);
        } else if (from === 'chosen') {
          announce(`${it.name} moved to ${lists.other.cfg.title}`);
          move(it.id, 'other', undefined, focus);
        }
      },
    });
  }

  return {
    el,
    set({ chosen, other }) {
      lists.chosen.items = chosen || [];
      lists.other.items = other || [];
      draw();
    },
  };
}

// ---- Drag ----

// dragRow lets handle drag row. opts:
//   - from: the key of the list the row is in.
//   - targets: {key: {box, body, ordered}}. box is the drop area, body
//     holds the rows. Rows are the children with a data-drag-row
//     attribute. In an ordered list the drop index follows the pointer.
//     An unordered list takes the row back where it was, or at the end.
//   - onDrop(key, index): runs on a drop. index counts the rows without
//     the dragged one; it is undefined for an unordered list.
// Escape, a pointercancel or a lost capture end the drag with no drop.
export function dragRow(handle, row, opts) {
  const { from, targets } = opts;
  const keys = Object.keys(targets);
  let d = null;

  const listAt = (x, y) => keys.find((key) => {
    const r = targets[key].box.getBoundingClientRect();
    return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
  });

  // place puts the placeholder where the row would land.
  const place = () => {
    const key = listAt(d.x, d.y) || d.over;
    if (key !== d.over) {
      targets[d.over].box.classList.remove('dl-over');
      targets[key].box.classList.add('dl-over');
      d.over = key;
    }
    const { body, ordered } = targets[key];
    if (!ordered) {
      if (key === from) row.after(d.ph);
      else body.append(d.ph);
      d.index = undefined;
      return;
    }
    const rows = [...body.children].filter((c) => c !== row && c.hasAttribute('data-drag-row'));
    let index = rows.length;
    for (let k = 0; k < rows.length; k++) {
      const r = rows[k].getBoundingClientRect();
      if (d.y < r.top + r.height / 2) { index = k; break; }
    }
    if (index < rows.length) rows[index].before(d.ph);
    else body.append(d.ph);
    d.index = index;
  };

  const follow = () => {
    d.ghost.style.transform = `translate(${d.x - d.startX}px, ${d.y - d.startY}px)`;
    place();
  };

  // scrollStep scrolls the page while the pointer is near the top or
  // bottom edge, a bit faster the closer it gets.
  const scrollStep = () => {
    if (!d) return;
    const vh = window.innerHeight;
    let dy = 0;
    if (d.y < EDGE_TOP) dy = -Math.ceil((EDGE_TOP - d.y) / 4);
    else if (d.y > vh - EDGE_BOTTOM) dy = Math.ceil((d.y - (vh - EDGE_BOTTOM)) / 4);
    if (dy) {
      window.scrollBy(0, dy);
      place();
    }
    d.raf = requestAnimationFrame(scrollStep);
  };

  const begin = () => {
    const r = row.getBoundingClientRect();
    d.ghost = row.cloneNode(true);
    d.ghost.classList.add('dl-ghost');
    d.ghost.removeAttribute('tabindex');
    d.ghost.removeAttribute('data-drag-row');
    d.ghost.setAttribute('aria-hidden', 'true');
    d.ghost.style.width = `${r.width}px`;
    d.ghost.style.left = `${r.left}px`;
    d.ghost.style.top = `${r.top}px`;
    d.ph = h(row.tagName === 'LI' ? 'li' : 'div', { class: 'dl-placeholder', 'aria-hidden': 'true' });
    // A tall row (a rule card) gets a shorter placeholder, so the page
    // does not jump.
    d.ph.style.height = `${Math.min(r.height, 120)}px`;
    row.after(d.ph);
    row.classList.add('dl-dragging');
    document.body.append(d.ghost);
    document.body.classList.add('dl-active');
    d.over = from;
    targets[from].box.classList.add('dl-over');
    d.started = true;
    d.raf = requestAnimationFrame(scrollStep);
  };

  const end = (commit) => {
    if (!d) return;
    const s = d;
    d = null;
    document.removeEventListener('keydown', onKey, true);
    if (handle.hasPointerCapture(s.pointerId)) handle.releasePointerCapture(s.pointerId);
    if (!s.started) return;
    cancelAnimationFrame(s.raf);
    s.ghost.remove();
    s.ph.remove();
    row.classList.remove('dl-dragging');
    document.body.classList.remove('dl-active');
    targets[s.over].box.classList.remove('dl-over');
    if (commit) opts.onDrop(s.over, s.index);
  };

  const onKey = (ev) => {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      end(false);
    }
  };

  handle.addEventListener('pointerdown', (ev) => {
    if (ev.button !== 0 || d) return;
    // No text selection and no native drag; focus the handle by hand.
    ev.preventDefault();
    handle.focus({ preventScroll: true });
    d = { pointerId: ev.pointerId, startX: ev.clientX, startY: ev.clientY, x: ev.clientX, y: ev.clientY, started: false };
    handle.setPointerCapture(ev.pointerId);
    document.addEventListener('keydown', onKey, true);
  });
  handle.addEventListener('pointermove', (ev) => {
    if (!d || ev.pointerId !== d.pointerId) return;
    d.x = ev.clientX;
    d.y = ev.clientY;
    if (!d.started) {
      if (Math.hypot(d.x - d.startX, d.y - d.startY) < START_PX) return;
      begin();
    }
    follow();
  });
  handle.addEventListener('pointerup', (ev) => { if (d && ev.pointerId === d.pointerId) end(true); });
  handle.addEventListener('pointercancel', () => end(false));
  handle.addEventListener('lostpointercapture', () => end(false));
}

// sortHandle makes the drag handle of one card in an ordered stack, such
// as a rule. box holds the cards; the card needs data-drag-row. index()
// gives the card's place now. onMove(from, to) runs on a drop or on
// Alt+ArrowUp or Alt+ArrowDown on the handle; to counts the cards
// without the moved one. count() gives the number of cards. label()
// names the handle; it runs on focus, since the card may not be in the
// list yet while it is built. first is the label until then.
export function sortHandle({ card, box, first, label, index, count, onMove }) {
  const handle = h('button', {
    type: 'button', class: 'dl-handle', 'data-role': 'handle', 'aria-label': first,
    title: 'Drag to move. Alt+Up and Alt+Down move it too.',
  });
  handle.addEventListener('focus', () => handle.setAttribute('aria-label', label()));
  handle.addEventListener('keydown', (ev) => {
    if (!ev.altKey || (ev.key !== 'ArrowUp' && ev.key !== 'ArrowDown')) return;
    ev.preventDefault();
    const i = index();
    const to = i + (ev.key === 'ArrowUp' ? -1 : 1);
    if (to < 0 || to >= count()) return;
    onMove(i, to);
    handle.focus();
    handle.setAttribute('aria-label', label());
  });
  dragRow(handle, card, {
    from: 'list',
    targets: { list: { box, body: box, ordered: true } },
    onDrop: (_key, to) => {
      const i = index();
      if (to !== i) onMove(i, to);
      handle.focus({ preventScroll: true });
    },
  });
  return handle;
}
