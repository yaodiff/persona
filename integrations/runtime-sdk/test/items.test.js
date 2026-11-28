import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clipInput, clipTo, light, MAX_INPUT, page, PAGE_OUTPUT, PAGE_TEXT, summaryOf } from '../index.js'

const jsonBytes = (v) => Buffer.byteLength(JSON.stringify(v), 'utf8')

function legacyMapStrings(value, f) {
  if (typeof value === 'string') return f(value)
  if (Array.isArray(value)) return value.map((v) => legacyMapStrings(v, f))
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, legacyMapStrings(v, f)]))
  }
  return value
}

function legacyClipIfShorter(s, n) {
  const size = Buffer.byteLength(s, 'utf8')
  if (size <= n) return s
  const clipped = clipTo(s, n)
  return size > n + 48 || jsonBytes(clipped) < jsonBytes(s) ? clipped : s
}

function legacyClipInput(input) {
  const originalSize = jsonBytes(input)
  if (originalSize <= MAX_INPUT) return input
  const out = legacyMapStrings(input, (s) => clipTo(s, MAX_INPUT / 2))
  const size = jsonBytes(out)
  if (size <= MAX_INPUT) return out
  let long = 0
  let kept = 0
  const rest = jsonBytes(legacyMapStrings(input, (s) => {
    const bytes = Buffer.byteLength(s, 'utf8')
    if (bytes <= 256) return s
    long++
    kept += Math.min(bytes, MAX_INPUT / 2)
    return ''
  }))
  if (long === 0) return out
  let share = Math.trunc((MAX_INPUT - rest) * kept / (size - rest) / long)
  share = Math.min(Math.max(share - 48, 256), MAX_INPUT / 2)
  return legacyMapStrings(input, (s) => legacyClipIfShorter(s, share))
}

test('clipInput leaves fitting first- and second-pass results byte-identical to the legacy implementation', () => {
  const medium = 'Some notes on the plan. '.repeat(125)
  const inputs = [
    { file_path: 'a.go', edits: [{ old_string: 'a', new_string: 'x'.repeat(1_000_000) }] },
    { page_id: 'p1', children: Array.from({ length: 40 }, () => ({ type: 'paragraph', text: medium })) },
  ]
  for (const input of inputs) {
    const legacy = legacyClipInput(input)
    assert.ok(jsonBytes(legacy) <= MAX_INPUT)
    const out = clipInput(input)
    assert.equal(JSON.stringify(out), JSON.stringify(legacy))
    assert.equal(out.zz_persona_clipped, undefined)
  }
})

test('clipInput clips a million-byte multi-edit string without changing the edits or source', () => {
  const input = { file_path: 'a.go', edits: [
    { old_string: 'a', new_string: 'x'.repeat(1_000_000) },
    { old_string: 'b', new_string: 'c', replace_all: true },
  ] }
  const original = structuredClone(input)
  const out = clipInput(input)
  assert.ok(jsonBytes(out) < MAX_INPUT)
  assert.equal(out.file_path, 'a.go')
  assert.equal(out.edits.length, 2)
  assert.equal(out.edits[0].old_string, 'a')
  assert.equal(out.edits[0].new_string, clipTo(input.edits[0].new_string, MAX_INPUT / 2))
  assert.deepEqual(out.edits[1], input.edits[1])
  assert.deepEqual(input, original)
})

test('clipInput visits nested objects and arrays, preserving keys and non-string values', () => {
  const big = 'x'.repeat(50_000)
  const input = { blocks: [{ nested: [[big, null, true, 42]], empty: [], object: {} }],
    ...JSON.parse('{"__proto__":{"content":"small"}}') }
  const out = clipInput(input)
  assert.deepEqual(out, { ...input, blocks: [{ ...input.blocks[0], nested: [[clipTo(big, MAX_INPUT / 2), null, true, 42]] }] })
  assert.ok(Object.hasOwn(out, '__proto__'))
  assert.equal(Object.getPrototypeOf(out), Object.prototype)
  assert.equal(input.blocks[0].nested[0][0], big)
})

