import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { appendFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-stream.mjs', import.meta.url))
const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`

const CARD_REPLY = [
  'Choose an option:',
  '```persona-view',
  JSON.stringify({
    title: 'Next step',
    fallback: 'Choose A or B.',
    sections: [{
      items: [
        { type: 'row', title: 'Option A', action: { id: 'a', reply: 'Choose A' } },
        { type: 'row', title: 'Option B', action: { id: 'b', reply: 'Choose B' } },
      ],
    }],
  }),
  '```',
].join('\n')

function writeTranscriptFile(directory, name, rows) {
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, name), rows.map((row) => JSON.stringify(row)).join('\n') + '\n')
}

async function fixture(t, runtimeOpts = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'agy-inline-view-'))
  writeFileSync(join(directory, 'updates.jsonl'), '')
  writeFileSync(join(directory, 'calls.jsonl'), '')
  mkdirSync(join(directory, 'config/brain', RAW), { recursive: true })
  const socketPath = process.platform === 'win32'
    ? `\\\\.\\pipe\\agy-inline-view-${randomUUID()}`
    : undefined
  const node = await startFakeNode(t, socketPath ? { path: socketPath } : {})
  const runtime = createAgyRuntime({
    agyBin: process.execPath,
    agyArgs: [FAKE, directory],
    agyDir: join(directory, 'config'),
    home: directory,
    socketPath: node.path,
    exportPollMs: 40,
    exportSettleTimeoutMs: 5_000,
    log: () => {},
    ...runtimeOpts,
  })
  t.after(async () => {
    await runtime.stop()
    rmSync(directory, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  await node.peer.call('session.start', { cwd: directory, assistant: true })
  const calls = () => readFileSync(join(directory, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const update = (value) => appendFileSync(join(directory, 'updates.jsonl'), `${JSON.stringify(value)}\n`)
  const promptCount = (count) => until(`${count} prompts started`, () => calls().filter((r) => r.method === 'session/prompt').length === count, 15_000)
  return { node, directory, update, promptCount }
}

test('unexported settled turn syncs final persisted item id once native transcript flushes', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const initial = [
    { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:00:00Z', content: 'Hello' },
    { step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-01T00:00:01Z', content: 'Ready.' },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', initial)
  await node.peer.call('session.watch', { session_id: SESSION })
  const before = (await node.peer.call('session.items', { session_id: SESSION })).history

  await node.peer.call('session.send', { session_id: SESSION, text: 'Show me options.', assistant: true })
  await promptCount(1)
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: CARD_REPLY } })
  await until('live card reply streams', () => node.last('session.delta')?.text === CARD_REPLY, 15_000)
  update({ end: true })

  await until('settled unexported turn pushed with temporary item id', () => {
    const batch = node.last('session.items')
    return batch?.history === before &&
      batch.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.text === CARD_REPLY)
  }, 15_000)
  const tempBatch = node.last('session.items')
  const tempReply = tempBatch.items.findLast((it) => it.role === 'assistant' && it.kind === 'text')
  assert.ok(tempReply.id.endsWith('#0'), `expected temporary id without thinking sub-index, got ${tempReply.id}`)

  // Partial export (USER_INPUT only) must not drop the settled reply before the final PLANNER_RESPONSE lands.
  const partial = [
    ...initial,
    { step_index: 2, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:00:02Z', content: 'Show me options.' },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', partial)
  const duringPartial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(duringPartial.history, before)
  assert.equal(duringPartial.items.at(-1).id, tempReply.id)

  // Real flushed transcript has shifted step_index and a thinking block, producing the final stable #1 item id.
  const flushed = [
    ...partial,
    {
      step_index: 5,
      source: 'MODEL',
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-01-01T00:00:05Z',
      thinking: 'Preparing choices for the user.',
      content: CARD_REPLY,
    },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', flushed)

  await until('rewritten session.items pushed with final persisted item id', () => {
    const batch = node.last('session.items')
    return batch && batch.history !== before && batch.to_seq === 6 &&
      batch.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.id.startsWith('6@') && it.id.endsWith('#1'))
  }, 15_000)

  const finalBatch = node.last('session.items')
  const finalReply = finalBatch.items.findLast((it) => it.role === 'assistant' && it.kind === 'text')
  assert.equal(finalBatch.from_seq, 0)
  assert.notEqual(finalReply.id, tempReply.id)
  assert.equal(finalReply.text, CARD_REPLY)

  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.items.findLast((it) => it.role === 'assistant' && it.kind === 'text')?.id, finalReply.id)
  assert.equal(page.items.at(-1).kind, 'turn_duration')
})

test('unwatched settled turn clears unexportedTurns and publishes updated rev once native transcript flushes', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const initial = [
    { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:00:00Z', content: 'Hello' },
    { step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-01T00:00:01Z', content: 'Ready.' },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', initial)
  const before = (await node.peer.call('session.items', { session_id: SESSION })).history

  await node.peer.call('session.send', { session_id: SESSION, text: 'Show options while unwatched.', assistant: true })
  await promptCount(1)
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: CARD_REPLY } })
  update({ end: true })

  await until('unwatched settled turn exposes temporary item id', async () => {
    const page = await node.peer.call('session.items', { session_id: SESSION })
    return page.history === before && page.items.some((it) => it.role === 'assistant' && it.text === CARD_REPLY)
  }, 15_000)

  const flushed = [
    ...initial,
    { step_index: 2, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:00:02Z', content: 'Show options while unwatched.' },
    {
      step_index: 7,
      source: 'MODEL',
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-01-01T00:00:07Z',
      thinking: 'Building options card.',
      content: CARD_REPLY,
    },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', flushed)

  await until('export poll publishes updated session rev without an active watch', () => {
    const sessions = node.last('runtime.sessions')?.sessions || []
    return sessions.find((s) => s.session_id === SESSION)?.rev === 8
  }, 15_000)

  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.notEqual(page.history, before)
  const reply = page.items.findLast((it) => it.role === 'assistant' && it.kind === 'text')
  assert.ok(reply.id.startsWith('8@') && reply.id.endsWith('#1'), `expected final persisted id, got ${reply.id}`)
  assert.equal(reply.text, CARD_REPLY)
})

test('compact-to-full transcript transition at the same step_index pushes final item id', { timeout: 20_000 }, async (t) => {
  const { node, directory } = await fixture(t)
  await node.peer.call('session.watch', { session_id: SESSION })
  await until('initial session is listed', () => node.last('runtime.sessions')?.sessions.some((session) => session.session_id === SESSION))
  const compact = [
    { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:00:00Z', content: 'Show card.' },
    { step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-01T00:00:01Z', content: CARD_REPLY },
  ]
  writeTranscriptFile(directory, 'transcript.jsonl', compact)

  await until('compact transcript pushed', () => {
    const batch = node.last('session.items')
    return batch?.history?.includes(':compact') && batch.to_seq === 2
  }, 15_000)
  const compactReply = node.last('session.items').items.findLast((it) => it.role === 'assistant' && it.kind === 'text')
  assert.ok(compactReply.id.endsWith('#0'))

  const full = [
    compact[0],
    { ...compact[1], thinking: 'Full reasoning included in transcript_full.jsonl.' },
  ]
  writeTranscriptFile(directory, 'transcript_full.jsonl', full)

  await until('full transcript replaces compact history at the same step_index', () => {
    const batch = node.last('session.items')
    return batch && !batch.history?.includes(':compact') && batch.to_seq === 2 &&
      batch.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.id.endsWith('#1'))
  }, 15_000)
  const fullReply = node.last('session.items').items.findLast((it) => it.role === 'assistant' && it.kind === 'text')
  assert.notEqual(fullReply.id, compactReply.id)
  assert.equal(fullReply.text, CARD_REPLY)
})
