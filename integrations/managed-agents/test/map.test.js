import assert from 'node:assert/strict'
import { test } from 'node:test'
import { answerOf, cardOf, eventItems, lastText, logStatus, NOTE_ERROR_LEAD, NOTE_INTERRUPTED, pendingAsks } from '../lib/map.js'

const TOOLS = { questionTools: ['AskQuestion'], planTools: ['ExitPlanMode'] }

test('events become the items the apps draw', () => {
  const images = new Map()
  const at = '2026-11-20T08:00:00Z'
  const items = [
    ...eventItems({ id: 'e1', type: 'user.message', processed_at: at, content: [{ type: 'text', text: 'hi' }, { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: 'AAA' } }] }, 1, images),
    ...eventItems({ id: 'e2', type: 'agent.thinking', content: [{ type: 'text', text: 'hmm' }] }, 2, images),
    ...eventItems({ id: 'e3', type: 'agent.tool_use', name: 'bash', input: { command: 'ls' } }, 3, images),
    ...eventItems({ id: 'e4', type: 'agent.tool_result', tool_use_id: 'e3', content: [{ type: 'text', text: 'a.txt' }] }, 4, images),
    ...eventItems({ id: 'e5', type: 'agent.mcp_tool_use', mcp_server_name: 'notion', name: 'search', input: '{"q":"x"}' }, 5, images),
    ...eventItems({ id: 'e6', type: 'agent.message', content: [{ type: 'text', text: 'done' }] }, 6, images),
    ...eventItems({ id: 'e7', type: 'session.status_idle', stop_reason: { type: 'end_turn' } }, 7, images),
    ...eventItems({ id: 'e8', type: 'user.interrupt' }, 8, images),
    ...eventItems({ id: 'e9', type: 'session.error', error: { message: 'model failed', retry_status: { type: 'exhausted' } } }, 9, images),
    ...eventItems({ id: 'e10', type: 'session.error', error: { message: 'again', retry_status: { type: 'retrying' } } }, 10, images),
  ]
  assert.deepEqual(items.map((i) => [i.id, i.role, i.kind]), [
    ['1#0', 'user', 'text'], ['1#1', 'user', 'image'], ['2#0', 'assistant', 'thinking'], ['3#0', 'assistant', 'tool_use'],
    ['4#0', 'user', 'tool_result'], ['5#0', 'assistant', 'tool_use'], ['6#0', 'assistant', 'text'],
    ['8#0', 'system', 'notice'], ['9#0', 'system', 'notice'],
  ])
  assert.equal(items[0].ts, at)
  assert.equal(items[3].tool_name, 'Bash')
  assert.deepEqual(items[3].tool_input, { command: 'ls' })
  assert.equal(items[4].tool_use_id, 'e3')
  assert.equal(items[5].tool_name, 'mcp__notion__search')
  assert.deepEqual(items[5].tool_input, { q: 'x' })
  assert.equal(items[1].image.lazy, true)
  assert.equal(images.get('1#1').data, 'AAA')
  assert.equal(items[7].text, NOTE_INTERRUPTED)
  assert.equal(items[8].text, `${NOTE_ERROR_LEAD}model failed`)
})

test('botmux sentinel replies preserve the current turn preview and original content', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const reply = (content) => ({ type: 'agent.message', content })
  const mixed = reply('Notes\n' + marker + '\nready')
  assert.equal(lastText([mixed]), 'Notes ready')
  assert.equal(lastText([mixed, reply(marker)]), 'Notes ready')
  assert.equal(lastText([mixed, { type: 'user.message' }, reply(marker)]), '')
  assert.equal(eventItems(mixed, 1, new Map())[0].text, mixed.content)
})

test('status and last line come from the log', () => {
  assert.equal(logStatus([]), 'unknown')
  assert.equal(logStatus([{ type: 'user.message' }]), 'working')
  assert.equal(logStatus([{ type: 'session.status_running' }, { type: 'agent.message' }]), 'working')
  assert.equal(logStatus([{ type: 'session.status_idle', stop_reason: { type: 'end_turn' } }]), 'done')
  assert.equal(logStatus([{ type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: ['x'] } }]), 'waiting')
  assert.equal(logStatus([{ type: 'session.status_terminated' }]), 'terminated')
  assert.equal(lastText([{ type: 'agent.message', content: [{ type: 'text', text: 'line one\nline two' }] }]), 'line one line two')
  assert.equal(lastText([{ type: 'agent.message', content: [{ type: 'text', text: 'old' }] }, { type: 'user.message' }]), '')
})

