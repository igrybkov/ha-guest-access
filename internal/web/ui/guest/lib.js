// Pure helpers for the guest page. No DOM here, so node --test can run them.

// linkBase returns the link's API base from the page path: "/g/abc" and
// "/g/abc/" both give "/g/abc". Anything else gives "".
export function linkBase(pathname) {
  const m = /^(\/g\/[A-Za-z0-9_-]+)\/?$/.exec(pathname);
  return m ? m[1] : '';
}

// actionLabel turns "light.turn_on" into "Turn on".
export function actionLabel(id) {
  const name = String(id).split('.').slice(1).join('.')
    .replace(/^media_/, '').replace(/^alarm_/, '').replace(/_/g, ' ');
  return name ? name[0].toUpperCase() + name.slice(1) : String(id);
}

// cards returns the groups of a /state reply as
// [{name, entities: [{id, name, state, actions}]}], in reply order. Actions
// with data fields are left out: this page only shows plain buttons.
export function cards(view) {
  const out = [];
  for (const g of view.groups || []) {
    const entities = [];
    for (const id of g.entities || []) {
      const e = (view.entities || {})[id];
      if (!e) continue;
      const actions = (e.actions || []).filter((a) => !a.fields || Object.keys(a.fields).length === 0);
      if (actions.length === 0) continue;
      const st = (view.states || {})[id];
      entities.push({ id, name: e.name || id, state: st ? st.state : '', actions });
    }
    if (entities.length > 0) out.push({ name: g.name || 'More', entities });
  }
  return out;
}
