import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { startFakeNode } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { readTranscript, rememberObservedWake, stepsToItems } from '../lib/map.js'

const TINY_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
)


async function waitUntil(fn, timeoutMs = 4000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (fn()) return
    await new Promise((r) => setTimeout(r, 25))
  }
  assert.fail('condition did not become true within timeout')
}

function line(step) {
  return JSON.stringify(step) + '\n'
}

test('stepsToItems targetItemId matches full expansion across every item kind without touching unrelated output.txt or itemsCache', (t) => {
  const root = mkdtempSync(join(tmpdir(), 'agy-targeted-item-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const brain = join(root, 'brain', 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee')
  const logs = join(brain, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const imgA = join(root, 'user-a.png')
  const imgB = join(root, 'tool-b.png')
  writeFileSync(imgA, TINY_PNG)
  writeFileSync(imgB, TINY_PNG)

  const rawSteps = [
    {
      step_index: 0,
      type: 'USER_INPUT',
      source: 'USER_EXPLICIT',
      status: 'DONE',
      created_at: '2026-11-26T00:00:00Z',
      content: '<USER_REQUEST>\nFirst prompt\n</USER_REQUEST>',
      media: [{ uri: `file://${imgA}`, mime_type: 'image/png' }],
      prompt_notice: { text: 'Prompt note', ts: '2026-11-26T00:00:00Z' },
    },
    {
      step_index: 1,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:01Z',
      model: 'gemini-2.5-pro',
      thinking: 'Thinking step 1',
      content: 'Running two commands',
      tool_calls: [
        { name: 'run_command', args: { CommandLine: 'echo one', toolSummary: 'One' } },
        { name: 'run_command', args: { CommandLine: 'echo two', toolSummary: 'Two' } },
      ],
      usage: { input_tokens: 100, output_tokens: 20 },
    },
    {
      step_index: 2,
      type: 'GENERIC',
      status: 'DONE',
      created_at: '2026-11-26T00:00:02Z',
      content: 'Created At: 2026-11-26T00:00:02Z\nOutput:\ntruncated-1',
      truncated_fields: ['content'],
    },
    {
      step_index: 3,
      type: 'GENERIC',
      status: 'DONE',
      created_at: '2026-11-26T00:00:03Z',
      content: 'Created At: 2026-11-26T00:00:03Z\nOutput:\ntruncated-2',
      truncated_fields: ['content'],
      media: [{ uri: `file://${imgB}`, mime_type: 'image/png' }],
    },
    {
      step_index: 4,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:04Z',
      content: 'Delivered reply before background wake.',
    },
    {
      step_index: 5,
      type: 'SYSTEM_MESSAGE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:05Z',
      content: '<SYSTEM_MESSAGE>\n[Message] sender=sub/task-7 content=Task id "sub/task-7" finished with result:\nCommand exited with code 0.\nok detail\n</SYSTEM_MESSAGE>',
    },
    {
      step_index: 6,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:06Z',
      content: 'Follow-up after wake.',
    },
    {
      step_index: 7,
      type: 'CHECKPOINT',
      status: 'DONE',
      created_at: '2026-11-26T00:00:07Z',
    },
    {
      step_index: 8,
      type: 'USER_INPUT',
      source: 'USER_EXPLICIT',
      status: 'DONE',
      created_at: '2026-11-26T00:00:08Z',
      content: 'Second prompt for ordinal check',
    },
    {
      step_index: 9,
      type: 'ERROR_MESSAGE',
      status: 'ERROR',
      created_at: '2026-11-26T00:00:09Z',
      error: 'request interrupted by user',
    },
    {
      step_index: 10,
      type: 'USER_INPUT',
      source: 'USER_EXPLICIT',
      status: 'DONE',
      created_at: '2026-11-26T00:00:10Z',
      content: 'Third prompt',
    },
    {
      step_index: 11,
      type: 'ERROR_MESSAGE',
      status: 'ERROR',
      created_at: '2026-11-26T00:00:11Z',
      error: 'quota exhausted',
    },
  ]

  mkdirSync(join(brain, '.system_generated', 'steps', '2'), { recursive: true })
  writeFileSync(join(brain, '.system_generated', 'steps', '2', 'output.txt'), 'Created At: 2026-11-26T00:00:02Z\nOutput:\nFULL_OUTPUT_STEP_2_' + 'A'.repeat(6000))
  mkdirSync(join(brain, '.system_generated', 'steps', '3'), { recursive: true })
  writeFileSync(join(brain, '.system_generated', 'steps', '3', 'output.txt'), 'Created At: 2026-11-26T00:00:03Z\nOutput:\nFULL_OUTPUT_STEP_3_' + 'B'.repeat(6000))

  writeFileSync(join(logs, 'transcript_full.jsonl'), rawSteps.map(line).join(''))
  const tr = readTranscript(brain, true)
  rememberObservedWake(brain, tr.steps, 6)
  const trWithWake = readTranscript(brain, true)
  const stepsWithNotice = [
    ...trWithWake.steps,
    { step_index: 12, type: 'PERSONA_MODE_NOTICE', status: 'DONE', created_at: '2026-11-26T00:00:12Z', content: 'mode notice text' },
  ]
  const clientMsgIdFor = (_idx, _text, ordinal) => `cmsg-${ordinal}`

  const fullAll = stepsToItems(stepsWithNotice, {
    brainDir: brain,
    full: true,
    includeMedia: true,
    clientMsgIdFor,
    skipCache: true,
  })
  assert.ok(fullAll.items.some((it) => it.wake === true), 'fixture includes wake item')
  assert.ok(fullAll.items.some((it) => it.kind === 'image'), 'fixture includes image item')
  assert.ok(fullAll.items.some((it) => it.kind === 'tool_result' && it.text.includes('FULL_OUTPUT_STEP_3_')))

  for (const expected of fullAll.items) {
    const targeted = stepsToItems(stepsWithNotice, {
      brainDir: brain,
      full: true,
      includeMedia: true,
      clientMsgIdFor,
      targetItemId: expected.id,
    })
    assert.equal(targeted.items.length, 1, `expected single item for ${expected.id}`)
    assert.deepEqual(targeted.items[0], expected, `targeted mismatch for ${expected.id} (${expected.kind})`)
  }

  // Missing item returns empty list cleanly
  const missing = stepsToItems(stepsWithNotice, {
    brainDir: brain,
    full: true,
    includeMedia: true,
    clientMsgIdFor,
    targetItemId: '9999@missing#0',
  })
  assert.deepEqual(missing.items, [])

  // mediaItemId fast path returns exact media entry for both USER_INPUT and GENERIC images
  const imageItems = fullAll.items.filter((it) => it.kind === 'image')
  assert.equal(imageItems.length, 2)
  for (const imgItem of imageItems) {
    const expectedMedia = stepsToItems(stepsWithNotice, { includeMedia: true, mediaItemId: imgItem.id })
    assert.ok(expectedMedia.media?.uri, `media found for ${imgItem.id}`)
  }
})

test('session.item reads only the requested step output.txt and never evicts cached session items', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-item-isolation-'))
  const agyDir = join(dir, 'config')
  const rawA = '11111111-1111-4111-8111-111111111111'
  const rawB = '22222222-2222-4222-8222-222222222222'
  const brainA = join(agyDir, 'brain', rawA)
  const brainB = join(agyDir, 'brain', rawB)
  mkdirSync(join(brainA, '.system_generated', 'logs'), { recursive: true })
  mkdirSync(join(brainB, '.system_generated', 'logs'), { recursive: true })

  // Session A: small session that should stay cached in itemsCache
  writeFileSync(join(brainA, '.system_generated', 'logs', 'transcript_full.jsonl'),
    line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-11-26T00:00:00Z', content: 'alpha' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-26T00:00:01Z', content: 'beta' }))

  // Session B: 120 truncated GENERIC steps with large output.txt files
  const bLines = [line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-11-26T00:00:00Z', content: 'run many tools' })]
  let idx = 1
  for (let i = 0; i < 120; i++) {
    bLines.push(line({
      step_index: idx++,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:01Z',
      tool_calls: [{ name: 'run_command', args: { CommandLine: `cmd-${i}`, toolSummary: `Cmd ${i}` } }],
    }))
    const gIdx = idx++
    bLines.push(line({
      step_index: gIdx,
      type: 'GENERIC',
      status: 'DONE',
      created_at: '2026-11-26T00:00:02Z',
      content: `Created At: 2026-11-26T00:00:02Z\nOutput:\nshort-${i}`,
      truncated_fields: ['content'],
    }))
    const sdir = join(brainB, '.system_generated', 'steps', String(gIdx))
    mkdirSync(sdir, { recursive: true })
    writeFileSync(join(sdir, 'output.txt'), `Created At: 2026-11-26T00:00:02Z\nOutput:\nTARGET_MARKER_${i}_` + 'X'.repeat(65536))
  }
  writeFileSync(join(brainB, '.system_generated', 'logs', 'transcript_full.jsonl'), bLines.join(''))

  // Warm itemsCache directly for session A to verify session.item on B does not evict it
  const trA = readTranscript(brainA, true)
  const cacheKeyA = `test-iso|${brainA}`
  const snapA1 = stepsToItems(trA.steps, { brainDir: brainA, full: false, includeMedia: true, cacheKey: cacheKeyA })

  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: join(dir, 'missing-agy'), agyDir, home: dir, socketPath: node.path, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    rmSync(dir, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready

  const pageB = await node.peer.call('session.items', { session_id: `agy-${rawB}`, limit: 10 })
  const lastToolResult = pageB.items.findLast((it) => it.kind === 'tool_result')
  assert.ok(lastToolResult)

  const fullItem = await node.peer.call('session.item', { session_id: `agy-${rawB}`, item_id: lastToolResult.id })
  assert.equal(fullItem.id, lastToolResult.id)
  assert.equal(fullItem.tool_use_id, lastToolResult.tool_use_id, 'tool_use_id pairing must match')
  assert.ok(fullItem.text.startsWith('TARGET_MARKER_119_'), 'requested output.txt must be hydrated in full')
  assert.ok(fullItem.text.length > 65000)

  // Session A's itemsCache entry must still be intact (exact same object reference)
  const snapA2 = stepsToItems(trA.steps, { brainDir: brainA, full: false, includeMedia: true, cacheKey: cacheKeyA })
  assert.equal(snapA2, snapA1, 'session.item must not pollute or evict itemsCache')
})

test('conversation_summaries.db point-lookup and settled stat cache observe fast writes and recover from removal', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-summary-db-'))
  const agyDir = join(dir, 'config')
  const rawId = '33333333-3333-4333-8333-333333333333'
  const logs = join(agyDir, 'brain', rawId, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript_full.jsonl'),
    line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-11-26T00:00:00Z', content: 'hi' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-26T00:00:01Z', content: 'hello' }))

  const dbPath = join(agyDir, 'conversation_summaries.db')
  const db = new DatabaseSync(dbPath)
  db.exec('PRAGMA journal_mode = WAL')
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)').run(
    rawId, 'CASCADE_RUN_STATUS_IDLE', 2, 'Initial Title', JSON.stringify(['file:///tmp/ws']), '2026-11-26T00:00:02Z',
  )
  db.close()
  // Backdate db & wal to settled (>2s ago)
  const past = new Date(Date.now() - 5000)
  utimesSync(dbPath, past, past)

  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: join(dir, 'missing-agy'), agyDir, home: dir, socketPath: node.path, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    rmSync(dir, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready

  await waitUntil(() => (node.last('runtime.sessions')?.sessions ?? []).some((s) => s.session_id === `agy-${rawId}` && s.title === 'Initial Title' && s.status === 'done'))

  // Immediate unsettled update in WAL mode must be visible right away
  const db2 = new DatabaseSync(dbPath)
  db2.prepare('UPDATE conversation_summaries SET status = ?, title = ? WHERE conversation_id = ?').run(
    'CASCADE_RUN_STATUS_RUNNING', 'Updated Title', rawId,
  )
  db2.close()

  // Trigger list refresh via rename on another id or direct session.items + push
  await node.peer.call('session.rename', { session_id: `agy-44444444-4444-4444-8444-444444444444`, title: 'trigger' })
  await waitUntil(() => (node.last('runtime.sessions')?.sessions ?? []).some((s) => s.session_id === `agy-${rawId}` && s.status === 'working' && s.title === 'Updated Title'))

  // Removing conversation_summaries.db clears cache cleanly instead of returning stale settled rows
  rmSync(dbPath, { force: true })
  rmSync(`${dbPath}-wal`, { force: true })
  rmSync(`${dbPath}-shm`, { force: true })
  await node.peer.call('session.rename', { session_id: `agy-55555555-5555-4555-8555-555555555555`, title: 'trigger2' })
  await waitUntil(() => (node.last('runtime.sessions')?.sessions ?? []).some((s) => s.session_id === `agy-${rawId}` && s.status === 'unknown'))
})

