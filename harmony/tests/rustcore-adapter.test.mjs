import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

// These callbacks model callee-unhandled NAPI functions: the payload is the first argument,
// never the second argument after a leading null error value.
export function invokeNapiCallback(callback, ...args) {
  assert.equal(args[0] === null, false, 'NAPI callbacks must not prepend a null error argument');
  return callback(...args);
}
registerHooks({
  resolve(specifier, context, next) {
    if (specifier === 'libpersona_core_ohos.so') return {url: 'fake-persona-core:', shortCircuit: true};
    if (specifier.endsWith('/Wire')) return {url: new URL('../entry/src/main/ets/core/rustcore/Wire.ts', import.meta.url).href, shortCircuit: true};
    if (specifier.endsWith('/Wire.ts')) return {url: new URL(specifier, context.parentURL).href, shortCircuit: true};
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'fake-persona-core:') return {format: 'module', shortCircuit: true, source: `
      export default {
        coreConfig() { return new Uint8Array(); },
        coreCreate(config, changed, effects) { globalThis.coreHost = {changed, effects}; globalThis.createKind = 'legacy'; return ++globalThis.handle; },
        coreCreateWithChat(config, changed, changedChat, effects) { globalThis.coreHost = {changed, changedChat, effects}; globalThis.createKind = 'targeted'; return ++globalThis.handle; },
        coreDispatch(handle, action) { globalThis.lastAction = action; return handle === globalThis.handle; },
        coreDrain(handle, chat) { return globalThis.patchBytes; },
        coreFlush(handle, timeout) { return handle === globalThis.handle && timeout === 5000; },
        coreClose(handle) { globalThis.closedHandle = handle; },
      };
    `};
    if (url.endsWith('.ts')) return {format: 'module', shortCircuit: true, source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'))};
    return next(url, context);
  },
});
const {PersonaCore} = await import('../entry/src/main/ets/core/PersonaCore.ts');
const wire = await import('../entry/src/main/ets/core/rustcore/Wire.ts');

test('the ArkTS adapter owns one NAPI handle, decodes effects and closes idempotently', () => {
  globalThis.handle = 0;
  globalThis.patchBytes = wire.encodeChatPatch({version: 1, covered: 0, history: null, has_more: null, loaded_once: false, verified: false, acked: null, ops: []});
  const effects = [], wakes = [];
  const core = new PersonaCore();
  core.open({changed(version) { wakes.push(version); }, run(effect) { effects.push(effect); }});
  assert.throws(() => core.open({changed() {}, run() {}}));
  assert.equal(core.dispatch({kind: 'continue_fill', chat: 'fixture'}), true);
  assert.deepEqual(wire.decodeAction(globalThis.lastAction), {kind: 'continue_fill', chat: 'fixture'});
  const effectBytes = wire.encodeEffect({kind: 'rejected', reason: 'synthetic'});
  assert.ok(effectBytes instanceof Uint8Array);
  invokeNapiCallback(globalThis.coreHost.effects, effectBytes);
  assert.deepEqual(effects, [{kind: 'rejected', reason: 'synthetic'}]);
  invokeNapiCallback(globalThis.coreHost.changed, 7);
  assert.deepEqual(wakes, [7]);
  assert.throws(() => invokeNapiCallback(globalThis.coreHost.effects, null, effectBytes), /null error argument/);
  assert.throws(() => invokeNapiCallback(globalThis.coreHost.effects, Array.from(effectBytes)), TypeError);
  assert.deepEqual(effects, [{kind: 'rejected', reason: 'synthetic'}]);
  assert.equal(core.drain('fixture').version, 1);
  assert.equal(core.flush(5000), true);
  core.close();
  core.close();
  assert.equal(globalThis.closedHandle, 1);
  assert.equal(core.dispatch({kind: 'continue_fill', chat: 'fixture'}), false);
  assert.equal(core.drain('fixture'), null);
  assert.equal(globalThis.createKind, 'legacy');
  assert.equal(core.flush(5000), false);
});

test('the ArkTS adapter selects targeted wake only when supplied', () => {
  globalThis.handle = 0;
  globalThis.patchBytes = wire.encodeChatPatch({version: 1, covered: 0, history: null, has_more: null, loaded_once: false, verified: false, acked: null, ops: []});
  const core = new PersonaCore();
  const wakes = [], legacyWakes = [], effects = [];
  core.open({changed(version) { legacyWakes.push(version); }, changedChat(chat, version) { wakes.push([chat, version]); }, run(effect) { effects.push(effect); }});
  assert.equal(globalThis.createKind, 'targeted');
  invokeNapiCallback(globalThis.coreHost.changedChat, 'fixture', 1);
  assert.deepEqual(wakes, [['fixture', 1]]);
  assert.throws(() => invokeNapiCallback(globalThis.coreHost.changedChat, null, 'fixture', 1), /null error argument/);
  const effectBytes = wire.encodeEffect({kind: 'rejected', reason: 'targeted'});
  invokeNapiCallback(globalThis.coreHost.effects, effectBytes);
  assert.deepEqual(effects, [{kind: 'rejected', reason: 'targeted'}]);
  assert.throws(() => invokeNapiCallback(globalThis.coreHost.effects, Array.from(effectBytes)), TypeError);
  core.close();
  const legacy = new PersonaCore();
  legacy.open({changed(version) { legacyWakes.push(version); }, run() {}});
  assert.equal(globalThis.createKind, 'legacy');
  invokeNapiCallback(globalThis.coreHost.changed, 8);
  assert.deepEqual(legacyWakes, [8]);
  legacy.close();
});