test('clipInput shares the budget across medium strings, accounting for JSON escapes', () => {
  for (const [text, count] of [['Some notes on the plan. '.repeat(125), 40], ['\\"\n\t\u0000'.repeat(600), 20]]) {
    const input = { page_id: 'p1', children: Array.from({ length: count }, () => ({ type: 'paragraph', text })) }
    const out = clipInput(input)
    assert.ok(jsonBytes(input) > 100_000)
    assert.ok(jsonBytes(out) <= MAX_INPUT, `clipped to ${jsonBytes(out)} bytes`)
    assert.equal(out.page_id, input.page_id)
    assert.equal(out.children.length, count)
    const first = out.children[0].text
    assert.notEqual(first, text)
    assert.ok(first.startsWith(text.slice(0, 100)))
    assert.ok(Buffer.byteLength(first) >= 256)
    assert.ok(first.endsWith(` ${Buffer.byteLength(text)} 字节）`))
    for (const block of out.children) assert.deepEqual(block, { type: 'paragraph', text: first })
    assert.ok(input.children.every((block) => block.text === text))
  }
})

test('clipInput keeps short inputs and top-level non-objects unchanged, and bounds many short values', () => {
  for (const input of [null, undefined, true, 42, 'x'.repeat(50_000), ['x'.repeat(50_000)], { text: 'small' }]) {
    assert.equal(clipInput(input), input)
  }
  const input = { values: Array.from({ length: 4_000 }, () => ['short', 42, true, null]) }
  const original = structuredClone(input)
  assert.ok(jsonBytes(input) > MAX_INPUT)
  const out = clipInput(input)
  assert.ok(jsonBytes(out) <= MAX_INPUT)
  assert.ok(out.values.length > 0 && out.values.length < input.values.length)
  assert.deepEqual(out.values, input.values.slice(0, out.values.length))
  assert.match(out.zz_persona_clipped.note, /^Omitted \d+ array items and 0 object fields\./)
  assert.deepEqual(input, original)
})

test('clipInput only replaces a near-share string when its JSON is strictly shorter', () => {
  // With a three-digit original size the note costs 37 JSON bytes (excluding its enclosing quotes).
  for (const [extra, cut] of [[30, false], [36, false], [37, false], [38, true], [48, true], [49, true]]) {
    const text = 'x'.repeat(256 + extra)
    const input = { items: Array.from({ length: 200 }, () => ({ text })) }
    const out = clipInput(input)
    const candidate = clipTo(text, 256)
    if (extra === 37) assert.equal(jsonBytes(candidate), jsonBytes(text))
    assert.ok(jsonBytes(out) <= jsonBytes(input))
    assert.ok(jsonBytes(out) <= MAX_INPUT)
    assert.ok(out.items.length > 0 && out.items.length < 200)
    assert.equal(typeof out.zz_persona_clipped.note, 'string')
    for (const item of out.items) assert.equal(item.text, cut ? candidate : text)
  }
  for (const tail of ['\\'.repeat(30), '"'.repeat(30), '\u0000'.repeat(30), '<>&'.repeat(10), '\u2028\u2029'.repeat(5)]) {
    const text = 'x'.repeat(256) + tail
    const candidate = clipTo(text, 256)
    const input = { items: Array.from({ length: 200 }, () => ({ text })) }
    const out = clipInput(input)
    const shouldCut = jsonBytes(candidate) < jsonBytes(text)
    assert.equal(shouldCut, ['\\', '"', '\u0000'].includes(tail[0]), 'use JSON.stringify escaping, not Go HTML escaping')
    assert.ok(jsonBytes(out) <= MAX_INPUT)
    assert.ok(out.items.length > 0 && out.items.length < 200)
    assert.equal(typeof out.zz_persona_clipped.note, 'string')
    for (const item of out.items) assert.equal(item.text, shouldCut ? candidate : text)
  }
})

