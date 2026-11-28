import { test } from 'node:test'
import assert from 'node:assert/strict'
import { eventItems, itemsOf, personaSeq, lastText, streamText, toolInput, toolName } from '../lib/map.js'
import { answerOf } from '../lib/dsh.js'
import { light, PAGE_OUTPUT, summaryOf } from '../../runtime-sdk/index.js'

const userMessage = (seq, kind, content) => ({ type: 'user/message', seq, time: 1_790_000_000_000, data: { role: 'user', id: 'm', content, source: { kind } }, surfaceOp: 'append' })

test('a human prompt becomes user text and image items; injected context does not', () => {
  const items = eventItems(userMessage(9, 'user', [
    { type: 'text', text: 'fix the build' },
    { type: 'image', attachment: { attachmentId: 'sha256:aa', mediaType: 'image/webp', width: 10, height: 20, bytes: 300 } },
  ]))
  assert.deepEqual(items.map((i) => [i.id, i.seq, i.role, i.kind]), [['10#0', 10, 'user', 'text'], ['10#1', 10, 'user', 'image']])
  assert.equal(items[0].text, 'fix the build')
  assert.deepEqual(items[1].image, { mime: 'image/webp', data: '', lazy: true, size: 300, width: 10, height: 20 })
  assert.equal(items[0].ts, new Date(1_790_000_000_000).toISOString())
  for (const kind of ['time-context', 'skill-catalog', 'runtime-context', 'agent-instructions']) {
    assert.deepEqual(eventItems(userMessage(3, kind, [{ type: 'text', text: 'context' }])), [])
  }
  const settled = eventItems({ type: 'user/message', seq: 4, data: { content: [], source: { kind: 'subagent-settled', summary: 'review done' } } })
  assert.equal(settled[0].kind, 'notice')
  assert.match(settled[0].text, /review done/)
})

test('row preview finds the latest visible assistant text across blocks and events', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const message = (seq, ...texts) => ({
    type: 'assistant/message', seq,
    data: { message: { role: 'assistant', content: texts.map(text => ({ type: 'text', text })) } },
  })
  const mention = `The guide mentions ${marker}.`
  const mixed = `${mention}\n${marker}`
  const answer = message(2, mixed, marker)
  assert.equal(lastText([message(1, 'older answer'), answer, message(3, marker)]), mention)
  assert.equal(lastText([message(1, marker)]), '')
  assert.deepEqual(eventItems(answer).map(item => item.text), [mixed, marker])
})

test('assistant blocks become thinking, text and tool calls under the apps\' tool names', () => {
  const items = eventItems({ type: 'assistant/message', seq: 39, time: 0, surfaceOp: 'append', data: { message: { role: 'assistant', content: [
    { type: 'reasoning', text: 'look at the file' },
    { type: 'tool-call', id: 'toolu_1', name: 'bash', arguments: '{"command":"ls -la","description":"list"}' },
    { type: 'text', text: 'Done.' },
  ] } } })
  assert.deepEqual(items.map((i) => i.kind), ['thinking', 'tool_use', 'text'])
  assert.equal(items[1].tool_name, 'Bash')
  assert.equal(items[1].tool_use_id, 'toolu_1')
  assert.deepEqual(items[1].tool_input, { command: 'ls -la', description: 'list' })
  assert.equal(lastText([{ type: 'assistant/message', data: { message: { content: [{ type: 'text', text: '  Done.\n' }] } } }]), 'Done.')
})

test('tool results carry the call id, error flag and a clipped output', () => {
  const long = 'x'.repeat(PAGE_OUTPUT + 100)
  const [res] = eventItems({ type: 'tool/result', seq: 40, surfaceOp: 'append', data: { message: { toolCallId: 'toolu_1', isError: true, content: [{ type: 'text', text: long }] } } })
  assert.equal(res.kind, 'tool_result')
  assert.equal(res.tool_use_id, 'toolu_1')
  assert.equal(res.is_error, true)
  assert.equal(res.full_size, long.length)
  assert.match(res.text, /点开看全文/)
  const [whole] = eventItems({ type: 'tool/result', seq: 40, surfaceOp: 'append', data: { message: { toolCallId: 'toolu_1', content: [{ type: 'text', text: long }] } } }, { full: true })
  assert.equal(whole.text, long)
  // A result compaction re-surfaces is not shown twice.
  assert.deepEqual(eventItems({ type: 'tool/result', seq: 41, surfaceOp: { op: 'replace', startSeq: 1, endSeq: 2 }, data: { message: { content: [] } } }), [])
})

