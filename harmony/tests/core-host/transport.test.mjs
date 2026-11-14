// The wire corpus through the real transport decoder and the generated ArkTS Action codec, on the host. The raw JSON
// text is sliced from each fixture: neither a response nor an item is re-serialised before it reaches the core.
// This is host evidence only, not a NAPI, HAP or device test. A missing helper fails rather than skips.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { isDeepStrictEqual } from 'node:util';
import { PayloadKind, ReplayedChat, at, parseJsonSource, pointer, rawItems, startCore } from './host.mjs';

const corpus = [
  {file: 'items.json', decoders: new Set(['items', 'items_after', 'items_range']), kind: {items: 'latest', items_after: 'newer', items_range: 'range'}},
  {file: 'item_events.json', decoders: new Set(['event', 'compat_event']), kind: 'frame'},
  {file: 'compatibility.json', decoders: new Set(['event', 'compat_event']), kind: 'frame'},
];
const cases = [];
for (const entry of corpus) {
  const document = parseJsonSource(readFileSync(new URL('../../../spec/fixtures/wire/' + entry.file, import.meta.url), 'utf8'));
  assert.ok(Array.isArray(document.value), entry.file + ': a case array');
  for (let index = 0; index < document.value.length; index++) {
    const fixture = document.value[index];
    if (!entry.decoders.has(fixture.decoder) || (entry.kind === 'frame' && fixture.input.type !== 'items')) continue;
    cases.push({
      ...fixture,
      inputSource: document.elements[index].value.__sources.input,
      kind: typeof entry.kind === 'string' ? entry.kind : entry.kind[fixture.decoder],
      file: entry.file,
    });
  }
}
const inScopeNames = [
  // items.json
  'items_all_kinds', 'items_folded_work', 'items_folded_tasks', 'items_inline_image', 'items_task_notifications',
  'items_reply_models', 'items_http_after', 'items_fold_range', 'items_recorded_turn_duration',
  // item_events.json
  'items_live_lazy', 'items_empty_advance', 'items_backlog_latest', 'items_backlog_catchup', 'items_backlog_capped',
  'items_live_recorded_turn_duration',
  // compatibility.json
  'items_legacy_null', 'item_unknown_kind',
];
assert.deepEqual(cases.map((fixture) => fixture.name).sort(), [...inScopeNames].sort(), 'the exact in-scope wire case set');
assert.equal(new Set(cases.map((fixture) => fixture.name)).size, cases.length, 'wire case names are unique');

// The helper projects a page or an items frame back into the wire keys the fixtures assert. Item fields the decoder
// does not read come from its kept JSON, already compared byte for byte with the fixture.
function projection(decoded) {
  const value = {history: decoded.history, from_seq: decoded.from_seq, to_seq: decoded.to_seq, backlog: decoded.backlog,
    has_more: decoded.has_more, more_after: decoded.more_after, items: decoded.items.map((item) => JSON.parse(item.json))};
  if (decoded.frame === undefined && decoded.history === null && decoded.has_more === null && decoded.backlog === null) {
    // Range's `more` flag is reported in the common batch projection's `more_after` slot.
    value.more = decoded.more_after;
    value.more_after = null;
  }
  if (decoded.frame !== undefined) {
    value.type = decoded.frame;
    value.machine_id = decoded.machine_id;
    value.session_id = decoded.session_id;
  }
  return value;
}

