import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier.endsWith('/Wire') || specifier.endsWith('/Wire.ts')) {
      return {url: new URL('../entry/src/main/ets/core/rustcore/Wire.ts', import.meta.url).href, shortCircuit: true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('/rustcore/Wire.ts')) {
      return {format: 'module', shortCircuit: true, source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'))};
    }
    return next(url, context);
  },
});

const wire = await import('../entry/src/main/ets/core/rustcore/Wire.ts');

function snapshot() {
  return {items_json: '[{"id":"1#0"}]', history: '历史🌍', covered: -7, has_more: false, loaded_once: true};
}

test('generated ArkTS config has the exact empty-struct bincode representation', () => {
  assert.equal(wire.encodeConfig({}).length, 0);
  assert.deepEqual(wire.decodeConfig(new Uint8Array()), {});
  assert.throws(() => wire.decodeConfig(Uint8Array.of(0)));
});

test('generated ArkTS round-trips all event, action, patch and effect shapes', () => {
  const batch = {items_json: '[]', history: null, from_seq: -9, to_seq: 0, backlog: true, has_more: null, more_after: true};
  const values = [
    ['ChatSnapshot', snapshot()],
    ['WireBatch', batch],
    ['ChatEvent', {kind: 'latest', batch}],
    ['ChatEvent', {kind: 'reloaded', items_json: '[]', history: 'h', to_seq: -1, has_more: false}],
    ['ChatEvent', {kind: 'fill_failed'}],
    ['ChatEvent', {kind: 'range', fold_id: null, items_json: '[]', more: true}],
    ['Action', {kind: 'open_chat', chat: 'fixture', snapshot: snapshot()}],
    ['Action', {kind: 'continue_fill', chat: 'fixture'}],
    ['ChatPatch', {version: 4294967297, covered: -9, history: null, has_more: null, loaded_once: false, verified: true, acked: 3,
      ops: [{kind: 'remove', index: 1}, {kind: 'insert', index: 0, id: '1#0', json: '{}'}, {kind: 'update', index: 0, id: '1#0', json: '{"new":true}'}]}],
    ['Effect', {kind: 'fetch', chat: 'fixture', request: {kind: 'fetch_range', fold_id: null, from_seq: 2, through_seq: 8}}],
    ['Effect', {kind: 'fetch', chat: 'fixture', request: {kind: 'subscribe', since_seq: null, fold_work: true}}],
    ['Effect', {kind: 'rejected', reason: 'synthetic malformed fixture'}],
    ['ListOp', {kind: 'update', index: 0, id: '1#0', json: '{}'}],
  ];
  for (const [type, value] of values) {
    assert.deepEqual(wire['decode' + type](wire['encode' + type](value)), value, type);
  }
});

test('generated ArkTS rejects malformed lengths and enum discriminants', () => {
  assert.throws(() => wire.decodeChatEvent(Uint8Array.of(99, 0, 0, 0)));
  assert.throws(() => wire.decodeWireBatch(wire.encodeWireBatch({items_json: '[]', history: null, from_seq: null, to_seq: null, backlog: false, has_more: null, more_after: false}).slice(0, 1)));
  const malformed = wire.encodeChatSnapshot({items_json: 'x', history: null, covered: 0, has_more: null, loaded_once: false});
  malformed[7] = 2;
  assert.throws(() => wire.decodeChatSnapshot(malformed));
});