test('interrupted, failed and compacted turns leave a notice; completed ones none', () => {
  const notice = (reason) => eventItems({ type: 'turn/end', seq: 5, data: { turn: 1, reason } })
  assert.deepEqual(notice({ kind: 'completed' }), [])
  assert.equal(notice({ kind: 'aborted', reason: { kind: 'user' } })[0].text, '已中断')
  assert.match(notice({ kind: 'error', error: { message: 'rate limited', code: 'RATE' } })[0].text, /rate limited/)
  assert.equal(eventItems({ type: 'compaction/end', seq: 6, data: {} })[0].text, '上下文已压缩')
  assert.deepEqual(itemsOf([{ type: 'step/start', seq: 7, data: {} }, { type: 'agent/inbox/spliced', seq: 8, data: {} }]), [])
})

test('seqs are DSH seqs plus one', () => {
  assert.equal(personaSeq(0), 1)
})

test('long text is cut on a UTF-8 boundary with the note the apps read', () => {
  const text = '中'.repeat(10_000) // 30000 bytes
  const it = light({ kind: 'text', text })
  assert.equal(it.full_size, 30_000)
  assert.ok(Buffer.byteLength(it.text) < 25_000)
  assert.match(it.text, /已截断，共 30000 字节/)
  assert.doesNotMatch(it.text, /\uFFFD/)
})

test('tool names and inputs', () => {
  assert.equal(toolName('edit'), 'Edit')
  assert.equal(toolName('job_output'), 'job_output')
  assert.deepEqual(toolInput('web_search', '{"queries":["a","b"]}'), { queries: ['a', 'b'], query: 'a / b' })
  assert.deepEqual(toolInput('x', 'not json'), { arguments: 'not json' })
  assert.equal(summaryOf('bash', { command: 'rm -rf build' }), 'rm -rf build')
})

test('stream frames add their text deltas only', () => {
  assert.equal(streamText({ type: 'chunk', chunk: { type: 'text-delta', index: 0, text: 'He' } }), 'He')
  assert.equal(streamText({ type: 'chunk', chunk: { type: 'reasoning-delta', index: 0, text: 'hm' } }), '')
  assert.equal(streamText({ type: 'start' }), '')
})

test('the phone\'s answers become DSH answer batches', () => {
  const qs = [
    { id: 'q1', question: 'Color?', options: [{ label: 'Red' }, { label: 'Blue' }] },
    { id: 'q2', question: 'Sizes?', multiSelect: true, options: [{ label: 'S' }, { label: 'M' }, { label: 'L' }] },
    { id: 'q3', question: 'Name?', options: [{ label: 'A' }] },
  ]
  assert.deepEqual(answerOf(qs, undefined, { answers: { 'Color?': 'Blue', 'Sizes?': 'S, L', 'Name?': 'Something else' } }), [
    { id: 'q1', selected: ['Blue'] },
    { id: 'q2', selected: ['S', 'L'] },
    { id: 'q3', selected: [], custom: 'Something else' },
  ])
  const plan = { id: 'review', question: 'Approve?', detail: '# Plan', options: [{ label: 'Approve' }, { label: 'Keep planning' }], intent: { kind: 'plan-review', approve: 'Approve' } }
  assert.deepEqual(answerOf([plan], plan, { approve: true }), [{ id: 'review', selected: ['Approve'] }])
  assert.deepEqual(answerOf([plan], plan, { approve: false, feedback: 'split step 2' }), [{ id: 'review', selected: ['Keep planning'], custom: 'split step 2' }])
})

test('the assistant\'s live snapshot is not shown; a quoted context (with attributes) is', () => {
  const ev = { type: 'user/message', seq: 3, time: 0, data: { role: 'user', source: { kind: 'user' }, content: [
    { type: 'text', text: '<persona-context>\n- 现在：x\n</persona-context>' },
    { type: 'text', text: '<persona-context>\n- 现在：y\n</persona-context>' },
    { type: 'text', text: '<persona-context kind="context" title="t">quoted</persona-context> hi' },
  ] } }
  const items = eventItems(ev)
  assert.deepEqual(items.map((i) => i.text), ['<persona-context kind="context" title="t">quoted</persona-context> hi'])
})

test('a subagent call is the apps\' Agent, with its description', () => {
  assert.equal(toolName('subagent'), 'Agent')
  assert.equal(toolName('subagent_fork'), 'Agent')
  assert.equal(toolName('bash'), 'Bash')
})
