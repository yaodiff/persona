import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

const state = {effects: [], patch: null, flushes: 0, drains: 0};

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@ohos/hypium') return {url: 'fake-hypium:', shortCircuit: true};
    if (specifier === '@kit.ArkTS') return {url: 'fake-arkts:', shortCircuit: true};
    if (specifier === 'libpersona_core_ohos.so') return {url: 'fake-persona-core:', shortCircuit: true};
    if (specifier.endsWith('/Wire')) {
      return {url: new URL('../entry/src/main/ets/core/rustcore/Wire.ts', import.meta.url).href, shortCircuit: true};
    }
    if (specifier.endsWith('/NativeContext')) return {url: 'fake-native-context:', shortCircuit: true};
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'fake-hypium:') {
      return {format: 'module', shortCircuit: true, source: `
        export const describe = (_name, body) => body();
        export const it = (_name, _options, _body) => {};
        export const expect = (value) => ({
          assertTrue() { if (value !== true) throw new Error('expected true'); },
          assertEqual(expected) { if (!Object.is(value, expected)) throw new Error('values differ'); },
        });
      `};
    }
    if (url === 'fake-arkts:') {
      return {format: 'module', shortCircuit: true, source: 'export const util = {TextDecoder: {create() { return new TextDecoder(); }}};'};
    }
    if (url === 'fake-native-context:') {
      return {format: 'module', shortCircuit: true, source: 'export function nativeTestContext() { throw new Error("no host resource context"); }'};
    }
    if (url === 'fake-persona-core:') {
      return {format: 'module', shortCircuit: true, source: `
        export default {
          coreConfig() { return new Uint8Array(); },
          coreCreate(config, changed, effects) { globalThis.nativeCoreCallbacks = {changed, effects}; return 1; },
          coreCreateWithChat(config, changed, changedChat, effects) {
            globalThis.nativeCoreCallbacks = {changed, changedChat, effects};
            return 2;
          },
          coreFlush() { globalThis.nativeCoreState.flushes++; return true; },
          coreDrain() {
            globalThis.nativeCoreState.drains++;
            globalThis.nativeCoreState.effectsAtDrain ??= [];
            globalThis.nativeCoreState.effectsAtDrain.push(globalThis.nativeCoreState.effects.length);
            return globalThis.nativeCoreState.patch;
          },
        };
      `};
    }
    if (url.endsWith('.ets') || url.endsWith('.ts')) {
      return {format: 'module', shortCircuit: true, source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'))};
    }
    return next(url, context);
  },
});

globalThis.nativeCoreState = state;
const wire = await import('../entry/src/main/ets/core/rustcore/Wire.ts');
const { awaitDeliveredEffects, drainCore } = await import('../entry/src/ohosTest/ets/test/NativeCore.test.ets');

function fetchRequest(chat = 'fixture') {
  return {kind: 'fetch_after', after_seq: 9};
}

function fetchEffect(chat = 'fixture') {
  return {kind: 'fetch', chat, request: fetchRequest(chat)};
}

function patch(version = 1) {
  state.patch = wire.encodeChatPatch({version, covered: 1, history: null, has_more: null,
    loaded_once: true, verified: false, acked: null,
    ops: [{kind: 'insert', index: 0, id: '1#0', json: '{"id":"1#0"}'}]});
}

test('effect delivery waits for a queued callback, not a synchronous effect array', {timeout: 1000}, async () => {
  const effects = [];
  let completed = false;
  const pending = awaitDeliveredEffects(effects, [fetchRequest()], 'delayed effect', 200);
  pending.then(() => { completed = true; });
  setTimeout(() => { effects.push(fetchEffect()); }, 15);
  assert.equal(completed, false, 'the wait must yield before its predicate is satisfied');
  await pending;
  assert.deepEqual(effects, [fetchEffect()]);
});

test('zero-effect steps yield to the event loop and reject stale callbacks', async () => {
  const effects = [];
  let delivered = false;
  setTimeout(() => { delivered = true; }, 0);
  await awaitDeliveredEffects(effects, [], 'no effects', 25);
  assert.equal(delivered, true, 'a zero-effect step must still yield before clearing the shared array');
  await assert.rejects(awaitDeliveredEffects([fetchEffect()], [], 'stale effect', 25), /expected \[\], received|unexpected extra effects/);
});

test('a late effect after a no-patch action is still part of that step', {timeout: 1000}, async () => {
  patch(0);
  const effects = state.effects = [];
  const list = [];
  const pending = drainCore(1, 'fixture', list, effects, [fetchRequest()], 'no-patch effect');
  let completed = false;
  pending.then(() => { completed = true; });
  setTimeout(() => { effects.push(fetchEffect()); }, 15);
  const step = await pending;
  assert.equal(step.patch.version, 0);
  assert.deepEqual(step.effects, [fetchEffect()]);
  assert.equal(completed, true);
});

test('missing effects fail within the supplied bound instead of passing an empty request comparison', async () => {
  const started = Date.now();
  await assert.rejects(awaitDeliveredEffects([], [fetchRequest()], 'missing effect', 30), /expected 1 effects, received 0/);
  const elapsed = Date.now() - started;
  assert.ok(elapsed >= 20 && elapsed < 1000, `bounded wait took ${elapsed}ms`);
});

test('duplicate effects are rejected by the exact request sequence', async () => {
  const effects = [fetchEffect(), fetchEffect()];
  await assert.rejects(awaitDeliveredEffects(effects, [fetchRequest()], 'duplicate effect', 200),
    /unexpected extra effects before the expected request sequence/);
});

test('a stale effect cannot satisfy a later step with the same request', async () => {
  await assert.rejects(awaitDeliveredEffects([fetchEffect()], [], 'previous no-effect step', 30), /expected \[\], received|unexpected extra effects/);
  const different = {kind: 'fetch', chat: 'fixture', request: {kind: 'fetch_after', after_seq: 8}};
  await assert.rejects(awaitDeliveredEffects([different], [fetchRequest()], 'different step', 30), /after_seq.*8/);
});

test('drainCore flushes, waits for the action effects, then drains and applies its patch', {timeout: 1000}, async () => {
  state.flushes = 0;
  state.drains = 0;
  state.effectsAtDrain = [];
  const effects = state.effects = [];
  const list = [];
  patch();
  const pending = drainCore(1, 'fixture', list, effects, [fetchRequest()], 'delayed drain');
  assert.equal(state.flushes, 1);
  assert.equal(state.drains, 0, 'a synchronous drain would observe no queued NAPI effects');
  setTimeout(() => { effects.push(fetchEffect()); }, 15);
  const step = await pending;
  assert.equal(state.drains, 1);
  assert.equal(step.patch.version, 1);
  assert.deepEqual(step.effects, [fetchEffect()]);
  assert.equal(list.length, 1);
});
