import assert from 'node:assert/strict'
import { appendFileSync, chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-stream.mjs', import.meta.url))
const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`

async function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'agy-stream-'))
  writeFileSync(join(directory, 'updates.jsonl'), '')
  writeFileSync(join(directory, 'calls.jsonl'), '')
  const binary = join(directory, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${directory}' "$@"\n`)
  chmodSync(binary, 0o700)
  mkdirSync(join(directory, 'config/brain', RAW), { recursive: true })
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: binary, agyDir: join(directory, 'config'), home: directory, socketPath: node.path, log: () => {} })
  t.after(async () => { await runtime.stop(); rmSync(directory, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await node.peer.call('session.start', { cwd: directory })
  await node.peer.call('session.watch', { session_id: SESSION })
  const calls = () => readFileSync(join(directory, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const update = (value) => appendFileSync(join(directory, 'updates.jsonl'), `${JSON.stringify(value)}\n`)
  const chunk = async (text, expected = text) => {
    update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })
    await until(`live text ${expected}`, () => node.last('session.delta')?.text === expected, 15_000)
  }
  const promptCount = (count) => until(`${count} prompts started`, () => calls().filter((r) => r.method === 'session/prompt').length === count, 15_000)
  return { node, directory, calls, update, chunk, promptCount }
}

test('AGY streams one assistant message at a time across tool calls', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Make the change.' })
  await promptCount(1)
  await chunk('Preparing ')
  await chunk('the change.', 'Preparing the change.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-1', title: 'Read' })
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tool-1', status: 'completed' })
  await chunk('The change is ')
  await chunk('complete.', 'The change is complete.')
  update({ end: true })
  await until('live text cleared at completion', () => node.last('session.delta')?.text === '', 15_000)
})

test('a queued prompt does not receive the executing prompt text and starts with an empty stream', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await chunk(' complete.', 'First reply complete.')
  update({ end: true })
  await promptCount(2)
  await until('queued prompt clears the previous text', () => node.last('session.delta')?.text === '', 15_000)
  await chunk('Second reply.')
  update({ end: true })
  await until('second prompt completes', () => node.last('session.delta')?.text === '', 15_000)
})

test('each prompt clears any live text before its first response', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply.')
  update({ end: true })
  await until('first prompt finishes', () => node.last('session.delta')?.text === '', 15_000)
  const before = node.all('session.delta').length
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  await until('new prompt publishes its empty boundary', () => node.all('session.delta').length > before, 15_000)
  assert.equal(node.last('session.delta').text, '')
  await chunk('First reply.')
  assert.equal(node.last('session.delta').text, 'First reply.', 'a new turn may legitimately repeat earlier words')
  update({ end: true })
})

test('AGY completion retires the cached reply after the viewer leaves', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('Previous reply.')
  await node.peer.call('session.unwatch', { session_id: SESSION })
  update({ end: true })
  await until('unwatched completion clears the reply', () => node.last('session.delta')?.text === '', 2000)
  await node.peer.call('session.watch', { session_id: SESSION })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  assert.equal(node.last('session.delta').text, '')
  await chunk('New reply.')
  update({ end: true })
})

function transcripts(directory, compact, complete) {
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  for (const [file, rows] of [['transcript.jsonl', compact], ['transcript_full.jsonl', complete]]) {
    writeFileSync(join(logs, file), rows.map((row) => JSON.stringify(row)).join('\n') + '\n')
  }
}

