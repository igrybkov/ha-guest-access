// The admin's own name and description of an entity: loading them, and
// the inline form the Shown lists open with the edit button. A save
// writes at once and applies app-wide, apart from the template or link
// being edited.
import { h, fill, get, put, toast, field, APIError } from './dom.js';
import { paths, entityTextProblems, entityTextMap, ENTITY_NAME_MAX, ENTITY_DESCRIPTION_MAX } from './lib.js';

// loadEntityTexts fetches every entity's text as a Map by entity id. On
// failure it returns null, and the lists show HA names with no edit
// button.
export async function loadEntityTexts() {
  try {
    return entityTextMap(await get(paths.entityTexts));
  } catch {
    return null;
  }
}

let formSeq = 0;

// entityTextForm builds the inline form for one entity. opts:
//   - id, haName: the entity id and its HA name.
//   - text: the saved {name, description}, or undefined.
//   - onSaved(text): runs after a save with what the server stored. An
//     empty text means the entry is gone.
//   - close(): shuts the form.
// Enter in Name saves; Ctrl+Enter or Cmd+Enter saves from Description;
// Escape cancels. Save and Cancel are plain buttons, so the form never
// submits the template or link form around it.
export function entityTextForm({ id, haName, text, onSaved, close }) {
  const hintID = `et-hint-${++formSeq}`;
  const name = h('input', { value: text?.name || '', placeholder: haName || id, autocomplete: 'off', 'aria-describedby': hintID });
  const desc = h('textarea', { rows: 2, placeholder: 'Optional. A short line under the name.', 'aria-describedby': hintID });
  desc.value = text?.description || '';
  const nameErr = h('div', { class: 'error small', role: 'alert' });
  const descErr = h('div', { class: 'error small', role: 'alert' });
  const topErr = h('div', { class: 'error small', role: 'alert' });

  const show = (p) => {
    fill(nameErr, p.name || '');
    fill(descErr, p.description || '');
    name.setAttribute('aria-invalid', String(!!p.name));
    desc.setAttribute('aria-invalid', String(!!p.description));
    return !p.name && !p.description;
  };
  const check = () => show(entityTextProblems({ name: name.value, description: desc.value }));
  name.addEventListener('input', check);
  desc.addEventListener('input', check);

  const save = h('button', { type: 'button', class: 'primary', text: 'Save' });
  const cancel = h('button', { type: 'button', text: 'Cancel', onclick: () => close() });
  const doSave = async () => {
    fill(topErr);
    if (!check()) return;
    save.disabled = true;
    try {
      const saved = await put(paths.entityText(id), { name: name.value, description: desc.value });
      onSaved(saved || {});
      toast('Saved. Guests see it in every template and link.');
      close();
    } catch (e) {
      if (e instanceof APIError && e.problems.length) {
        show(Object.fromEntries(e.problems.map((p) => [p.path, p.message])));
      } else {
        fill(topErr, e.message);
      }
    } finally {
      save.disabled = false;
    }
  };
  save.addEventListener('click', doSave);

  const onKey = (ev) => {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      ev.stopPropagation();
      close();
    } else if (ev.key === 'Enter' && (ev.target === name || ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      doSave();
    }
  };
  const form = h('div', { class: 'dl-form', role: 'group', 'aria-label': `Name and description of ${haName || id}` },
    field('Name', name), nameErr,
    field('Description', desc), descErr,
    h('small', { id: hintID, class: 'muted', text: `Guests see these on the card, in every template and link. Empty uses the HA name. Up to ${ENTITY_NAME_MAX} and ${ENTITY_DESCRIPTION_MAX} bytes.` }),
    topErr,
    h('div', { class: 'actions' }, save, cancel));
  form.addEventListener('keydown', onKey);
  return form;
}