test('stepWeight and itemWeight bound large tool_input entries inside itemsCache', (t) => {
  const largeArg = 'A'.repeat(20000)
  const makeSteps = (tag) => Array.from({ length: 250 }, (_, i) => ({
    step_index: i,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-26T00:00:00Z',
    tool_calls: [{ name: 'write_file', args: { TargetFile: `/tmp/${tag}-${i}.txt`, Content: largeArg } }],
  }))

  // Each snapshot has ~250 * ~40KB (UTF-16) = ~10MB of tool_calls + ~10MB of tool_input => ~20MB each.
  // Inserting 5 distinct large snapshots (~100MB total) must evict the oldest under the 64MB itemsCache cap.
  const firstSteps = makeSteps('s0')
  const first = stepsToItems(firstSteps, { cacheKey: 'weight-test-0', cacheSlot: 'weight-slot-0', full: false })
  assert.equal(stepsToItems(firstSteps, { cacheKey: 'weight-test-0', cacheSlot: 'weight-slot-0', full: false }), first)

  for (let s = 1; s <= 4; s++) {
    stepsToItems(makeSteps(`s${s}`), { cacheKey: `weight-test-${s}`, cacheSlot: `weight-slot-${s}`, full: false })
  }

  const afterEvict = stepsToItems(firstSteps, { cacheKey: 'weight-test-0', cacheSlot: 'weight-slot-0', full: false })
  assert.notEqual(afterEvict, first, 'large tool_input snapshots must be weighted accurately and evicted under the cache cap')
})