const userStep = (index, text) => ({ step_index: index, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: text })
const replyStep = (index, text) => ({ step_index: index, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', content: text })

test('watched native turns push new content after an older quota error without ACP updates', async (t) => {
  const { node, directory, calls, update } = await fixture(t)
  const old = [userStep(0, 'First request.'), { step_index: 1, type: 'ERROR_MESSAGE', content: 'Quota exhausted.' }]
  transcripts(directory, old, old)
  update({ sessionUpdate: 'tool_call', toolCallId: 'initial', title: 'Read' })
  await until('old quota is pushed', () => node.last('session.items')?.to_seq === 2)
  const next = [...old, userStep(2, 'Second request.'), replyStep(3, 'Current progress.')]
  transcripts(directory, next, next)
  await until('external turn is pushed', () => node.last('session.items')?.to_seq === 4)
  assert.equal(node.last('session.items').from_seq, 2)
  assert.deepEqual(node.last('session.items').items.map((item) => item.text), ['Second request.', 'Current progress.'])
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)

  await node.peer.call('session.unwatch', { session_id: SESSION })
  const before = node.all('session.items').length
  const final = [...next, replyStep(4, 'Current result.')]
  transcripts(directory, final, final)
  await new Promise((resolve) => setTimeout(resolve, 600))
  assert.equal(node.all('session.items').length, before)
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: 4 })
  await until('rewatching catches up', () => node.last('session.items')?.to_seq === 5)
  assert.equal(node.last('session.items').items.at(-1).text, 'Current result.')
})

test('a native planner completion at the same index replaces watched content', async (t) => {
  const { node, directory, update } = await fixture(t)
  const pending = { ...replyStep(1, ''), thinking: 'Reviewing the change.', created_at: '2026-01-02T12:00:01Z' }
  const old = [userStep(0, 'Review the change.'), pending]
  transcripts(directory, old, old)
  update({ sessionUpdate: 'tool_call', toolCallId: 'initial', title: 'Read' })
  await until('initial work is pushed', () => node.last('session.items')?.to_seq === 2)
  const history = node.last('session.items').history
  const complete = [old[0], { ...pending, content: 'Review complete.' }]
  transcripts(directory, complete, complete)
  await until('same-index completion is pushed', () => node.last('session.items')?.items.some((item) => item.text === 'Review complete.'))
  assert.equal(node.last('session.items').to_seq, 2)
  assert.notEqual(node.last('session.items').history, history)
})

test('normal AGY pages recover the complete native reply through the same source as full items', async (t) => {
  const { node, directory } = await fixture(t)
  const complete = '## Report\n' + 'A complete result with Unicode 字段.\n'.repeat(80) + 'End of report.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 4840 bytes>\nEnd of report.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  const page = await node.peer.call('session.items', { session_id: SESSION })
  const reply = page.items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.text, complete)
  assert.equal(reply.full_size, undefined)
  assert.equal(page.to_seq, 2)
  assert.notEqual(page.history, `agy:${RAW}`, 'compact transcript caches use a different history')
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
})

test('live AGY items apply Persona page limits to the complete native reply', async (t) => {
  const { node, directory, update } = await fixture(t)
  const complete = '## Report\n' + 'A detailed report row.\n'.repeat(2000) + 'Literal example: <truncated 7 bytes>.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 40000 bytes>\nEnd.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  update({ sessionUpdate: 'tool_call', toolCallId: 'read-report', title: 'Read' })
  await until('the reply is pushed', () => node.last('session.items')?.to_seq === 2)
  const reply = node.last('session.items').items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.full_size, Buffer.byteLength(complete))
  assert.ok(reply.text.startsWith('## Report\nA detailed report row.'))
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
  assert.ok(full.text.includes('Literal example: <truncated 7 bytes>.'))
})

test('AGY watch cursors follow the complete export while its compact export is ahead', async (t) => {
  const { node, directory, update } = await fixture(t)
  const old = [userStep(0, 'First request.'), replyStep(1, 'First reply.')]
  const nextUser = userStep(2, 'Second request.')
  transcripts(directory, [...old, nextUser], old)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.to_seq, 2)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read-first', title: 'Read' })
  await until('the initial complete export is pushed', () => node.last('session.items')?.to_seq === 2)
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION })
  const complete = [...old, nextUser, replyStep(3, 'Second reply.')]
  transcripts(directory, complete, complete)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read-next', title: 'Read' })
  await until('the complete export catches up', () => node.last('session.items')?.to_seq === 4)
  assert.deepEqual(node.last('session.items').items.map((item) => item.seq), [3, 4])
})
