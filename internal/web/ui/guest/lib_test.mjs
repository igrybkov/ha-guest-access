import { test } from 'node:test';
import assert from 'node:assert/strict';
import { linkBase, actionLabel, cards } from './lib.js';

test('linkBase', () => {
  assert.equal(linkBase('/g/abc_DEF-123'), '/g/abc_DEF-123');
  assert.equal(linkBase('/g/abc/'), '/g/abc');
  assert.equal(linkBase('/g/abc/state'), '');
  assert.equal(linkBase('/x/abc'), '');
  assert.equal(linkBase('/g/a.b'), '');
});

test('actionLabel', () => {
  assert.equal(actionLabel('light.turn_on'), 'Turn on');
  assert.equal(actionLabel('media_player.media_play_pause'), 'Play pause');
  assert.equal(actionLabel('alarm_control_panel.alarm_disarm'), 'Disarm');
  assert.equal(actionLabel('odd'), 'odd');
});

test('cards keeps plain actions in group order', () => {
  const view = {
    groups: [{ name: 'Entry', entities: ['lock.front', 'light.hall'] }, { name: '', entities: ['switch.fan'] }],
    entities: {
      'lock.front': { name: 'Front door', actions: [{ id: 'lock.unlock' }] },
      'light.hall': { name: 'Hall', actions: [{ id: 'light.turn_on', fields: { brightness_pct: {} } }] },
      'switch.fan': { actions: [{ id: 'switch.turn_on', fields: {} }] },
    },
    states: { 'lock.front': { state: 'locked' } },
  };
  assert.deepEqual(cards(view), [
    { name: 'Entry', entities: [{ id: 'lock.front', name: 'Front door', state: 'locked', actions: [{ id: 'lock.unlock' }] }] },
    { name: 'More', entities: [{ id: 'switch.fan', name: 'switch.fan', state: '', actions: [{ id: 'switch.turn_on', fields: {} }] }] },
  ]);
  assert.deepEqual(cards({}), []);
});