test('clipTo and nested clipInput cut UTF-8 characters and surrogate pairs only at boundaries', () => {
  for (const char of ['é', '界', '🙂']) {
    const width = Buffer.byteLength(char)
    for (let offset = 0; offset < width; offset++) {
      const text = 'a'.repeat(offset) + char.repeat(20_000)
      const prefix = 'a'.repeat(offset) + char.repeat(Math.floor((MAX_INPUT / 2 - offset) / width))
      const expected = prefix + `\n…（已截断，共 ${Buffer.byteLength(text)} 字节）`
      assert.equal(clipTo(text, MAX_INPUT / 2), expected)
      const out = clipInput({ nested: { text } })
      assert.equal(out.nested.text, expected)
      assert.ok(!out.nested.text.includes('\ufffd'))
      assert.ok(out.nested.text.isWellFormed())
      const medium = 'a'.repeat(offset) + char.repeat(1_000)
      const shared = clipInput({ rows: Array.from({ length: 40 }, () => [medium]) })
      assert.ok(jsonBytes(shared) <= MAX_INPUT)
      for (const [value] of shared.rows) {
        assert.notEqual(value, medium)
        assert.ok(value.endsWith(` ${Buffer.byteLength(medium)} 字节）`))
        assert.ok(!value.includes('\ufffd'))
        assert.ok(value.isWellFormed())
      }
    }
  }
})

test('summaryOf says what a call does, not just which tool', () => {
  assert.equal(summaryOf('bash', { command: 'rm -rf build', description: 'clean' }), 'rm -rf build', 'what runs comes first')
  assert.equal(summaryOf('Agent', { description: 'Find the flaky test', prompt: 'long…' }), 'Find the flaky test')
  assert.equal(summaryOf('mcp__lark__send', { chat_id: 'oc_1', message: 'hello there' }), 'hello there')
  assert.equal(summaryOf('mcp__x__create', { repo: 'a/b', count: 3 }), 'repo: a/b', 'any other text, with its name')
  assert.equal(summaryOf('mcp__x__create', clipInput({ repo: 'a/b', values: Array.from({ length: 4000 }, () => ['short', 42, true, null]) })), 'repo: a/b',
    'a real fallback argument stays ahead of the appended structural clip marker')
  const clippedWithoutText = clipInput({ values: Array.from({ length: 4000 }, () => ['short', 42, true, null]) })
  assert.equal(summaryOf('mcp__x__create', clippedWithoutText), 'mcp__x__create',
    'an object marker cannot become the call summary when the input has no other top-level string')
  assert.deepEqual(Object.keys(clippedWithoutText.zz_persona_clipped), ['note'])
  assert.equal(summaryOf('mcp__x__ping', { count: 3 }), 'mcp__x__ping', 'no text at all: the tool name')
  assert.equal(summaryOf('mcp__x__ping', { path: '  ', count: 3 }), 'mcp__x__ping', 'blank strings say nothing')
  assert.equal(summaryOf('t', null), 't')
  assert.equal(summaryOf('t', ['a']), 't')
  assert.equal([...summaryOf('t', { text: 'x'.repeat(300) })].length, 201, 'one line, cut')
  assert.equal(summaryOf('x', { api_key: 'sk-live-123', repo: 'a' }), 'repo: a', 'a credential is skipped')
  for (const k of ['token', 'api_key', 'apiKey', 'APIKey', 'x-api-key', 'authorization', 'Authorization', 'password', 'client_secret', 'cookie', 'credentials',
    'session_id', 'sessionId', 'private_key', 'headers', 'bearer', 'Bearer', 'totp', 'pin', 'dsn', 'database_dsn', 'connection_string', 'connStr', 'webhook_url', 'slackWebhook', 'pwd', 'pass', 'jwt', 'access_key', 'accessKeyId', 'key', 'signature', 'auth',
    'x-auth', 'auth_code', 'otp', 'SSHKey', 'GPGKey', 'AWSKey', 'DBPass', 'HTTPAuth', 'MFAPin', 'PGPKeys']) {
    assert.equal(summaryOf('mcp__x__call', { [k]: 'secret-value' }), 'mcp__x__call', `${k} alone: the tool name`)
  }
  assert.equal(summaryOf('x', { password: 'hunter2', user: 'bob' }), 'user: bob')
  for (const k of ['author', 'authorName', 'oauth_scope', 'session_name', 'keyword', 'passage', 'monkey', 'OAuthScope', 'URLPath', 'HTTPMethod', 'IDList']) {
    assert.equal(summaryOf('x', { [k]: 'v' }), `${k}: v`, `${k} is no credential: shown`)
  }
})

