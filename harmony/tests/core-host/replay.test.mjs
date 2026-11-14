// The generated ArkTS codec against the real core, on the host (#7450). Every action is encoded by the generated
// codec (Wire.ts), applied by the real runtime through persona-core-ohos's registry, the code under the NAPI entry
// points, and every effect and patch is decoded by the generated codec again. The registry runs in a helper process:
// native/app-core/crates/persona-core-ohos/examples/host_replay.rs, whose header documents the frames used here.
//
// This is host evidence only. It does not load the NAPI library, build a HAP or run on a device; the on-device
// `coreReplay` (entry/src/ohosTest/ets/test/NativeCore.test.ets) is still the only test of those.
//
// It needs the helper, so it is not one of the portable tests/*.test.mjs files:
//
//   (cd ../native/app-core && cargo build --locked -p persona-core-ohos --example host_replay)
//   PERSONA_CORE_HOST_REPLAY=../native/app-core/target/debug/examples/host_replay node --test tests/core-host/replay.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { isDeepStrictEqual } from 'node:util';
import { Command, Record, ReplayedChat, at, frame, handleField, helperPath, hex, startCore, wire, wireSource } from './host.mjs';

// The same values as `samples()` in the helper, written by hand here and built in Rust there. Neither side is
// generated from the other, and neither comes from the codec under test.
const sampleSnapshot = {items_json: '[{"id":"s#0","text":"snapshot é ✓ 🌍"}]', history: 'snapshot-history', covered: 4294967303, has_more: true, loaded_once: false};
const sampleBatch = {items_json: '[{"id":"b#0"}]', history: 'batch-history', from_seq: -3, to_seq: 8589934597, backlog: true, has_more: false, more_after: false};
const sampleBareBatch = {items_json: '[]', history: null, from_seq: null, to_seq: null, backlog: false, has_more: null, more_after: true};
const sampleInsert = {kind: 'insert', index: 258, id: '7#1', json: '{"id":"7#1","kind":"text"}'};
const sampleUpdate = {kind: 'update', index: 65537, id: '7#2', json: '{"id":"7#2","text":"new"}'};
const sampleRange = {kind: 'fetch_range', fold_id: 'fold:3#0', from_seq: 2, through_seq: 8589934600};
const expectedSamples = {
  'Action.open_chat': {kind: 'open_chat', chat: 'chat/open', snapshot: sampleSnapshot},
  'Action.close_chat': {kind: 'close_chat', chat: 'chat/close'},
  'Action.chat': {kind: 'chat', chat: 'chat/event', event: {kind: 'open_fold', fold_id: 'fold:3#0'}},
  'Action.continue_fill': {kind: 'continue_fill', chat: 'chat/fill'},
  'Action.hub_changed': {kind: 'hub_changed', chat: 'chat/hub'},
  'Action.ack': {kind: 'ack', chat: 'chat/ack', seq: 4294967301},
  'Action.wire_payload': {kind: 'wire_payload', chat: 'chat/payload', payload: {kind: 'newer', body: '{"items":[],"more_after":true}'}},
  'ChatEvent.latest': {kind: 'latest', batch: sampleBatch},
  'ChatEvent.pushed': {kind: 'pushed', batch: sampleBareBatch},
  'ChatEvent.after': {kind: 'after', batch: sampleBatch},
  'ChatEvent.reloaded': {kind: 'reloaded', items_json: '[{"id":"r#0"}]', history: 'reloaded-history', to_seq: 77, has_more: true},
  'ChatEvent.reloaded_bare': {kind: 'reloaded', items_json: '[]', history: null, to_seq: null, has_more: false},
  'ChatEvent.older': {kind: 'older', items_json: '[{"id":"o#0"}]', has_more: true},
  'ChatEvent.range': {kind: 'range', fold_id: null, items_json: '[{"id":"g#0"}]', more: false},
  'ChatEvent.reconnected': {kind: 'reconnected'},
  'ChatEvent.load_older': {kind: 'load_older'},
  'ChatEvent.open_fold': {kind: 'open_fold', fold_id: 'fold:12#0'},
  'ChatEvent.fill_failed': {kind: 'fill_failed'},
  'ChatEvent.older_failed': {kind: 'older_failed'},
  'ChatEvent.range_failed': {kind: 'range_failed', fold_id: null},
  'ChatPatch.full': {version: 4294967298, covered: -9, history: 'patch-history', has_more: true, loaded_once: false, verified: true, acked: 11,
    ops: [{kind: 'remove', index: 3}, sampleInsert, sampleUpdate]},
  'ChatPatch.bare': {version: 0, covered: 0, history: null, has_more: null, loaded_once: true, verified: false, acked: null, ops: []},
  'ChatSnapshot.full': sampleSnapshot,
  'ChatSnapshot.bare': {items_json: '', history: null, covered: -2, has_more: null, loaded_once: true},
  'Config.default': {},
  'Effect.fetch': {kind: 'fetch', chat: 'chat/effect', request: sampleRange},
  'Effect.rejected': {kind: 'rejected', reason: 'rejected ✗'},
  'ListOp.remove': {kind: 'remove', index: 4294967295},
  'ListOp.insert': sampleInsert,
  'ListOp.update': sampleUpdate,
  'Payload.latest': {kind: 'latest', body: '{}'},
  'Payload.newer': {kind: 'newer', body: '{"items":[],"more_after":true}'},
  'Payload.reloaded': {kind: 'reloaded', body: '{"items":[],"has_more":false}'},
  'Payload.older': {kind: 'older', body: '{"items":[],"has_more":true}'},
  'Payload.range': {kind: 'range', fold_id: null, body: '{"items":[],"more":true}'},
  'Payload.frame': {kind: 'frame', body: '{"type":"items","machine_id":"m1","session_id":"s1"}'},
  'Request.fetch_after': {kind: 'fetch_after', after_seq: 4294967301},
  'Request.fetch_latest': {kind: 'fetch_latest', fold_work: true},
  'Request.subscribe': {kind: 'subscribe', since_seq: 41, fold_work: false},
  'Request.subscribe_from_start': {kind: 'subscribe', since_seq: null, fold_work: true},
  'Request.fetch_older': {kind: 'fetch_older', before_seq: 12, fold_work: false},
  'Request.fetch_range': sampleRange,
  'WireBatch.full': sampleBatch,
  'WireBatch.bare': sampleBareBatch,
};