test('every in-scope wire case decodes through the real core with byte-identical items', {timeout: 120_000}, async () => {
  const core = startCore();
  let checkedFields = 0;
  for (const fixture of cases) {
    const label = fixture.file + '/' + fixture.name;
    assert.notEqual(fixture.inputSource.raw, JSON.stringify(fixture.input), label + ': the test uses source text, not re-serialised JSON');
    assert.equal(isDeepStrictEqual(JSON.parse(fixture.inputSource.raw), fixture.input), true, label + ': the raw slice is the input value');
    const body = fixture.inputSource.raw;
    const decoded = await core.transport(PayloadKind[fixture.kind], body);
    if (decoded.error !== undefined) {
      assert.equal(fixture.expected.accepted, false, label + ': expected acceptance, got ' + JSON.stringify(decoded.error));
      assert.deepEqual(Object.keys(decoded.error).sort(), ['code', 'detail'], label + ': only a refusal code and detail');
      assert.match(decoded.error.code, /^(invalid_utf8|truncated_json|malformed_json|not_object|missing_key|wrong_type|bad_item)$/);
      assert.ok(!JSON.stringify(decoded.error).includes('entry_'), label + ': a refusal does not repeat an item id');
      continue;
    }
    assert.equal(fixture.expected.accepted, true, label + ': accepted');
    assert.equal(decoded.accepted, true, label + ': result acceptance');
    if (fixture.kind === 'frame') assert.equal(decoded.frame, 'items', label + ': an items frame');
    const raws = rawItems(fixture);
    assert.equal(decoded.item_count, raws.length, label + ': item count');
    assert.equal(decoded.items.length, raws.length, label + ': item records');
    for (let index = 0; index < raws.length; index++) {
      assert.equal(decoded.items[index].json, raws[index], label + ': item ' + index + ' is byte-identical');
      assert.equal(decoded.items[index].id, JSON.parse(raws[index]).id, label + ': item ' + index + ' id');
      assert.equal(decoded.items[index].seq, JSON.parse(raws[index]).seq, label + ': item ' + index + ' seq');
    }
    const projected = projection(decoded);
    for (const [path, expected] of Object.entries(fixture.expected.fields)) {
      let actual = pointer(projected, path);
      if (actual === undefined) actual = null;
      // iOS maps an unknown ItemKind to `unknown`; the shared core deliberately keeps the hub's original string.
      if (fixture.name === 'item_unknown_kind' && path === '/items/0/kind') {
        assert.equal(expected, 'unknown', label + ': the iOS exception is unchanged');
        assert.equal(actual, 'future_kind', label + ': the core keeps the original kind');
      } else {
        at(label + ' ' + path, () => assert.deepStrictEqual(actual, expected));
      }
      checkedFields++;
    }
  }
  await core.end();
  assert.equal(checkedFields, 79, 'every expected field was compared');
});

test('a non-items frame is Other and a refused page returns only its code and detail', {timeout: 60_000}, async () => {
  const core = startCore();
  assert.deepEqual(await core.transport(PayloadKind.frame, '{"type":"hello","features":["future_feature"]}'),
    {accepted: true, frame: 'other'});
  assert.deepEqual(await core.transport(PayloadKind.latest, '{"items":[{"id":"private-item"}],"has_more":false}'),
    {error: {code: 'bad_item', detail: 'items[0] missing_key seq'}});
  assert.deepEqual(await core.transport(PayloadKind.frame, '{"type":"items","machine_id":"m1","session_id":"s1"}'), {
    accepted: true, frame: 'items', machine_id: 'm1', session_id: 's1', history: null, from_seq: null, to_seq: null,
    backlog: false, has_more: null, more_after: false, item_count: 0, items: [],
  });
  assert.deepEqual(await core.transport(PayloadKind.latest, '{"items":[],"has_more":false,"history":"h","to_seq":3}'), {
    accepted: true, history: 'h', from_seq: null, to_seq: 3, backlog: true, has_more: false, more_after: null, item_count: 0, items: [],
  });
  assert.deepEqual(await core.transport(PayloadKind.range, '{"items":[],"more":true}'), {
    accepted: true, history: null, from_seq: null, to_seq: null, backlog: null, has_more: null, more_after: true, item_count: 0, items: [],
  });
  await core.end();
});