test('light is idempotent and page preserves full_size on already-lightened items', () => {
  const cases = [
    { id: '1#0', seq: 1, role: 'user', kind: 'tool_result', text: 'a'.repeat(PAGE_OUTPUT + 5_000) },
    { id: '2#0', seq: 2, role: 'user', kind: 'tool_result', text: 'line\n'.repeat(1_200) },
    { id: '3#0', seq: 3, role: 'user', kind: 'tool_result', text: '界'.repeat(2_000) },
    { id: '4#0', seq: 4, role: 'assistant', kind: 'text', text: 'b'.repeat(PAGE_TEXT + 10_000) },
    { id: '5#0', seq: 5, role: 'assistant', kind: 'text', text: '中'.repeat(10_000) },
    { id: '6#0', seq: 6, role: 'user', kind: 'tool_result', text: 'c'.repeat(PAGE_OUTPUT + 1) },
    { id: '7#0', seq: 7, role: 'assistant', kind: 'text', text: 'd'.repeat(PAGE_TEXT + 1) },
    { id: '8#0', seq: 8, role: 'user', kind: 'tool_result', text: '🙂'.repeat(1_500) },
    { id: '9#0', seq: 9, role: 'assistant', kind: 'text', text: '🙂'.repeat(7_000) },
  ]
  for (const raw of cases) {
    const expectedSize = Buffer.byteLength(raw.text, 'utf8')
    const once = light(raw)
    assert.equal(once.full_size, expectedSize)
    assert.ok(once.text.isWellFormed())
    const twice = light(once)
    assert.strictEqual(twice, once, `second light() must return clipped ${raw.kind} unchanged`)
    assert.equal(twice.full_size, expectedSize)
    assert.equal(twice.text, once.text)
    const paged = page([once], {}, { history: 'h', toSeq: raw.seq })
    assert.equal(paged.items[0].full_size, expectedSize)
    assert.equal(paged.items[0].text, once.text)
  }

  // (1) Unclipped oversized text carrying a valid matching suffix (limit + 1 bytes + suffix) must still be clipped.
  const validSuffix = '\n…（已截断，点开看全文，共 99999 字节）'
  const oversizedWithSuffix = { id: '10#0', seq: 10, role: 'user', kind: 'tool_result', text: 'x'.repeat(PAGE_OUTPUT + 1) + validSuffix, full_size: 99_999 }
  const clippedOversized = light(oversizedWithSuffix)
  assert.notEqual(clippedOversized, oversizedWithSuffix)
  assert.equal(clippedOversized.full_size, Buffer.byteLength(oversizedWithSuffix.text, 'utf8'))

  // (2) Invalid full_size values with a matching suffix must still be clipped.
  for (const badFullSize of [NaN, Infinity, PAGE_OUTPUT, 5000.5, Number.MAX_SAFE_INTEGER + 1, '9000']) {
    const suffix = `\n…（已截断，点开看全文，共 ${String(badFullSize)} 字节）`
    const item = { id: '11#0', seq: 11, role: 'user', kind: 'tool_result', text: 'x'.repeat(PAGE_OUTPUT) + suffix, full_size: badFullSize }
    const out = light(item)
    assert.notEqual(out, item, `invalid full_size ${String(badFullSize)} must not bypass light()`)
    assert.equal(out.full_size, Buffer.byteLength(item.text, 'utf8'))
  }

  // (3) Cross-kind suffix mismatch (kind: 'text' carrying tool_result suffix) must still be clipped.
  const crossKindSuffix = '\n…（已截断，点开看全文，共 50000 字节）'
  const crossKind = { id: '12#0', seq: 12, role: 'assistant', kind: 'text', text: 'y'.repeat(PAGE_TEXT) + crossKindSuffix, full_size: 50_000 }
  const clippedCrossKind = light(crossKind)
  assert.notEqual(clippedCrossKind, crossKind)
  assert.equal(clippedCrossKind.full_size, Buffer.byteLength(crossKind.text, 'utf8'))

  // Unclipped oversized text that happens to carry a full_size field without a matching suffix must still be clipped.
  const spoofed = { id: '13#0', seq: 13, role: 'user', kind: 'tool_result', text: 'x'.repeat(PAGE_OUTPUT + 2_000), full_size: 99_999 }
  const clipped = light(spoofed)
  assert.notEqual(clipped, spoofed)
  assert.equal(clipped.full_size, PAGE_OUTPUT + 2_000)
  assert.ok(clipped.text.endsWith(`共 ${PAGE_OUTPUT + 2_000} 字节）`))
})