const WireType = {Action: 1, ChatEvent: 2, ChatPatch: 3, ChatSnapshot: 4, Config: 5, Effect: 6, ListOp: 7, Request: 8, WireBatch: 9, Payload: 10};

// Every type the generated codec declares, with the variants of each union: what the samples have to cover.
function declaredShapes() {
  const shapes = new Map();
  for (const match of wireSource.matchAll(/^export interface (\w+) \{/gm)) shapes.set(match[1], null);
  for (const match of wireSource.matchAll(/^export type (\w+) =\n((?: {2}\| .*\n)+)/gm)) {
    const kinds = [...match[2].matchAll(/readonly kind: "(\w+)"/g)].map((kind) => kind[1]);
    assert.equal(kinds.length, match[2].trimEnd().split('\n').length, match[1] + ': every variant declares its kind');
    shapes.set(match[1], kinds);
  }
  return shapes;
}

test('ArkTS and Rust write the same bytes for every wire shape, and read them as the same values', {timeout: 60_000}, async () => {
  const core = startCore();
  const rustSamples = await core.samples();
  assert.deepEqual([...rustSamples.keys()].sort(), Object.keys(expectedSamples).sort(), 'the helper and this test name the same samples');

  const shapes = declaredShapes();
  assert.deepEqual([...shapes.keys()].sort(), Object.keys(WireType).sort(), 'every declared wire type has a helper type number');
  for (const [type, kinds] of shapes) {
    const values = Object.entries(expectedSamples).filter(([name]) => name.startsWith(type + '.')).map(([, value]) => value);
    assert.ok(values.length > 0, type + ' has no sample');
    for (const kind of kinds ?? []) assert.ok(values.some((value) => value.kind === kind), type + '.' + kind + ' has no sample');
  }

  // Every sample is checked before any failure is reported, so one run names all the shapes that disagree.
  const disagreements = [];
  for (const [name, value] of Object.entries(expectedSamples)) {
    const type = name.slice(0, name.indexOf('.'));
    const rust = rustSamples.get(name);
    // Rust to ArkTS: the bytes Rust wrote for its own value decode to the value written here.
    try {
      const read = wire['decode' + type](rust);
      if (!isDeepStrictEqual(read, value)) disagreements.push(name + ': ArkTS reads Rust\'s ' + hex(rust) + ' as ' + JSON.stringify(read));
    } catch (error) {
      disagreements.push(name + ': ArkTS cannot read Rust\'s ' + hex(rust) + ': ' + error.message);
    }
    // ArkTS to Rust: the value written here encodes to exactly the bytes Rust wrote, and Rust reads them back.
    try {
      const arkts = wire['encode' + type](value);
      if (hex(arkts) !== hex(rust)) disagreements.push(name + ': ArkTS writes ' + hex(arkts) + ', Rust writes ' + hex(rust));
      else assert.equal(hex(await core.reencode(type, arkts)), hex(rust));
    } catch (error) {
      disagreements.push(name + ': ' + error.message);
    }
  }
  assert.equal(disagreements.length, 0, 'ArkTS and Rust disagree on ' + disagreements.length + ' of ' +
    rustSamples.size * 2 + ' checks:\n' + disagreements.join('\n'));
  await core.end();
});

// The fixture event -> Action mapping of NativeCore.test.ets, which this replay mirrors on the host.
function batch(event) {
  return {items_json: JSON.stringify(event.items ?? []), history: event.history ?? null, from_seq: event.from_seq ?? null,
    to_seq: event.to_seq ?? null, backlog: event.backlog === true, has_more: event.has_more ?? null, more_after: event.more_after === true};
}
function eventAction(event, chat) {
  const items_json = JSON.stringify(event.items ?? []);
  switch (event.kind) {
    case 'continue_fill': return {kind: 'continue_fill', chat};
    case 'http_latest': return {kind: 'chat', chat, event: {kind: 'latest', batch: batch(event)}};
    case 'ws_items': return {kind: 'chat', chat, event: {kind: 'pushed', batch: batch(event)}};
    case 'http_after': return {kind: 'chat', chat, event: {kind: 'after', batch: batch(event)}};
    case 'http_reload': return {kind: 'chat', chat, event: {kind: 'reloaded', items_json, history: event.history ?? null,
      to_seq: event.to_seq ?? null, has_more: event.has_more === true}};
    case 'http_older': return {kind: 'chat', chat, event: {kind: 'older', items_json, has_more: event.has_more === true}};
    case 'http_range': return {kind: 'chat', chat, event: {kind: 'range', fold_id: null, items_json, more: event.more === true}};
    case 'reconnect': return {kind: 'chat', chat, event: {kind: 'reconnected'}};
    case 'load_older': return {kind: 'chat', chat, event: {kind: 'load_older'}};
    case 'open_fold': return {kind: 'chat', chat, event: {kind: 'open_fold', fold_id: event.fold_id ?? ''}};
    default: throw new Error('unknown fixture event: ' + event.kind);
  }
}
test('every continuity case replays through the ArkTS codec and the real runtime', {timeout: 120_000}, async () => {
  const directory = new URL('../../../spec/fixtures/continuity/', import.meta.url);
  const files = readdirSync(directory).filter((name) => name.endsWith('.json')).sort();
  const core = startCore();
  const config = await core.coreConfig();
  assert.equal(hex(config), hex(wire.encodeConfig({})), 'the core\'s default config is the ArkTS codec\'s empty config');
  const chat = 'fixture/continuity';
  let cases = 0, steps = 0;
  for (const file of files) {
    const document = JSON.parse(readFileSync(new URL(file, directory), 'utf8'));
    for (const fixture of document.cases) {
      cases++;
      const handle = await core.coreCreate(config);
      const replay = new ReplayedChat(core, handle, chat, fixture.name + ' open');
      const initial = fixture.input.initial;
      await replay.dispatch({kind: 'open_chat', chat, snapshot: {items_json: JSON.stringify(initial.items ?? []),
        history: initial.history ?? null, covered: initial.covered ?? 0, has_more: initial.has_more ?? null,
        loaded_once: initial.loaded_once === true}});
      await replay.settle();
      assert.equal(fixture.input.events.length, fixture.expected.length, fixture.name + ': one expectation per event');
      for (let index = 0; index < fixture.input.events.length; index++) {
        const label = fixture.name + ' step ' + index;
        const expected = fixture.expected[index];
        replay.label = label;
        await replay.dispatch(eventAction(fixture.input.events[index], chat));
        const {patch, requests} = await replay.settle();
        // Compared as values, as the Rust host replay does. The core writes an item it changes (a promoted
        // fold) with its keys in another order, so comparing the text would fail on steps that are right.
        at(label + ' items', () => assert.deepStrictEqual(replay.list.map((json) => JSON.parse(json)), expected.items));
        assert.equal(patch.covered, expected.covered, label + ' covered');
        assert.equal(patch.history, expected.history, label + ' history');
        assert.equal(patch.has_more, expected.has_more, label + ' has_more');
        assert.equal(patch.loaded_once, expected.loaded_once, label + ' loaded_once');
        at(label + ' requests', () => assert.deepStrictEqual(requests, expected.actions));
        steps++;
      }
      await core.coreClose(handle);
      assert.deepEqual(core.takeCallbacks(handle), [], fixture.name + ': no callback after the last flush');
    }
  }
  await core.end();
  assert.equal(cases, 38, 'every continuity case replays');
  assert.equal(steps, 94, 'every continuity step replays');
});

test('a repeated item is one Update, and a closed handle stays closed', {timeout: 60_000}, async () => {
  const core = startCore();
  const chat = 'update';
  const item = (text) => '[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"' + text + '"}]';
  const handle = await core.coreCreate(await core.coreConfig());
  const replay = new ReplayedChat(core, handle, chat, 'update');
  await replay.dispatch({kind: 'open_chat', chat, snapshot: {items_json: item('old'), history: 'h', covered: 1, has_more: false, loaded_once: true}});
  await replay.settle();
  await replay.dispatch({kind: 'chat', chat, event: {kind: 'pushed', batch: {items_json: item('newest'), history: 'h', from_seq: 0,
    to_seq: 1, backlog: false, has_more: null, more_after: false}}});
  const {patch} = await replay.settle();
  assert.equal(patch.ops.length, 1);
  assert.equal(patch.ops[0].kind, 'update');
  assert.equal(patch.ops[0].index, 0);
  assert.deepEqual(replay.list, [item('newest').slice(1, -1)]);
  await core.coreClose(handle);
  await core.coreClose(handle);
  await assert.rejects(core.coreDispatch(handle, wire.encodeAction({kind: 'continue_fill', chat})), /core is closed/);
  await assert.rejects(core.coreFlush(handle, 5000), /core is closed/);
  await assert.rejects(core.coreDrain(handle, chat), /core is closed/);
  await core.end();
});

test('the core rejects what it cannot use, and the helper ends on a malformed script', {timeout: 60_000}, async () => {
  // Refusals of a well-formed command come back in band, the way the NAPI entry points throw.
  const core = startCore();
  await assert.rejects(core.coreCreate(Uint8Array.of(255)), /the core refused/);
  await assert.rejects(core.reencode('Action', Uint8Array.of(9, 0, 0, 0)), /the core refused/);
  const handle = await core.coreCreate(await core.coreConfig());
  // Bytes that are no Action are still queued; the worker answers with a `rejected` effect and no wake-up.
  assert.equal(await core.coreDispatch(handle, Uint8Array.of(9, 0, 0, 0)), true);
  assert.equal(await core.coreFlush(handle, 5000), true);
  const callbacks = core.takeCallbacks(handle);
  assert.deepEqual(callbacks.map((callback) => callback.kind), ['effect']);
  const effect = wire.decodeEffect(callbacks[0].bytes);
  assert.equal(effect.kind, 'rejected');
  assert.match(effect.reason, /^undecodable action/);
  // The handle is left open: the end of the script closes it.
  await core.end();

  // A script that is not well formed ends the helper with status 2 and one line, never a panic.
  const handleBytes = handleField(1);
  const malformed = {
    'a length over the limit': Buffer.from('ffffffff', 'hex'),
    'an empty frame': Buffer.alloc(4),
    'a length cut short': Buffer.from('0100', 'hex'),
    'a frame cut short': frame(Command.dispatch, handleBytes).subarray(0, 8),
    'an unknown command': frame(99),
    'bytes after config': frame(Command.config, Buffer.of(0)),
    'a short handle': frame(Command.dispatch, handleBytes.subarray(0, 7)),
    'a flush without a timeout': frame(Command.flush, handleBytes),
    'a timeout over the limit': frame(Command.flush, handleBytes, Buffer.from('61ea0000', 'hex')),
    'a chat that is not UTF-8': frame(Command.drain, handleBytes, Buffer.of(0xff)),
    'bytes after close': frame(Command.close, handleBytes, Buffer.of(0)),
    'an unknown wire type': frame(Command.reencode, Buffer.of(0)),
    'a reencode without a type': frame(Command.reencode),
    'bytes after samples': frame(Command.samples, Buffer.of(0)),
  };
  for (const [name, script] of Object.entries(malformed)) {
    const result = spawnSync(helperPath, [], {input: script, timeout: 30_000});
    assert.equal(result.status, 2, name + ': exit status, stderr: ' + result.stderr);
    assert.match(result.stderr.toString(), /^host_replay: [^\n]+\n$/, name);
    assert.equal(result.stdout.length, 0, name + ': nothing is written for a script that never was valid');
  }
  // What was answered before the malformed frame is still written.
  const partial = spawnSync(helperPath, [], {input: Buffer.concat([frame(Command.config), frame(99)]), timeout: 30_000});
  assert.equal(partial.status, 2);
  assert.equal(hex(partial.stdout), hex(frame(Record.ok)));
});