function rawArray(raws) {
  return '[' + raws.join(',') + ']';
}
function batchFor(fixture) {
  const input = fixture.input;
  return {items_json: rawArray(rawItems(fixture)), history: input.history ?? null, from_seq: input.from_seq ?? null,
    to_seq: input.to_seq ?? null, backlog: input.backlog === true, has_more: input.has_more ?? null, more_after: input.more_after === true};
}
function handBuiltAction(fixture, chat) {
  const batch = batchFor(fixture);
  switch (fixture.kind) {
    case 'latest': return {kind: 'chat', chat, event: {kind: 'latest', batch: {...batch, backlog: true}}};
    case 'newer': return {kind: 'chat', chat, event: {kind: 'after', batch: {...batch, backlog: false}}};
    case 'frame': return {kind: 'chat', chat, event: {kind: 'pushed', batch}};
    case 'reloaded': return {kind: 'chat', chat, event: {kind: 'reloaded', items_json: batch.items_json,
      history: batch.history, to_seq: batch.to_seq, has_more: batch.has_more === true}};
    case 'older': return {kind: 'chat', chat, event: {kind: 'older', items_json: batch.items_json, has_more: batch.has_more === true}};
    case 'range': return {kind: 'chat', chat, event: {kind: 'range', items_json: batch.items_json, more: fixture.input.more === true}};
    default: throw new Error('unknown payload kind: ' + fixture.kind);
  }
}
function comparable(result) {
  const {version, ...patch} = result.patch;
  return {patch, requests: result.requests};
}

// Covers each page-to-Session mapping as well as real fixture frames. The pages are the same decoder input as the
// corpus above; the two extra Latest variants only change how that decoded page is applied.
const representatives = [
  {name: 'items_all_kinds', kind: 'latest'},
  {name: 'items_http_after', kind: 'newer'},
  {name: 'items_folded_work', kind: 'reloaded'},
  {name: 'items_all_kinds', kind: 'older'},
  {name: 'items_fold_range', kind: 'range'},
  {name: 'items_live_lazy', kind: 'frame'},
  {name: 'items_legacy_null', kind: 'frame'},
];
test('WirePayload and hand-built WireBatch actions produce the same patches and effects', {timeout: 120_000}, async () => {
  const core = startCore();
  const config = await core.coreConfig();
  for (const selected of representatives) {
    const fixture = cases.find((fixture) => fixture.name === selected.name);
    assert.ok(fixture, selected.name + ': a corpus case');
    const variant = {...fixture, kind: selected.kind};
    if (variant.kind === 'frame') assert.equal(fixture.kind, 'frame', selected.name + ': a frame stays a frame');
    else assert.notEqual(fixture.kind, 'frame', selected.name + ': a page stays a page');
    const chat = variant.kind === 'frame' ? fixture.input.session_id : 'wire/' + selected.name + '/' + selected.kind;
    const results = [];
    for (const path of ['payload', 'batch']) {
      const handle = await core.coreCreate(config);
      const replay = new ReplayedChat(core, handle, chat, selected.name + ' ' + path);
      await replay.dispatch({kind: 'open_chat', chat, snapshot: {items_json: '', history: null, covered: 0, has_more: null, loaded_once: false}});
      await replay.settle();
      const action = path === 'payload'
        ? {kind: 'wire_payload', chat, payload: {kind: variant.kind, body: fixture.inputSource.raw}}
        : handBuiltAction(variant, chat);
      await replay.dispatch(action);
      results.push({result: comparable(await replay.settle()), list: replay.list});
      await core.coreClose(handle);
      assert.deepEqual(core.takeCallbacks(handle), [], selected.name + ': no callbacks after close');
    }
    at(selected.name + ' ' + selected.kind + ' patches and effects', () => assert.deepStrictEqual(results[0].result, results[1].result));
    assert.deepEqual(results[0].list, results[1].list, selected.name + ': both paths preserve the same item JSON');
    if (selected.kind !== 'range') {
      // Range only updates an already-open fold, so this open-chat comparison intentionally leaves its list empty.
      assert.deepEqual(results[0].list, rawItems(fixture), selected.name + ': raw fixture items survive the runtime');
    }
  }
  await core.end();
});