test('a tool call whose policy asks, and the question and plan tools, are pending until answered', () => {
  const log = [
    { id: 't1', type: 'agent.tool_use', name: 'bash', input: { command: 'rm -rf build' }, evaluated_permission: 'ask' },
    { id: 't2', type: 'agent.tool_use', name: 'read', input: { file_path: '/a' }, evaluated_permission: 'allow' },
    { id: 'q1', type: 'agent.custom_tool_use', name: 'AskQuestion', input: { questions: [{ question: 'Which?', header: 'Pick', options: [{ label: 'A', description: 'first' }, { label: 'B' }], multiSelect: false }] } },
    { id: 'p1', type: 'agent.custom_tool_use', name: 'ExitPlanMode', input: { title: 'Plan', summary: 'Do it', plan: '1. a\n2. b' } },
    { id: 'c1', type: 'agent.custom_tool_use', name: 'generate_video', input: {} },
    { id: 's1', type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: ['t1', 'q1', 'p1', 'c1'] } },
  ]
  const pending = pendingAsks(log, TOOLS)
  assert.deepEqual(pending.map((a) => [a.kind, a.event.id]), [['permission', 't1'], ['question', 'q1'], ['plan', 'p1']])
  const [perm, q, plan] = pending.map(cardOf)
  assert.equal(perm.kind, 'permission')
  assert.equal(perm.tool_name, 'Bash')
  assert.equal(perm.summary, 'rm -rf build')
  assert.deepEqual(q.questions, [{ question: 'Which?', header: 'Pick', multi_select: false, options: [{ label: 'A', description: 'first' }, { label: 'B', description: undefined }] }])
  assert.equal(plan.plan, '# Plan\n\nDo it\n\n1. a\n2. b')
  const answered = [...log, { type: 'user.tool_confirmation', tool_use_id: 't1', result: 'allow' }, { type: 'user.custom_tool_result', custom_tool_use_id: 'q1' }]
  assert.deepEqual(pendingAsks(answered, TOOLS).map((a) => a.event.id), ['p1'])
  // An interrupt after the pause drops what it named.
  assert.deepEqual(pendingAsks([...log, { type: 'user.interrupt' }], TOOLS), [])
  // A finished turn names nothing.
  assert.deepEqual(pendingAsks([...log, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } }], TOOLS), [])
})

test('answers become the events Managed Agents expects', () => {
  const perm = { kind: 'permission', event: { id: 't1', session_thread_id: 'th1' } }
  assert.deepEqual(answerOf(perm, { allow: true }), { type: 'user.tool_confirmation', tool_use_id: 't1', result: 'allow', session_thread_id: 'th1' })
  assert.deepEqual(answerOf(perm, { allow: false, message: 'not on prod' }), { type: 'user.tool_confirmation', tool_use_id: 't1', result: 'deny', deny_message: 'not on prod', session_thread_id: 'th1' })
  assert.equal(answerOf(perm, { dismiss: true }), undefined)
  const q = answerOf({ kind: 'question', event: { id: 'q1' } }, { answers: { 'Which?': 'A' } })
  assert.equal(q.type, 'user.custom_tool_result')
  assert.equal(q.custom_tool_use_id, 'q1')
  assert.deepEqual(JSON.parse(q.content[0].text), { answers: { 'Which?': 'A' } })
  const ok = answerOf({ kind: 'plan', event: { id: 'p1' } }, { approve: true, mode: 'acceptEdits' })
  assert.deepEqual(JSON.parse(ok.content[0].text), { decision: 'approved', executionMode: 'acceptEdits', feedback: null })
  const back = answerOf({ kind: 'plan', event: { id: 'p1' } }, { approve: false, feedback: 'smaller' })
  assert.deepEqual(JSON.parse(back.content[0].text), { decision: 'changes_requested', feedback: 'smaller' })
})

test('self-hosted user.tool_result maps to a tool_result item and clears pending asks', () => {
  const images = new Map()
  const at = '2026-11-23T08:00:00Z'
  const okItems = eventItems(
    { id: 'e2', type: 'user.tool_result', tool_use_id: 't1', content: [{ type: 'text', text: 'stdout line' }], is_error: false, created_at: at },
    2,
    images,
  )
  assert.deepEqual(okItems, [
    { id: '2#0', seq: 2, ts: at, role: 'user', kind: 'tool_result', tool_use_id: 't1', text: 'stdout line', is_error: false },
  ])
  const errItems = eventItems(
    { id: 'e3', type: 'user.tool_result', tool_use_id: 't2', content: 'boom', is_error: true, processed_at: at },
    3,
    images,
  )
  assert.deepEqual(errItems, [
    { id: '3#0', seq: 3, ts: at, role: 'user', kind: 'tool_result', tool_use_id: 't2', text: 'boom', is_error: true },
  ])
  const log = [
    { id: 't1', type: 'agent.tool_use', name: 'bash', input: { command: 'ls' }, evaluated_permission: 'ask' },
    { id: 's1', type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: ['t1'] } },
    { id: 'e2', type: 'user.tool_result', tool_use_id: 't1', content: [{ type: 'text', text: 'stdout line' }], is_error: false },
  ]
  assert.deepEqual(pendingAsks(log, TOOLS), [])
})
